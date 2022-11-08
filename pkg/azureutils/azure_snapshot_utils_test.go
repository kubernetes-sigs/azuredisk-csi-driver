/*
Copyright 2021 The Kubernetes Authors.

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

package azureutils

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-03-01/compute"
	"github.com/Azure/go-autorest/autorest/date"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
)

func TestGenerateCSISnapshot(t *testing.T) {
	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "snap shot property not exist",
			testFunc: func(t *testing.T) {
				snapshot := compute.Snapshot{}
				sourceVolumeID := "unit-test"
				_, err := GenerateCSISnapshot(sourceVolumeID, &snapshot)
				expectedErr := fmt.Errorf("snapshot property is nil")
				if !reflect.DeepEqual(err, expectedErr) {
					t.Errorf("actualErr: (%v), expectedErr: (%v)", err, expectedErr)
				}
			},
		},
		{
			name: "diskSizeGB of snapshot property is nil",
			testFunc: func(t *testing.T) {
				provisioningState := "true"
				snapshot := compute.Snapshot{
					SnapshotProperties: &compute.SnapshotProperties{
						TimeCreated:       &date.Time{},
						ProvisioningState: &provisioningState,
					},
				}
				sourceVolumeID := "unit-test"
				_, err := GenerateCSISnapshot(sourceVolumeID, &snapshot)
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
				response, err := GenerateCSISnapshot(sourceVolumeID, &snapshot)
				tp := timestamppb.New(snapshot.SnapshotProperties.TimeCreated.ToTime())
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
		{
			name: "sourceVolumeID property is missed",
			testFunc: func(t *testing.T) {
				provisioningState := "succeeded"
				DiskSize := int32(10)
				snapshotID := "test"
				sourceResourceID := "unit test"
				snapshot := compute.Snapshot{
					SnapshotProperties: &compute.SnapshotProperties{
						TimeCreated:       &date.Time{},
						ProvisioningState: &provisioningState,
						DiskSizeGB:        &DiskSize,
						CreationData: &compute.CreationData{
							SourceResourceID: &sourceResourceID,
						},
					},
					ID: &snapshotID,
				}
				sourceVolumeID := ""
				response, err := GenerateCSISnapshot(sourceVolumeID, &snapshot)
				tp := timestamppb.New(snapshot.SnapshotProperties.TimeCreated.ToTime())
				ready := true
				expectedresponse := &csi.Snapshot{
					SizeBytes:      volumehelper.GiBToBytes(int64(*snapshot.SnapshotProperties.DiskSizeGB)),
					SnapshotId:     *snapshot.ID,
					SourceVolumeId: sourceResourceID,
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
	csiSnapshot, _ := GenerateCSISnapshot(sourceVolumeID, &snapshot)
	entries = append(entries, &csi.ListSnapshotsResponse_Entry{Snapshot: csiSnapshot})
	tests := []struct {
		request          *csi.ListSnapshotsRequest
		snapshots        []compute.Snapshot
		expectedResponse *csi.ListSnapshotsResponse
		expectedError    error
	}{
		{
			nil,
			[]compute.Snapshot{},
			nil,
			status.Errorf(codes.Aborted, "request is nil"),
		},
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
				MaxEntries:    2,
				StartingToken: "0",
			},
			append([]compute.Snapshot{}, compute.Snapshot{}),
			nil,
			fmt.Errorf("failed to generate snapshot entry: %v", fmt.Errorf("snapshot property is nil")),
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
		{
			&csi.ListSnapshotsRequest{
				MaxEntries:     1,
				SourceVolumeId: sourceVolumeID,
			},
			append(snapshots, snapshot),
			&csi.ListSnapshotsResponse{
				Entries:   entries,
				NextToken: "1",
			},
			error(nil),
		},
	}

	for _, test := range tests {
		resultResponse, resultError := GetEntriesAndNextToken(test.request, test.snapshots)
		if !reflect.DeepEqual(resultResponse, test.expectedResponse) || (!reflect.DeepEqual(resultError, test.expectedError)) {
			t.Errorf("request: %v, snapshotListPage: %v, resultResponse: %v, expectedResponse: %v, resultError: %v, expectedError: %v", test.request, test.snapshots, resultResponse, test.expectedResponse, resultError, test.expectedError)
		}
	}
}

func TestGetSnapshotName(t *testing.T) {
	tests := []struct {
		options   string
		expected1 string
		expected2 error
	}{
		{
			options:   "testurl/subscriptions/12/resourceGroups/23/providers/Microsoft.Compute/snapshots/snapshot-name",
			expected1: "snapshot-name",
			expected2: nil,
		},
		{
			options:   "testurl/subscriptions/12/resourcegroups/23/providers/microsoft.compute/SNAPSHOTS/snapshot-name",
			expected1: "snapshot-name",
			expected2: nil,
		},
		{
			options:   "testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name",
			expected1: "",
			expected2: fmt.Errorf("could not get snapshot name from testurl/subscriptions/23/providers/Microsoft.Compute/snapshots/snapshot-name, correct format: %s", diskSnapshotPathRE),
		},
	}

	for _, test := range tests {
		result1, result2 := GetSnapshotNameFromURI(test.options)
		if !reflect.DeepEqual(result1, test.expected1) || !reflect.DeepEqual(result2, test.expected2) {
			t.Errorf("input: %q, getSnapshotName result1: %q, expected1: %q, result2: %q, expected2: %q", test.options, result1, test.expected1,
				result2, test.expected2)
		}
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
