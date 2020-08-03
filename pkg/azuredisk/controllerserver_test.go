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
	"github.com/Azure/go-autorest/autorest/date"
	"github.com/golang/protobuf/ptypes"
	"github.com/stretchr/testify/assert"
	"k8s.io/legacy-cloud-providers/azure"
	"k8s.io/legacy-cloud-providers/azure/clients/snapshotclient/mocksnapshotclient"
	"k8s.io/legacy-cloud-providers/azure/retry"
	"reflect"
	"testing"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2019-12-01/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/mock/gomock"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"k8s.io/legacy-cloud-providers/azure/clients/diskclient/mockdiskclient"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
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
			map[string]string{"cachingmode": ""},
			compute.CachingTypes(defaultAzureDataDiskCachingMode),
			false,
		},
		{
			map[string]string{"cachingmode": "None"},
			compute.CachingTypes("None"),
			false,
		},
		{
			map[string]string{"cachingmode": "ReadOnly"},
			compute.CachingTypes("ReadOnly"),
			false,
		},
		{
			map[string]string{"cachingmode": "ReadWrite"},
			compute.CachingTypes("ReadWrite"),
			false,
		},
		{
			map[string]string{"cachingmode": "WriteOnly"},
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
	}

	for _, test := range tests {
		resultResponse, resultError := getEntriesAndNextToken(test.request, test.snapshots)
		if resultResponse != test.expectedResponse || resultError.Error() != test.expectedError.Error() {
			t.Errorf("request: %v, snapshotListPage: %v, resultResponse: %v, expectedResponse: %v, resultError: %v, expectedError: %v", test.request, test.snapshots, resultResponse, test.expectedResponse, resultError, test.expectedError)
		}
	}
}

func TestCreateVolume(t *testing.T) {
	d, err := NewFakeDriver(t)
	if err != nil {
		t.Fatalf("Error getting driver: %v", err)
	}
	tests := []struct {
		desc            string
		req             *csi.CreateVolumeRequest
		expectedResp    *csi.CreateVolumeResponse
		expectedErrCode codes.Code
	}{
		{
			desc: "success standard",
			req: &csi.CreateVolumeRequest{
				Name:               testVolumeName,
				VolumeCapabilities: stdVolumeCapabilities,
				CapacityRange:      stdCapacityRange,
			},
			expectedResp: &csi.CreateVolumeResponse{
				Volume: &csi.Volume{
					VolumeId:      testVolumeID,
					CapacityBytes: stdCapacityRange.RequiredBytes,
					VolumeContext: nil,
					ContentSource: &csi.VolumeContentSource{},
					AccessibleTopology: []*csi.Topology{
						{
							Segments: map[string]string{topologyKey: ""},
						},
					},
				},
			},
		},
		{
			desc: "fail with no name",
			req: &csi.CreateVolumeRequest{
				Name: "",
			},
			expectedResp:    nil,
			expectedErrCode: codes.InvalidArgument,
		},
		{
			desc: "fail with no volume capabilities",
			req: &csi.CreateVolumeRequest{
				Name: testVolumeName,
			},
			expectedResp:    nil,
			expectedErrCode: codes.InvalidArgument,
		},
		{
			desc: "fail with the invalid capabilities",
			req: &csi.CreateVolumeRequest{
				Name:               testVolumeName,
				VolumeCapabilities: createVolumeCapabilities(csi.VolumeCapability_AccessMode_UNKNOWN),
			},
			expectedResp:    nil,
			expectedErrCode: codes.InvalidArgument,
		},
		{
			desc: "fail with the invalid requested size",
			req: &csi.CreateVolumeRequest{
				Name:               testVolumeName,
				VolumeCapabilities: stdVolumeCapabilities,
				CapacityRange: &csi.CapacityRange{
					RequiredBytes: volumehelper.GiBToBytes(20),
					LimitBytes:    volumehelper.GiBToBytes(15),
				},
			},
			expectedResp:    nil,
			expectedErrCode: codes.InvalidArgument,
		},
	}

	for _, test := range tests {
		ctx, cancel := context.WithCancel(context.TODO())
		defer cancel()
		name := test.req.Name
		id := fmt.Sprintf(managedDiskPath, "subs", "rg", name)
		size := int32(1)
		if test.req.CapacityRange != nil {
			size = int32(volumehelper.BytesToGiB(test.req.CapacityRange.RequiredBytes))
		}
		state := string(compute.ProvisioningStateSucceeded)
		disk := compute.Disk{
			ID:   &id,
			Name: &name,
			DiskProperties: &compute.DiskProperties{
				DiskSizeGB:        &size,
				ProvisioningState: &state,
			},
		}
		d.cloud.DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Eq(ctx), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
		d.cloud.DisksClient.(*mockdiskclient.MockInterface).EXPECT().CreateOrUpdate(gomock.Eq(ctx), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

		result, err := d.CreateVolume(ctx, test.req)
		if err != nil {
			checkTestError(t, test.expectedErrCode, err)
		}
		if !reflect.DeepEqual(result, test.expectedResp) {
			t.Errorf("desc: %v,\ninput request: %v, CreateVolume result: %v, expected: %v", test.desc, test.req, result, test.expectedResp)
		}
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

		d.cloud.DisksClient.(*mockdiskclient.MockInterface).EXPECT().Get(gomock.Eq(ctx), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
		d.cloud.DisksClient.(*mockdiskclient.MockInterface).EXPECT().Delete(gomock.Eq(ctx), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()

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

func TestExtractSnapshotInfo(t *testing.T) {
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
		{
			// case snapshotID does not contain subscriptions
			snapshotID: "testurl/subscription/12/resourcegroups/23/providers/Microsoft.Compute/snapshots/snapshot-name",
			expected1:  "testurl/subscription/12/resourcegroups/23/providers/Microsoft.Compute/snapshots/snapshot-name",
			expected2:  d.cloud.ResourceGroup,
			expected3:  nil,
		},
	}
	for _, test := range tests {
		snapshotName, resourceGroup, err := d.extractSnapshotInfo(test.snapshotID)
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
	d.cloud = &azure.Cloud{}
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
		d.cloud = &azure.Cloud{}
		d.cloud.DisksClient = mockDiskClient
		mockDiskClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(disk, nil).AnyTimes()
		_, err := d.ControllerPublishVolume(context.Background(), test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
}

func TestControllerUnpublishVolume(t *testing.T) {
	d, err := NewFakeDriver(t)
	d.cloud = &azure.Cloud{}
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
	d.Cap = capList
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
				d.Cap = csc
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
				d.cloud = &azure.Cloud{}
				d.cloud.DisksClient = mockDiskClient
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
				d.cloud = &azure.Cloud{}
				parameter := make(map[string]string)
				parameter["tags"] = "unit-test"
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
				d.cloud = &azure.Cloud{}
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.cloud.SnapshotsClient = mockSnapshotClient
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
				d.cloud = &azure.Cloud{}
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.cloud.SnapshotsClient = mockSnapshotClient
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
				d.cloud = &azure.Cloud{}
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.cloud.SnapshotsClient = mockSnapshotClient
				rerr := &retry.Error{
					RawError: fmt.Errorf("get snapshot error"),
				}
				snapshot := compute.Snapshot{}
				mockSnapshotClient.EXPECT().CreateOrUpdate(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
				mockSnapshotClient.EXPECT().Get(gomock.Any(), gomock.Any(), gomock.Any()).Return(snapshot, rerr).AnyTimes()
				_, err := d.CreateSnapshot(context.Background(), req)
				expectedErr := status.Errorf(codes.Internal, "get snapshot unit-test from rg() error: Retriable: false, RetryAfter: 0s, HTTPStatusCode: 0, RawError: get snapshot error")
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
				d.cloud = &azure.Cloud{}
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.cloud.SnapshotsClient = mockSnapshotClient
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
					SnapshotId: "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name",
				}
				d, _ := NewFakeDriver(t)
				expectedErr := fmt.Errorf("could not get snapshot name from testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name, correct format: (?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/snapshots/(.+)")
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
				d.cloud = &azure.Cloud{}
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.cloud.SnapshotsClient = mockSnapshotClient
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
				d.cloud = &azure.Cloud{}
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.cloud.SnapshotsClient = mockSnapshotClient
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
				d.cloud = &azure.Cloud{}
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
				d.cloud = &azure.Cloud{}
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
				d.cloud = &azure.Cloud{}
				snapshotID := "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name"
				expectedErr := fmt.Errorf("could not get snapshot name from testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name, correct format: (?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/snapshots/(.+)")
				_, err := d.getSnapshotByID(ctx, snapshotID, sourceVolumeID)
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "snapshot get error",
			testFunc: func(t *testing.T) {
				d, _ := NewFakeDriver(t)
				d.cloud = &azure.Cloud{}
				ctrl := gomock.NewController(t)
				defer ctrl.Finish()
				mockSnapshotClient := mocksnapshotclient.NewMockInterface(ctrl)
				d.cloud.SnapshotsClient = mockSnapshotClient
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
				_, err := d.getSnapshotByID(context.Background(), snapshotID, snapshotVolumeID)
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
	d, _ := NewFakeDriver(t)
	req := csi.ListVolumesRequest{}
	resp, err := d.ListVolumes(context.Background(), &req)
	assert.Nil(t, resp)
	if !reflect.DeepEqual(err, status.Error(codes.Unimplemented, "")) {
		t.Errorf("Unexpected error: %v", err)
	}
}
