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
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-03-01/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"
	testingexec "k8s.io/utils/exec/testing"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	csicommon "sigs.k8s.io/azuredisk-csi-driver/pkg/csi-common"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization/mockoptimization"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

const (
	fakeDriverName    = "disk.csi.azure.com"
	fakeNodeID        = "fakeNodeID"
	fakeDriverVersion = "fakeDriverVersion"
)

var (
	stdVolumeCapability = &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{
			Mount: &csi.VolumeCapability_MountVolume{},
		},
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
	}
	stdVolumeCapabilities = []*csi.VolumeCapability{
		stdVolumeCapability,
	}
	stdCapacityRange = &csi.CapacityRange{
		RequiredBytes: volumehelper.GiBToBytes(10),
		LimitBytes:    volumehelper.GiBToBytes(15),
	}
)

// FakeDriver defines an interface unit tests use to test either the v1 or v2 implementation of the Azure Disk CSI Driver.
type FakeDriver interface {
	CSIDriver

	GetSourceDiskSize(ctx context.Context, subsID, resourceGroup, diskName string, curDepth, maxDepth int) (*int32, error)

	setNextCommandOutputScripts(scripts ...testingexec.FakeAction)
	setIsBlockDevicePathError(string, bool, error)

	getVolumeLocks() *volumehelper.VolumeLocks
	setControllerCapabilities([]*csi.ControllerServiceCapability)
	setNodeCapabilities([]*csi.NodeServiceCapability)
	setName(string)
	setNodeID(string)
	setVersion(version string)
	getCloud() *provider.Cloud
	setCloud(*provider.Cloud)
	getCrdProvisioner() CrdProvisioner
	setCrdProvisioner(crdProvisioner CrdProvisioner)

	getDeviceHelper() optimization.Interface
	setPerfOptimizationEnabled(bool)
	getPerfOptimizationEnabled() bool
	setMounter(*mount.SafeFormatAndMount)
	setPathIsDeviceResult(path string, isDevice bool, err error)

	checkDiskCapacity(context.Context, string, string, string, int) (bool, error)
	checkDiskExists(ctx context.Context, diskURI string) (*compute.Disk, error)
	getSnapshotInfo(string) (string, string, string, error)

	getSnapshotByID(context.Context, string, string, string, string) (*csi.Snapshot, error)
	ensureMountPoint(string) (bool, error)

	setDiskThrottlingCache(key string, value string)
}

type fakeDriverV1 struct {
	Driver
}

func newFakeDriverV1(t *testing.T) (*fakeDriverV1, error) {
	driver := fakeDriverV1{}
	driver.Name = fakeDriverName
	driver.Version = fakeDriverVersion
	driver.NodeID = fakeNodeID
	driver.CSIDriver = *csicommon.NewFakeCSIDriver()
	driver.ready = make(chan struct{})
	driver.volumeLocks = volumehelper.NewVolumeLocks()
	driver.VolumeAttachLimit = -1
	driver.supportZone = true
	driver.ioHandler = azureutils.NewFakeIOHandler()
	driver.hostUtil = azureutils.NewFakeHostUtil()
	driver.useCSIProxyGAInterface = true

	driver.VolumeAttachLimit = -1
	driver.ioHandler = azureutils.NewFakeIOHandler()
	driver.hostUtil = azureutils.NewFakeHostUtil()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	driver.cloud = provider.GetTestCloud(ctrl)
	mounter, err := mounter.NewSafeMounter(driver.useCSIProxyGAInterface)
	if err != nil {
		return nil, err
	}

	driver.mounter = mounter

	cache, err := azcache.NewTimedcache(time.Minute, func(key string) (interface{}, error) {
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	driver.getDiskThrottlingCache = cache

	mockDeviceHelper := mockoptimization.NewMockInterface(ctrl)
	driver.deviceHelper = mockDeviceHelper

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

func (d *fakeDriverV1) setNextCommandOutputScripts(scripts ...testingexec.FakeAction) {
	d.mounter.Exec.(*mounter.FakeSafeMounter).SetNextCommandOutputScripts(scripts...)
}

func (d *fakeDriverV1) setIsBlockDevicePathError(path string, isDevice bool, result error) {
	klog.Warning("setIsBlockDevicePathError ignored for driver v1")
}

func (d *fakeDriverV1) getCloud() *provider.Cloud {
	return d.cloud
}

func (d *fakeDriverV1) setCloud(cloud *provider.Cloud) {
	d.cloud = cloud
}

func (d *fakeDriverV1) setPathIsDeviceResult(path string, isDevice bool, err error) {
	d.getHostUtil().(*azureutils.FakeHostUtil).SetPathIsDeviceResult(path, isDevice, err)
}

func (d *fakeDriverV1) setDiskThrottlingCache(key string, value string) {
	d.getDiskThrottlingCache.Set(key, value)
}

func (d *fakeDriverV1) getCrdProvisioner() CrdProvisioner { return nil }

func (d *fakeDriverV1) setCrdProvisioner(crdProvisioner CrdProvisioner) {}

func createVolumeCapabilities(accessMode csi.VolumeCapability_AccessMode_Mode) []*csi.VolumeCapability {
	return []*csi.VolumeCapability{
		createVolumeCapability(accessMode),
	}
}

func createVolumeCapability(accessMode csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability {
	return &csi.VolumeCapability{
		AccessType: &csi.VolumeCapability_Mount{
			Mount: &csi.VolumeCapability_MountVolume{},
		},
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: accessMode,
		},
	}
}

func (d *Driver) setMounter(mounter *mount.SafeFormatAndMount) {
	d.mounter = mounter
}
