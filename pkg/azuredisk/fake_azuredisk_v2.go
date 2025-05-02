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
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"go.uber.org/mock/gomock"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2"
	testingexec "k8s.io/utils/exec/testing"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	csicommon "sigs.k8s.io/azuredisk-csi-driver/pkg/csi-common"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization/mockoptimization"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

type fakeDriverV2 struct {
	DriverV2
}

// NewFakeDriver returns a driver implementation suitable for use in unit tests.
func NewFakeDriver(ctrl *gomock.Controller) (FakeDriver, error) {
	var d FakeDriver
	var err error

	if !*useDriverV2 {
		d, err = newFakeDriverV1(ctrl)
	} else {
		d, err = newFakeDriverV2(ctrl)
	}

	return d, err
}

func newFakeDriverV2(ctrl *gomock.Controller) (*fakeDriverV2, error) {
	klog.Warning("Using DriverV2")
	driver := fakeDriverV2{}
	driver.Name = fakeDriverName
	driver.Version = fakeDriverVersion
	driver.NodeID = fakeNodeID
	driver.CSIDriver = *csicommon.NewFakeCSIDriver()
	driver.volumeLocks = volumehelper.NewVolumeLocks()
	driver.VolumeAttachLimit = -1
	driver.supportZone = true
	driver.ioHandler = azureutils.NewFakeIOHandler()
	driver.hostUtil = azureutils.NewFakeHostUtil()
	driver.useCSIProxyGAInterface = true
	driver.allowEmptyCloudConfig = true
	driver.endpoint = "tcp://127.0.0.1:0"
	driver.disableAVSetNodes = true
	driver.kubeClient = fake.NewSimpleClientset()

	driver.cloud = azure.GetTestCloud(ctrl)
	driver.diskController = NewManagedDiskController(driver.cloud)
	driver.clientFactory = driver.cloud.ComputeClientFactory

	mounter, err := mounter.NewSafeMounter(true, true, driver.useCSIProxyGAInterface, int(driver.maxConcurrentFormat), time.Duration(driver.concurrentFormatTimeout)*time.Second)
	if err != nil {
		return nil, err
	}

	driver.mounter = mounter

	driver.deviceHelper = mockoptimization.NewMockInterface(ctrl)

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
			csi.ControllerServiceCapability_RPC_MODIFY_VOLUME,
		})
	driver.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER})
	driver.AddNodeServiceCapabilities([]csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
	})
	driver.AddGroupControllerServiceCapabilities([]csi.GroupControllerServiceCapability_RPC_Type{
		csi.GroupControllerServiceCapability_RPC_CREATE_DELETE_GET_VOLUME_GROUP_SNAPSHOT,
	})

	return &driver, nil
}

func (d *fakeDriverV2) setNextCommandOutputScripts(scripts ...testingexec.FakeAction) {
	d.mounter.Exec.(*mounter.FakeSafeMounter).SetNextCommandOutputScripts(scripts...)
}
func (d *fakeDriverV2) getClientFactory() azclient.ClientFactory {
	return d.clientFactory
}

func (d *DriverV2) setThrottlingCache(key string, value string) {
}
