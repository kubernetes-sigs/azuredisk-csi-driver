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
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-03-01/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/volume/util/hostutil"
	"k8s.io/mount-utils"

	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	csicommon "sigs.k8s.io/azuredisk-csi-driver/pkg/csi-common"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

// CSIDriver defines the interface for a CSI driver.
type CSIDriver interface {
	csi.ControllerServer
	csi.NodeServer
	csi.IdentityServer

	Run(endpoint, kubeconfig string, disableAVSetNodes, testMode bool)

	// Ready returns a closed channel when the driver's Run function has completed initialization
	Ready() <-chan struct{}
}

type hostUtil interface {
	PathIsDevice(string) (bool, error)
}

// DriverCore contains fields common to both the V1 and V2 driver, and implements all interfaces of CSI drivers
type DriverCore struct {
	csicommon.CSIDriver
	ready        chan struct{}
	deviceHelper optimization.Interface
	nodeInfo     *optimization.NodeInfo
	ioHandler    azureutils.IOHandler
}

// Driver is the v1 implementation of the Azure Disk CSI Driver.
type Driver struct {
	DriverCore
	cloud       *azure.Cloud
	kubeconfig  string
	mounter     *mount.SafeFormatAndMount
	volumeLocks *volumehelper.VolumeLocks
	hostUtil    hostUtil
	// a timed cache GetDisk throttling
	getDiskThrottlingCache     *azcache.TimedCache
	perfOptimizationEnabled    bool
	cloudConfigSecretName      string
	cloudConfigSecretNamespace string
	customUserAgent            string
	userAgentSuffix            string
	useCSIProxyGAInterface     bool
	enableDiskOnlineResize     bool
	allowEmptyCloudConfig      bool
	enableAsyncAttach          bool
	enableListVolumes          bool
	enableListSnapshots        bool
	supportZone                bool
	getNodeInfoFromLabels      bool
	enableDiskCapacityCheck    bool
	enableTrafficManager       bool
	trafficManagerPort         int64
	disableUpdateCache         bool
	vmssCacheTTLInSeconds      int64
	vmType                     string
}

func NewDefaultDriverConfig() *azdiskv1beta2.AzDiskDriverConfiguration {
	return &azdiskv1beta2.AzDiskDriverConfiguration{
		ControllerConfig: azdiskv1beta2.ControllerConfiguration{
			DisableAVSetNodes:             consts.DefaultDisableAVSetNodes,
			VMType:                        consts.DefaultVMType,
			EnableDiskOnlineResize:        consts.DefaultEnableDiskOnlineResize,
			EnableAsyncAttach:             consts.DefaultEnableAsyncAttach,
			EnableListVolumes:             consts.DefaultEnableListVolumes,
			EnableListSnapshots:           consts.DefaultEnableListSnapshots,
			EnableDiskCapacityCheck:       consts.DefaultEnableDiskCapacityCheck,
			Enabled:                       consts.DefaultIsControllerPlugin,
			LeaseDurationInSec:            consts.DefaultControllerLeaseDurationInSec,
			LeaseRenewDeadlineInSec:       consts.DefaultControllerLeaseRenewDeadlineInSec,
			LeaseRetryPeriodInSec:         consts.DefaultControllerLeaseRetryPeriodInSec,
			LeaderElectionNamespace:       consts.ReleaseNamespace,
			PartitionName:                 consts.DefaultControllerPartitionName,
			WorkerThreads:                 consts.DefaultWorkerThreads,
			WaitForLunEnabled:             consts.DefaultWaitForLunEnabled,
			ReplicaVolumeAttachRetryLimit: consts.DefaultReplicaVolumeAttachRetryLimit,
		},
		NodeConfig: azdiskv1beta2.NodeConfiguration{
			VolumeAttachLimit:       consts.DefaultVolumeAttachLimit,
			SupportZone:             consts.DefaultSupportZone,
			EnablePerfOptimization:  consts.DefaultEnablePerfOptimization,
			UseCSIProxyGAInterface:  consts.DefaultUseCSIProxyGAInterface,
			GetNodeInfoFromLabels:   consts.DefaultGetNodeInfoFromLabels,
			Enabled:                 consts.DefaultIsNodePlugin,
			HeartbeatFrequencyInSec: consts.DefaultHeartbeatFrequencyInSec,
			PartitionName:           consts.DefaultNodePartitionName,
		},
		CloudConfig: azdiskv1beta2.CloudConfiguration{
			SecretName:                                       consts.DefaultCloudConfigSecretName,
			SecretNamespace:                                  consts.DefaultCloudConfigSecretNamespace,
			CustomUserAgent:                                  consts.DefaultCustomUserAgent,
			UserAgentSuffix:                                  consts.DefaultUserAgentSuffix,
			EnableTrafficManager:                             consts.DefaultEnableTrafficManager,
			TrafficManagerPort:                               consts.DefaultTrafficManagerPort,
			AllowEmptyCloudConfig:                            consts.DefaultAllowEmptyCloudConfig,
			DisableUpdateCache:                               consts.DefaultDisableUpdateCache,
			VMSSCacheTTLInSeconds:                            consts.DefaultVMSSCacheTTLInSeconds,
			EnableAzureClientAttachDetachRateLimiter:         consts.DefaultEnableAzureClientAttachDetachRateLimiter,
			AzureClientAttachDetachRateLimiterQPS:            consts.DefaultAzureClientAttachDetachRateLimiterQPS,
			AzureClientAttachDetachRateLimiterBucket:         consts.DefaultAzureClientAttachDetachRateLimiterBucket,
			AzureClientAttachDetachBatchInitialDelayInMillis: int(consts.DefaultAzureClientAttachDetachBatchInitialDelay.Milliseconds()),
		},
		ClientConfig: azdiskv1beta2.ClientConfiguration{
			Kubeconfig:      consts.DefaultKubeconfig,
			KubeClientQPS:   consts.DefaultKubeClientQPS,
			KubeClientBurst: consts.DefaultKubeClientBurst,
		},
		ObjectNamespace: consts.DefaultAzureDiskCrdNamespace,
		Endpoint:        consts.DefaultEndpoint,
		MetricsAddress:  consts.DefaultMetricsAddress,
		DriverName:      consts.DefaultDriverName,
		ProfilerAddress: consts.DefaultProfilerAddress,
	}
}

// newDriverV1 Creates a NewCSIDriver object. Assumes vendor version is equal to driver version &
// does not support optional driver plugin info manifest field. Refer to CSI spec for more details.
func newDriverV1(config *azdiskv1beta2.AzDiskDriverConfiguration) *Driver {
	driver := Driver{}
	driver.Name = config.DriverName
	driver.Version = driverVersion
	driver.NodeID = config.NodeConfig.NodeID
	driver.VolumeAttachLimit = config.NodeConfig.VolumeAttachLimit
	driver.ready = make(chan struct{})
	driver.perfOptimizationEnabled = config.NodeConfig.EnablePerfOptimization
	driver.cloudConfigSecretName = config.CloudConfig.SecretName
	driver.cloudConfigSecretNamespace = config.CloudConfig.SecretNamespace
	driver.customUserAgent = config.CloudConfig.CustomUserAgent
	driver.userAgentSuffix = config.CloudConfig.UserAgentSuffix
	driver.useCSIProxyGAInterface = config.NodeConfig.UseCSIProxyGAInterface
	driver.enableDiskOnlineResize = config.ControllerConfig.EnableDiskOnlineResize
	driver.allowEmptyCloudConfig = config.CloudConfig.AllowEmptyCloudConfig
	driver.enableAsyncAttach = config.ControllerConfig.EnableAsyncAttach
	driver.enableListVolumes = config.ControllerConfig.EnableListVolumes
	driver.enableListSnapshots = config.ControllerConfig.EnableListSnapshots
	driver.supportZone = config.NodeConfig.SupportZone
	driver.getNodeInfoFromLabels = config.NodeConfig.GetNodeInfoFromLabels
	driver.enableDiskCapacityCheck = config.ControllerConfig.EnableDiskCapacityCheck
	driver.vmssCacheTTLInSeconds = config.CloudConfig.VMSSCacheTTLInSeconds
	driver.vmType = config.ControllerConfig.VMType
	driver.disableUpdateCache = config.CloudConfig.DisableUpdateCache
	driver.enableTrafficManager = config.CloudConfig.EnableTrafficManager
	driver.trafficManagerPort = config.CloudConfig.TrafficManagerPort
	driver.volumeLocks = volumehelper.NewVolumeLocks()
	driver.ioHandler = azureutils.NewOSIOHandler()
	driver.hostUtil = hostutil.NewHostUtil()

	topologyKey = fmt.Sprintf("topology.%s/zone", driver.Name)

	cache, err := azcache.NewTimedcache(5*time.Minute, func(key string) (interface{}, error) {
		return nil, nil
	})
	if err != nil {
		klog.Fatalf("%v", err)
	}
	driver.getDiskThrottlingCache = cache
	return &driver
}

// Run driver initialization
func (d *Driver) Run(endpoint, kubeconfig string, disableAVSetNodes, testingMock bool) {
	versionMeta, err := GetVersionYAML(d.Name)
	if err != nil {
		klog.Fatalf("%v", err)
	}
	klog.Infof("\nDRIVER INFORMATION:\n-------------------\n%s\n\nStreaming logs below:", versionMeta)

	userAgent := GetUserAgent(d.Name, d.customUserAgent, d.userAgentSuffix)
	klog.V(2).Infof("driver userAgent: %s", userAgent)

	cloud, err := azureutils.GetCloudProvider(
		context.Background(),
		kubeconfig,
		d.vmType,
		disableAVSetNodes,
		d.NodeID,
		d.cloudConfigSecretName,
		d.cloudConfigSecretNamespace,
		userAgent,
		d.allowEmptyCloudConfig,
		consts.DefaultEnableAzureClientAttachDetachRateLimiter,
		consts.DefaultAzureClientAttachDetachRateLimiterQPS,
		consts.DefaultAzureClientAttachDetachRateLimiterBucket,
		d.enableTrafficManager,
		d.trafficManagerPort,
		d.disableUpdateCache,
		d.vmssCacheTTLInSeconds)
	if err != nil {
		klog.Fatalf("failed to get Azure Cloud Provider, error: %v", err)
	}
	d.cloud = cloud
	d.kubeconfig = kubeconfig

	d.deviceHelper = optimization.NewSafeDeviceHelper()

	if d.isPerfOptimizationEnabled() {
		klog.V(2).Infof("Starting to populate node and disk sku information.")

		instances, ok := cloud.Instances()
		if !ok {
			klog.Error("Failed to get instances from Azure cloud provider")
		} else {
			instanceType, err := instances.InstanceType(context.TODO(), types.NodeName(d.NodeID))
			if err != nil {
				klog.Errorf("Failed to get instance type from Azure cloud provider, nodeName: %v, error: %v", d.NodeID, err)
			} else {
				d.nodeInfo, err = optimization.NewNodeInfo(context.TODO(), instanceType)
				if err != nil {
					klog.Warningf("Failed to get node info. Error: %v", err)
				}
			}

		}
	}

	d.mounter, err = mounter.NewSafeMounter(d.useCSIProxyGAInterface)
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
	if d.enableListVolumes {
		controllerCap = append(controllerCap, csi.ControllerServiceCapability_RPC_LIST_VOLUMES, csi.ControllerServiceCapability_RPC_LIST_VOLUMES_PUBLISHED_NODES)
	}
	if d.enableListSnapshots {
		controllerCap = append(controllerCap, csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS)
	}

	d.AddControllerServiceCapabilities(controllerCap)
	d.AddVolumeCapabilityAccessModes(
		[]csi.VolumeCapability_AccessMode_Mode{
			csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
			csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER,
			csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER,
		})
	d.AddNodeServiceCapabilities([]csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
		csi.NodeServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
	})

	s := csicommon.NewNonBlockingGRPCServer()
	// Driver d act as IdentityServer, ControllerServer and NodeServer
	s.Start(endpoint, d, d, d, testingMock)

	// Signal that the driver is ready.
	d.signalReady()

	// Wait for the GRPC Server to exit
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

// isPerfOptimizationEnabled returns the value of the perfOptimizationEnabled field. It is intended for use with unit tests.
func (d *Driver) isPerfOptimizationEnabled() bool {
	return d.perfOptimizationEnabled
}

// setPerfOptimizationEnabled sets the value of the perfOptimizationEnabled field. It is intended for use with unit tests.
func (d *Driver) setPerfOptimizationEnabled(enabled bool) {
	d.perfOptimizationEnabled = enabled
}

func (d *DriverCore) Ready() <-chan struct{} {
	return d.ready
}

func (d *DriverCore) signalReady() {
	close(d.ready)
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

// getDeviceHelper returns the value of the deviceHelper field. It is intended for use with unit tests.
func (d *DriverCore) getDeviceHelper() optimization.Interface {
	return d.deviceHelper
}

// getNodeInfo returns the value of the nodeInfo field. It is intended for use with unit tests.
func (d *DriverCore) getNodeInfo() *optimization.NodeInfo {
	return d.nodeInfo
}

func (d *Driver) getHostUtil() hostUtil {
	return d.hostUtil
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
