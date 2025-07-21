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
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	autorestmocks "github.com/Azure/go-autorest/autorest/mocks"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	mockvmclient "sigs.k8s.io/cloud-provider-azure/pkg/azclient/virtualmachineclient/mock_virtualmachineclient"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	"sigs.k8s.io/cloud-provider-azure/pkg/consts"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

func TestCommonAttachDisk(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initVM := func(testCloud *provider.Cloud, expectedVMs []armcompute.VirtualMachine) {
		mockVMClient := testCloud.ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)
		for _, vm := range expectedVMs {
			mockVMClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, *vm.Name, gomock.Any()).Return(&vm, nil).AnyTimes()
		}
		if len(expectedVMs) == 0 {
			mockVMClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any()).Return(&armcompute.VirtualMachine{}, errors.New("instance not found")).AnyTimes()
		}
	}

	defaultSetup := func(testCloud *provider.Cloud, expectedVMs []armcompute.VirtualMachine, _ int, _ error) {
		initVM(testCloud, expectedVMs)
		mockVMClient := testCloud.ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)
		mockVMClient.EXPECT().CreateOrUpdate(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any()).Return(nil, nil).MaxTimes(1)
	}

	maxShare := int32(1)
	goodInstanceID := fmt.Sprintf("/subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/%s", "vm1")
	diskEncryptionSetID := fmt.Sprintf("/subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/diskEncryptionSets/%s", "diskEncryptionSet-name")
	testTags := make(map[string]*string)
	testTags[WriteAcceleratorEnabled] = ptr.To("true")
	testCases := []struct {
		desc                 string
		diskName             string
		existedDisk          *armcompute.Disk
		nodeName             types.NodeName
		vmList               map[string]string
		isDataDisksFull      bool
		isBadDiskURI         bool
		isDiskUsed           bool
		setup                func(testCloud *provider.Cloud, expectedVMs []armcompute.VirtualMachine, statusCode int, result error)
		expectErr            bool
		isContextDeadlineErr bool
		statusCode           int
		waitResult           error
		expectedLun          int32
		contextDuration      time.Duration
	}{
		{
			desc:        "correct LUN and no error shall be returned if disk is nil",
			vmList:      map[string]string{"vm1": "PowerState/Running"},
			nodeName:    "vm1",
			diskName:    "disk-name",
			existedDisk: nil,
			expectedLun: 3,
			expectErr:   false,
			statusCode:  200,
		},
		{
			desc:        "LUN -1 and error shall be returned if there's no such instance corresponding to given nodeName",
			nodeName:    "vm1",
			diskName:    "disk-name",
			existedDisk: &armcompute.Disk{Name: ptr.To("disk-name")},
			expectedLun: -1,
			expectErr:   true,
		},
		{
			desc:            "LUN -1 and error shall be returned if there's no available LUN for instance",
			vmList:          map[string]string{"vm1": "PowerState/Running"},
			nodeName:        "vm1",
			isDataDisksFull: true,
			diskName:        "disk-name",
			existedDisk:     &armcompute.Disk{Name: ptr.To("disk-name")},
			expectedLun:     -1,
			expectErr:       true,
		},
		{
			desc:     "correct LUN and no error shall be returned if everything is good",
			vmList:   map[string]string{"vm1": "PowerState/Running"},
			nodeName: "vm1",
			diskName: "disk-name",
			existedDisk: &armcompute.Disk{Name: ptr.To("disk-name"),
				Properties: &armcompute.DiskProperties{
					Encryption: &armcompute.Encryption{DiskEncryptionSetID: &diskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)},
					DiskSizeGB: ptr.To(int32(4096)),
					DiskState:  to.Ptr(armcompute.DiskStateUnattached),
				},
				Tags: testTags},
			expectedLun: 3,
			expectErr:   false,
			statusCode:  200,
		},
		{
			desc:     "an error shall be returned if disk state is not Unattached",
			vmList:   map[string]string{"vm1": "PowerState/Running"},
			nodeName: "vm1",
			diskName: "disk-name",
			existedDisk: &armcompute.Disk{Name: ptr.To("disk-name"),
				Properties: &armcompute.DiskProperties{
					Encryption: &armcompute.Encryption{DiskEncryptionSetID: &diskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)},
					DiskSizeGB: ptr.To(int32(4096)),
					DiskState:  to.Ptr(armcompute.DiskStateAttached),
				},
				Tags: testTags},
			expectedLun: -1,
			expectErr:   true,
		},
		{
			desc:        "an error shall be returned if attach an already attached disk with good ManagedBy instance id",
			vmList:      map[string]string{"vm1": "PowerState/Running"},
			nodeName:    "vm1",
			diskName:    "disk-name",
			existedDisk: &armcompute.Disk{Name: ptr.To("disk-name"), ManagedBy: ptr.To(goodInstanceID), Properties: &armcompute.DiskProperties{MaxShares: &maxShare}},
			expectedLun: -1,
			expectErr:   true,
		},
	}

	for i, test := range testCases {
		tt := test
		t.Run(tt.desc, func(t *testing.T) {
			testCloud := provider.GetTestCloud(ctrl)
			diskURI := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s",
				testCloud.SubscriptionID, testCloud.ResourceGroup, test.diskName)
			if tt.isBadDiskURI {
				diskURI = fmt.Sprintf("/baduri/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s",
					testCloud.SubscriptionID, testCloud.ResourceGroup, tt.diskName)
			}
			expectedVMs := setTestVirtualMachines(testCloud, tt.vmList, tt.isDataDisksFull)
			if tt.isDiskUsed {
				vm0 := setTestVirtualMachines(testCloud, map[string]string{"vm0": "PowerState/Running"}, tt.isDataDisksFull)[0]
				expectedVMs = append(expectedVMs, vm0)
			}
			if tt.setup == nil {
				defaultSetup(testCloud, expectedVMs, tt.statusCode, tt.waitResult)
			} else {
				tt.setup(testCloud, expectedVMs, tt.statusCode, tt.waitResult)
			}

			if tt.contextDuration > 0 {
				oldCtx := ctx
				ctx, cancel = context.WithTimeout(oldCtx, tt.contextDuration)
				defer cancel()
				defer func() {
					ctx = oldCtx
				}()
			}
			testdiskController := &controllerCommon{
				cloud:               testCloud,
				lockMap:             newLockMap(),
				DisableDiskLunCheck: true,
				WaitForDetach:       true,
			}
			getter := func(_ context.Context, _ string) (interface{}, error) { return nil, nil }
			testdiskController.hitMaxDataDiskCountCache, _ = azcache.NewTimedCache(5*time.Minute, getter, false)
			lun, err := testdiskController.AttachDisk(ctx, test.diskName, diskURI, tt.nodeName, armcompute.CachingTypesReadOnly, tt.existedDisk, nil)

			assert.Equal(t, tt.expectedLun, lun, "TestCase[%d]: %s", i, tt.desc)
			assert.Equal(t, tt.expectErr, err != nil, "TestCase[%d]: %s, return error: %v", i, tt.desc, err)

			assert.Equal(t, tt.isContextDeadlineErr, errors.Is(err, context.DeadlineExceeded))
		})
	}
}

func TestCommonDetachDisk(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testCases := []struct {
		desc        string
		vmList      map[string]string
		nodeName    types.NodeName
		diskName    string
		expectedErr bool
	}{
		{
			desc:        "error should not be returned if there's no such instance corresponding to given nodeName",
			nodeName:    "vm1",
			expectedErr: true,
		},
		{
			desc:        "no error shall be returned if there's no matching disk according to given diskName",
			vmList:      map[string]string{"vm1": "PowerState/Running"},
			nodeName:    "vm1",
			diskName:    "diskx",
			expectedErr: false,
		},
		{
			desc:        "error shall be returned if the disk exists",
			vmList:      map[string]string{"vm1": "PowerState/Running"},
			nodeName:    "vm1",
			diskName:    "disk1",
			expectedErr: false,
		},
	}

	for i, test := range testCases {
		testCloud := provider.GetTestCloud(ctrl)
		common := &controllerCommon{
			cloud:              testCloud,
			lockMap:            newLockMap(),
			ForceDetachBackoff: true,
		}
		diskURI := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/disk-name",
			testCloud.SubscriptionID, testCloud.ResourceGroup)
		expectedVMs := setTestVirtualMachines(testCloud, test.vmList, false)
		mockVMClient := testCloud.ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)
		for _, vm := range expectedVMs {
			mockVMClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, *vm.Name, gomock.Any()).Return(&vm, nil).AnyTimes()
		}
		if len(expectedVMs) == 0 {
			mockVMClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any()).Return(&armcompute.VirtualMachine{}, errors.New("instance not found")).AnyTimes()
		}
		mockVMClient.EXPECT().CreateOrUpdate(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

		err := common.DetachDisk(ctx, test.diskName, diskURI, test.nodeName)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s, err: %v", i, test.desc, err)
	}
}

func TestCommonUpdateVM(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testCases := []struct {
		desc             string
		vmList           map[string]string
		nodeName         types.NodeName
		diskName         string
		isErrorRetriable bool
		expectedErr      bool
	}{
		{
			desc:        "error should not be returned if there's no such instance corresponding to given nodeName",
			nodeName:    "vm1",
			expectedErr: false,
		},
		{
			desc:             "an error should be returned if vmset detach failed with isErrorRetriable error",
			vmList:           map[string]string{"vm1": "PowerState/Running"},
			nodeName:         "vm1",
			isErrorRetriable: true,
			expectedErr:      true,
		},
		{
			desc:        "no error shall be returned if there's no matching disk according to given diskName",
			vmList:      map[string]string{"vm1": "PowerState/Running"},
			nodeName:    "vm1",
			diskName:    "diskx",
			expectedErr: false,
		},
		{
			desc:        "no error shall be returned if the disk exists",
			vmList:      map[string]string{"vm1": "PowerState/Running"},
			nodeName:    "vm1",
			diskName:    "disk1",
			expectedErr: false,
		},
	}

	for i, test := range testCases {
		testCloud := provider.GetTestCloud(ctrl)
		common := &controllerCommon{
			cloud:   testCloud,
			lockMap: newLockMap(),
		}
		expectedVMs := setTestVirtualMachines(testCloud, test.vmList, false)
		mockVMClient := testCloud.ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)
		for _, vm := range expectedVMs {
			mockVMClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, *vm.Name, gomock.Any()).Return(&vm, nil).AnyTimes()
		}
		if len(expectedVMs) == 0 {
			mockVMClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any()).Return(&armcompute.VirtualMachine{}, nil).AnyTimes()
		}
		r := autorestmocks.NewResponseWithStatus("200", 200)
		r.Body.Close()
		r.Request.Method = http.MethodPut

		if test.isErrorRetriable {
			testCloud.CloudProviderBackoff = true
			testCloud.ResourceRequestBackoff = wait.Backoff{Steps: 1}
			mockVMClient.EXPECT().CreateOrUpdate(ctx, testCloud.ResourceGroup, gomock.Any(), gomock.Any()).Return(nil, fmt.Errorf("retriable error")).AnyTimes()
		} else {
			mockVMClient.EXPECT().CreateOrUpdate(ctx, testCloud.ResourceGroup, gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
		}

		err := common.UpdateVM(ctx, test.nodeName)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s, err: %v", i, test.desc, err)
	}
}

func TestGetDiskLun(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		desc        string
		diskName    string
		diskURI     string
		expectedLun int32
		expectedErr bool
	}{
		{
			desc:        "LUN -1 and error shall be returned if diskName != disk.Name or diskURI != disk.Vhd.URI",
			diskName:    "diskx",
			expectedLun: -1,
			expectedErr: true,
		},
		{
			desc:        "correct LUN and no error shall be returned if diskName = disk.Name",
			diskName:    "disk1",
			expectedLun: 0,
			expectedErr: false,
		},
	}

	for i, test := range testCases {
		testCloud := provider.GetTestCloud(ctrl)
		common := &controllerCommon{
			cloud:   testCloud,
			lockMap: newLockMap(),
		}
		expectedVMs := setTestVirtualMachines(testCloud, map[string]string{"vm1": "PowerState/Running"}, false)
		mockVMClient := testCloud.ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)
		for _, vm := range expectedVMs {
			mockVMClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, *vm.Name, gomock.Any()).Return(&vm, nil).AnyTimes()
		}

		lun, _, err := common.GetDiskLun(context.Background(), test.diskName, test.diskURI, "vm1")
		assert.Equal(t, test.expectedLun, lun, "TestCase[%d]: %s", i, test.desc)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
	}
}

func TestSetDiskLun(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		desc            string
		nodeName        string
		diskURI         string
		diskMap         map[string]*provider.AttachDiskOptions
		occupiedLuns    []int
		isDataDisksFull bool
		expectedErr     bool
		expectedLun     int32
	}{
		{
			desc:         "the minimal LUN shall be returned if there's enough room for extra disks",
			nodeName:     "nodeName",
			diskURI:      "diskURI",
			occupiedLuns: []int{0, 1, 2},
			diskMap:      map[string]*provider.AttachDiskOptions{"diskURI": {}},
			expectedLun:  3,
			expectedErr:  false,
		},
		{
			desc:         "occupied LUNs shall be skipped",
			nodeName:     "nodeName",
			diskURI:      "diskURI",
			occupiedLuns: []int{0, 1, 2, 3},
			diskMap:      map[string]*provider.AttachDiskOptions{"diskURI": {}},
			expectedLun:  4,
			expectedErr:  false,
		},
		{
			desc:            "LUN -1 and error shall be returned if there's no available LUN",
			nodeName:        "nodeName",
			diskURI:         "diskURI",
			diskMap:         map[string]*provider.AttachDiskOptions{"diskURI": {}},
			isDataDisksFull: true,
			expectedLun:     -1,
			expectedErr:     true,
		},
		{
			desc:        "diskURI1 is not in VM data disk list nor in diskMap",
			nodeName:    "nodeName",
			diskURI:     "diskURI1",
			diskMap:     map[string]*provider.AttachDiskOptions{"diskURI2": {}},
			expectedLun: -1,
			expectedErr: true,
		},
	}

	for i, test := range testCases {
		testCloud := provider.GetTestCloud(ctrl)
		common := &controllerCommon{
			cloud:   testCloud,
			lockMap: newLockMap(),
		}
		expectedVMs := setTestVirtualMachines(testCloud, map[string]string{test.nodeName: "PowerState/Running"}, test.isDataDisksFull)
		mockVMClient := testCloud.ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)
		for _, vm := range expectedVMs {
			mockVMClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, *vm.Name, gomock.Any()).Return(&vm, nil).AnyTimes()
		}

		lun, err := common.SetDiskLun(context.Background(), types.NodeName(test.nodeName), test.diskURI, test.diskMap, test.occupiedLuns)
		assert.Equal(t, test.expectedLun, lun, "TestCase[%d]: %s", i, test.desc)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
	}
}

func TestGetValidCreationData(t *testing.T) {
	sourceResourceSnapshotID := "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/xxx"
	sourceResourceVolumeID := "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/xxx"
	upperSourceResourceSnapshotID := strings.ToUpper(sourceResourceSnapshotID)
	upperSourceResourceVolumeID := strings.ToUpper(sourceResourceVolumeID)

	tests := []struct {
		subscriptionID   string
		resourceGroup    string
		sourceResourceID string
		sourceType       string
		expected1        armcompute.CreationData
		expected2        error
	}{
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "",
			sourceType:       "",
			expected1: armcompute.CreationData{
				CreateOption: to.Ptr(armcompute.DiskCreateOptionEmpty),
			},
			expected2: nil,
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/xxx",
			sourceType:       sourceSnapshot,
			expected1: armcompute.CreationData{
				CreateOption:     to.Ptr(armcompute.DiskCreateOptionCopy),
				SourceResourceID: &sourceResourceSnapshotID,
			},
			expected2: nil,
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: upperSourceResourceSnapshotID,
			sourceType:       sourceSnapshot,
			expected1: armcompute.CreationData{
				CreateOption:     to.Ptr(armcompute.DiskCreateOptionCopy),
				SourceResourceID: &upperSourceResourceSnapshotID,
			},
			expected2: nil,
		},
		{
			subscriptionID:   "xxx",
			resourceGroup:    "xxx",
			sourceResourceID: "xxx",
			sourceType:       sourceSnapshot,
			expected1: armcompute.CreationData{
				CreateOption:     to.Ptr(armcompute.DiskCreateOptionCopy),
				SourceResourceID: &sourceResourceSnapshotID,
			},
			expected2: nil,
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "/subscriptions/23/providers/Microsoft.Compute/disks/name",
			sourceType:       sourceSnapshot,
			expected1:        armcompute.CreationData{},
			expected2:        fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", "/subscriptions//resourceGroups//providers/Microsoft.Compute/snapshots//subscriptions/23/providers/Microsoft.Compute/disks/name", azureconstants.DiskSnapshotPathRE),
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "http://test.com/vhds/name",
			sourceType:       sourceSnapshot,
			expected1:        armcompute.CreationData{},
			expected2:        fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", "/subscriptions//resourceGroups//providers/Microsoft.Compute/snapshots/http://test.com/vhds/name", azureconstants.DiskSnapshotPathRE),
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "/subscriptions/xxx/snapshots/xxx",
			sourceType:       sourceSnapshot,
			expected1:        armcompute.CreationData{},
			expected2:        fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", "/subscriptions//resourceGroups//providers/Microsoft.Compute/snapshots//subscriptions/xxx/snapshots/xxx", azureconstants.DiskSnapshotPathRE),
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/xxx/snapshots/xxx/snapshots/xxx",
			sourceType:       sourceSnapshot,
			expected1:        armcompute.CreationData{},
			expected2:        fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/xxx/snapshots/xxx/snapshots/xxx", azureconstants.DiskSnapshotPathRE),
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "xxx",
			sourceType:       "",
			expected1: armcompute.CreationData{
				CreateOption: to.Ptr(armcompute.DiskCreateOptionEmpty),
			},
			expected2: nil,
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/xxx",
			sourceType:       sourceVolume,
			expected1: armcompute.CreationData{
				CreateOption:     to.Ptr(armcompute.DiskCreateOptionCopy),
				SourceResourceID: &sourceResourceVolumeID,
			},
			expected2: nil,
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: upperSourceResourceVolumeID,
			sourceType:       sourceVolume,
			expected1: armcompute.CreationData{
				CreateOption:     to.Ptr(armcompute.DiskCreateOptionCopy),
				SourceResourceID: &upperSourceResourceVolumeID,
			},
			expected2: nil,
		},
		{
			subscriptionID:   "xxx",
			resourceGroup:    "xxx",
			sourceResourceID: "xxx",
			sourceType:       sourceVolume,
			expected1: armcompute.CreationData{
				CreateOption:     to.Ptr(armcompute.DiskCreateOptionCopy),
				SourceResourceID: &sourceResourceVolumeID,
			},
			expected2: nil,
		},
		{
			subscriptionID:   "",
			resourceGroup:    "",
			sourceResourceID: "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/xxx",
			sourceType:       sourceVolume,
			expected1:        armcompute.CreationData{},
			expected2:        fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", "/subscriptions//resourceGroups//providers/Microsoft.Compute/disks//subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/xxx", azureconstants.ManagedDiskPathRE),
		},
	}

	for _, test := range tests {
		options := ManagedDiskOptions{
			SourceResourceID: test.sourceResourceID,
			SourceType:       test.sourceType,
		}
		result, err := getValidCreationData(test.subscriptionID, test.resourceGroup, &options)
		if !reflect.DeepEqual(result, test.expected1) || !reflect.DeepEqual(err, test.expected2) {
			t.Errorf("input sourceResourceID: %v, sourceType: %v, getValidCreationData result: %v, expected1 : %v, err: %v, expected2: %v", test.sourceResourceID, test.sourceType, result, test.expected1, err, test.expected2)
		}
	}
}

func TestIsInstanceNotFoundError(t *testing.T) {
	testCases := []struct {
		errMsg         string
		expectedResult bool
	}{
		{
			errMsg:         "",
			expectedResult: false,
		},
		{
			errMsg:         "other error",
			expectedResult: false,
		},
		{
			errMsg:         "The provided instanceId 857 is not an active Virtual Machine Scale Set VM instanceId.",
			expectedResult: true,
		},
		{
			errMsg:         `compute.VirtualMachineScaleSetVMsClient#Update: Failure sending request: StatusCode=400 -- Original Error: Code="InvalidParameter" Message="The provided instanceId 1181 is not an active Virtual Machine Scale Set VM instanceId." Target="instanceIds"`,
			expectedResult: true,
		},
	}

	for i, test := range testCases {
		result := isInstanceNotFoundError(fmt.Errorf("%v", test.errMsg))
		assert.Equal(t, test.expectedResult, result, "TestCase[%d]", i, result)
	}
}

func TestAttachDiskRequest(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		desc                 string
		diskURI              string
		nodeName             string
		diskName             string
		diskNum              int
		duplicateDiskRequest bool
		expectedErr          bool
	}{
		{
			desc:        "one disk request in queue",
			diskURI:     "diskURI",
			nodeName:    "nodeName",
			diskName:    "diskName",
			diskNum:     1,
			expectedErr: false,
		},
		{
			desc:        "multiple disk requests in queue",
			diskURI:     "diskURI",
			nodeName:    "nodeName",
			diskName:    "diskName",
			diskNum:     10,
			expectedErr: false,
		},
		{
			desc:        "zero disk request in queue",
			diskURI:     "diskURI",
			nodeName:    "nodeName",
			diskName:    "diskName",
			diskNum:     0,
			expectedErr: false,
		},
		{
			desc:                 "multiple disk requests in queue",
			diskURI:              "diskURI",
			nodeName:             "nodeName",
			diskName:             "diskName",
			duplicateDiskRequest: true,
			diskNum:              10,
			expectedErr:          false,
		},
	}

	for i, test := range testCases {
		testCloud := provider.GetTestCloud(ctrl)
		common := &controllerCommon{
			cloud:   testCloud,
			lockMap: newLockMap(),
		}
		for i := 1; i <= test.diskNum; i++ {
			diskURI := fmt.Sprintf("%s%d", test.diskURI, i)
			diskName := fmt.Sprintf("%s%d", test.diskName, i)
			ops := &provider.AttachDiskOptions{DiskName: diskName}
			_, err := common.batchAttachDiskRequest(diskURI, test.nodeName, ops)
			assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
			if test.duplicateDiskRequest {
				_, err := common.batchAttachDiskRequest(diskURI, test.nodeName, ops)
				assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
			}
		}

		diskURI := fmt.Sprintf("%s%d", test.diskURI, test.diskNum)
		diskMap, err := common.retrieveAttachBatchedDiskRequests(test.nodeName, diskURI)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
		assert.Equal(t, test.diskNum, len(diskMap), "TestCase[%d]: %s", i, test.desc)
		for diskURI, opt := range diskMap {
			assert.Equal(t, strings.Contains(diskURI, test.diskURI), true, "TestCase[%d]: %s", i, test.desc)
			assert.Equal(t, strings.Contains(opt.DiskName, test.diskName), true, "TestCase[%d]: %s", i, test.desc)
		}
	}
}

func TestDetachDiskRequest(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		desc                 string
		diskURI              string
		nodeName             string
		diskName             string
		diskNum              int
		duplicateDiskRequest bool
		expectedErr          bool
	}{
		{
			desc:        "one disk request in queue",
			diskURI:     "diskURI",
			nodeName:    "nodeName",
			diskName:    "diskName",
			diskNum:     1,
			expectedErr: false,
		},
		{
			desc:        "multiple disk requests in queue",
			diskURI:     "diskURI",
			nodeName:    "nodeName",
			diskName:    "diskName",
			diskNum:     10,
			expectedErr: false,
		},
		{
			desc:        "zero disk request in queue",
			diskURI:     "diskURI",
			nodeName:    "nodeName",
			diskName:    "diskName",
			diskNum:     0,
			expectedErr: false,
		},
		{
			desc:                 "multiple disk requests in queue",
			diskURI:              "diskURI",
			nodeName:             "nodeName",
			diskName:             "diskName",
			duplicateDiskRequest: true,
			diskNum:              10,
			expectedErr:          false,
		},
	}

	for i, test := range testCases {
		testCloud := provider.GetTestCloud(ctrl)
		common := &controllerCommon{
			cloud:   testCloud,
			lockMap: newLockMap(),
		}
		diskURI := ""
		for i := 1; i <= test.diskNum; i++ {
			diskURI = fmt.Sprintf("%s%d", test.diskURI, i)
			diskName := fmt.Sprintf("%s%d", test.diskName, i)
			_, err := common.batchDetachDiskRequest(diskName, diskURI, test.nodeName)
			assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
			if test.duplicateDiskRequest {
				_, err := common.batchDetachDiskRequest(diskName, diskURI, test.nodeName)
				assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
			}
		}

		diskMap, err := common.retrieveDetachBatchedDiskRequests(test.nodeName, diskURI)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
		assert.Equal(t, test.diskNum, len(diskMap), "TestCase[%d]: %s", i, test.desc)
		for diskURI, diskName := range diskMap {
			assert.Equal(t, strings.Contains(diskURI, test.diskURI), true, "TestCase[%d]: %s", i, test.desc)
			assert.Equal(t, strings.Contains(diskName, test.diskName), true, "TestCase[%d]: %s", i, test.desc)
		}
	}
}

func TestGetDetachDiskRequestNum(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		desc                 string
		diskURI              string
		nodeName             string
		diskName             string
		diskNum              int
		duplicateDiskRequest bool
		expectedErr          bool
	}{
		{
			desc:        "one disk request in queue",
			diskURI:     "diskURI",
			nodeName:    "nodeName",
			diskName:    "diskName",
			diskNum:     1,
			expectedErr: false,
		},
		{
			desc:        "multiple disk requests in queue",
			diskURI:     "diskURI",
			nodeName:    "nodeName",
			diskName:    "diskName",
			diskNum:     10,
			expectedErr: false,
		},
		{
			desc:        "zero disk request in queue",
			diskURI:     "diskURI",
			nodeName:    "nodeName",
			diskName:    "diskName",
			diskNum:     0,
			expectedErr: false,
		},
		{
			desc:                 "multiple disk requests in queue",
			diskURI:              "diskURI",
			nodeName:             "nodeName",
			diskName:             "diskName",
			duplicateDiskRequest: true,
			diskNum:              10,
			expectedErr:          false,
		},
	}

	for i, test := range testCases {
		testCloud := provider.GetTestCloud(ctrl)
		common := &controllerCommon{
			cloud:   testCloud,
			lockMap: newLockMap(),
		}
		for i := 1; i <= test.diskNum; i++ {
			diskURI := fmt.Sprintf("%s%d", test.diskURI, i)
			diskName := fmt.Sprintf("%s%d", test.diskName, i)
			_, err := common.batchDetachDiskRequest(diskName, diskURI, test.nodeName)
			assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
			if test.duplicateDiskRequest {
				_, err := common.batchDetachDiskRequest(diskName, diskURI, test.nodeName)
				assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
			}
		}

		detachDiskReqeustNum, err := common.getDetachDiskRequestNum(test.nodeName)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
		assert.Equal(t, test.diskNum, detachDiskReqeustNum, "TestCase[%d]: %s", i, test.desc)
	}
}

func TestVerifyAttach(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tests := []struct {
		name           string
		diskName       string
		diskURI        string
		nodeName       types.NodeName
		expectedVM     map[string]string
		expectDisk     bool
		wantLun        int32
		wantErr        bool
		wantErrContent string
	}{
		{
			name:       "successfully finds disk lun",
			diskName:   "disk1",
			diskURI:    "diskuri1",
			nodeName:   "node1",
			expectedVM: map[string]string{"node1": "PowerState/Running"},
			expectDisk: true,
			wantLun:    2,
			wantErr:    false,
		},
		{
			name:           "returns error when disk not found",
			diskName:       "diskNotFound",
			diskURI:        "diskuri2",
			nodeName:       "node2",
			expectedVM:     map[string]string{"node2": "PowerState/Stopped"},
			wantLun:        -1,
			wantErr:        true,
			wantErrContent: "could not be found",
		},
		{
			name:           "returns error with vm not found",
			diskName:       "disk1",
			diskURI:        "diskuri3",
			nodeName:       "node3",
			expectedVM:     nil,
			wantLun:        -1,
			wantErr:        true,
			wantErrContent: "could not be found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testCloud := provider.GetTestCloud(ctrl)
			mockVMClient := testCloud.ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)
			common := &controllerCommon{
				cloud:   testCloud,
				lockMap: newLockMap(),
			}
			if tt.expectedVM == nil {
				mockVMClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any()).Return(&armcompute.VirtualMachine{}, errors.New("could not be found")).AnyTimes()
			} else {

				expectedVMs := setTestVirtualMachines(testCloud, tt.expectedVM, false)
				for _, vm := range expectedVMs {
					if tt.expectDisk {
						vm.Properties.StorageProfile.DataDisks = []*armcompute.DataDisk{
							{
								Lun:  ptr.To(int32(tt.wantLun)),
								Name: ptr.To(tt.diskName),
							},
						}
					}
					mockVMClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, *vm.Name, gomock.Any()).Return(&vm, nil).AnyTimes()
				}
			}

			lun, err := common.verifyAttach(context.Background(), tt.diskName, tt.diskURI, tt.nodeName)
			assert.Equal(t, tt.wantLun, lun)
			if tt.wantErr {
				assert.Error(t, err)
				if tt.wantErrContent != "" {
					assert.Contains(t, err.Error(), tt.wantErrContent)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestVerifyDetach(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	tests := []struct {
		name           string
		diskName       string
		diskURI        string
		nodeName       types.NodeName
		expectedVM     map[string]string
		expectDisk     bool
		wantLun        int32
		expectedErr    bool
		expectedErrMsg string
	}{
		{
			name:        "successfully detached - error contains CannotFindDiskLUN",
			diskName:    "diskNotFound",
			diskURI:     "diskuri1",
			nodeName:    "node1",
			expectedVM:  map[string]string{"node1": "PowerState/Running"},
			expectedErr: false,
		},
		{
			name:           "error returned from GetDiskLun for VM does not exist",
			diskName:       "disk2",
			diskURI:        "diskuri2",
			nodeName:       "node2",
			expectedVM:     nil,
			expectedErr:    true,
			expectedErrMsg: "could not be found",
		},
		{
			name:           "disk still attached",
			expectDisk:     true,
			diskName:       "disk1",
			diskURI:        "diskuri",
			wantLun:        2,
			nodeName:       "node3",
			expectedErr:    true,
			expectedVM:     map[string]string{"node3": "PowerState/Running"},
			expectedErrMsg: "disk(diskuri) is still attached to node(node3) on lun(2), vmState: Succeeded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testCloud := provider.GetTestCloud(ctrl)
			mockVMClient := testCloud.ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)
			common := &controllerCommon{
				cloud:   testCloud,
				lockMap: newLockMap(),
			}
			if tt.expectedVM == nil {
				mockVMClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any()).Return(&armcompute.VirtualMachine{}, errors.New("could not be found")).AnyTimes()
			} else {

				expectedVMs := setTestVirtualMachines(testCloud, tt.expectedVM, false)
				for _, vm := range expectedVMs {
					if tt.expectDisk {
						vm.Properties.StorageProfile.DataDisks = []*armcompute.DataDisk{
							{
								Lun:  ptr.To(int32(tt.wantLun)),
								Name: ptr.To(tt.diskName),
							},
						}
					}
					mockVMClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, *vm.Name, gomock.Any()).Return(&vm, nil).AnyTimes()
				}
			}

			err := common.verifyDetach(context.Background(), tt.diskName, tt.diskURI, tt.nodeName)
			if tt.expectedErr {
				assert.Error(t, err)
				klog.Info("Expected error occurred", "error", err)
				if tt.expectedErrMsg != "" {
					assert.Contains(t, err.Error(), tt.expectedErrMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestConcurrentDetachDisk(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	testCloud := provider.GetTestCloud(ctrl)
	common := &controllerCommon{
		cloud:                        testCloud,
		lockMap:                      newLockMap(),
		ForceDetachBackoff:           true,
		AttachDetachInitialDelayInMs: 1000,
		DisableDiskLunCheck:          true,
	}
	expectedVM := setTestVirtualMachines(testCloud, map[string]string{"vm1": "PowerState/Running"}, false)
	mockVMClient := testCloud.ComputeClientFactory.GetVirtualMachineClient().(*mockvmclient.MockInterface)

	// Mock Get to always return the expected VM
	mockVMClient.EXPECT().
		Get(gomock.Any(), testCloud.ResourceGroup, *expectedVM[0].Name, gomock.Any()).
		Return(&expectedVM[0], nil).
		AnyTimes()

	// Use a counter to simulate different return values for CreateOrUpdate
	// First call should succeed, subsequent calls should fail
	callCount := int32(0)
	mockVMClient.EXPECT().
		CreateOrUpdate(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any()).
		DoAndReturn(
			func(_ context.Context, _ string, name string, params armcompute.VirtualMachine) (*armcompute.VirtualMachine, error) {
				if atomic.AddInt32(&callCount, 1) == 1 {
					klog.Info("First call to CreateOrUpdate succeeded", "VM Name:", name, "Params:", params)
					time.Sleep(100 * time.Millisecond) // Simulate some processing time to hold the node lock while the 3rd detach request is made
					return nil, nil                    // First call succeeds
				}
				return nil, errors.New("internal error") // Subsequent calls fail
			}).
		AnyTimes()

	// Simulate concurrent detach requests that would be batched
	wg := &sync.WaitGroup{}
	errorChan := make(chan error, 1)
	for i := 0; i < 2; i++ {
		diskURI := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s",
			testCloud.SubscriptionID, testCloud.ResourceGroup, fmt.Sprintf("disk-batched-%d", i))
		go func() {
			wg.Add(1)
			defer wg.Done()
			err := common.DetachDisk(ctx, fmt.Sprintf("disk-batched-%d", i), diskURI, "vm1")
			if err != nil {
				errorChan <- err
			}
		}()
	}

	time.Sleep(1005 * time.Millisecond) // Wait for the batching timeout
	diskURI := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s",
		testCloud.SubscriptionID, testCloud.ResourceGroup, "disk-not-batched")
	klog.Info("Calling DetachDisk for non-batched disk detach", expectedVM)
	// This should trigger a second CreateOrUpdate call that fails
	err := common.DetachDisk(ctx, "disk-not-batched", diskURI, "vm1")

	wg.Wait()
	select {
	case err := <-errorChan:
		// Will only fail if it triggered the second CreateOrUpdate call which could only be for the wrong disk - "disk-not-batched"
		// because the first CreateOrUpdate call was successful for the batched disks
		assert.NoError(t, err, "DetachDisk should not return an error for the batched disk detach")
	default:
		klog.Info("No error received from detach disk requests")
	}
	// Should fail due to the second CreateOrUpdate call returning an error
	assert.Error(t, err, "DetachDisk should return an error for the non-batched disk detach")
}

// setTestVirtualMachines sets test virtual machine with powerstate.
func setTestVirtualMachines(c *provider.Cloud, vmList map[string]string, isDataDisksFull bool) []armcompute.VirtualMachine {
	expectedVMs := make([]armcompute.VirtualMachine, 0)

	for nodeName, powerState := range vmList {
		nodeName := nodeName
		instanceID := fmt.Sprintf("/subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/%s", nodeName)
		vm := armcompute.VirtualMachine{
			Name:     &nodeName,
			ID:       &instanceID,
			Location: &c.Location,
		}
		status := []*armcompute.InstanceViewStatus{
			{
				Code: ptr.To(powerState),
			},
			{
				Code: ptr.To("ProvisioningState/succeeded"),
			},
		}
		vm.Properties = &armcompute.VirtualMachineProperties{
			ProvisioningState: ptr.To(string(consts.ProvisioningStateSucceeded)),
			HardwareProfile: &armcompute.HardwareProfile{
				VMSize: ptr.To(armcompute.VirtualMachineSizeTypesStandardA0),
			},
			InstanceView: &armcompute.VirtualMachineInstanceView{
				Statuses: status,
			},
			StorageProfile: &armcompute.StorageProfile{
				DataDisks: []*armcompute.DataDisk{},
			},
		}
		if !isDataDisksFull {
			vm.Properties.StorageProfile.DataDisks = []*armcompute.DataDisk{
				{
					Lun:  ptr.To(int32(0)),
					Name: ptr.To("disk1"),
				},
				{
					Lun:  ptr.To(int32(1)),
					Name: ptr.To("disk2"),
				},
				{
					Lun:  ptr.To(int32(2)),
					Name: ptr.To("disk3"),
				},
			}
		} else {
			dataDisks := make([]*armcompute.DataDisk, maxLUN)
			for i := 0; i < maxLUN; i++ {
				dataDisks[i] = &armcompute.DataDisk{Lun: ptr.To(int32(i))}
			}
			vm.Properties.StorageProfile.DataDisks = dataDisks
		}

		expectedVMs = append(expectedVMs, vm)
	}

	return expectedVMs
}
