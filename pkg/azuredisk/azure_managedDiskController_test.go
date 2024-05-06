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
	"fmt"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/pointer"

	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/diskclient/mock_diskclient"
	"sigs.k8s.io/cloud-provider-azure/pkg/azclient/mock_azclient"
	"sigs.k8s.io/cloud-provider-azure/pkg/consts"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

const (
	fakeGetDiskFailed = "fakeGetDiskFailed"
	disk1Name         = "disk1"
	disk1ID           = "diskid1"
)

func TestCreateManagedDisk(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	maxShare := int32(2)
	goodDiskEncryptionSetID := fmt.Sprintf("/subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/diskEncryptionSets/%s", "diskEncryptionSet-name")
	badDiskEncryptionSetID := "badDiskEncryptionSetID"
	testTags := make(map[string]*string)
	testTags[WriteAcceleratorEnabled] = pointer.String("true")
	testCases := []struct {
		desc                string
		diskID              string
		diskName            string
		storageAccountType  armcompute.DiskStorageAccountTypes
		diskIOPSReadWrite   string
		diskMBPSReadWrite   string
		diskEncryptionSetID string
		diskEncryptionType  string
		subscriptionID      string
		resouceGroup        string
		publicNetworkAccess armcompute.PublicNetworkAccess
		networkAccessPolicy armcompute.NetworkAccessPolicy
		diskAccessID        *string
		expectedDiskID      string
		existedDisk         *armcompute.Disk
		expectedErr         bool
		expectedErrMsg      error
	}{
		{
			desc:                "disk Id and no error shall be returned if everything is good with DiskStorageAccountTypesUltraSSDLRS storage account",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesUltraSSDLRS,
			diskIOPSReadWrite:   "100",
			diskMBPSReadWrite:   "100",
			diskEncryptionSetID: goodDiskEncryptionSetID,
			expectedDiskID:      disk1ID,
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         false,
		},
		{
			desc:                "disk Id and no error shall be returned if everything is good with PremiumV2LRS storage account",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesPremiumV2LRS,
			diskIOPSReadWrite:   "100",
			diskMBPSReadWrite:   "100",
			diskEncryptionSetID: goodDiskEncryptionSetID,
			expectedDiskID:      disk1ID,
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         false,
		},
		{
			desc:                "disk Id and no error shall be returned if everything is good with PremiumV2LRS storage account",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesPremiumV2LRS,
			diskIOPSReadWrite:   "",
			diskMBPSReadWrite:   "",
			diskEncryptionSetID: goodDiskEncryptionSetID,
			expectedDiskID:      disk1ID,
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         false,
		},
		{
			desc:                "disk Id and no error shall be returned if everything is good with DiskStorageAccountTypesStandardLRS storage account",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesStandardLRS,
			diskIOPSReadWrite:   "",
			diskMBPSReadWrite:   "",
			diskEncryptionSetID: goodDiskEncryptionSetID,
			expectedDiskID:      disk1ID,
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         false,
		},
		{
			desc:                "empty diskid and an error shall be returned if everything is good with DiskStorageAccountTypesUltraSSDLRS storage account but DiskIOPSReadWrite is invalid",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesUltraSSDLRS,
			diskIOPSReadWrite:   "invalid",
			diskMBPSReadWrite:   "100",
			diskEncryptionSetID: goodDiskEncryptionSetID,
			expectedDiskID:      "",
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         true,
			expectedErrMsg:      fmt.Errorf("AzureDisk - failed to parse DiskIOPSReadWrite: strconv.Atoi: parsing \"invalid\": invalid syntax"),
		},
		{
			desc:                "empty diskid and an error shall be returned if everything is good with DiskStorageAccountTypesUltraSSDLRS storage account but DiskMBPSReadWrite is invalid",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesUltraSSDLRS,
			diskIOPSReadWrite:   "100",
			diskMBPSReadWrite:   "invalid",
			diskEncryptionSetID: goodDiskEncryptionSetID,
			expectedDiskID:      "",
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         true,
			expectedErrMsg:      fmt.Errorf("AzureDisk - failed to parse DiskMBpsReadWrite: strconv.Atoi: parsing \"invalid\": invalid syntax"),
		},
		{
			desc:                "empty diskid and an error shall be returned if everything is good with DiskStorageAccountTypesUltraSSDLRS storage account with bad Disk EncryptionSetID",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesUltraSSDLRS,
			diskIOPSReadWrite:   "100",
			diskMBPSReadWrite:   "100",
			diskEncryptionSetID: badDiskEncryptionSetID,
			expectedDiskID:      "",
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         true,
			expectedErrMsg:      fmt.Errorf("AzureDisk - format of DiskEncryptionSetID(%s) is incorrect, correct format: %s", badDiskEncryptionSetID, consts.DiskEncryptionSetIDFormat),
		},
		{
			desc:                "DiskEncryptionType should be empty when DiskEncryptionSetID is not set",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesStandardLRS,
			diskEncryptionSetID: "",
			diskEncryptionType:  "EncryptionAtRestWithCustomerKey",
			expectedDiskID:      "",
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         true,
			expectedErrMsg:      fmt.Errorf("AzureDisk - DiskEncryptionType(EncryptionAtRestWithCustomerKey) should be empty when DiskEncryptionSetID is not set"),
		},
		{
			desc:                "disk Id and no error shall be returned if everything is good with DiskStorageAccountTypesStandardLRS storage account with not empty diskIOPSReadWrite",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesStandardLRS,
			diskIOPSReadWrite:   "100",
			diskMBPSReadWrite:   "",
			diskEncryptionSetID: goodDiskEncryptionSetID,
			expectedDiskID:      "",
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         true,
			expectedErrMsg:      fmt.Errorf("AzureDisk - DiskIOPSReadWrite parameter is only applicable in UltraSSD_LRS disk type"),
		},
		{
			desc:                "disk Id and no error shall be returned if everything is good with DiskStorageAccountTypesStandardLRS storage account with not empty diskMBPSReadWrite",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesStandardLRS,
			diskIOPSReadWrite:   "",
			diskMBPSReadWrite:   "100",
			diskEncryptionSetID: goodDiskEncryptionSetID,
			expectedDiskID:      "",
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         true,
			expectedErrMsg:      fmt.Errorf("AzureDisk - DiskMBpsReadWrite parameter is only applicable in UltraSSD_LRS disk type"),
		},
		{
			desc:                "correct NetworkAccessPolicy(DenyAll) setting",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesStandardLRS,
			diskEncryptionSetID: goodDiskEncryptionSetID,
			networkAccessPolicy: armcompute.NetworkAccessPolicyDenyAll,
			publicNetworkAccess: armcompute.PublicNetworkAccessDisabled,
			expectedDiskID:      disk1ID,
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         false,
		},
		{
			desc:                "correct NetworkAccessPolicy(AllowAll) setting",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesStandardLRS,
			diskEncryptionSetID: goodDiskEncryptionSetID,
			diskEncryptionType:  "EncryptionAtRestWithCustomerKey",
			networkAccessPolicy: armcompute.NetworkAccessPolicyAllowAll,
			publicNetworkAccess: armcompute.PublicNetworkAccessEnabled,
			expectedDiskID:      disk1ID,
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         false,
		},
		{
			desc:                "DiskAccessID should not be empty when NetworkAccessPolicy is AllowPrivate",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesStandardLRS,
			diskEncryptionSetID: goodDiskEncryptionSetID,
			networkAccessPolicy: armcompute.NetworkAccessPolicyAllowPrivate,
			expectedDiskID:      "",
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         true,
			expectedErrMsg:      fmt.Errorf("DiskAccessID should not be empty when NetworkAccessPolicy is AllowPrivate"),
		},
		{
			desc:                "DiskAccessID(%s) must be empty when NetworkAccessPolicy(%s) is not AllowPrivate",
			diskID:              disk1ID,
			diskName:            disk1Name,
			storageAccountType:  armcompute.DiskStorageAccountTypesStandardLRS,
			diskEncryptionSetID: goodDiskEncryptionSetID,
			networkAccessPolicy: armcompute.NetworkAccessPolicyAllowAll,
			diskAccessID:        pointer.String("diskAccessID"),
			expectedDiskID:      "",
			existedDisk:         &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:         true,
			expectedErrMsg:      fmt.Errorf("DiskAccessID(diskAccessID) must be empty when NetworkAccessPolicy(AllowAll) is not AllowPrivate"),
		},
		{
			desc:           "resourceGroup must be specified when subscriptionID is not empty",
			diskID:         "",
			diskName:       disk1Name,
			subscriptionID: "abc",
			resouceGroup:   "",
			expectedDiskID: "",
			existedDisk:    &armcompute.Disk{ID: pointer.String(disk1ID), Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{Encryption: &armcompute.Encryption{DiskEncryptionSetID: &goodDiskEncryptionSetID, Type: to.Ptr(armcompute.EncryptionTypeEncryptionAtRestWithCustomerKey)}, ProvisioningState: pointer.String("Succeeded")}, Tags: testTags},
			expectedErr:    true,
			expectedErrMsg: fmt.Errorf("resourceGroup must be specified when subscriptionID(abc) is not empty"),
		},
	}

	for i, test := range testCases {
		testCloud := provider.GetTestCloud(ctrl)

		common := &controllerCommon{
			cloud:                        testCloud,
			lockMap:                      newLockMap(),
			AttachDetachInitialDelayInMs: defaultAttachDetachInitialDelayInMs,
			clientFactory:                testCloud.ComputeClientFactory,
		}

		managedDiskController := &ManagedDiskController{common}
		volumeOptions := &ManagedDiskOptions{
			DiskName:            test.diskName,
			StorageAccountType:  test.storageAccountType,
			ResourceGroup:       test.resouceGroup,
			SizeGB:              1,
			Tags:                map[string]string{"tag1": "azure-tag1"},
			AvailabilityZone:    "westus-testzone",
			DiskIOPSReadWrite:   test.diskIOPSReadWrite,
			DiskMBpsReadWrite:   test.diskMBPSReadWrite,
			DiskEncryptionSetID: test.diskEncryptionSetID,
			DiskEncryptionType:  test.diskEncryptionType,
			MaxShares:           maxShare,
			NetworkAccessPolicy: test.networkAccessPolicy,
			PublicNetworkAccess: test.publicNetworkAccess,
			DiskAccessID:        test.diskAccessID,
			SubscriptionID:      test.subscriptionID,
		}

		mockDisksClient := mock_diskclient.NewMockInterface(ctrl)
		common.clientFactory.(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(testCloud.SubscriptionID).Return(mockDisksClient, nil).AnyTimes()
		//disk := getTestDisk(test.diskName)
		mockDisksClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), test.diskName, gomock.Any()).Return(test.existedDisk, nil).AnyTimes()
		mockDisksClient.EXPECT().Get(gomock.Any(), gomock.Any(), test.diskName).Return(test.existedDisk, nil).AnyTimes()

		actualDiskID, err := managedDiskController.CreateManagedDisk(ctx, volumeOptions)
		assert.Equal(t, test.expectedDiskID, actualDiskID, "TestCase[%d]: %s", i, test.desc)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s, return error: %v", i, test.desc, err)
		if test.expectedErr {
			assert.EqualError(t, test.expectedErrMsg, err.Error(), "TestCase[%d]: %s, expected error: %v, return error: %v", i, test.desc, test.expectedErrMsg, err)
		}
	}
}

func TestCreateManagedDiskWithExtendedLocation(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testCloud := provider.GetTestCloudWithExtendedLocation(ctrl)
	diskName := disk1Name
	expectedDiskID := disk1ID
	el := &armcompute.ExtendedLocation{
		Name: pointer.String("microsoftlosangeles1"),
		Type: to.Ptr(armcompute.ExtendedLocationTypesEdgeZone),
	}

	diskreturned := armcompute.Disk{
		ID:               pointer.String(expectedDiskID),
		Name:             pointer.String(diskName),
		ExtendedLocation: el,
		Properties: &armcompute.DiskProperties{
			ProvisioningState: pointer.String("Succeeded"),
		},
	}

	common := &controllerCommon{
		cloud:                        testCloud,
		lockMap:                      newLockMap(),
		AttachDetachInitialDelayInMs: defaultAttachDetachInitialDelayInMs,
		clientFactory:                testCloud.ComputeClientFactory,
	}

	managedDiskController := &ManagedDiskController{common}
	volumeOptions := &ManagedDiskOptions{
		DiskName:           diskName,
		StorageAccountType: armcompute.DiskStorageAccountTypes(armcompute.EdgeZoneStorageAccountTypePremiumLRS),
		ResourceGroup:      "",
		SizeGB:             1,
		AvailabilityZone:   "westus-testzone",
	}

	mockDisksClient := mock_diskclient.NewMockInterface(ctrl)
	common.clientFactory.(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(testCloud.SubscriptionID).Return(mockDisksClient, nil).AnyTimes()
	mockDisksClient.EXPECT().CreateOrUpdate(gomock.Any(), testCloud.ResourceGroup, diskName, gomock.Any()).
		Do(func(ctx interface{}, rg, dn string, disk armcompute.Disk) {
			assert.Equal(t, el.Name, disk.ExtendedLocation.Name, "The extended location name should match.")
			assert.Equal(t, el.Type, disk.ExtendedLocation.Type, "The extended location type should match.")
		}).Return(to.Ptr(diskreturned), nil)

	mockDisksClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, diskName).Return(&diskreturned, nil).AnyTimes()

	actualDiskID, err := managedDiskController.CreateManagedDisk(ctx, volumeOptions)
	assert.Equal(t, expectedDiskID, actualDiskID, "Disk ID does not match.")
	assert.Nil(t, err, "There should not be an error.")
}

func TestDeleteManagedDisk(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	testCases := []struct {
		desc           string
		diskName       string
		diskState      string
		existedDisk    *armcompute.Disk
		expectedErr    bool
		expectedErrMsg error
	}{
		{
			desc:           "an error shall be returned if delete an attaching disk",
			diskName:       disk1Name,
			diskState:      "attaching",
			existedDisk:    &armcompute.Disk{Name: pointer.String(disk1Name)},
			expectedErr:    true,
			expectedErrMsg: fmt.Errorf("failed to delete disk(/subscriptions/subscription/resourceGroups/rg/providers/Microsoft.Compute/disks/disk1) since it's in attaching state"),
		},
		{
			desc:        "no error shall be returned if everything is good",
			diskName:    disk1Name,
			existedDisk: &armcompute.Disk{Name: pointer.String(disk1Name)},
			expectedErr: false,
		},
		{
			desc:           "an error shall be returned if get disk failed",
			diskName:       fakeGetDiskFailed,
			existedDisk:    &armcompute.Disk{Name: pointer.String(fakeGetDiskFailed)},
			expectedErr:    true,
			expectedErrMsg: fmt.Errorf("Get Disk failed"),
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	for i, test := range testCases {
		testCloud := provider.GetTestCloud(ctrl)

		common := &controllerCommon{
			cloud:                        testCloud,
			lockMap:                      newLockMap(),
			AttachDetachInitialDelayInMs: defaultAttachDetachInitialDelayInMs,
			clientFactory:                testCloud.ComputeClientFactory,
		}

		managedDiskController := &ManagedDiskController{common}
		diskURI := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s",
			testCloud.SubscriptionID, testCloud.ResourceGroup, *test.existedDisk.Name)
		if test.diskState == "attaching" {
			managedDiskController.diskStateMap.Store(strings.ToLower(diskURI), test.diskState)
		}

		mockDisksClient := mock_diskclient.NewMockInterface(ctrl)
		common.clientFactory.(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(testCloud.SubscriptionID).Return(mockDisksClient, nil).AnyTimes()
		if test.diskName == fakeGetDiskFailed {
			mockDisksClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, test.diskName).Return(test.existedDisk, fmt.Errorf("Get Disk failed")).AnyTimes()
		} else {
			mockDisksClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, test.diskName).Return(test.existedDisk, nil).AnyTimes()
		}
		mockDisksClient.EXPECT().Delete(gomock.Any(), testCloud.ResourceGroup, test.diskName).Return(nil).AnyTimes()

		err := managedDiskController.DeleteManagedDisk(ctx, diskURI)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s, return error: %v", i, test.desc, err)
		if test.expectedErr {
			assert.EqualError(t, test.expectedErrMsg, err.Error(), "TestCase[%d]: %s, expected: %v, return: %v", i, test.desc, test.expectedErrMsg, err)
		}
	}
}

func TestGetDisk(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	testCases := []struct {
		desc                      string
		diskName                  string
		existedDisk               *armcompute.Disk
		expectedErr               bool
		expectedErrMsg            error
		expectedProvisioningState string
		expectedDiskID            string
	}{
		{
			desc:                      "no error shall be returned if get a normal disk without DiskProperties",
			diskName:                  disk1Name,
			existedDisk:               &armcompute.Disk{Name: pointer.String(disk1Name)},
			expectedErr:               false,
			expectedProvisioningState: "",
			expectedDiskID:            "",
		},
		{
			desc:                      "an error shall be returned if get disk failed",
			diskName:                  fakeGetDiskFailed,
			existedDisk:               &armcompute.Disk{Name: pointer.String(fakeGetDiskFailed)},
			expectedErr:               true,
			expectedErrMsg:            fmt.Errorf("Get Disk failed"),
			expectedProvisioningState: "",
			expectedDiskID:            "",
		},
	}

	for i, test := range testCases {
		testCloud := provider.GetTestCloud(ctrl)
		managedDiskController := &ManagedDiskController{
			controllerCommon: &controllerCommon{
				cloud:               testCloud,
				lockMap:             newLockMap(),
				DisableDiskLunCheck: true,
				clientFactory:       testCloud.ComputeClientFactory,
			},
		}

		mockDisksClient := mock_diskclient.NewMockInterface(ctrl)
		managedDiskController.controllerCommon.clientFactory.(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub("").Return(mockDisksClient, nil).AnyTimes()
		if test.diskName == fakeGetDiskFailed {
			mockDisksClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, test.diskName).Return(test.existedDisk, fmt.Errorf("Get Disk failed")).AnyTimes()
		} else {
			mockDisksClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, test.diskName).Return(test.existedDisk, nil).AnyTimes()
		}

		provisioningState, diskid, err := managedDiskController.GetDisk(ctx, "", testCloud.ResourceGroup, test.diskName)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s, return error: %v", i, test.desc, err)
		if test.expectedErr {
			assert.EqualError(t, test.expectedErrMsg, err.Error(), "TestCase[%d]: %s, expected: %v, return: %v", i, test.desc, test.expectedErrMsg, err)
		}
		assert.Equal(t, test.expectedProvisioningState, provisioningState, "TestCase[%d]: %s, expected ProvisioningState: %v, return ProvisioningState: %v", i, test.desc, test.expectedProvisioningState, provisioningState)
		assert.Equal(t, test.expectedDiskID, diskid, "TestCase[%d]: %s, expected DiskID: %v, return DiskID: %v", i, test.desc, test.expectedDiskID, diskid)
	}
}

func TestResizeDisk(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	diskSizeGB := int32(2)
	diskName := disk1Name
	fakeCreateDiskFailed := "fakeCreateDiskFailed"
	testCases := []struct {
		desc             string
		diskName         string
		oldSize          resource.Quantity
		newSize          resource.Quantity
		existedDisk      *armcompute.Disk
		expectedQuantity resource.Quantity
		expectedErr      bool
		expectedErrMsg   error
	}{
		{
			desc:             "new quantity and no error shall be returned if everything is good",
			diskName:         diskName,
			oldSize:          *resource.NewQuantity(2*(1024*1024*1024), resource.BinarySI),
			newSize:          *resource.NewQuantity(3*(1024*1024*1024), resource.BinarySI),
			existedDisk:      &armcompute.Disk{Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{DiskSizeGB: &diskSizeGB, DiskState: to.Ptr(armcompute.DiskStateUnattached)}},
			expectedQuantity: *resource.NewQuantity(3*(1024*1024*1024), resource.BinarySI),
			expectedErr:      false,
		},
		{
			desc:             "new quantity and no error shall be returned if everything is good with DiskProperties is null",
			diskName:         diskName,
			oldSize:          *resource.NewQuantity(2*(1024*1024*1024), resource.BinarySI),
			newSize:          *resource.NewQuantity(3*(1024*1024*1024), resource.BinarySI),
			existedDisk:      &armcompute.Disk{Name: pointer.String(disk1Name)},
			expectedQuantity: *resource.NewQuantity(2*(1024*1024*1024), resource.BinarySI),
			expectedErr:      true,
			expectedErrMsg:   fmt.Errorf("DiskProperties of disk(%s) is nil", diskName),
		},
		{
			desc:             "new quantity and no error shall be returned if everything is good with disk already of greater or equal size than requested",
			diskName:         diskName,
			oldSize:          *resource.NewQuantity(1*(1024*1024*1024), resource.BinarySI),
			newSize:          *resource.NewQuantity(2*(1024*1024*1024), resource.BinarySI),
			existedDisk:      &armcompute.Disk{Name: pointer.String(disk1Name), Properties: &armcompute.DiskProperties{DiskSizeGB: &diskSizeGB, DiskState: to.Ptr(armcompute.DiskStateUnattached)}},
			expectedQuantity: *resource.NewQuantity(2*(1024*1024*1024), resource.BinarySI),
			expectedErr:      false,
		},
		{
			desc:             "an error shall be returned if everything is good but get disk failed",
			diskName:         fakeGetDiskFailed,
			oldSize:          *resource.NewQuantity(2*(1024*1024*1024), resource.BinarySI),
			newSize:          *resource.NewQuantity(3*(1024*1024*1024), resource.BinarySI),
			existedDisk:      &armcompute.Disk{Name: pointer.String(fakeGetDiskFailed), Properties: &armcompute.DiskProperties{DiskSizeGB: &diskSizeGB, DiskState: to.Ptr(armcompute.DiskStateUnattached)}},
			expectedQuantity: *resource.NewQuantity(2*(1024*1024*1024), resource.BinarySI),
			expectedErr:      true,
			expectedErrMsg:   fmt.Errorf("Get Disk failed"),
		},
		{
			desc:             "an error shall be returned if everything is good but create disk failed",
			diskName:         fakeCreateDiskFailed,
			oldSize:          *resource.NewQuantity(2*(1024*1024*1024), resource.BinarySI),
			newSize:          *resource.NewQuantity(3*(1024*1024*1024), resource.BinarySI),
			existedDisk:      &armcompute.Disk{Name: pointer.String(fakeCreateDiskFailed), Properties: &armcompute.DiskProperties{DiskSizeGB: &diskSizeGB, DiskState: to.Ptr(armcompute.DiskStateUnattached)}},
			expectedQuantity: *resource.NewQuantity(2*(1024*1024*1024), resource.BinarySI),
			expectedErr:      true,
			expectedErrMsg:   fmt.Errorf("Create Disk failed"),
		},
		{
			desc:             "an error shall be returned if disk is not in Unattached state",
			diskName:         fakeCreateDiskFailed,
			oldSize:          *resource.NewQuantity(2*(1024*1024*1024), resource.BinarySI),
			newSize:          *resource.NewQuantity(3*(1024*1024*1024), resource.BinarySI),
			existedDisk:      &armcompute.Disk{Name: pointer.String(fakeCreateDiskFailed), Properties: &armcompute.DiskProperties{DiskSizeGB: &diskSizeGB, DiskState: to.Ptr(armcompute.DiskStateAttached)}},
			expectedQuantity: *resource.NewQuantity(2*(1024*1024*1024), resource.BinarySI),
			expectedErr:      true,
			expectedErrMsg:   fmt.Errorf("azureDisk - disk resize is only supported on Unattached disk, current disk state: Attached, already attached to "),
		},
	}

	for i, test := range testCases {
		testCloud := provider.GetTestCloud(ctrl)
		managedDiskController := &ManagedDiskController{
			controllerCommon: &controllerCommon{
				cloud:               testCloud,
				lockMap:             newLockMap(),
				DisableDiskLunCheck: true,
				clientFactory:       testCloud.ComputeClientFactory,
			},
		}
		diskURI := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s",
			testCloud.SubscriptionID, testCloud.ResourceGroup, *test.existedDisk.Name)

		mockDisksClient := mock_diskclient.NewMockInterface(ctrl)
		managedDiskController.controllerCommon.clientFactory.(*mock_azclient.MockClientFactory).EXPECT().GetDiskClientForSub(testCloud.SubscriptionID).Return(mockDisksClient, nil).AnyTimes()
		if test.diskName == fakeGetDiskFailed {
			mockDisksClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, test.diskName).Return(test.existedDisk, fmt.Errorf("Get Disk failed")).AnyTimes()
		} else {
			mockDisksClient.EXPECT().Get(gomock.Any(), testCloud.ResourceGroup, test.diskName).Return(test.existedDisk, nil).AnyTimes()
		}
		if test.diskName == fakeCreateDiskFailed {
			mockDisksClient.EXPECT().Patch(gomock.Any(), testCloud.ResourceGroup, test.diskName, gomock.Any()).Return(test.existedDisk, fmt.Errorf("Create Disk failed")).AnyTimes()
		} else {
			mockDisksClient.EXPECT().Patch(gomock.Any(), testCloud.ResourceGroup, test.diskName, gomock.Any()).Return(test.existedDisk, nil).AnyTimes()
		}

		result, err := managedDiskController.ResizeDisk(ctx, diskURI, test.oldSize, test.newSize, false)
		assert.Equal(t, test.expectedErr, err != nil, "TestCase[%d]: %s, return error: %v", i, test.desc, err)
		if test.expectedErr {
			assert.EqualError(t, test.expectedErrMsg, err.Error(), "TestCase[%d]: %s, expected: %v, return: %v", i, test.desc, test.expectedErrMsg, err)
		}
		assert.Equal(t, test.expectedQuantity.Value(), result.Value(), "TestCase[%d]: %s, expected Quantity: %v, return Quantity: %v", i, test.desc, test.expectedQuantity, result)
	}
}
