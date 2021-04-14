// +build azurediskv2

/*
Copyright 2020 The Kubernetes Authors.

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
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/mock/gomock"
	"k8s.io/klog/v2"
	testingexec "k8s.io/utils/exec/testing"
	csicommon "sigs.k8s.io/azuredisk-csi-driver/pkg/csi-common"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/provisioner"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

type fakeDriverV2 struct {
	DriverV2
}

// NewFakeDriver returns a driver implementation suitable for use in unit tests.
func NewFakeDriver(t *testing.T) (FakeDriver, error) {
	var d FakeDriver
	var err error

	if !*useDriverV2 {
		d, err = newFakeDriverV1(t)
	} else {
		d, err = newFakeDriverV2(t)
	}

	return d, err
}

func newFakeDriverV2(t *testing.T) (*fakeDriverV2, error) {
	klog.Warning("Using DriverV2")
	driver := fakeDriverV2{}
	driver.Name = fakeDriverName
	driver.Version = fakeDriverVersion
	driver.NodeID = fakeNodeID
	driver.CSIDriver = *csicommon.NewFakeCSIDriver()
	driver.volumeLocks = volumehelper.NewVolumeLocks()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cloudProvisioner, err := provisioner.NewFakeCloudProvisioner(ctrl)
	if err != nil {
		return nil, err
	}

	driver.cloudProvisioner = cloudProvisioner

	nodeProvisioner, err := provisioner.NewFakeNodeProvisioner()
	if err != nil {
		return nil, err
	}

	driver.nodeProvisioner = nodeProvisioner

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
		})
	driver.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER})
	driver.AddNodeServiceCapabilities([]csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
	})

	return &driver, nil
}

func (d *fakeDriverV2) setNextCommandOutputScripts(scripts ...testingexec.FakeAction) {
	d.nodeProvisioner.(*provisioner.FakeNodeProvisioner).SetNextCommandOutputScripts(scripts...)
}

func (d *fakeDriverV2) setIsBlockDevicePathError(path string, isDevice bool, result error) {
	d.nodeProvisioner.(*provisioner.FakeNodeProvisioner).SetIsBlockDevicePathResult(path, isDevice, result)
}

func (d *fakeDriverV2) getCloud() *provider.Cloud {
	return d.cloudProvisioner.(*provisioner.FakeCloudProvisioner).GetCloud()
}

func (d *fakeDriverV2) setCloud(cloud *provider.Cloud) {
	d.cloudProvisioner.(*provisioner.FakeCloudProvisioner).SetCloud(cloud)
}

func (d *fakeDriverV2) checkDiskCapacity(ctx context.Context, resourceGroup, diskName string, requestGiB int) (bool, error) {
	return d.cloudProvisioner.(*provisioner.FakeCloudProvisioner).CheckDiskCapacity(ctx, resourceGroup, diskName, requestGiB)
}

func (d *fakeDriverV2) getSnapshotInfo(snapshotID string) (string, string, error) {
	return d.cloudProvisioner.(*provisioner.FakeCloudProvisioner).GetSnapshotAndResourceNameFromSnapshotID(snapshotID)
}

func (d *fakeDriverV2) getSnapshotByID(ctx context.Context, resourceGroup string, snapshotName string, sourceVolumeID string) (*csi.Snapshot, error) {
	return d.cloudProvisioner.(*provisioner.FakeCloudProvisioner).GetSnapshotByID(ctx, resourceGroup, snapshotName, sourceVolumeID)
}

func (d *fakeDriverV2) GetSourceDiskSize(ctx context.Context, resourceGroup, diskName string, curDepth, maxDepth int) (*int32, error) {
	return d.cloudProvisioner.(*provisioner.FakeCloudProvisioner).GetSourceDiskSize(ctx, resourceGroup, diskName, curDepth, maxDepth)
}

func skipIfTestingDriverV2(t *testing.T) {
	if *useDriverV2 {
		t.Skip("Skipping test on DriverV2")
	}
}

func isTestingDriverV2() bool {
	return *useDriverV2
}
