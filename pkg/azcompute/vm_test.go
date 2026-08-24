/*
Copyright 2026 The Kubernetes Authors.

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

package azcompute

import (
	"context"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/diskclient/mock_diskclient"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/mock_azclient"
	mockvmclient "sigs.k8s.io/cloud-provider-azure/pkg/azclient/virtualmachineclient/mock_virtualmachineclient"
	mockvmssvmclient "sigs.k8s.io/cloud-provider-azure/pkg/azclient/virtualmachinescalesetvmclient/mock_virtualmachinescalesetvmclient"
)

const (
	testDiskURI = "/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/disks/disk1"
)

func staticResolver(providerID string) NodeProviderIDResolver {
	return func(context.Context, string) (string, error) { return providerID, nil }
}

func TestAzclientVMDiskVMSet_AttachDisk(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	factory := mock_azclient.NewMockClientFactory(ctrl)
	vmClient := mockvmclient.NewMockInterface(ctrl)
	factory.EXPECT().GetVirtualMachineClient().Return(vmClient).AnyTimes()

	existingVM := &armcompute.VirtualMachine{
		Name:     to.Ptr("aks-nodepool1-000000"),
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineProperties{
			StorageProfile: &armcompute.StorageProfile{DataDisks: nil},
		},
	}
	vmClient.EXPECT().Get(gomock.Any(), "rg1", "aks-nodepool1-000000", gomock.Any()).Return(existingVM, nil)

	var pushed *armcompute.VirtualMachine
	vmClient.EXPECT().CreateOrUpdate(gomock.Any(), "rg1", "aks-nodepool1-000000", gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, vm armcompute.VirtualMachine) (*armcompute.VirtualMachine, error) {
			pushed = &vm
			return &vm, nil
		})

	s := NewVM(factory, staticResolver(testStandardProviderID), false)
	diskMap := map[string]*AttachDiskOptions{
		testDiskURI: {DiskName: "disk1", Lun: 0},
	}
	err := s.AttachDisk(context.Background(), types.NodeName("node1"), diskMap)
	assert.NoError(t, err)

	assert.NotNil(t, pushed)
	assert.Len(t, pushed.Properties.StorageProfile.DataDisks, 1)
	got := pushed.Properties.StorageProfile.DataDisks[0]
	assert.Equal(t, testDiskURI, *got.ManagedDisk.ID)
	assert.Equal(t, int32(0), *got.Lun)
}

func TestAzclientVMDiskVMSet_DetachDisk(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	factory := mock_azclient.NewMockClientFactory(ctrl)
	vmClient := mockvmclient.NewMockInterface(ctrl)
	factory.EXPECT().GetVirtualMachineClient().Return(vmClient).AnyTimes()

	existingVM := &armcompute.VirtualMachine{
		Name:     to.Ptr("aks-nodepool1-000000"),
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineProperties{
			StorageProfile: &armcompute.StorageProfile{
				DataDisks: []*armcompute.DataDisk{
					{Name: to.Ptr("disk1"), Lun: to.Ptr(int32(0)), ManagedDisk: &armcompute.ManagedDiskParameters{ID: to.Ptr(testDiskURI)}},
				},
			},
		},
	}
	vmClient.EXPECT().Get(gomock.Any(), "rg1", "aks-nodepool1-000000", gomock.Any()).Return(existingVM, nil)

	var pushed *armcompute.VirtualMachine
	vmClient.EXPECT().CreateOrUpdate(gomock.Any(), "rg1", "aks-nodepool1-000000", gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, vm armcompute.VirtualMachine) (*armcompute.VirtualMachine, error) {
			pushed = &vm
			return &vm, nil
		})

	s := NewVM(factory, staticResolver(testStandardProviderID), false)
	err := s.DetachDisk(context.Background(), types.NodeName("node1"), map[string]string{testDiskURI: "disk1"}, false)
	assert.NoError(t, err)

	assert.NotNil(t, pushed)
	assert.Len(t, pushed.Properties.StorageProfile.DataDisks, 1)
	assert.True(t, ptr.Deref(pushed.Properties.StorageProfile.DataDisks[0].ToBeDetached, false),
		"detached disk should be marked ToBeDetached")
}

func TestAzclientVMDiskVMSet_AttachDisk_FilterNonExistingOnNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	factory := mock_azclient.NewMockClientFactory(ctrl)
	vmClient := mockvmclient.NewMockInterface(ctrl)
	diskClient := mock_diskclient.NewMockInterface(ctrl)
	factory.EXPECT().GetVirtualMachineClient().Return(vmClient).AnyTimes()
	factory.EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()

	existingVM := &armcompute.VirtualMachine{
		Name:     to.Ptr("aks-nodepool1-000000"),
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineProperties{
			StorageProfile: &armcompute.StorageProfile{DataDisks: nil},
		},
	}
	vmClient.EXPECT().Get(gomock.Any(), "rg1", "aks-nodepool1-000000", gomock.Any()).Return(existingVM, nil)

	notFound := &azcore.ResponseError{StatusCode: http.StatusNotFound}
	// The disk no longer exists, so it must be filtered out and the retry sees 0 disks.
	diskClient.EXPECT().Get(gomock.Any(), "rg1", "disk1").Return(nil, notFound)

	gomock.InOrder(
		vmClient.EXPECT().CreateOrUpdate(gomock.Any(), "rg1", "aks-nodepool1-000000", gomock.Any()).Return(nil, notFound),
		vmClient.EXPECT().CreateOrUpdate(gomock.Any(), "rg1", "aks-nodepool1-000000", gomock.Any()).
			DoAndReturn(func(_ context.Context, _, _ string, vm armcompute.VirtualMachine) (*armcompute.VirtualMachine, error) {
				assert.Empty(t, vm.Properties.StorageProfile.DataDisks, "missing disk should have been filtered before retry")
				return &vm, nil
			}),
	)

	s := NewVM(factory, staticResolver(testStandardProviderID), false)
	diskMap := map[string]*AttachDiskOptions{
		testDiskURI: {DiskName: "disk1", Lun: 0},
	}
	err := s.AttachDisk(context.Background(), types.NodeName("node1"), diskMap)
	assert.NoError(t, err)
}

func TestAzclientVMSSDiskVMSet_AttachDisk(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	factory := mock_azclient.NewMockClientFactory(ctrl)
	vmssClient := mockvmssvmclient.NewMockInterface(ctrl)
	factory.EXPECT().GetVirtualMachineScaleSetVMClient().Return(vmssClient).AnyTimes()

	existingVM := &armcompute.VirtualMachineScaleSetVM{
		InstanceID: to.Ptr("3"),
		Properties: &armcompute.VirtualMachineScaleSetVMProperties{
			StorageProfile: &armcompute.StorageProfile{DataDisks: nil},
		},
	}
	vmssClient.EXPECT().Get(gomock.Any(), "rg1", "aks-nodepool1-vmss", "3").Return(existingVM, nil)

	var pushed *armcompute.VirtualMachineScaleSetVM
	vmssClient.EXPECT().Update(gomock.Any(), "rg1", "aks-nodepool1-vmss", "3", gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _, _ string, vm armcompute.VirtualMachineScaleSetVM) (*armcompute.VirtualMachineScaleSetVM, error) {
			pushed = &vm
			return &vm, nil
		})

	s := NewVMSS(factory, staticResolver(testVMSSProviderID), false)
	diskMap := map[string]*AttachDiskOptions{
		testDiskURI: {DiskName: "disk1", Lun: 1},
	}
	err := s.AttachDisk(context.Background(), types.NodeName("node1"), diskMap)
	assert.NoError(t, err)

	assert.NotNil(t, pushed)
	assert.Len(t, pushed.Properties.StorageProfile.DataDisks, 1)
	assert.Equal(t, int32(1), *pushed.Properties.StorageProfile.DataDisks[0].Lun)
}

func TestAzclientVMSSDiskVMSet_AttachDisk_FilterNonExistingOnNotFound(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	factory := mock_azclient.NewMockClientFactory(ctrl)
	vmssClient := mockvmssvmclient.NewMockInterface(ctrl)
	diskClient := mock_diskclient.NewMockInterface(ctrl)
	factory.EXPECT().GetVirtualMachineScaleSetVMClient().Return(vmssClient).AnyTimes()
	factory.EXPECT().GetDiskClientForSub(gomock.Any()).Return(diskClient, nil).AnyTimes()

	existingVM := &armcompute.VirtualMachineScaleSetVM{
		InstanceID: to.Ptr("3"),
		Properties: &armcompute.VirtualMachineScaleSetVMProperties{StorageProfile: &armcompute.StorageProfile{}},
	}
	vmssClient.EXPECT().Get(gomock.Any(), "rg1", "aks-nodepool1-vmss", "3").Return(existingVM, nil)

	notFound := &azcore.ResponseError{StatusCode: http.StatusNotFound}
	diskClient.EXPECT().Get(gomock.Any(), "rg1", "disk1").Return(nil, notFound)

	gomock.InOrder(
		vmssClient.EXPECT().Update(gomock.Any(), "rg1", "aks-nodepool1-vmss", "3", gomock.Any()).Return(nil, notFound),
		vmssClient.EXPECT().Update(gomock.Any(), "rg1", "aks-nodepool1-vmss", "3", gomock.Any()).
			DoAndReturn(func(_ context.Context, _, _, _ string, vm armcompute.VirtualMachineScaleSetVM) (*armcompute.VirtualMachineScaleSetVM, error) {
				assert.Empty(t, vm.Properties.StorageProfile.DataDisks, "missing disk should have been filtered before retry")
				return &vm, nil
			}),
	)

	s := NewVMSS(factory, staticResolver(testVMSSProviderID), false)
	diskMap := map[string]*AttachDiskOptions{testDiskURI: {DiskName: "disk1", Lun: 1}}
	err := s.AttachDisk(context.Background(), types.NodeName("node1"), diskMap)
	assert.NoError(t, err)
}

func TestAzclientVMSSDiskVMSet_DetachDisk_NoMatchingDiskSkipsUpdate(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	factory := mock_azclient.NewMockClientFactory(ctrl)
	vmssClient := mockvmssvmclient.NewMockInterface(ctrl)
	factory.EXPECT().GetVirtualMachineScaleSetVMClient().Return(vmssClient).AnyTimes()

	existingVM := &armcompute.VirtualMachineScaleSetVM{
		InstanceID: to.Ptr("3"),
		Properties: &armcompute.VirtualMachineScaleSetVMProperties{
			StorageProfile: &armcompute.StorageProfile{
				DataDisks: []*armcompute.DataDisk{
					{
						Name:        to.Ptr("other"),
						Lun:         to.Ptr(int32(5)),
						ManagedDisk: &armcompute.ManagedDiskParameters{ID: to.Ptr("/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/disks/other")},
					},
				},
			},
		},
	}
	vmssClient.EXPECT().Get(gomock.Any(), "rg1", "aks-nodepool1-vmss", "3").Return(existingVM, nil)
	// No Update is expected: the target disk is not attached, so the update must be skipped.
	// gomock will fail the test if Update is called.

	s := NewVMSS(factory, staticResolver(testVMSSProviderID), false)
	err := s.DetachDisk(context.Background(), types.NodeName("node1"), map[string]string{testDiskURI: "disk1"}, false)
	assert.NoError(t, err)
}

func TestAzclientVMDiskVMSet_AttachDisk_InheritsOSDiskDES(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	factory := mock_azclient.NewMockClientFactory(ctrl)
	vmClient := mockvmclient.NewMockInterface(ctrl)
	factory.EXPECT().GetVirtualMachineClient().Return(vmClient).AnyTimes()

	desID := "/subscriptions/sub1/resourceGroups/rg1/providers/Microsoft.Compute/diskEncryptionSets/des1"
	existingVM := &armcompute.VirtualMachine{
		Name:     to.Ptr("aks-nodepool1-000000"),
		Location: to.Ptr("eastus"),
		Properties: &armcompute.VirtualMachineProperties{
			StorageProfile: &armcompute.StorageProfile{
				OSDisk: &armcompute.OSDisk{
					ManagedDisk: &armcompute.ManagedDiskParameters{
						DiskEncryptionSet: &armcompute.DiskEncryptionSetParameters{ID: to.Ptr(desID)},
					},
				},
			},
		},
	}
	vmClient.EXPECT().Get(gomock.Any(), "rg1", "aks-nodepool1-000000", gomock.Any()).Return(existingVM, nil)

	var pushed *armcompute.VirtualMachine
	vmClient.EXPECT().CreateOrUpdate(gomock.Any(), "rg1", "aks-nodepool1-000000", gomock.Any()).
		DoAndReturn(func(_ context.Context, _, _ string, vm armcompute.VirtualMachine) (*armcompute.VirtualMachine, error) {
			pushed = &vm
			return &vm, nil
		})

	s := NewVM(factory, staticResolver(testStandardProviderID), false)
	// The attach options do not specify a DES, so it must be inherited from the OS disk.
	diskMap := map[string]*AttachDiskOptions{testDiskURI: {DiskName: "disk1", Lun: 0}}
	err := s.AttachDisk(context.Background(), types.NodeName("node1"), diskMap)
	assert.NoError(t, err)

	assert.Len(t, pushed.Properties.StorageProfile.DataDisks, 1)
	got := pushed.Properties.StorageProfile.DataDisks[0]
	assert.NotNil(t, got.ManagedDisk.DiskEncryptionSet)
	assert.Equal(t, desID, *got.ManagedDisk.DiskEncryptionSet.ID)
}
