//go:build azurediskv2
// +build azurediskv2

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
	"errors"
	"flag"
	"fmt"
	"os"
	"reflect"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/volume/util/hostutil"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	csicommon "sigs.k8s.io/azuredisk-csi-driver/pkg/csi-common"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	consts "sigs.k8s.io/cloud-provider-azure/pkg/consts"
)

var useDriverV2 = flag.Bool("temp-use-driver-v2", false, "A temporary flag to enable early test and development of Azure Disk CSI Driver V2. This will be removed in the future.")

// DriverV2 implements all interfaces of CSI drivers
type DriverV2 struct {
	DriverCore
	volumeLocks *volumehelper.VolumeLocks
}

// NewDriver creates a Driver or DriverV2 object depending on the --temp-use-driver-v2 flag.
func NewDriver(options *DriverOptions) CSIDriver {
	if !*useDriverV2 {
		return newDriverV1(options)
	} else {
		return newDriverV2(options)
	}
}

// newDriverV2 Creates a NewCSIDriver object. Assumes vendor version is equal to driver version &
// does not support optional driver plugin info manifest field. Refer to CSI spec for more details.
func newDriverV2(options *DriverOptions) *DriverV2 {
	klog.Warning("Using DriverV2")
	driver := DriverV2{}
	driver.Name = options.DriverName
	driver.Version = driverVersion
	driver.NodeID = options.NodeID
	driver.VolumeAttachLimit = options.VolumeAttachLimit
	driver.volumeLocks = volumehelper.NewVolumeLocks()
	driver.perfOptimizationEnabled = options.EnablePerfOptimization
	driver.cloudConfigSecretName = options.CloudConfigSecretName
	driver.cloudConfigSecretNamespace = options.CloudConfigSecretNamespace
	driver.customUserAgent = options.CustomUserAgent
	driver.userAgentSuffix = options.UserAgentSuffix
	driver.useCSIProxyGAInterface = options.UseCSIProxyGAInterface
	driver.enableOtelTracing = options.EnableOtelTracing
	driver.ioHandler = azureutils.NewOSIOHandler()
	driver.hostUtil = hostutil.NewHostUtil()
	driver.disableAVSetNodes = options.DisableAVSetNodes
	driver.endpoint = options.Endpoint

	topologyKey = fmt.Sprintf("topology.%s/zone", driver.Name)
	userAgent := GetUserAgent(driver.Name, driver.customUserAgent, driver.userAgentSuffix)
	klog.V(2).Infof("driver userAgent: %s", userAgent)

	kubeClient, err := azureutils.GetKubeClient(options.Kubeconfig)
	if err != nil {
		klog.Warningf("get kubeconfig(%s) failed with error: %v", options.Kubeconfig, err)
		if !os.IsNotExist(err) && !errors.Is(err, rest.ErrNotInCluster) {
			klog.Fatalf("failed to get KubeClient: %v", err)
		}
	}
	driver.kubeClient = kubeClient

	cloud, err := azureutils.GetCloudProviderFromClient(context.Background(), kubeClient, driver.cloudConfigSecretName, driver.cloudConfigSecretNamespace,
		userAgent, driver.allowEmptyCloudConfig, driver.enableTrafficManager, driver.trafficManagerPort)
	if err != nil {
		klog.Fatalf("failed to get Azure Cloud Provider, error: %v", err)
	}
	driver.cloud = cloud

	if driver.cloud != nil {
		driver.diskController = NewManagedDiskController(driver.cloud)
		driver.diskController.DisableUpdateCache = driver.disableUpdateCache
		driver.diskController.AttachDetachInitialDelayInMs = int(driver.attachDetachInitialDelayInMs)
		driver.clientFactory = driver.cloud.ComputeClientFactory
		if driver.vmType != "" {
			klog.V(2).Infof("override VMType(%s) in cloud config as %s", driver.cloud.VMType, driver.vmType)
			driver.cloud.VMType = driver.vmType
		}

		if driver.NodeID == "" {
			// Disable UseInstanceMetadata for controller to mitigate a timeout issue using IMDS
			// https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/168
			klog.V(2).Infof("disable UseInstanceMetadata for controller")
			driver.cloud.Config.UseInstanceMetadata = false

			if driver.cloud.VMType == consts.VMTypeStandard && driver.cloud.DisableAvailabilitySetNodes {
				klog.V(2).Infof("set DisableAvailabilitySetNodes as false since VMType is %s", driver.cloud.VMType)
				driver.cloud.DisableAvailabilitySetNodes = false
			}

			if driver.cloud.VMType == consts.VMTypeVMSS && !driver.cloud.DisableAvailabilitySetNodes && driver.disableAVSetNodes {
				klog.V(2).Infof("DisableAvailabilitySetNodes for controller since current VMType is vmss")
				driver.cloud.DisableAvailabilitySetNodes = true
			}
			klog.V(2).Infof("cloud: %s, location: %s, rg: %s, VMType: %s, PrimaryScaleSetName: %s, PrimaryAvailabilitySetName: %s, DisableAvailabilitySetNodes: %v", driver.cloud.Cloud, driver.cloud.Location, driver.cloud.ResourceGroup, driver.cloud.VMType, driver.cloud.PrimaryScaleSetName, driver.cloud.PrimaryAvailabilitySetName, driver.cloud.DisableAvailabilitySetNodes)
		}
	}

	driver.deviceHelper = optimization.NewSafeDeviceHelper()

	if driver.getPerfOptimizationEnabled() {
		driver.nodeInfo, err = optimization.NewNodeInfo(context.TODO(), driver.getCloud(), driver.NodeID)
		if err != nil {
			klog.Errorf("Failed to get node info. Error: %v", err)
		}
	}

	driver.mounter, err = mounter.NewSafeMounter(driver.enableWindowsHostProcess, driver.useCSIProxyGAInterface)
	if err != nil {
		klog.Fatalf("Failed to get safe mounter. Error: %v", err)
	}

	driver.AddControllerServiceCapabilities(
		[]csi.ControllerServiceCapability_RPC_Type{
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
			csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
			csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
			csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
			csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
			csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
			csi.ControllerServiceCapability_RPC_LIST_VOLUMES_PUBLISHED_NODES,
			csi.ControllerServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
		})
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
func (d *DriverV2) Run(ctx context.Context) error {
	versionMeta, err := GetVersionYAML(d.Name)
	if err != nil {
		klog.Fatalf("%v", err)
	}
	klog.Infof("\nDRIVER INFORMATION:\n-------------------\n%s\n\nStreaming logs below:", versionMeta)

	grpcInterceptor := grpc.UnaryInterceptor(csicommon.LogGRPC)
	if d.enableOtelTracing {
		grpcInterceptor = grpc.ChainUnaryInterceptor(csicommon.LogGRPC, otelgrpc.UnaryServerInterceptor())
	}
	opts := []grpc.ServerOption{
		grpcInterceptor,
	}
	s := grpc.NewServer(opts...)
	csi.RegisterIdentityServer(s, d)
	csi.RegisterControllerServer(s, d)
	csi.RegisterNodeServer(s, d)

	go func() {
		//graceful shutdown
		<-ctx.Done()
		s.GracefulStop()
	}()
	// Driver d act as IdentityServer, ControllerServer and NodeServer
	listener, err := csicommon.Listen(ctx, d.endpoint)
	if err != nil {
		klog.Fatalf("failed to listen to endpoint, error: %v", err)
	}
	err = s.Serve(listener)
	if errors.Is(err, grpc.ErrServerStopped) {
		klog.Infof("gRPC server stopped serving")
		return nil
	}
	return err
}

func (d *DriverV2) checkDiskExists(ctx context.Context, diskURI string) (*armcompute.Disk, error) {
	diskName, err := azureutils.GetDiskName(diskURI)
	if err != nil {
		return nil, err
	}

	resourceGroup, err := azureutils.GetResourceGroupFromURI(diskURI)
	if err != nil {
		return nil, err
	}

	subsID := azureutils.GetSubscriptionIDFromURI(diskURI)
	diskClient, err := d.clientFactory.GetDiskClientForSub(subsID)
	if err != nil {
		return nil, err
	}

	disk, err := diskClient.Get(ctx, resourceGroup, diskName)
	if err != nil {
		return nil, err
	}

	return disk, nil
}

func (d *DriverV2) checkDiskCapacity(ctx context.Context, subsID, resourceGroup, diskName string, requestGiB int) (bool, error) {
	diskClient, err := d.clientFactory.GetDiskClientForSub(subsID)
	if err != nil {
		return false, err
	}
	disk, err := diskClient.Get(ctx, resourceGroup, diskName)
	// Because we can not judge the reason of the error. Maybe the disk does not exist.
	// So here we do not handle the error.
	if err == nil {
		if !reflect.DeepEqual(disk, &armcompute.Disk{}) && disk.Properties != nil && disk.Properties.DiskSizeGB != nil && int(*disk.Properties.DiskSizeGB) != requestGiB {
			return false, status.Errorf(codes.AlreadyExists, "the request volume already exists, but its capacity(%v) is different from (%v)", *disk.Properties.DiskSizeGB, requestGiB)
		}
	}
	return true, nil
}

func (d *DriverV2) getVolumeLocks() *volumehelper.VolumeLocks {
	return d.volumeLocks
}
