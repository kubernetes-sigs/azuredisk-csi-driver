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
