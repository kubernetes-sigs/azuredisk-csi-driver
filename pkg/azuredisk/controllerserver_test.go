/*
Copyright 2019 The Kubernetes Authors.

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
	"reflect"
	"testing"

	"github.com/Azure/go-autorest/autorest/date"
	"github.com/golang/protobuf/ptypes"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/mock/gomock"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockcorev1"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockkubeclient"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/mockpersistentvolume"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/cloud-provider-azure/pkg/azureclients/diskclient/mockdiskclient"
	"sigs.k8s.io/cloud-provider-azure/pkg/azureclients/snapshotclient/mocksnapshotclient"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
	"sigs.k8s.io/cloud-provider-azure/pkg/retry"
)

var (
	testVolumeName = "unit-test-volume"
	testVolumeID   = fmt.Sprintf(managedDiskPath, "subs", "rg", testVolumeName)
)

func checkTestError(t *testing.T, expectedErrCode codes.Code, err error) {
	s, ok := status.FromError(err)
	if !ok {
		t.Errorf("could not get error status from err: %v", s)
	}
	if s.Code() != expectedErrCode {
		t.Errorf("expected error code: %v, actual: %v, err: %v", expectedErrCode, s.Code(), err)
	}
}

func TestGetCachingMode(t *testing.T) {
	tests := []struct {
		options             map[string]string
		expectedCachingMode compute.CachingTypes
		expectedError       bool
	}{
		{
			nil,
			compute.CachingTypes(defaultAzureDataDiskCachingMode),
			false,
		},
		{
			map[string]string{},
			compute.CachingTypes(defaultAzureDataDiskCachingMode),
			false,
		},
		{
			map[string]string{cachingModeField: ""},
			compute.CachingTypes(defaultAzureDataDiskCachingMode),
			false,
		},
		{
			map[string]string{cachingModeField: "None"},
			compute.CachingTypes("None"),
			false,
		},
		{
			map[string]string{cachingModeField: "ReadOnly"},
			compute.CachingTypes("ReadOnly"),
			false,
		},
		{
			map[string]string{cachingModeField: "ReadWrite"},
			compute.CachingTypes("ReadWrite"),
			false,
		},
		{
			map[string]string{cachingModeField: "WriteOnly"},
			compute.CachingTypes(""),
			true,
		},
	}

	for _, test := range tests {
		resultCachingMode, resultError := getCachingMode(test.options)
		if resultCachingMode != test.expectedCachingMode || (resultError != nil) != test.expectedError {
			t.Errorf("input: %s, getCachingMode resultCachingMode: %s, expectedCachingMode: %s, resultError: %s, expectedError: %t", test.options, resultCachingMode, test.expectedCachingMode, resultError, test.expectedError)
		}
	}
}

func TestGetEntriesAndNextToken(t *testing.T) {
	provisioningState := "succeeded"
	DiskSize := int32(10)
	snapshotID := "test"
	sourceVolumeID := "unit-test"
	creationdate := compute.CreationData{
		SourceResourceID: &sourceVolumeID,
	}
	snapshot := compute.Snapshot{
		SnapshotProperties: &compute.SnapshotProperties{
			TimeCreated:       &date.Time{},
			ProvisioningState: &provisioningState,
			DiskSizeGB:        &DiskSize,
			CreationData:      &creationdate,
		},
		ID: &snapshotID,
	}
	snapshots := []compute.Snapshot{}
	snapshots = append(snapshots, snapshot)
	entries := []*csi.ListSnapshotsResponse_Entry{}
	csiSnapshot, _ := generateCSISnapshot(sourceVolumeID, &snapshot)
	entries = append(entries, &csi.ListSnapshotsResponse_Entry{Snapshot: csiSnapshot})
	tests := []struct {
		request          *csi.ListSnapshotsRequest
		snapshots        []compute.Snapshot
		expectedResponse *csi.ListSnapshotsResponse
		expectedError    error
	}{
		{
			&csi.ListSnapshotsRequest{
				MaxEntries:    2,
				StartingToken: "a",
			},
			[]compute.Snapshot{},
			nil,
			status.Errorf(codes.Aborted, "ListSnapshots starting token(a) parsing with error: strconv.Atoi: parsing \"a\": invalid syntax"),
		},
		{
			&csi.ListSnapshotsRequest{
				MaxEntries:    2,
				StartingToken: "01",
			},
			[]compute.Snapshot{},
			nil,
			status.Errorf(codes.Aborted, "ListSnapshots starting token(1) is greater than total number of snapshots"),
		},
		{
			&csi.ListSnapshotsRequest{
				MaxEntries:    2,
				StartingToken: "0",
			},
			[]compute.Snapshot{},
			nil,
			status.Errorf(codes.Aborted, "ListSnapshots starting token(0) is greater than total number of snapshots"),
		},
		{
			&csi.ListSnapshotsRequest{
				MaxEntries:    2,
				StartingToken: "-1",
			},
			[]compute.Snapshot{},
			nil,
			status.Errorf(codes.Aborted, "ListSnapshots starting token(-1) can not be negative"),
		},
		{
			&csi.ListSnapshotsRequest{
				MaxEntries:     2,
				SourceVolumeId: sourceVolumeID,
			},
			snapshots,
			&csi.ListSnapshotsResponse{
				Entries:   entries,
				NextToken: "1",
			},
			error(nil),
		},
	}

	for _, test := range tests {
		resultResponse, resultError := getEntriesAndNextToken(test.request, test.snapshots)
		if !reflect.DeepEqual(resultResponse, test.expectedResponse) || (!reflect.DeepEqual(resultError, test.expectedError)) {
			t.Errorf("request: %v, snapshotListPage: %v, resultResponse: %v, expectedResponse: %v, resultError: %v, expectedError: %v", test.request, test.snapshots, resultResponse, test.expectedResponse, resultError, test.expectedError)
		}
	}
}

func TestCreateVolume(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: " invalid ",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				d.setControllerCapabilities([]*csi.ControllerServiceCapability{})

				req := &csi.CreateVolumeRequest{}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "CREATE_DELETE_VOLUME")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: " volume name missing",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				req := &csi.CreateVolumeRequest{}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "CreateVolume Name must be provided")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "volume capabilities missing",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				req := &csi.CreateVolumeRequest{
					Name: "unit-test",
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "CreateVolume Volume capabilities must be provided")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "require volume size exceed",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				stdCapacityRange = &csi.CapacityRange{
					RequiredBytes: volumehelper.GiBToBytes(15),
					LimitBytes:    volumehelper.GiBToBytes(10),
				}
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					CapacityRange:      stdCapacityRange,
					VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "After round-up, volume size exceeds the limit specified")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "maxshare parse error ",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				mp := make(map[string]string)
				mp[maxSharesField] = "aaa"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					Parameters:         mp,
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "parse aaa failed with error: strconv.Atoi: parsing \"aaa\": invalid syntax")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "maxshare invalid value ",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				mp := make(map[string]string)
				mp[maxSharesField] = "0"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: stdVolumeCapabilities,
					Parameters:         mp,
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "parse 0 returned with invalid value: 0")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "invalid perf profile",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				mp := make(map[string]string)
				mp[perfProfileField] = "blah"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: stdVolumeCapabilities,
					Parameters:         mp,
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "Perf profile blah is not supported. Supported tuning modes are none and basic.")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Volume capability not supported ",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				mp := make(map[string]string)
				mp[maxSharesField] = "1"
				mp[skuNameField] = "ut"
				mp[locationField] = "ut"
				mp[storageAccountTypeField] = "ut"
				mp[resourceGroupField] = "ut"
				mp[diskIOPSReadWriteField] = "ut"
				mp[diskMBPSReadWriteField] = "ut"
				mp[diskNameField] = "ut"
				mp["diskencryptionsetid"] = "ut"
				mp["writeacceleratorenabled"] = "ut"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_MULTI_NODE_READER_ONLY),
					Parameters:         mp,
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "Volume capability(MULTI_NODE_READER_ONLY) not supported")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "normalize storageaccounttype error ",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				mp := make(map[string]string)
				mp[storageAccountTypeField] = "NOT_EXISTING"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					Parameters:         mp,
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := fmt.Errorf("azureDisk - NOT_EXISTING is not supported sku/storageaccounttype. Supported values are [Premium_LRS Premium_ZRS Standard_LRS StandardSSD_LRS StandardSSD_ZRS UltraSSD_LRS]")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "normalize cache mode error ",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				mp := make(map[string]string)
				mp[cachingModeField] = "WriteOnly"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					Parameters:         mp,
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := fmt.Errorf("azureDisk - WriteOnly is not supported cachingmode. Supported values are [None ReadOnly ReadWrite]")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "custom tags error ",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				mp := make(map[string]string)
				mp["tags"] = "unit-test"
				req := &csi.CreateVolumeRequest{
					Name:               "unit-test",
					VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					Parameters:         mp,
				}
				disk := compute.Disk{
					DiskProperties: &compute.DiskProperties{},
				}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := fmt.Errorf("Tags 'unit-test' are invalid, the format should like: 'key1=value1,key2=value2'")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "create managed disk error ",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				mp := make(map[string]string)
				mp["tags"] = "unit=test"
				volumeSnapshotSource := &csi.VolumeContentSource_SnapshotSource{
					SnapshotId: "unit-test",
				}
				volumeContentSourceSnapshotSource := &csi.VolumeContentSource_Snapshot{
					Snapshot: volumeSnapshotSource,
				}
				volumecontensource := csi.VolumeContentSource{
					Type: volumeContentSourceSnapshotSource,
				}
				req := &csi.CreateVolumeRequest{
					Name:                "unit-test",
					VolumeCapabilities:  createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					Parameters:          mp,
					VolumeContentSource: &volumecontensource,
				}
				disk := compute.Disk{
					DiskProperties: &compute.DiskProperties{},
				}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				rerr := &retry.Error{
					RawError: fmt.Errorf("test"),
				}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(rerr).AnyTimes()
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := fmt.Errorf("Retriable: false, RetryAfter: 0s, HTTPStatusCode: 0, RawError: test")
				if err.Error() != expectedErr.Error() {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "create managed disk not found error ",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				mp := make(map[string]string)
				volumeContentSourceSnapshotSource := &csi.VolumeContentSource_Snapshot{}
				volumecontensource := csi.VolumeContentSource{
					Type: volumeContentSourceSnapshotSource,
				}
				req := &csi.CreateVolumeRequest{
					Name:                "unit-test",
					VolumeCapabilities:  createVolumeCapabilities(csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER),
					Parameters:          mp,
					VolumeContentSource: &volumecontensource,
				}
				disk := compute.Disk{
					DiskProperties: &compute.DiskProperties{},
				}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				rerr := &retry.Error{
					RawError: fmt.Errorf(NotFound),
				}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(rerr).AnyTimes()
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := status.Error(codes.NotFound, "Retriable: false, RetryAfter: 0s, HTTPStatusCode: 0, RawError: NotFound")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "valid request",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				stdCapacityRangetest := &csi.CapacityRange{
					RequiredBytes: volumehelper.GiBToBytes(10),
					LimitBytes:    volumehelper.GiBToBytes(15),
				}
				req := &csi.CreateVolumeRequest{
					Name:               testVolumeName,
					VolumeCapabilities: stdVolumeCapabilities,
					CapacityRange:      stdCapacityRangetest,
				}
				size := int32(volumehelper.BytesToGiB(req.CapacityRange.RequiredBytes))
				id := fmt.Sprintf(managedDiskPath, "subs", "rg", testVolumeName)
				state := string(compute.ProvisioningStateSucceeded)
				disk := compute.Disk{
					ID:   &id,
					Name: &testVolumeName,
					DiskProperties: &compute.DiskProperties{
						DiskSizeGB:        &size,
						ProvisioningState: &state,
					},
				}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := error(nil)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "invalid parameter",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				stdCapacityRangetest := &csi.CapacityRange{
					RequiredBytes: volumehelper.GiBToBytes(10),
					LimitBytes:    volumehelper.GiBToBytes(15),
				}
				req := &csi.CreateVolumeRequest{
					Name:               testVolumeName,
					VolumeCapabilities: stdVolumeCapabilities,
					CapacityRange:      stdCapacityRangetest,
					Parameters:         map[string]string{"invalidparameter": "value"},
				}
				_, err := d.CreateVolume(context.Background(), req)
				expectedErr := fmt.Errorf("invalid parameter %s in storage class", "invalidparameter")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestDeleteVolume(t *testing.T) {
	d, err := NewFakeDriver(t)
	if err != nil {
		t.Fatalf("Error getting driver: %v", err)
	}

	tests := []struct {
		desc            string
		req             *csi.DeleteVolumeRequest
		expectedResp    *csi.DeleteVolumeResponse
		expectedErrCode codes.Code
	}{
		{
			desc: "success standard",
			req: &csi.DeleteVolumeRequest{
				VolumeId: testVolumeID,
			},
			expectedResp: &csi.DeleteVolumeResponse{},
		},
		{
			desc: "fail with no volume id",
			req: &csi.DeleteVolumeRequest{
				VolumeId: "",
			},
			expectedResp:    nil,
			expectedErrCode: codes.InvalidArgument,
		},
		{
			desc: "fail with the invalid diskURI",
			req: &csi.DeleteVolumeRequest{
				VolumeId: "123",
			},
			expectedResp: &csi.DeleteVolumeResponse{},
		},
	}

	for _, test := range tests {
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		id := test.req.VolumeId
		disk := compute.Disk{
			ID: &id,
		}

		d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Eq(ctx), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
		d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().Delete(gomock.Eq(ctx), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

		result, err := d.DeleteVolume(context.Background(), test.req)
		if err != nil {
			checkTestError(t, test.expectedErrCode, err)
		}
		if !reflect.DeepEqual(result, test.expectedResp) {
			t.Errorf("input request: %v, DeleteVolume result: %v, expected: %v", test.req, result, test.expectedResp)
		}
	}
}

func TestControllerGetVolume(t *testing.T) {
	d, err := NewFakeDriver(t)
	if err != nil {
		t.Fatalf("Error getting driver: %v", err)
	}
	req := csi.ControllerGetVolumeRequest{}
	resp, err := d.ControllerGetVolume(context.Background(), &req)
	assert.Nil(t, resp)
	if !reflect.DeepEqual(err, status.Error(codes.Unimplemented, "")) {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestIsCSISnapshotReady(t *testing.T) {
	tests := []struct {
		state        string
		expectedResp bool
	}{
		{
			state:        "Succeeded",
			expectedResp: true,
		},
		{
			state:        "succeeded",
			expectedResp: true,
		},
		{
			state:        "fail",
			expectedResp: false,
		},
	}
	for _, test := range tests {
		flag, err := isCSISnapshotReady(test.state)

		if flag != test.expectedResp {
			t.Errorf("testdesc: %v \n expected result:%t \n actual result:%t", test.state, test.expectedResp, flag)
		}
		assert.Nil(t, err)
	}
}

func TestGetSnapshotInfo(t *testing.T) {
	d, err := NewFakeDriver(t)
	if err != nil {
		t.Fatalf("Error getting driver: %v", err)
	}
	tests := []struct {
		snapshotID string
		expected1  string
		expected2  string
		expected3  error
	}{
		{
			snapshotID: "testurl/subscriptions/12/resourceGroups/23/providers/Microsoft.Compute/snapshots/snapshot-name",
			expected1:  "snapshot-name",
			expected2:  "23",
			expected3:  nil,
		},
		{
			// case insentive check
			snapshotID: "testurl/subscriptions/12/resourcegroups/23/providers/Microsoft.Compute/snapshots/snapshot-name",
			expected1:  "snapshot-name",
			expected2:  "23",
			expected3:  nil,
		},
		{
			snapshotID: "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name",
			expected1:  "",
			expected2:  "",
			expected3:  fmt.Errorf("could not get snapshot name from testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name, correct format: (?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/snapshots/(.+)"),
		},
	}
	for _, test := range tests {
		snapshotName, resourceGroup, err := d.getSnapshotInfo(test.snapshotID)
		if !reflect.DeepEqual(snapshotName, test.expected1) || !reflect.DeepEqual(resourceGroup, test.expected2) || !reflect.DeepEqual(err, test.expected3) {
			t.Errorf("input: %q, getSnapshotName result: %q, expected1: %q, getresourcegroup result: %q, expected2: %q\n", test.snapshotID, snapshotName, test.expected1,
				resourceGroup, test.expected2)
			if err != nil {
				t.Errorf("err result %q\n", err)
			}
		}
	}
}

func TestControllerPublishVolume(t *testing.T) {
	d, err := NewFakeDriver(t)
	d.setCloud(&azure.Cloud{})
	if err != nil {
		t.Fatalf("Error getting driver: %v", err)
	}
	volumeCap := csi.VolumeCapability_AccessMode{Mode: 2}
	volumeCapWrong := csi.VolumeCapability_AccessMode{Mode: 10}
	tests := []struct {
		desc        string
		req         *csi.ControllerPublishVolumeRequest
		expectedErr error
	}{
		{
			desc:        "Volume ID missing",
			req:         &csi.ControllerPublishVolumeRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID not provided"),
		},
		{
			desc: "Volume capability missing",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId: "vol_1",
			},
			expectedErr: status.Error(codes.InvalidArgument, "Volume capability not provided"),
		},
		{
			desc: "Volume capability not supported",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         "vol_1",
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCapWrong},
			},
			expectedErr: status.Error(codes.InvalidArgument, "Volume capability not supported"),
		},
		{
			desc: "diskName error",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         "vol_1",
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
			},
			expectedErr: status.Error(codes.NotFound, "Volume not found, failed with error: could not get disk name from vol_1, correct format: (?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/disks/(.+)"),
		},
		{
			desc: "NodeID missing",
			req: &csi.ControllerPublishVolumeRequest{
				VolumeId:         testVolumeID,
				VolumeCapability: &csi.VolumeCapability{AccessMode: &volumeCap},
			},
			expectedErr: status.Error(codes.InvalidArgument, "Node ID not provided"),
		},
	}

	for _, test := range tests {
		id := test.req.VolumeId
		disk := compute.Disk{
			ID: &id,
		}
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		mockDiskClient := mockdiskclient.NewMockInterface(ctrl)
		d.setCloud(&azure.Cloud{})
		d.getCloud().DisksClient = mockDiskClient
		mockDiskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
		_, err := d.ControllerPublishVolume(context.Background(), test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
}

func TestControllerUnpublishVolume(t *testing.T) {
	d, err := NewFakeDriver(t)
	d.setCloud(&azure.Cloud{})
	if err != nil {
		t.Fatalf("Error getting driver: %v", err)
	}
	tests := []struct {
		desc        string
		req         *csi.ControllerUnpublishVolumeRequest
		expectedErr error
	}{
		{
			desc:        "Volume ID missing",
			req:         &csi.ControllerUnpublishVolumeRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Volume ID not provided"),
		},
		{
			desc: "Node ID missing",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: "vol_1",
			},
			expectedErr: status.Error(codes.InvalidArgument, "Node ID not provided"),
		},
		{
			desc: "DiskName error",
			req: &csi.ControllerUnpublishVolumeRequest{
				VolumeId: "vol_1",
				NodeId:   "unit-test-node",
			},
			expectedErr: fmt.Errorf("could not get disk name from vol_1, correct format: (?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/disks/(.+)"),
		},
	}
	for _, test := range tests {
		_, err := d.ControllerUnpublishVolume(context.Background(), test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
}

func TestControllerGetCapabilities(t *testing.T) {
	d, _ := NewFakeDriver(t)
	capType := &csi.ControllerServiceCapability_Rpc{
		Rpc: &csi.ControllerServiceCapability_RPC{
			Type: csi.ControllerServiceCapability_RPC_UNKNOWN,
		},
	}
	capList := []*csi.ControllerServiceCapability{{
		Type: capType,
	}}
	d.setControllerCapabilities(capList)
	// Test valid request
	req := csi.ControllerGetCapabilitiesRequest{}
	resp, err := d.ControllerGetCapabilities(context.Background(), &req)
	assert.NotNil(t, resp)
	assert.Equal(t, resp.Capabilities[0].GetType(), capType)
	assert.NoError(t, err)
}

func TestIsValidVolumeCapabilities(t *testing.T) {
	var caps []*csi.VolumeCapability
	stdVolCap := csi.VolumeCapability{
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
		},
	}
	caps = append(caps, &stdVolCap)
	if !isValidVolumeCapabilities(caps) {
		t.Errorf("Unexpected error")
	}
	stdVolCap1 := csi.VolumeCapability{
		AccessMode: &csi.VolumeCapability_AccessMode{
			Mode: 10,
		},
	}
	caps = append(caps, &stdVolCap1)
	if isValidVolumeCapabilities(caps) {
		t.Errorf("Unexpected error")
	}
}

func TestControllerExpandVolume(t *testing.T) {
	stdVolSize := int64(5 * 1024 * 1024 * 1024)
	stdCapRange := &csi.CapacityRange{RequiredBytes: stdVolSize}

	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Volume ID missing",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{}

				ctx := context.Background()
				d, _ := NewFakeDriver(t)

				expectedErr := status.Error(codes.InvalidArgument, "Volume ID missing in request")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Volume capabilities missing",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId: "vol_1",
				}

				ctx := context.Background()
				d, _ := NewFakeDriver(t)
				var csc []*csi.ControllerServiceCapability
				d.setControllerCapabilities(csc)
				expectedErr := status.Error(codes.InvalidArgument, "invalid expand volume request: volume_id:\"vol_1\" ")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Volume Capacity range missing",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId: "vol_1",
				}

				ctx := context.Background()
				d, _ := NewFakeDriver(t)

				expectedErr := status.Error(codes.InvalidArgument, "volume capacity range missing in request")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "disk type is not managedDisk",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId:      "httptest",
					CapacityRange: stdCapRange,
				}
				ctx := context.Background()
				d, _ := NewFakeDriver(t)

				expectedErr := status.Error(codes.InvalidArgument, "disk URI(httptest) is not valid: Inavlid DiskURI: httptest, correct format: [/subscriptions/{sub-id}/resourcegroups/{group-name}/providers/microsoft.compute/disks/{disk-id}]")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
		{
			name: "Disk URI not valid",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId:      "vol_1",
					CapacityRange: stdCapRange,
				}

				ctx := context.Background()
				d, _ := NewFakeDriver(t)

				expectedErr := status.Errorf(codes.InvalidArgument, "disk URI(vol_1) is not valid: Inavlid DiskURI: vol_1, correct format: [/subscriptions/{sub-id}/resourcegroups/{group-name}/providers/microsoft.compute/disks/{disk-id}]")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "DiskSize missing",
			testFunc: func(t *testing.T) {
				req := &csi.ControllerExpandVolumeRequest{
					VolumeId:      testVolumeID,
					CapacityRange: stdCapRange,
				}
				id := req.VolumeId
				diskProperties := compute.DiskProperties{}
				disk := compute.Disk{
					ID:             &id,
					DiskProperties: &diskProperties,
				}
				ctx := context.Background()
				d, _ := NewFakeDriver(t)
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockDiskClient := mockdiskclient.NewMockInterface(ctrl)
				d.setCloud(&azure.Cloud{})
				d.getCloud().DisksClient = mockDiskClient
				mockDiskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				expectedErr := status.Errorf(codes.Internal, "could not get size of the disk(unit-test-volume)")
				_, err := d.ControllerExpandVolume(ctx, req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestCreateSnapshot(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Source volume ID missing",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				req := &csi.CreateSnapshotRequest{}
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "CreateSnapshot Source Volume ID must be provided")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Snapshot name missing",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: "vol_1"}
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Error(codes.InvalidArgument, "snapshot name must be provided")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Invalid parameter option",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				parameter := make(map[string]string)
				parameter["unit-test"] = "test"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: "vol_1",
					Name:           "snapname",
					Parameters:     parameter,
				}

				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := fmt.Errorf("AzureDisk - invalid option unit-test in VolumeSnapshotClass")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Invalid volume ID",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: "vol_1",
					Name:           "snapname",
				}
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.InvalidArgument, "could not get resource group from diskURI(vol_1) with error(invalid disk URI: vol_1)")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Invalid tag ",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				parameter := make(map[string]string)
				parameter["tags"] = "unit-test"
				parameter[incrementalField] = "false"
				parameter[resourceGroupField] = "test"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "snapname",
					Parameters:     parameter,
				}

				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := fmt.Errorf("Tags 'unit-test' are invalid, the format should like: 'key1=value1,key2=value2'")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "create snapshot error ",
			testFunc: func(t *testing.T) {
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "snapname",
				}
				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.getCloud().SnapshotsClient = mockSnapshotClient
				rerr := &retry.Error{
					RawError: fmt.Errorf("test"),
				}
				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(rerr).AnyTimes()

				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.Internal, "create snapshot error: Retriable: false, RetryAfter: 0s, HTTPStatusCode: 0, RawError: test")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "create snapshot already exist ",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "snapname",
					Parameters:     parameter,
				}
				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.getCloud().SnapshotsClient = mockSnapshotClient
				rerr := &retry.Error{
					RawError: fmt.Errorf("existing disk"),
				}
				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(rerr).AnyTimes()
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.AlreadyExists, "request snapshot(snapname) under rg(rg) already exists, but the SourceVolumeId is different, error details: Retriable: false, RetryAfter: 0s, HTTPStatusCode: 0, RawError: existing disk")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Get Snapshot ID error ",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "unit-test",
					Parameters:     parameter,
				}
				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.getCloud().SnapshotsClient = mockSnapshotClient
				rerr := &retry.Error{
					RawError: fmt.Errorf("get snapshot error"),
				}
				snapshot := compute.Snapshot{}
				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, rerr).AnyTimes()
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.Internal, "get snapshot unit-test from rg(rg) error: Retriable: false, RetryAfter: 0s, HTTPStatusCode: 0, RawError: get snapshot error")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "valid request ",
			testFunc: func(t *testing.T) {
				parameter := make(map[string]string)
				parameter["tags"] = "unit=test"
				req := &csi.CreateSnapshotRequest{
					SourceVolumeId: testVolumeID,
					Name:           "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name",
					Parameters:     parameter,
				}
				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.getCloud().SnapshotsClient = mockSnapshotClient
				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := compute.Snapshot{
					SnapshotProperties: &compute.SnapshotProperties{
						TimeCreated:       &date.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}

				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).AnyTimes()
				actualresponse, err := d.CreateSnapshot(context.Background(), req)
				tp, _ := ptypes.TimestampProto(snapshot.SnapshotProperties.TimeCreated.ToTime())
				ready := true
				expectedresponse := &csi.CreateSnapshotResponse{
					Snapshot: &csi.Snapshot{
						SizeBytes:      volumehelper.GiBToBytes(int64(*snapshot.SnapshotProperties.DiskSizeGB)),
						SnapshotId:     *snapshot.ID,
						SourceVolumeId: req.SourceVolumeId,
						CreationTime:   tp,
						ReadyToUse:     ready,
					},
				}
				if !reflect.DeepEqual(expectedresponse, actualresponse) || err != nil {
					t.Errorf("actualresponse: (%+v), expectedresponse: (%+v)\n", actualresponse, expectedresponse)
					t.Errorf("err:%v", err)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestDeleteSnapshot(t *testing.T) {

	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Snapshot ID missing",
			testFunc: func(t *testing.T) {
				req := &csi.DeleteSnapshotRequest{}
				expectedErr := status.Error(codes.InvalidArgument, "Snapshot ID must be provided")
				d, _ := NewFakeDriver(t)
				_, err := d.DeleteSnapshot(context.Background(), req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Snapshot ID invalid",
			testFunc: func(t *testing.T) {
				req := &csi.DeleteSnapshotRequest{
					SnapshotId: "/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name",
				}
				d, _ := NewFakeDriver(t)
				expectedErr := fmt.Errorf("could not get snapshot name from /subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name, correct format: (?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/snapshots/(.+)")
				_, err := d.DeleteSnapshot(context.Background(), req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "delete Snapshot error",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.getCloud().SnapshotsClient = mockSnapshotClient
				req := &csi.DeleteSnapshotRequest{
					SnapshotId: "testurl/subscriptions/12/resourceGroups/23/providers/Microsoft.Compute/snapshots/snapshot-name",
				}
				rerr := &retry.Error{
					RawError: fmt.Errorf("get snapshot error"),
				}
				mockSnapshotClient.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any()).Return(rerr).AnyTimes()
				expectedErr := status.Errorf(codes.Internal, "delete snapshot error: Retriable: false, RetryAfter: 0s, HTTPStatusCode: 0, RawError: get snapshot error")
				_, err := d.DeleteSnapshot(context.Background(), req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Valid delete Snapshot ",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.getCloud().SnapshotsClient = mockSnapshotClient
				req := &csi.DeleteSnapshotRequest{
					SnapshotId: "testurl/subscriptions/12/resourceGroups/23/providers/Microsoft.Compute/snapshots/snapshot-name",
				}
				mockSnapshotClient.EXPECT().Delete(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				_, err := d.DeleteSnapshot(context.Background(), req)
				if !reflect.DeepEqual(err, nil) {
					t.Errorf("actualErr: (%v), expectedErr: nil)", err)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestGenerateCSISnapshot(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "snap shot property not exist",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				snapshot := compute.Snapshot{}
				sourceVolumeID := "unit-test"
				_, err := generateCSISnapshot(sourceVolumeID, &snapshot)
				expectedErr := fmt.Errorf("snapshot property is nil")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "diskSizeGB of snapshot property is nil",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				provisioningState := "true"
				snapshot := compute.Snapshot{
					SnapshotProperties: &compute.SnapshotProperties{
						TimeCreated:       &date.Time{},
						ProvisioningState: &provisioningState,
					},
				}
				sourceVolumeID := "unit-test"
				_, err := generateCSISnapshot(sourceVolumeID, &snapshot)
				expectedErr := fmt.Errorf("diskSizeGB of snapshot property is nil")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "valid request",
			testFunc: func(t *testing.T) {
				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := compute.Snapshot{
					SnapshotProperties: &compute.SnapshotProperties{
						TimeCreated:       &date.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}
				sourceVolumeID := "unit-test"
				response, err := generateCSISnapshot(sourceVolumeID, &snapshot)
				tp, _ := ptypes.TimestampProto(snapshot.SnapshotProperties.TimeCreated.ToTime())
				ready := true
				expectedresponse := &csi.Snapshot{
					SizeBytes:      volumehelper.GiBToBytes(int64(*snapshot.SnapshotProperties.DiskSizeGB)),
					SnapshotId:     *snapshot.ID,
					SourceVolumeId: sourceVolumeID,
					CreationTime:   tp,
					ReadyToUse:     ready,
				}
				if !reflect.DeepEqual(expectedresponse, response) || err != nil {
					t.Errorf("actualresponse: (%+v), expectedresponse: (%+v)\n", response, expectedresponse)
					t.Errorf("err:%v", err)
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}

}

func TestGetSnapshotByID(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "snapshotID not valid",
			testFunc: func(t *testing.T) {
				sourceVolumeID := "unit-test"
				ctx := context.Background()
				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				snapshotID := "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name"
				expectedErr := fmt.Errorf("could not get snapshot name from testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name, correct format: (?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/snapshots/(.+)")
				_, err := d.getSnapshotByID(ctx, d.getCloud().ResourceGroup, snapshotID, sourceVolumeID)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "snapshot get error",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.getCloud().SnapshotsClient = mockSnapshotClient
				rerr := &retry.Error{
					RawError: fmt.Errorf("test"),
				}
				snapshotID := "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name"
				snapshot := compute.Snapshot{
					SnapshotProperties: &compute.SnapshotProperties{},
					ID:                 &snapshotID,
				}
				snapshotVolumeID := "unit-test"
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, rerr).AnyTimes()
				expectedErr := fmt.Errorf("could not get snapshot name from testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name, correct format: (?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/snapshots/(.+)")
				_, err := d.getSnapshotByID(context.Background(), d.getCloud().ResourceGroup, snapshotID, snapshotVolumeID)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestListSnapshots(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "snapshotID not valid",
			testFunc: func(t *testing.T) {
				req := csi.ListSnapshotsRequest{
					SnapshotId: "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-nametestVolumeName",
				}
				d, _ := NewFakeDriver(t)
				expectedErr := fmt.Errorf("could not get snapshot name from testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-nametestVolumeName, correct format: (?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/snapshots/(.+)")
				_, err := d.ListSnapshots(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "valid List",
			testFunc: func(t *testing.T) {
				req := csi.ListSnapshotsRequest{
					SnapshotId: "testurl/subscriptions/12/resourceGroups/23/providers/Microsoft.Compute/snapshots/snapshot-name",
				}
				d, _ := NewFakeDriver(t)
				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				snapshot := compute.Snapshot{
					SnapshotProperties: &compute.SnapshotProperties{
						TimeCreated:       &date.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
					},
					ID: &snapshotID,
				}
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.getCloud().SnapshotsClient = mockSnapshotClient
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, nil).AnyTimes()
				expectedErr := error(nil)
				_, err := d.ListSnapshots(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "List resource error",
			testFunc: func(t *testing.T) {
				req := csi.ListSnapshotsRequest{}
				d, _ := NewFakeDriver(t)
				snapshot := compute.Snapshot{}
				snapshots := []compute.Snapshot{}
				snapshots = append(snapshots, snapshot)
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				rerr := &retry.Error{
					RawError: fmt.Errorf("test"),
				}
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.getCloud().SnapshotsClient = mockSnapshotClient
				mockSnapshotClient.EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return(snapshots, rerr).AnyTimes()
				expectedErr := status.Error(codes.Internal, "Unknown list snapshot error: Retriable: false, RetryAfter: 0s, HTTPStatusCode: 0, RawError: test")
				_, err := d.ListSnapshots(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "snapshot property nil",
			testFunc: func(t *testing.T) {
				req := csi.ListSnapshotsRequest{}
				d, _ := NewFakeDriver(t)
				snapshot := compute.Snapshot{}
				snapshots := []compute.Snapshot{}
				snapshots = append(snapshots, snapshot)
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.getCloud().SnapshotsClient = mockSnapshotClient
				mockSnapshotClient.EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return(snapshots, nil).AnyTimes()
				expectedErr := fmt.Errorf("failed to generate snapshot entry: snapshot property is nil")
				_, err := d.ListSnapshots(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "List snapshots when source volumeId is given",
			testFunc: func(t *testing.T) {
				req := csi.ListSnapshotsRequest{SourceVolumeId: "test"}
				d, _ := NewFakeDriver(t)
				volumeID := "test"
				DiskSize := int32(10)
				snapshotID := "test"
				provisioningState := "succeeded"
				snapshot1 := compute.Snapshot{
					SnapshotProperties: &compute.SnapshotProperties{
						TimeCreated:       &date.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
						CreationData:      &compute.CreationData{SourceResourceID: &volumeID},
					},
					ID: &snapshotID}
				snapshot2 := compute.Snapshot{}
				snapshots := []compute.Snapshot{}
				snapshots = append(snapshots, snapshot1, snapshot2)
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.getCloud().SnapshotsClient = mockSnapshotClient
				mockSnapshotClient.EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return(snapshots, nil).AnyTimes()
				snapshotsResponse, _ := d.ListSnapshots(context.TODO(), &req)
				if len(snapshotsResponse.Entries) != 1 {
					t.Errorf("actualNumberOfEntries: (%v), expectedNumberOfEntries: (%v)", len(snapshotsResponse.Entries), 1)
				}
				if snapshotsResponse.Entries[0].Snapshot.SourceVolumeId != volumeID {
					t.Errorf("actualVolumeId: (%v), expectedVolumeId: (%v)", snapshotsResponse.Entries[0].Snapshot.SourceVolumeId, volumeID)
				}
				if snapshotsResponse.NextToken != "2" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", snapshotsResponse.NextToken, "2")
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}

}
func TestGetCapacity(t *testing.T) {
	d, _ := NewFakeDriver(t)
	req := csi.GetCapacityRequest{}
	resp, err := d.GetCapacity(context.Background(), &req)
	assert.Nil(t, resp)
	if !reflect.DeepEqual(err, status.Error(codes.Unimplemented, "")) {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestListVolumes(t *testing.T) {
	volume1 := v1.PersistentVolume{
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:       "disk.csi.azure.com",
					VolumeHandle: "/subscriptions/test-subscription/resourceGroups/test_resourcegroup-1/providers/Microsoft.Compute/disks/test-pv-1",
				},
			},
		},
	}
	volume2 := v1.PersistentVolume{
		Spec: v1.PersistentVolumeSpec{
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:       "disk.csi.azure.com",
					VolumeHandle: "/subscriptions/test-subscription/resourceGroups/test_resourcegroup-2/providers/Microsoft.Compute/disks/test-pv-2",
				},
			},
		},
	}

	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "When no KubeClient exists, Valid list without max_entries or starting_token",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{}
				d, _ := NewFakeDriver(t)
				fakeVolumeID := "test"
				disk := compute.Disk{ID: &fakeVolumeID}
				disks := []compute.Disk{}
				disks = append(disks, disk)
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return(disks, nil).AnyTimes()
				expectedErr := error(nil)
				listVolumesResponse, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
				if listVolumesResponse.NextToken != "" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", listVolumesResponse.NextToken, "")
				}
			},
		},
		{
			name: "When no KubeClient exists, Valid list with max_entries",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{
					MaxEntries: 1,
				}
				d, _ := NewFakeDriver(t)
				fakeVolumeID := "test"
				disk1, disk2 := compute.Disk{ID: &fakeVolumeID}, compute.Disk{ID: &fakeVolumeID}
				disks := []compute.Disk{}
				disks = append(disks, disk1, disk2)
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return(disks, nil).AnyTimes()
				expectedErr := error(nil)
				listVolumesResponse, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
				if len(listVolumesResponse.Entries) != int(req.MaxEntries) {
					t.Errorf("Actual number of entries: (%v), Expected number of entries: (%v)", len(listVolumesResponse.Entries), req.MaxEntries)
				}
				if listVolumesResponse.NextToken != "1" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", listVolumesResponse.NextToken, "1")
				}
			},
		},
		{
			name: "When no KubeClient exists, Valid list with max_entries and starting_token",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{
					StartingToken: "1",
					MaxEntries:    1,
				}
				d, _ := NewFakeDriver(t)
				fakeVolumeID1, fakeVolumeID12 := "test1", "test2"
				disk1, disk2 := compute.Disk{ID: &fakeVolumeID1}, compute.Disk{ID: &fakeVolumeID12}
				disks := []compute.Disk{}
				disks = append(disks, disk1, disk2)
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return(disks, nil).AnyTimes()
				expectedErr := error(nil)
				listVolumesResponse, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
				if len(listVolumesResponse.Entries) != int(req.MaxEntries) {
					t.Errorf("Actual number of entries: (%v), Expected number of entries: (%v)", len(listVolumesResponse.Entries), req.MaxEntries)
				}
				if listVolumesResponse.NextToken != "" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", listVolumesResponse.NextToken, "")
				}
				if listVolumesResponse.Entries[0].Volume.VolumeId != fakeVolumeID12 {
					t.Errorf("actualVolumeId: (%v), expectedVolumeId: (%v)", listVolumesResponse.Entries[0].Volume.VolumeId, fakeVolumeID12)
				}
			},
		},
		{
			name: "When no KubeClient exists, ListVolumes request with starting token but no entries in response",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{
					StartingToken: "1",
				}
				d, _ := NewFakeDriver(t)
				disks := []compute.Disk{}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return(disks, nil).AnyTimes()
				expectedErr := status.Error(codes.FailedPrecondition, "ListVolumes starting token(1) on rg(rg) is greater than total number of volumes")
				_, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "When no KubeClient exists, ListVolumes list resource error",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{
					StartingToken: "1",
				}
				d, _ := NewFakeDriver(t)
				disks := []compute.Disk{}
				rerr := &retry.Error{
					RawError: fmt.Errorf("test"),
				}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return(disks, rerr).AnyTimes()
				expectedErr := status.Error(codes.Internal, "ListVolumes on rg(rg) failed with error: Retriable: false, RetryAfter: 0s, HTTPStatusCode: 0, RawError: test")
				_, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "When KubeClient exists, Empty list without start token should not return error",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{}
				d := getFakeDriverWithKubeClient(t)
				pvList := v1.PersistentVolumeList{
					Items: []v1.PersistentVolume{},
				}
				d.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().List(gomock.Any(), gomock.Any()).Return(&pvList, nil)
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return([]compute.Disk{}, nil)
				expectedErr := error(nil)
				_, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "When KubeClient exists, Valid list without max_entries or starting_token",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{}
				fakeVolumeID := "/subscriptions/test-subscription/resourceGroups/test_resourcegroup-1/providers/Microsoft.Compute/disks/test-pv-1"
				d := getFakeDriverWithKubeClient(t)
				pvList := v1.PersistentVolumeList{
					Items: []v1.PersistentVolume{volume1},
				}
				d.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().List(gomock.Any(), gomock.Any()).Return(&pvList, nil)
				disk1 := compute.Disk{ID: &fakeVolumeID}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return([]compute.Disk{disk1}, nil)
				expectedErr := error(nil)
				listVolumesResponse, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
				if listVolumesResponse.NextToken != "" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", listVolumesResponse.NextToken, "")
				}
			},
		},
		{
			name: "When KubeClient exists, Valid list with max_entries",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{
					MaxEntries: 1,
				}
				d := getFakeDriverWithKubeClient(t)
				fakeVolumeID := "/subscriptions/test-subscription/resourceGroups/test_resourcegroup-1/providers/Microsoft.Compute/disks/test-pv-1"
				disk1, disk2 := compute.Disk{ID: &fakeVolumeID}, compute.Disk{ID: &fakeVolumeID}
				pvList := v1.PersistentVolumeList{
					Items: []v1.PersistentVolume{volume1, volume2},
				}
				d.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().List(gomock.Any(), gomock.Any()).Return(&pvList, nil)
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return([]compute.Disk{disk1}, nil)
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return([]compute.Disk{disk2}, nil)
				expectedErr := error(nil)
				listVolumesResponse, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
				if len(listVolumesResponse.Entries) != int(req.MaxEntries) {
					t.Errorf("Actual number of entries: (%v), Expected number of entries: (%v)", len(listVolumesResponse.Entries), req.MaxEntries)
				}
				if listVolumesResponse.NextToken != "1" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", listVolumesResponse.NextToken, "1")
				}
			},
		},
		{
			name: "When KubeClient exists, Valid list with max_entries and starting_token",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{
					StartingToken: "1",
					MaxEntries:    1,
				}
				d := getFakeDriverWithKubeClient(t)
				pvList := v1.PersistentVolumeList{
					Items: []v1.PersistentVolume{volume1, volume2},
				}
				fakeVolumeID11, fakeVolumeID12 := "/subscriptions/test-subscription/resourceGroups/test_resourcegroup-1/providers/Microsoft.Compute/disks/test-pv-1", "/subscriptions/test-subscription/resourceGroups/test_resourcegroup-2/providers/Microsoft.Compute/disks/test-pv-2"
				disk1, disk2 := compute.Disk{ID: &fakeVolumeID11}, compute.Disk{ID: &fakeVolumeID12}
				d.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().List(gomock.Any(), gomock.Any()).Return(&pvList, nil)
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return([]compute.Disk{disk1}, nil)
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().ListByResourceGroup(gomock.Any(), gomock.Any()).Return([]compute.Disk{disk2}, nil)
				expectedErr := error(nil)
				listVolumesResponse, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
				if len(listVolumesResponse.Entries) != int(req.MaxEntries) {
					t.Errorf("Actual number of entries: (%v), Expected number of entries: (%v)", len(listVolumesResponse.Entries), req.MaxEntries)
				}
				if listVolumesResponse.NextToken != "" {
					t.Errorf("actualNextToken: (%v), expectedNextToken: (%v)", listVolumesResponse.NextToken, "")
				}
				if listVolumesResponse.Entries[0].Volume.VolumeId != fakeVolumeID12 {
					t.Errorf("actualVolumeId: (%v), expectedVolumeId: (%v)", listVolumesResponse.Entries[0].Volume.VolumeId, fakeVolumeID12)
				}
			},
		},
		{
			name: "When KubeClient exists, ListVolumes request with starting token but no entries in response",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{
					StartingToken: "1",
				}
				d := getFakeDriverWithKubeClient(t)
				pvList := v1.PersistentVolumeList{
					Items: []v1.PersistentVolume{},
				}
				d.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().List(gomock.Any(), gomock.Any()).Return(&pvList, nil)
				expectedErr := status.Error(codes.FailedPrecondition, "ListVolumes starting token(1) is greater than total number of disks")
				_, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "When KubeClient exists, ListVolumes list pv error",
			testFunc: func(t *testing.T) {
				req := csi.ListVolumesRequest{
					StartingToken: "1",
				}
				d := getFakeDriverWithKubeClient(t)
				rerr := fmt.Errorf("test")
				d.getCloud().KubeClient.CoreV1().PersistentVolumes().(*mockpersistentvolume.MockInterface).EXPECT().List(gomock.Any(), gomock.Any()).Return(nil, rerr)
				expectedErr := status.Error(codes.Internal, "ListVolumes failed while fetching PersistentVolumes List with error: test")
				_, err := d.ListVolumes(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestValidateVolumeCapabilities(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Volume ID missing ",
			testFunc: func(t *testing.T) {
				req := csi.ValidateVolumeCapabilitiesRequest{}
				d, _ := NewFakeDriver(t)
				expectedErr := status.Errorf(codes.InvalidArgument, "Volume ID missing in request")
				_, err := d.ValidateVolumeCapabilities(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "Volume capabilities missing ",
			testFunc: func(t *testing.T) {
				req := csi.ValidateVolumeCapabilitiesRequest{
					VolumeId: "unit-test",
				}
				d, _ := NewFakeDriver(t)
				expectedErr := status.Errorf(codes.InvalidArgument, "Volume capabilities missing in request")
				_, err := d.ValidateVolumeCapabilities(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "check disk err ",
			testFunc: func(t *testing.T) {
				req := csi.ValidateVolumeCapabilitiesRequest{
					VolumeId:           "-",
					VolumeCapabilities: stdVolumeCapabilities,
				}
				d, _ := NewFakeDriver(t)
				expectedErr := status.Errorf(codes.NotFound, "Volume not found, failed with error: could not get disk name from -, correct format: (?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/disks/(.+)")
				_, err := d.ValidateVolumeCapabilities(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "invalid req ",
			testFunc: func(t *testing.T) {
				req := csi.ValidateVolumeCapabilitiesRequest{
					VolumeId:           testVolumeID,
					VolumeCapabilities: stdVolumeCapabilities,
				}
				d, _ := NewFakeDriver(t)
				disk := compute.Disk{
					DiskProperties: &compute.DiskProperties{},
				}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				expectedErr := error(nil)
				_, err := d.ValidateVolumeCapabilities(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "valid req ",
			testFunc: func(t *testing.T) {
				stdVolumeCapabilitytest := &csi.VolumeCapability{
					AccessMode: &csi.VolumeCapability_AccessMode{
						Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
					},
				}
				stdVolumeCapabilitiestest := []*csi.VolumeCapability{
					stdVolumeCapabilitytest,
				}
				req := csi.ValidateVolumeCapabilitiesRequest{
					VolumeId:           testVolumeID,
					VolumeCapabilities: stdVolumeCapabilitiestest,
				}
				d, _ := NewFakeDriver(t)
				disk := compute.Disk{
					DiskProperties: &compute.DiskProperties{},
				}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				expectedErr := error(nil)
				_, err := d.ValidateVolumeCapabilities(context.TODO(), &req)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestPickAvailabilityZone(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "requirement missing ",
			testFunc: func(t *testing.T) {
				expectedresponse := ""
				region := "test"
				actualresponse := pickAvailabilityZone(nil, region)
				if !reflect.DeepEqual(expectedresponse, actualresponse) {
					t.Errorf("actualresponse: (%v), expectedresponse: (%v)", actualresponse, expectedresponse)
				}
			},
		},
		{
			name: "valid get preferred",
			testFunc: func(t *testing.T) {
				expectedresponse := "test-01"
				region := "test"
				mp := make(map[string]string)
				mp["N/A"] = "test-01"
				topology := &csi.Topology{
					Segments: mp,
				}
				topologies := []*csi.Topology{}
				topologies = append(topologies, topology)
				req := &csi.TopologyRequirement{
					Preferred: topologies,
				}
				actualresponse := pickAvailabilityZone(req, region)
				if !reflect.DeepEqual(expectedresponse, actualresponse) {
					t.Errorf("actualresponse: (%v), expectedresponse: (%v)", actualresponse, expectedresponse)
				}
			},
		},
		{
			name: "valid get requisite",
			testFunc: func(t *testing.T) {
				expectedresponse := "test-01"
				region := "test"
				mp := make(map[string]string)
				mp["N/A"] = "test-01"
				topology := &csi.Topology{
					Segments: mp,
				}
				topologies := []*csi.Topology{}
				topologies = append(topologies, topology)
				req := &csi.TopologyRequirement{
					Requisite: topologies,
				}
				actualresponse := pickAvailabilityZone(req, region)
				if !reflect.DeepEqual(expectedresponse, actualresponse) {
					t.Errorf("actualresponse: (%v), expectedresponse: (%v)", actualresponse, expectedresponse)
				}
			},
		},
		{
			name: "valid get preferred - WellKnownTopologyKey",
			testFunc: func(t *testing.T) {
				expectedresponse := "test-02"
				region := "test"
				mp := make(map[string]string)
				mp["N/A"] = "test-01"
				mp[WellKnownTopologyKey] = "test-02"
				topology := &csi.Topology{
					Segments: mp,
				}
				topologies := []*csi.Topology{}
				topologies = append(topologies, topology)
				req := &csi.TopologyRequirement{
					Preferred: topologies,
				}
				actualresponse := pickAvailabilityZone(req, region)
				if !reflect.DeepEqual(expectedresponse, actualresponse) {
					t.Errorf("actualresponse: (%v), expectedresponse: (%v)", actualresponse, expectedresponse)
				}
			},
		},
		{
			name: "valid get requisite - WellKnownTopologyKey",
			testFunc: func(t *testing.T) {
				expectedresponse := "test-02"
				region := "test"
				mp := make(map[string]string)
				mp["N/A"] = "test-01"
				mp[WellKnownTopologyKey] = "test-02"
				topology := &csi.Topology{
					Segments: mp,
				}
				topologies := []*csi.Topology{}
				topologies = append(topologies, topology)
				req := &csi.TopologyRequirement{
					Requisite: topologies,
				}
				actualresponse := pickAvailabilityZone(req, region)
				if !reflect.DeepEqual(expectedresponse, actualresponse) {
					t.Errorf("actualresponse: (%v), expectedresponse: (%v)", actualresponse, expectedresponse)
				}
			},
		},
		{
			name: "empty request ",
			testFunc: func(t *testing.T) {
				req := &csi.TopologyRequirement{}
				expectedresponse := ""
				region := "test"
				actualresponse := pickAvailabilityZone(req, region)
				if !reflect.DeepEqual(expectedresponse, actualresponse) {
					t.Errorf("actualresponse: (%v), expectedresponse: (%v)", actualresponse, expectedresponse)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestGetSourceDiskSize(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "max depth reached",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				_, err := d.GetSourceDiskSize(context.Background(), "test-rg", "test-disk", 2, 1)
				expectedErr := status.Errorf(codes.Internal, "current depth (2) surpassed the max depth (1) while searching for the source disk size")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "diskproperty not found",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				disk := compute.Disk{}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				_, err := d.GetSourceDiskSize(context.Background(), "test-rg", "test-disk", 0, 1)
				expectedErr := status.Error(codes.Internal, "DiskProperty not found for disk (test-disk) in resource group (test-rg)")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "nil DiskSizeGB",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				diskProperties := compute.DiskProperties{}
				disk := compute.Disk{
					DiskProperties: &diskProperties,
				}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				_, err := d.GetSourceDiskSize(context.Background(), "test-rg", "test-disk", 0, 1)
				expectedErr := status.Error(codes.Internal, "DiskSizeGB for disk (test-disk) in resourcegroup (test-rg) is nil")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "successful search: depth 1",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				diskSizeGB := int32(8)
				diskProperties := compute.DiskProperties{
					DiskSizeGB: &diskSizeGB,
				}
				disk := compute.Disk{
					DiskProperties: &diskProperties,
				}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
				size, _ := d.GetSourceDiskSize(context.Background(), "test-rg", "test-disk", 0, 1)
				expectedOutput := diskSizeGB
				if *size != expectedOutput {
					t.Errorf("actualOutput: (%v), expectedOutput: (%v)", *size, expectedOutput)
				}
			},
		},
		{
			name: "successful serach: depth 2",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				diskSizeGB1 := int32(16)
				diskSizeGB2 := int32(8)
				sourceURI := "/subscriptions/xxxxxxxx/resourcegroups/test-rg/providers/microsoft.compute/disks/test-disk-1"
				creationData := compute.CreationData{
					CreateOption: "Copy",
					SourceURI:    &sourceURI,
				}
				diskProperties1 := compute.DiskProperties{
					CreationData: &creationData,
					DiskSizeGB:   &diskSizeGB1,
				}
				diskProperties2 := compute.DiskProperties{
					DiskSizeGB: &diskSizeGB2,
				}
				disk1 := compute.Disk{
					DiskProperties: &diskProperties1,
				}
				disk2 := compute.Disk{
					DiskProperties: &diskProperties2,
				}
				d.getCloud().DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk1, nil).Return(disk2, nil).AnyTimes()
				size, _ := d.GetSourceDiskSize(context.Background(), "test-rg", "test-disk-1", 0, 2)
				expectedOutput := diskSizeGB2
				if *size != expectedOutput {
					t.Errorf("actualOutput: (%v), expectedOutput: (%v)", *size, expectedOutput)
				}
			},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func getFakeDriverWithKubeClient(t *testing.T) FakeDriver {
	d, _ := NewFakeDriver(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	corev1 := mockcorev1.NewMockInterface(ctrl)
	persistentvolume := mockpersistentvolume.NewMockInterface(ctrl)
	d.getCloud().KubeClient = mockkubeclient.NewMockInterface(ctrl)
	d.getCloud().KubeClient.(*mockkubeclient.MockInterface).EXPECT().CoreV1().Return(corev1).AnyTimes()
	d.getCloud().KubeClient.CoreV1().(*mockcorev1.MockInterface).EXPECT().PersistentVolumes().Return(persistentvolume).AnyTimes()
	return d
}
