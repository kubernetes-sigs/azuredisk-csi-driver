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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v6"
	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-08-01/compute"
	"github.com/Azure/go-autorest/autorest/azure"
	autorestmocks "github.com/Azure/go-autorest/autorest/mocks"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/diskclient/mock_diskclient"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/mock_azclient"
	"sigs.k8s.io/cloud-provider-azure/pkg/azureclients/vmclient/mockvmclient"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	"sigs.k8s.io/cloud-provider-azure/pkg/consts"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
	"sigs.k8s.io/cloud-provider-azure/pkg/retry"
)

var (
	conflictingUserInputError = retry.NewError(false, errors.New(`Code="ConflictingUserInput" Message="Cannot attach the disk pvc-xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxx to VM /subscriptions/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxx/resourceGroups/test-rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-nodepool0-00000000-vmss/virtualMachines/aks-nodepool0-00000000-vmss_0 because it is already attached to VM /subscriptions/xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxx/resourceGroups/test-rg/providers/Microsoft.Compute/virtualMachineScaleSets/aks-nodepool0-00000000-vmss/virtualMachines/aks-nodepool0-00000000-vmss_1. A disk can be attached to only one VM at a time."`))
)

func fakeUpdateAsync(statusCode int) func(context.Context, string, string, compute.VirtualMachineUpdate, string) (*azure.Future, *retry.Error) {
	return func(_ context.Context, _, nodeName string, parameters compute.VirtualMachineUpdate, _ string) (*azure.Future, *retry.Error) {
		vm := &compute.VirtualMachine{
			Name:                     &nodeName,
			Plan:                     parameters.Plan,
			VirtualMachineProperties: parameters.VirtualMachineProperties,
			Identity:                 parameters.Identity,
			Zones:                    parameters.Zones,
			Tags:                     parameters.Tags,
		}
		s, err := json.Marshal(vm)
		if err != nil {
			return nil, retry.NewError(false, err)
		}

		body := autorestmocks.NewBodyWithBytes(s)

		r := autorestmocks.NewResponseWithBodyAndStatus(body, statusCode, strconv.Itoa(statusCode)) //nolint: bodyclose
		r.Request.Method = http.MethodPut

		f, err := azure.NewFutureFromResponse(r)
		if err != nil {
			return nil, retry.NewError(false, err)
		}

		return &f, nil
	}
}

func TestCommonAttachDisk(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	getter := func(_ context.Context, _ string) (interface{}, error) { return nil, nil }

	initVM := func(testCloud *provider.Cloud, expectedVMs []compute.VirtualMachine) {
		mockVMsClient := testCloud.VirtualMachinesClient.(*mockvmclient.MockInterface)
		for _, vm := range expectedVMs {
			mockVMsClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, *vm.Name, gomock.Any()).Return(vm, nil).AnyTimes()
		}
		if len(expectedVMs) == 0 {
			mockVMsClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any()).Return(compute.VirtualMachine{}, &retry.Error{HTTPStatusCode: http.StatusNotFound, RawError: cloudprovider.InstanceNotFound}).AnyTimes()
		}
	}

	defaultSetup := func(testCloud *provider.Cloud, expectedVMs []compute.VirtualMachine, statusCode int, result *retry.Error) {
		initVM(testCloud, expectedVMs)
		mockVMsClient := testCloud.VirtualMachinesClient.(*mockvmclient.MockInterface)
		mockVMsClient.EXPECT().UpdateAsync(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(fakeUpdateAsync(statusCode)).MaxTimes(1)
		mockVMsClient.EXPECT().Update(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).MaxTimes(1)
		mockVMsClient.EXPECT().WaitForUpdateResult(gomock.Any(), gomock.Any(), testCloud.ResourceGroup, gomock.Any()).Return(nil, result).MaxTimes(1)
	}

	maxShare := int32(1)
	goodInstanceID := fmt.Sprintf("/subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/%s", "vm1")
	diskEncryptionSetID := fmt.Sprintf("/subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/diskEncryptionSets/%s", "diskEncryptionSet-name")
	testTags := make(map[string]*string)
	testTags[WriteAcceleratorEnabled] = ptr.To("true")
	testCases := []struct {
		desc                       string
		diskName                   string
		existedDisk                *armcompute.Disk
		nodeName                   types.NodeName
		vmList                     map[string]string
		isDataDisksFull            bool
		isBadDiskURI               bool
		isDiskUsed                 bool
		isMaxDataDiskCountExceeded bool
		setup                      func(testCloud *provider.Cloud, expectedVMs []compute.VirtualMachine, statusCode int, result *retry.Error)
		expectErr                  bool
		isContextDeadlineErr       bool
		statusCode                 int
		waitResult                 *retry.Error
		expectedLun                int32
		contextDuration            time.Duration
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
			desc:                       "correct LUN and no error shall be returned if disk is nil with max data disk count exceeded",
			vmList:                     map[string]string{"vm1": "PowerState/Running"},
			nodeName:                   "vm1",
			diskName:                   "disk-name",
			isMaxDataDiskCountExceeded: true,
			existedDisk:                nil,
			expectedLun:                3,
			expectErr:                  false,
			statusCode:                 200,
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
		{
			desc:        "should return a PartialUpdateError type when storage configuration was accepted but wait fails with an error",
			vmList:      map[string]string{"vm1": "PowerState/Running"},
			nodeName:    "vm1",
			diskName:    "disk-name",
			existedDisk: nil,
			expectedLun: -1,
			expectErr:   true,
			statusCode:  200,
			waitResult:  conflictingUserInputError,
		},
		{
			desc:        "should not return a PartialUpdateError type when storage configuration was not accepted",
			vmList:      map[string]string{"vm1": "PowerState/Running"},
			nodeName:    "vm1",
			diskName:    "disk-name",
			existedDisk: nil,
			expectedLun: -1,
			expectErr:   true,
			statusCode:  400,
			waitResult:  conflictingUserInputError,
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
			}
			testdiskController.hitMaxDataDiskCountCache, _ = azcache.NewTimedCache(5*time.Minute, getter, false)
			if tt.isMaxDataDiskCountExceeded {
				testdiskController.hitMaxDataDiskCountCache.Set(string(tt.nodeName), "")
			}
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
			expectedErr: false,
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
			expectedErr: true,
		},
	}

	for i, test := range testCases {
		testCloud := provider.GetTestCloud(ctrl)
		common := &controllerCommon{
			cloud:   testCloud,
			lockMap: newLockMap(),
		}
		diskURI := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/disk-name",
			testCloud.SubscriptionID, testCloud.ResourceGroup)
		expectedVMs := setTestVirtualMachines(testCloud, test.vmList, false)
		mockVMsClient := testCloud.VirtualMachinesClient.(*mockvmclient.MockInterface)
		for _, vm := range expectedVMs {
			mockVMsClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, *vm.Name, gomock.Any()).Return(vm, nil).AnyTimes()
		}
		if len(expectedVMs) == 0 {
			mockVMsClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any()).Return(compute.VirtualMachine{}, &retry.Error{HTTPStatusCode: http.StatusNotFound, RawError: cloudprovider.InstanceNotFound}).AnyTimes()
		}
		mockVMsClient.EXPECT().Update(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()

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
		mockVMsClient := testCloud.VirtualMachinesClient.(*mockvmclient.MockInterface)
		for _, vm := range expectedVMs {
			mockVMsClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, *vm.Name, gomock.Any()).Return(vm, nil).AnyTimes()
		}
		if len(expectedVMs) == 0 {
			mockVMsClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any()).Return(compute.VirtualMachine{}, &retry.Error{HTTPStatusCode: http.StatusNotFound, RawError: cloudprovider.InstanceNotFound}).AnyTimes()
		}
		r := autorestmocks.NewResponseWithStatus("200", 200)
		r.Body.Close()
		r.Request.Method = http.MethodPut

		future, err := azure.NewFutureFromResponse(r)

		mockVMsClient.EXPECT().UpdateAsync(gomock.Any(), testCloud.ResourceGroup, gomock.Any(), gomock.Any(), gomock.Any()).Return(&future, err).AnyTimes()

		if test.isErrorRetriable {
			testCloud.CloudProviderBackoff = true
			testCloud.ResourceRequestBackoff = wait.Backoff{Steps: 1}
			mockVMsClient.EXPECT().WaitForUpdateResult(ctx, &future, testCloud.ResourceGroup, gomock.Any()).Return(nil, &retry.Error{HTTPStatusCode: http.StatusBadRequest, Retriable: true, RawError: fmt.Errorf("Retriable: true")}).AnyTimes()
		} else {
			mockVMsClient.EXPECT().WaitForUpdateResult(ctx, &future, testCloud.ResourceGroup, gomock.Any()).Return(nil, nil).AnyTimes()
		}

		err = common.UpdateVM(ctx, test.nodeName)
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
		mockVMsClient := testCloud.VirtualMachinesClient.(*mockvmclient.MockInterface)
		for _, vm := range expectedVMs {
			mockVMsClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, *vm.Name, gomock.Any()).Return(vm, nil).AnyTimes()
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
		mockVMsClient := testCloud.VirtualMachinesClient.(*mockvmclient.MockInterface)
		for _, vm := range expectedVMs {
			mockVMsClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, *vm.Name, gomock.Any()).Return(vm, nil).AnyTimes()
		}

		lun, err := common.SetDiskLun(context.Background(), types.NodeName(test.nodeName), test.diskURI, test.diskMap, test.occupiedLuns)
		assert.Equal(t, test.expectedLun, lun, "TestCase[%d]: %s", i, test.desc)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
	}
}

func TestCheckDiskExists(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testCloud := provider.GetTestCloud(ctrl)
	mockFactory := mock_azclient.NewMockClientFactory(ctrl)
	common := &controllerCommon{
		cloud:         testCloud,
		clientFactory: mockFactory,
		lockMap:       newLockMap(),
	}
	// create a new disk before running test
	newDiskName := "newdisk"
	newDiskURI := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s",
		testCloud.SubscriptionID, testCloud.ResourceGroup, newDiskName)

	mockDisksClient := mock_diskclient.NewMockInterface(ctrl)
	mockFactory.EXPECT().GetDiskClientForSub(gomock.Any()).Return(mockDisksClient, nil).AnyTimes()
	mockDisksClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, newDiskName).Return(&armcompute.Disk{}, nil).AnyTimes()
	mockDisksClient.EXPECT().Get(gomock.Any(), gomock.Not(testCloud.ResourceGroup), gomock.Any()).Return(&armcompute.Disk{}, &azcore.ResponseError{
		StatusCode: http.StatusNotFound,
		RawResponse: &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       ioutil.NopCloser(bytes.NewReader([]byte{})),
		},
	}).AnyTimes()

	testCases := []struct {
		diskURI        string
		expectedResult bool
		expectedErr    bool
	}{
		{
			diskURI:        "incorrect disk URI format",
			expectedResult: false,
			expectedErr:    true,
		},
		{
			diskURI:        "/subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/non-existing-disk",
			expectedResult: false,
			expectedErr:    false,
		},
		{
			diskURI:        newDiskURI,
			expectedResult: true,
			expectedErr:    false,
		},
	}

	for i, test := range testCases {
		exist, err := common.checkDiskExists(ctx, test.diskURI)
		assert.Equal(t, test.expectedResult, exist, "TestCase[%d]", i, exist)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d], return error: %v", i, err)
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

func TestAttachDiskRequestFuncs(t *testing.T) {
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
			_, err := common.insertAttachDiskRequest(diskURI, test.nodeName, ops)
			assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
			if test.duplicateDiskRequest {
				_, err := common.insertAttachDiskRequest(diskURI, test.nodeName, ops)
				assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
			}
		}

		diskMap, err := common.cleanAttachDiskRequests(test.nodeName)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
		assert.Equal(t, test.diskNum, len(diskMap), "TestCase[%d]: %s", i, test.desc)
		for diskURI, opt := range diskMap {
			assert.Equal(t, strings.Contains(diskURI, test.diskURI), true, "TestCase[%d]: %s", i, test.desc)
			assert.Equal(t, strings.Contains(opt.DiskName, test.diskName), true, "TestCase[%d]: %s", i, test.desc)
		}
	}
}

func TestDetachDiskRequestFuncs(t *testing.T) {
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
			_, err := common.insertDetachDiskRequest(diskName, diskURI, test.nodeName)
			assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
			if test.duplicateDiskRequest {
				_, err := common.insertDetachDiskRequest(diskName, diskURI, test.nodeName)
				assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
			}
		}

		diskMap, err := common.cleanDetachDiskRequests(test.nodeName)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s", i, test.desc)
		assert.Equal(t, test.diskNum, len(diskMap), "TestCase[%d]: %s", i, test.desc)
		for diskURI, diskName := range diskMap {
			assert.Equal(t, strings.Contains(diskURI, test.diskURI), true, "TestCase[%d]: %s", i, test.desc)
			assert.Equal(t, strings.Contains(diskName, test.diskName), true, "TestCase[%d]: %s", i, test.desc)
		}
	}
}

func TestIsMaxDataDiskCountExceeded(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	testCloud := provider.GetTestCloud(ctrl)
	common := &controllerCommon{
		cloud:   testCloud,
		lockMap: newLockMap(),
	}
	getter := func(_ context.Context, _ string) (interface{}, error) { return nil, nil }
	common.hitMaxDataDiskCountCache, _ = azcache.NewTimedCache(5*time.Minute, getter, false)

	testCases := []struct {
		desc           string
		nodeName       string
		expectedResult bool
	}{
		{
			desc:           "max data disk count is not exceeded",
			nodeName:       "",
			expectedResult: false,
		},
		{
			desc:           "max data disk count is not exceeded though another node has exceeded",
			nodeName:       "node1",
			expectedResult: false,
		},
		{
			desc:           "max data disk count is exceeded",
			nodeName:       "node1",
			expectedResult: true,
		},
	}

	for i, test := range testCases {
		if test.expectedResult {
			common.hitMaxDataDiskCountCache.Set(test.nodeName, "")
		} else if test.nodeName != "" {
			common.hitMaxDataDiskCountCache.Set("node2", "")
		}
		result := common.isMaxDataDiskCountExceeded(context.Background(), test.nodeName)
		assert.Equal(t, test.expectedResult, result, "TestCase[%d]: %s", i, test.desc)
	}
}

// setTestVirtualMachines sets test virtual machine with powerstate.
func setTestVirtualMachines(c *provider.Cloud, vmList map[string]string, isDataDisksFull bool) []compute.VirtualMachine {
	expectedVMs := make([]compute.VirtualMachine, 0)

	for nodeName, powerState := range vmList {
		nodeName := nodeName
		instanceID := fmt.Sprintf("/subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/virtualMachines/%s", nodeName)
		vm := compute.VirtualMachine{
			Name:     &nodeName,
			ID:       &instanceID,
			Location: &c.Location,
		}
		status := []compute.InstanceViewStatus{
			{
				Code: ptr.To(powerState),
			},
			{
				Code: ptr.To("ProvisioningState/succeeded"),
			},
		}
		vm.VirtualMachineProperties = &compute.VirtualMachineProperties{
			ProvisioningState: ptr.To(string(consts.ProvisioningStateSucceeded)),
			HardwareProfile: &compute.HardwareProfile{
				VMSize: compute.StandardA0,
			},
			InstanceView: &compute.VirtualMachineInstanceView{
				Statuses: &status,
			},
			StorageProfile: &compute.StorageProfile{
				DataDisks: &[]compute.DataDisk{},
			},
		}
		if !isDataDisksFull {
			vm.StorageProfile.DataDisks = &[]compute.DataDisk{
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
			dataDisks := make([]compute.DataDisk, maxLUN)
			for i := 0; i < maxLUN; i++ {
				dataDisks[i] = compute.DataDisk{Lun: ptr.To(int32(i))}
			}
			vm.StorageProfile.DataDisks = &dataDisks
		}

		expectedVMs = append(expectedVMs, vm)
	}

	return expectedVMs
}
