//go:build azurediskv2
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
	"sync"
	"testing"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2021-07-01/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"
	testingexec "k8s.io/utils/exec/testing"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	csicommon "sigs.k8s.io/azuredisk-csi-driver/pkg/csi-common"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization/mockoptimization"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/provisioner"

	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

const (
	fakeObjNamespace = consts.DefaultAzureDiskCrdNamespace
)

type fakeDriverV2 struct {
	DriverV2
}

// NewFakeDriver returns a driver implementation suitable for use in unit tests.
func NewFakeDriver(t *testing.T) (FakeDriver, error) {
	return newFakeDriverV2(t)
}

func newFakeDriverV2(t *testing.T) (*fakeDriverV2, error) {
	klog.Warning("Using DriverV2")
	driver := fakeDriverV2{}
	driver.config = &azdiskv1beta2.AzDiskDriverConfiguration{}
	driver.Name = fakeDriverName
	driver.Version = fakeDriverVersion
	driver.NodeID = fakeNodeID
	driver.CSIDriver = *csicommon.NewFakeCSIDriver()
	driver.ready = make(chan struct{})
	driver.volumeLocks = volumehelper.NewVolumeLocks()
	driver.config.ObjectNamespace = fakeObjNamespace

	driver.VolumeAttachLimit = -1
	driver.config.NodeConfig.SupportZone = true
	driver.ioHandler = azureutils.NewFakeIOHandler()
	driver.hostUtil = azureutils.NewFakeHostUtil()
	driver.config.NodeConfig.UseCSIProxyGAInterface = true

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

	driver.azdiskClient = azdiskfakes.NewSimpleClientset()

	crdProvisioner, err := provisioner.NewFakeCrdProvisioner(driver.cloudProvisioner.(*provisioner.FakeCloudProvisioner))
	if err != nil {
		return nil, err
	}

	driver.crdProvisioner = crdProvisioner

	driver.deviceHelper = mockoptimization.NewMockInterface(ctrl)

	driver.deviceChecker = &deviceChecker{lock: sync.RWMutex{}, entry: nil}

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

	nodeInfo := driver.getNodeInfo()
	assert.NotEqual(t, nil, nodeInfo)
	dh := driver.getDeviceHelper()
	assert.NotEqual(t, nil, dh)

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

func (d *fakeDriverV2) getSnapshotInfo(snapshotID string) (string, string, string, error) {
	snapshotName, resourceGroup, err := d.cloudProvisioner.(*provisioner.FakeCloudProvisioner).GetSnapshotAndResourceNameFromSnapshotID(snapshotID)
	subID := azureutils.GetSubscriptionIDFromURI(snapshotID)

	if err != nil {
		return "", "", "", err
	}

	return snapshotName, resourceGroup, subID, err
}

func (d *fakeDriverV2) checkDiskCapacity(ctx context.Context, subscriptionID, resourceGroup, diskName string, requestGiB int) (bool, error) {
	return false, nil
}

func (d *fakeDriverV2) checkDiskExists(ctx context.Context, diskURI string) (*compute.Disk, error) {
	return &compute.Disk{}, nil
}

func (d *fakeDriverV2) setMounter(mounter *mount.SafeFormatAndMount) {
	d.nodeProvisioner.(*provisioner.FakeNodeProvisioner).SetMounter(mounter)
}

func (d *fakeDriverV2) setPathIsDeviceResult(path string, isDevice bool, err error) {
	d.nodeProvisioner.(*provisioner.FakeNodeProvisioner).SetIsBlockDevicePathResult(path, isDevice, err)
}

func (d *fakeDriverV2) getCrdProvisioner() CrdProvisioner {
	return d.crdProvisioner
}

func (d *fakeDriverV2) setCrdProvisioner(crdProvisioner CrdProvisioner) {
	d.crdProvisioner = crdProvisioner
}

func (d *DriverV2) setDiskThrottlingCache(key string, value string) {
}

func skipIfTestingDriverV2(t *testing.T) {
	t.Skip("Skipping test on DriverV2")
}

func isTestingDriverV2() bool {
	return true
}

func (d *fakeDriverV2) setPerfOptimizationEnabled(enabled bool) {
	d.config.NodeConfig.EnablePerfOptimization = enabled
	d.cloudProvisioner.(*provisioner.FakeCloudProvisioner).SetPerfOptimizationEnabled(enabled)
}

func (d *fakeDriverV2) getPerfOptimizationEnabled() bool {
	return d.config.NodeConfig.EnablePerfOptimization
}

func (d *fakeDriverV2) GetSourceDiskSize(ctx context.Context, subsID, resourceGroup, diskName string, curDepth, maxDepth int) (*int32, error) {
	var returnVal int32 = 0
	return &returnVal, nil
}

func (d *fakeDriverV2) getSnapshotByID(ctx context.Context, subsID, resourceGroup, snapshotID, sourceVolumeID string) (*csi.Snapshot, error) {
	snapshotVal := csi.Snapshot{}
	return &snapshotVal, nil
}

func (d *fakeDriverV2) getVolumeLocks() *volumehelper.VolumeLocks {
	return d.volumeLocks
}
