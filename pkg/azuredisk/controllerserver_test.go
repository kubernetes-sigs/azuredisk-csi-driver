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
	"github.com/stretchr/testify/assert"
	"k8s.io/legacy-cloud-providers/azure"
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
	}
	for _, test := range tests {
		_, err := d.ControllerPublishVolume(context.Background(), test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
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
	}
	for _, test := range tests {
		_, err := d.ControllerUnpublishVolume(context.Background(), test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
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
					t.Errorf("Unexpected error: %v", err)
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func TestCreateSnapshot(t *testing.T) {
	d, _ := NewFakeDriver(t)
	d.cloud = &azure.Cloud{}

	tests := []struct {
		desc        string
		req         *csi.CreateSnapshotRequest
		expectedErr error
	}{
		{
			desc:        "Source volume ID missing",
			req:         &csi.CreateSnapshotRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "CreateSnapshot Source Volume ID must be provided"),
		},
		{
			desc: "Snapshot name missing",
			req: &csi.CreateSnapshotRequest{
				SourceVolumeId: "vol_1",
			},
			expectedErr: status.Error(codes.InvalidArgument, "snapshot name must be provided"),
		},
		{
			desc: "Invalid volume ID",
			req: &csi.CreateSnapshotRequest{
				SourceVolumeId: "vol_1",
				Name:           "snapname",
			},
			expectedErr: status.Errorf(codes.InvalidArgument, "could not get resource group from diskURI(vol_1) with error(invalid disk URI: vol_1)"),
		},
	}

	for _, test := range tests {
		_, err := d.CreateSnapshot(context.Background(), test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
}

func TestDeleteSnapshot(t *testing.T) {
	d, _ := NewFakeDriver(t)
	d.cloud = &azure.Cloud{}

	tests := []struct {
		desc        string
		req         *csi.DeleteSnapshotRequest
		expectedErr error
	}{
		{
			desc:        "Snaoshot ID missing",
			req:         &csi.DeleteSnapshotRequest{},
			expectedErr: status.Error(codes.InvalidArgument, "Snapshot ID must be provided"),
		},
	}

	for _, test := range tests {
		_, err := d.DeleteSnapshot(context.Background(), test.req)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
}
