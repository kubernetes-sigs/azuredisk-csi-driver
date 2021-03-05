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
	"flag"
	"reflect"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-06-30/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"k8s.io/klog/v2"

	csicommon "sigs.k8s.io/azuredisk-csi-driver/pkg/csi-common"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
)

var useDriverV2 = flag.Bool("temp-use-driver-v2", false, "A temporary flag to enable early test and development of Azure Disk CSI Driver V2. This will be removed in the future.")

// DriverV2 implements all interfaces of CSI drivers
type DriverV2 struct {
	DriverCore
	volumeLocks *volumehelper.VolumeLocks
}

// NewDriver creates a Driver or DriverV2 object depending on the --temp-use-driver-v2 flag.
func NewDriver(nodeID string) CSIDriver {
	var d CSIDriver

	if !*useDriverV2 {
		d = newDriverV1(nodeID)
	} else {
		d = newDriverV2(nodeID)
	}

	return d
}

// newDriverV2 Creates a NewCSIDriver object. Assumes vendor version is equal to driver version &
// does not support optional driver plugin info manifest field. Refer to CSI spec for more details.
func newDriverV2(nodeID string) *DriverV2 {
	klog.Warning("Using DriverV2")
	driver := DriverV2{}
	driver.Name = DriverName
	driver.Version = driverVersion
	driver.NodeID = nodeID
	driver.volumeLocks = volumehelper.NewVolumeLocks()
	return &driver
}

// Run driver initialization
func (d *DriverV2) Run(endpoint, kubeconfig string, testBool bool) {
	versionMeta, err := GetVersionYAML()
	if err != nil {
		klog.Fatalf("%v", err)
	}
	klog.Infof("\nDRIVER INFORMATION:\n-------------------\n%s\n\nStreaming logs below:", versionMeta)
	cloud, err := GetCloudProvider(kubeconfig)
	if err != nil || cloud.TenantID == "" || cloud.SubscriptionID == "" {
		klog.Fatalf("failed to get Azure Cloud Provider, error: %v", err)
	}
	d.cloud = cloud

	if d.NodeID == "" {
		// Disable UseInstanceMetadata for controller to mitigate a timeout issue using IMDS
		// https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/168
		klog.Infoln("disable UseInstanceMetadata for controller")
		d.cloud.Config.UseInstanceMetadata = false
	}

	d.mounter, err = mounter.NewSafeMounter()
	if err != nil {
		klog.Fatalf("Failed to get safe mounter. Error: %v", err)
	}

	d.AddControllerServiceCapabilities(
		[]csi.ControllerServiceCapability_RPC_Type{
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
			csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
			csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
			csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
			csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
			csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
			csi.ControllerServiceCapability_RPC_LIST_VOLUMES_PUBLISHED_NODES,
		})
	d.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER})
	d.AddNodeServiceCapabilities([]csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
	})

	s := csicommon.NewNonBlockingGRPCServer()
	// Driver d act as IdentityServer, ControllerServer and NodeServer
	s.Start(endpoint, d, d, d, testBool)
	s.Wait()
}

func (d *DriverV2) checkDiskExists(ctx context.Context, diskURI string) error {
	diskName, err := GetDiskName(diskURI)
	if err != nil {
		return err
	}

	resourceGroup, err := GetResourceGroupFromURI(diskURI)
	if err != nil {
		return err
	}

	if _, rerr := d.cloud.DisksClient.Get(ctx, resourceGroup, diskName); rerr != nil {
		return rerr.Error()
	}

	return nil
}

func (d *DriverV2) checkDiskCapacity(ctx context.Context, resourceGroup, diskName string, requestGiB int) (bool, error) {
	disk, err := d.cloud.DisksClient.Get(ctx, resourceGroup, diskName)
	// Because we can not judge the reason of the error. Maybe the disk does not exist.
	// So here we do not handle the error.
	if err == nil {
		if !reflect.DeepEqual(disk, compute.Disk{}) && disk.DiskSizeGB != nil && int(*disk.DiskSizeGB) != requestGiB {
			return false, status.Errorf(codes.AlreadyExists, "the request volume already exists, but its capacity(%v) is different from (%v)", *disk.DiskProperties.DiskSizeGB, requestGiB)
		}
	}
	return true, nil
}
