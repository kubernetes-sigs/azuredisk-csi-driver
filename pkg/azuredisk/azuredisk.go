/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package azuredisk

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-08-01/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/volume/util/hostutil"
	"k8s.io/mount-utils"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	csicommon "sigs.k8s.io/azuredisk-csi-driver/pkg/csi-common"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	azurecloudconsts "sigs.k8s.io/cloud-provider-azure/pkg/consts"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

// DriverOptions defines driver parameters specified in driver deployment
type DriverOptions struct {
	NodeID                       string
	DriverName                   string
	VolumeAttachLimit            int64
	EnablePerfOptimization       bool
	CloudConfigSecretName        string
	CloudConfigSecretNamespace   string
	CustomUserAgent              string
	UserAgentSuffix              string
	UseCSIProxyGAInterface       bool
	EnableDiskOnlineResize       bool
	AllowEmptyCloudConfig        bool
	EnableAsyncAttach            bool
	EnableListVolumes            bool
	EnableListSnapshots          bool
	SupportZone                  bool
	GetNodeInfoFromLabels        bool
	EnableDiskCapacityCheck      bool
	DisableUpdateCache           bool
	EnableTrafficManager         bool
	TrafficManagerPort           int64
	AttachDetachInitialDelayInMs int64
	VMSSCacheTTLInSeconds        int64
	VMType                       string
	EnableWindowsHostProcess     bool
	GetNodeIDFromIMDS            bool
	EnableOtelTracing            bool
	WaitForSnapshotReady         bool
	KubeConfig                   string
	DisableAVSetNodes            bool
	TestingMock                  bool
	Cloud                        *azure.Cloud
}

// CSIDriver defines the interface for a CSI driver.
type CSIDriver interface {
	csi.ControllerServer
	csi.NodeServer
	csi.IdentityServer

	Run(endpoint string)
}

type hostUtil interface {
	PathIsDevice(string) (bool, error)
}

// DriverCore contains fields common to both the V1 and V2 driver, and implements all interfaces of CSI drivers
type DriverCore struct {
	csicommon.CSIDriver
	options                      DriverOptions
	perfOptimizationEnabled      bool
	cloud                        *azure.Cloud
	mounter                      *mount.SafeFormatAndMount
	deviceHelper                 optimization.Interface
	nodeInfo                     *optimization.NodeInfo
	ioHandler                    azureutils.IOHandler
	hostUtil                     hostUtil
	enableDiskOnlineResize       bool
	enableAsyncAttach            bool
	supportZone                  bool
	getNodeInfoFromLabels        bool
	enableDiskCapacityCheck      bool
	attachDetachInitialDelayInMs int64
	vmType                       string
	getNodeIDFromIMDS            bool
	enableOtelTracing            bool
	shouldWaitForSnapshotReady   bool
	testingMock                  bool
}

// Driver is the v1 implementation of the Azure Disk CSI Driver.
type Driver struct {
	DriverCore
	volumeLocks *volumehelper.VolumeLocks
	// a timed cache GetDisk throttling
	getDiskThrottlingCache azcache.Resource
}

// newDriverV1 Creates a NewCSIDriver object. Assumes vendor version is equal to driver version &
// does not support optional driver plugin info manifest field. Refer to CSI spec for more details.
func newDriverV1(options *DriverOptions) *Driver {
	driver := Driver{}
	driver.Name = options.DriverName
	driver.Version = driverVersion
	driver.NodeID = options.NodeID
	driver.VolumeAttachLimit = options.VolumeAttachLimit
	driver.perfOptimizationEnabled = options.EnablePerfOptimization
	driver.enableDiskOnlineResize = options.EnableDiskOnlineResize
	driver.enableAsyncAttach = options.EnableAsyncAttach
	driver.supportZone = options.SupportZone
	driver.getNodeInfoFromLabels = options.GetNodeInfoFromLabels
	driver.enableDiskCapacityCheck = options.EnableDiskCapacityCheck
	driver.attachDetachInitialDelayInMs = options.AttachDetachInitialDelayInMs
	driver.vmType = options.VMType
	driver.getNodeIDFromIMDS = options.GetNodeIDFromIMDS
	driver.enableOtelTracing = options.EnableOtelTracing
	driver.shouldWaitForSnapshotReady = options.WaitForSnapshotReady
	driver.volumeLocks = volumehelper.NewVolumeLocks()
	driver.ioHandler = azureutils.NewOSIOHandler()
	driver.hostUtil = hostutil.NewHostUtil()
	driver.testingMock = options.TestingMock
	driver.options = *options
	topologyKey = fmt.Sprintf("topology.%s/zone", driver.Name)

	cache, err := azcache.NewTimedCache(5*time.Minute, func(key string) (interface{}, error) {
		return nil, nil
	}, false)
	if err != nil {
		klog.Fatalf("%v", err)
	}
	driver.getDiskThrottlingCache = cache
	driver.cloud = options.Cloud

	if driver.cloud != nil {
		if driver.vmType != "" {
			klog.V(2).Infof("override VMType(%s) in cloud config as %s", driver.cloud.VMType, driver.vmType)
			driver.cloud.VMType = driver.vmType
		}

		if driver.NodeID == "" {
			// Disable UseInstanceMetadata for controller to mitigate a timeout issue using IMDS
			// https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/168
			klog.V(2).Infof("disable UseInstanceMetadata for controller")
			driver.cloud.Config.UseInstanceMetadata = false

			if driver.cloud.VMType == azurecloudconsts.VMTypeStandard && driver.cloud.DisableAvailabilitySetNodes {
				klog.V(2).Infof("set DisableAvailabilitySetNodes as false since VMType is %s", driver.cloud.VMType)
				driver.cloud.DisableAvailabilitySetNodes = false
			}

			if driver.cloud.VMType == azurecloudconsts.VMTypeVMSS && !driver.cloud.DisableAvailabilitySetNodes && options.DisableAVSetNodes {
				klog.V(2).Infof("DisableAvailabilitySetNodes for controller since current VMType is vmss")
				driver.cloud.DisableAvailabilitySetNodes = true
			}
			klog.V(2).Infof("cloud: %s, location: %s, rg: %s, VMType: %s, PrimaryScaleSetName: %s, PrimaryAvailabilitySetName: %s, DisableAvailabilitySetNodes: %v", driver.cloud.Cloud, driver.cloud.Location, driver.cloud.ResourceGroup, driver.cloud.VMType, driver.cloud.PrimaryScaleSetName, driver.cloud.PrimaryAvailabilitySetName, driver.cloud.DisableAvailabilitySetNodes)
		}

		if options.VMSSCacheTTLInSeconds > 0 {
			klog.V(2).Infof("reset vmssCacheTTLInSeconds as %d", options.VMSSCacheTTLInSeconds)
			driver.cloud.VMCacheTTLInSeconds = int(options.VMSSCacheTTLInSeconds)
			driver.cloud.VmssCacheTTLInSeconds = int(options.VMSSCacheTTLInSeconds)
		}

		if driver.cloud.ManagedDiskController != nil {
			driver.cloud.DisableUpdateCache = options.DisableUpdateCache
			driver.cloud.AttachDetachInitialDelayInMs = int(driver.attachDetachInitialDelayInMs)
		}
	}

	driver.deviceHelper = optimization.NewSafeDeviceHelper()

	if driver.getPerfOptimizationEnabled() {
		driver.nodeInfo, err = optimization.NewNodeInfo(context.TODO(), driver.getCloud(), driver.NodeID)
		if err != nil {
			klog.Warningf("Failed to get node info. Error: %v", err)
		}
	}

	driver.mounter, err = mounter.NewSafeMounter(options.EnableWindowsHostProcess, options.UseCSIProxyGAInterface)
	if err != nil {
		klog.Fatalf("Failed to get safe mounter. Error: %v", err)
	}

	controllerCap := []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
		csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
		csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
		csi.ControllerServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
	}
	if options.EnableListVolumes {
		controllerCap = append(controllerCap, csi.ControllerServiceCapability_RPC_LIST_VOLUMES, csi.ControllerServiceCapability_RPC_LIST_VOLUMES_PUBLISHED_NODES)
	}
	if options.EnableListSnapshots {
		controllerCap = append(controllerCap, csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS)
	}

	driver.AddControllerServiceCapabilities(controllerCap)
	driver.AddVolumeCapabilityAccessModes(
		[]csi.VolumeCapability_AccessMode_Mode{
			csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
			csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER,
			csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER,
		})
	driver.AddNodeServiceCapabilities([]csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
		csi.NodeServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
	})
	return &driver
}

// Run driver initialization
func (d *Driver) Run(endpoint string) {
	versionMeta, err := GetVersionYAML(d.Name)
	if err != nil {
		klog.Fatalf("%v", err)
	}
	klog.Infof("\nDRIVER INFORMATION:\n-------------------\n%s\n\nStreaming logs below:", versionMeta)
	s := csicommon.NewNonBlockingGRPCServer()
	// Driver d act as IdentityServer, ControllerServer and NodeServer
	s.Start(endpoint, d, d, d, d.testingMock, d.enableOtelTracing)
	s.Wait()
}

func (d *Driver) isGetDiskThrottled() bool {
	cache, err := d.getDiskThrottlingCache.Get(consts.ThrottlingKey, azcache.CacheReadTypeDefault)
	if err != nil {
		klog.Warningf("getDiskThrottlingCache(%s) return with error: %s", consts.ThrottlingKey, err)
		return false
	}
	return cache != nil
}

func (d *Driver) checkDiskExists(ctx context.Context, diskURI string) (*compute.Disk, error) {
	diskName, err := azureutils.GetDiskName(diskURI)
	if err != nil {
		return nil, err
	}

	resourceGroup, err := azureutils.GetResourceGroupFromURI(diskURI)
	if err != nil {
		return nil, err
	}

	if d.isGetDiskThrottled() {
		klog.Warningf("skip checkDiskExists(%s) since it's still in throttling", diskURI)
		return nil, nil
	}
	subsID := azureutils.GetSubscriptionIDFromURI(diskURI)
	disk, rerr := d.cloud.DisksClient.Get(ctx, subsID, resourceGroup, diskName)
	if rerr != nil {
		if rerr.IsThrottled() || strings.Contains(rerr.RawError.Error(), consts.RateLimited) {
			klog.Warningf("checkDiskExists(%s) is throttled with error: %v", diskURI, rerr.Error())
			d.getDiskThrottlingCache.Set(consts.ThrottlingKey, "")
			return nil, nil
		}
		return nil, rerr.Error()
	}

	return &disk, nil
}

func (d *Driver) checkDiskCapacity(ctx context.Context, subsID, resourceGroup, diskName string, requestGiB int) (bool, error) {
	if d.isGetDiskThrottled() {
		klog.Warningf("skip checkDiskCapacity(%s, %s) since it's still in throttling", resourceGroup, diskName)
		return true, nil
	}

	disk, rerr := d.cloud.DisksClient.Get(ctx, subsID, resourceGroup, diskName)
	// Because we can not judge the reason of the error. Maybe the disk does not exist.
	// So here we do not handle the error.
	if rerr == nil {
		if !reflect.DeepEqual(disk, compute.Disk{}) && disk.DiskSizeGB != nil && int(*disk.DiskSizeGB) != requestGiB {
			return false, status.Errorf(codes.AlreadyExists, "the request volume already exists, but its capacity(%v) is different from (%v)", *disk.DiskProperties.DiskSizeGB, requestGiB)
		}
	} else {
		if rerr.IsThrottled() || strings.Contains(rerr.RawError.Error(), consts.RateLimited) {
			klog.Warningf("checkDiskCapacity(%s, %s) is throttled with error: %v", resourceGroup, diskName, rerr.Error())
			d.getDiskThrottlingCache.Set(consts.ThrottlingKey, "")
		}
	}
	return true, nil
}

func (d *Driver) getVolumeLocks() *volumehelper.VolumeLocks {
	return d.volumeLocks
}

// setControllerCapabilities sets the controller capabilities field. It is intended for use with unit tests.
func (d *DriverCore) setControllerCapabilities(caps []*csi.ControllerServiceCapability) {
	d.Cap = caps
}

// setNodeCapabilities sets the node capabilities field. It is intended for use with unit tests.
func (d *DriverCore) setNodeCapabilities(nodeCaps []*csi.NodeServiceCapability) {
	d.NSCap = nodeCaps
}

// setName sets the Name field. It is intended for use with unit tests.
func (d *DriverCore) setName(name string) {
	d.Name = name
}

// setName sets the NodeId field. It is intended for use with unit tests.
func (d *DriverCore) setNodeID(nodeID string) {
	d.NodeID = nodeID
}

// setName sets the Version field. It is intended for use with unit tests.
func (d *DriverCore) setVersion(version string) {
	d.Version = version
}

// getCloud returns the value of the cloud field. It is intended for use with unit tests.
func (d *DriverCore) getCloud() *azure.Cloud {
	return d.cloud
}

// setCloud sets the cloud field. It is intended for use with unit tests.
func (d *DriverCore) setCloud(cloud *azure.Cloud) {
	d.cloud = cloud
}

// getMounter returns the value of the mounter field. It is intended for use with unit tests.
func (d *DriverCore) getMounter() *mount.SafeFormatAndMount {
	return d.mounter
}

// setMounter sets the mounter field. It is intended for use with unit tests.
func (d *DriverCore) setMounter(mounter *mount.SafeFormatAndMount) {
	d.mounter = mounter
}

// getPerfOptimizationEnabled returns the value of the perfOptimizationEnabled field. It is intended for use with unit tests.
func (d *DriverCore) getPerfOptimizationEnabled() bool {
	return d.perfOptimizationEnabled
}

// setPerfOptimizationEnabled sets the value of the perfOptimizationEnabled field. It is intended for use with unit tests.
func (d *DriverCore) setPerfOptimizationEnabled(enabled bool) {
	d.perfOptimizationEnabled = enabled
}

// getDeviceHelper returns the value of the deviceHelper field. It is intended for use with unit tests.
func (d *DriverCore) getDeviceHelper() optimization.Interface {
	return d.deviceHelper
}

// getNodeInfo returns the value of the nodeInfo field. It is intended for use with unit tests.
func (d *DriverCore) getNodeInfo() *optimization.NodeInfo {
	return d.nodeInfo
}

func (d *DriverCore) getHostUtil() hostUtil {
	return d.hostUtil
}

// getSnapshotCompletionPercent returns the completion percent of snapshot
func (d *DriverCore) getSnapshotCompletionPercent(ctx context.Context, subsID, resourceGroup, snapshotName string) (float64, error) {
	copySnapshot, rerr := d.cloud.SnapshotsClient.Get(ctx, subsID, resourceGroup, snapshotName)
	if rerr != nil {
		return 0.0, rerr.Error()
	}

	if copySnapshot.SnapshotProperties == nil || copySnapshot.SnapshotProperties.CompletionPercent == nil {
		// If CompletionPercent is nil, it means the snapshot is complete
		klog.V(2).Infof("snapshot(%s) under rg(%s) has no SnapshotProperties or CompletionPercent is nil", snapshotName, resourceGroup)
		return 100.0, nil
	}

	return *copySnapshot.SnapshotProperties.CompletionPercent, nil
}

// waitForSnapshotReady wait for completionPercent of snapshot is 100.0
func (d *DriverCore) waitForSnapshotReady(ctx context.Context, subsID, resourceGroup, snapshotName string, intervel, timeout time.Duration) error {
	completionPercent, err := d.getSnapshotCompletionPercent(ctx, subsID, resourceGroup, snapshotName)
	if err != nil {
		return err
	}

	if completionPercent >= float64(100.0) {
		klog.V(2).Infof("snapshot(%s) under rg(%s) complete", snapshotName, resourceGroup)
		return nil
	}

	timeTick := time.Tick(intervel)
	timeAfter := time.After(timeout)
	for {
		select {
		case <-timeTick:
			completionPercent, err = d.getSnapshotCompletionPercent(ctx, subsID, resourceGroup, snapshotName)
			if err != nil {
				return err
			}

			if completionPercent >= float64(100.0) {
				klog.V(2).Infof("snapshot(%s) under rg(%s) complete", snapshotName, resourceGroup)
				return nil
			}
			klog.V(2).Infof("snapshot(%s) under rg(%s) completionPercent: %f", snapshotName, resourceGroup, completionPercent)
		case <-timeAfter:
			return fmt.Errorf("timeout waiting for snapshot(%s) under rg(%s)", snapshotName, resourceGroup)
		}
	}
}

// getNodeInfoFromLabels get zone, instanceType from node labels
func getNodeInfoFromLabels(ctx context.Context, nodeName string, kubeClient clientset.Interface) (string, string, error) {
	if kubeClient == nil || kubeClient.CoreV1() == nil {
		return "", "", fmt.Errorf("kubeClient is nil")
	}

	node, err := kubeClient.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return "", "", fmt.Errorf("get node(%s) failed with %v", nodeName, err)
	}

	if len(node.Labels) == 0 {
		return "", "", fmt.Errorf("node(%s) label is empty", nodeName)
	}
	return node.Labels[consts.WellKnownTopologyKey], node.Labels[consts.InstanceTypeKey], nil
}

// getDefaultDiskIOPSReadWrite according to requestGiB
//
//	ref: https://docs.microsoft.com/en-us/azure/virtual-machines/disks-types#ultra-disk-iops
func getDefaultDiskIOPSReadWrite(requestGiB int) int {
	iops := azurecloudconsts.DefaultDiskIOPSReadWrite
	if requestGiB > iops {
		iops = requestGiB
	}
	if iops > 160000 {
		iops = 160000
	}
	return iops
}

// getDefaultDiskMBPSReadWrite according to requestGiB
//
//	ref: https://docs.microsoft.com/en-us/azure/virtual-machines/disks-types#ultra-disk-throughput
func getDefaultDiskMBPSReadWrite(requestGiB int) int {
	bandwidth := azurecloudconsts.DefaultDiskMBpsReadWrite
	iops := getDefaultDiskIOPSReadWrite(requestGiB)
	if iops/256 > bandwidth {
		bandwidth = int(volumehelper.RoundUpSize(int64(iops), 256))
	}
	if bandwidth > iops/4 {
		bandwidth = int(volumehelper.RoundUpSize(int64(iops), 4))
	}
	if bandwidth > 4000 {
		bandwidth = 4000
	}
	return bandwidth
}

// getVMSSInstanceName get instance name from vmss compute name, e.g. "aks-agentpool-20657377-vmss_2" -> "aks-agentpool-20657377-vmss000002"
func getVMSSInstanceName(computeName string) (string, error) {
	names := strings.Split(computeName, "_")
	if len(names) != 2 {
		return "", fmt.Errorf("invalid vmss compute name: %s", computeName)
	}

	instanceID, err := strconv.Atoi(names[1])
	if err != nil {
		return "", fmt.Errorf("parsing vmss compute name(%s) failed with %v", computeName, err)
	}
	return fmt.Sprintf("%s%06s", names[0], strconv.FormatInt(int64(instanceID), 36)), nil
}
