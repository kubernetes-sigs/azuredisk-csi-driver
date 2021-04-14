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
	"strings"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/golang/protobuf/ptypes"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
)

// The format of snapshot id is /subscriptions/xxx/resourceGroups/xxx/providers/Microsoft.Compute/snapshots/snapshot-xxx-xxx.
func GetSnapshotAndResourceNameFromSnapshotID(snapshotID string) (snapshotName, resourceGroup string, err error) {
	if snapshotName, err = getSnapshotNameFromURI(snapshotID); err != nil {
		return "", "", err
	}
	if resourceGroup, err = GetResourceGroupFromAzureManagedDiskURI(snapshotID); err != nil {
		return "", "", err
	}
	return snapshotName, resourceGroup, err
}

func GenerateCSISnapshot(sourceVolumeID string, snapshot *compute.Snapshot) (*csi.Snapshot, error) {
	if snapshot == nil || snapshot.SnapshotProperties == nil {
		return nil, fmt.Errorf("snapshot property is nil")
	}

	tp, err := ptypes.TimestampProto(snapshot.SnapshotProperties.TimeCreated.ToTime())
	if err != nil {
		return nil, fmt.Errorf("Failed to covert creation timestamp: %v", err)
	}
	ready, _ := isCSISnapshotReady(*snapshot.SnapshotProperties.ProvisioningState)

	if snapshot.SnapshotProperties.DiskSizeGB == nil {
		return nil, fmt.Errorf("diskSizeGB of snapshot property is nil")
	}

	if sourceVolumeID == "" {
		sourceVolumeID = GetSnapshotSourceVolumeID(snapshot)
	}

	return &csi.Snapshot{
		SizeBytes:      volumehelper.GiBToBytes(int64(*snapshot.SnapshotProperties.DiskSizeGB)),
		SnapshotId:     *snapshot.ID,
		SourceVolumeId: sourceVolumeID,
		CreationTime:   tp,
		ReadyToUse:     ready,
	}, nil
}

func GetSnapshotSourceVolumeID(snapshot *compute.Snapshot) string {
	if snapshot != nil &&
		snapshot.SnapshotProperties != nil &&
		snapshot.SnapshotProperties.CreationData != nil &&
		snapshot.SnapshotProperties.CreationData.SourceResourceID != nil {
		return *snapshot.SnapshotProperties.CreationData.SourceResourceID
	}
	return ""
}

func isCSISnapshotReady(state string) (bool, error) {
	switch strings.ToLower(state) {
	case "succeeded":
		return true, nil
	default:
		return false, nil
	}
}

func getSnapshotNameFromURI(snapshotURI string) (string, error) {
	matches := diskSnapshotPathRE.FindStringSubmatch(snapshotURI)
	if len(matches) != 2 {
		return "", fmt.Errorf("could not get snapshot name from %s, correct format: %s", snapshotURI, diskSnapshotPathRE)
	}
	return matches[1], nil
}
