/*
Copyright 2017 The Kubernetes Authors.

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
	"regexp"
	"strings"
	"unicode"

	csicommon "sigs.k8s.io/azuredisk-csi-driver/pkg/csi-common"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2019-12-01/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/pborman/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog"
	"k8s.io/kubernetes/pkg/util/mount"
	"k8s.io/kubernetes/pkg/volume/util"
	"k8s.io/legacy-cloud-providers/azure"
)

const (
	DriverName = "disk.csi.azure.com"

	errDiskNotFound = "not found"
	// default IOPS Caps & Throughput Cap (MBps) per https://docs.microsoft.com/en-us/azure/virtual-machines/linux/disks-ultra-ssd
	// see https://docs.microsoft.com/en-us/rest/api/compute/disks/createorupdate#uri-parameters
	diskNameMinLength = 1
	diskNameMaxLength = 80
	// maxLength = 80 - (4 for ".vhd") = 76
	diskNameGenerateMaxLength = 76

	//default disk size is 1 GiB
	defaultDiskSize = 1

	resourceNotFound = "ResourceNotFound"

	// VolumeAttributes for Partition
	volumeAttributePartition = "partition"

	// see https://docs.microsoft.com/en-us/rest/api/compute/disks/createorupdate#create-a-managed-disk-by-copying-a-snapshot.
	diskSnapshotPath = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/snapshots/%s"

	// see https://docs.microsoft.com/en-us/rest/api/compute/disks/createorupdate#create-a-managed-disk-from-an-existing-managed-disk-in-the-same-or-different-subscription.
	managedDiskPath = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s"

	LUN = "LUN"
)

var (
	managedDiskPathRE       = regexp.MustCompile(`(?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/disks/(.+)`)
	unmanagedDiskPathRE     = regexp.MustCompile(`http(?:.*)://(?:.*)/vhds/(.+)`)
	diskSnapshotPathRE      = regexp.MustCompile(`(?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/snapshots/(.+)`)
	diskURISupportedManaged = []string{"/subscriptions/{sub-id}/resourcegroups/{group-name}/providers/microsoft.compute/disks/{disk-id}"}
	diskURISupportedBlob    = []string{"https://{account-name}.blob.core.windows.net/{container-name}/{disk-name}.vhd"}
)

// Driver implements all interfaces of CSI drivers
type Driver struct {
	csicommon.CSIDriver
	cloud   *azure.Cloud
	mounter *mount.SafeFormatAndMount
}

// NewDriver Creates a NewCSIDriver object. Assumes vendor version is equal to driver version &
// does not support optional driver plugin info manifest field. Refer to CSI spec for more details.
func NewDriver(nodeID string) *Driver {
	driver := Driver{}
	driver.Name = DriverName
	driver.Version = driverVersion
	driver.NodeID = nodeID
	return &driver
}

// Run driver initialization
func (d *Driver) Run(endpoint string) {
	versionMeta, err := GetVersionYAML()
	if err != nil {
		klog.Fatalf("%v", err)
	}
	klog.Infof("\nDRIVER INFORMATION:\n-------------------\n%s\n\nStreaming logs below:", versionMeta)
	cloud, err := GetCloudProvider()
	if err != nil {
		klog.Fatalf("failed to get Azure Cloud Provider, error: %v", err)
	}
	d.cloud = cloud

	d.mounter, err = mounter.NewSafeMounter()
	if err != nil {
		klog.Fatalf("Failed to get safe mounter. Error: %v", err)
	}

	d.AddControllerServiceCapabilities(
		[]csi.ControllerServiceCapability_RPC_Type{
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
			csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
			csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
			csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
			csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
		})
	d.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER})
	d.AddNodeServiceCapabilities([]csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
	})

	s := csicommon.NewNonBlockingGRPCServer()
	// Driver d act as IdentityServer, ControllerServer and NodeServer
	s.Start(endpoint, d, d, d)
	s.Wait()
}

func isManagedDisk(diskURI string) bool {
	if len(diskURI) > 4 && strings.ToLower(diskURI[:4]) == "http" {
		return false
	}
	return true
}

func getDiskName(diskURI string) (string, error) {
	diskPathRE := managedDiskPathRE
	if !isManagedDisk(diskURI) {
		diskPathRE = unmanagedDiskPathRE
	}

	matches := diskPathRE.FindStringSubmatch(diskURI)
	if len(matches) != 2 {
		return "", fmt.Errorf("could not get disk name from %s, correct format: %s", diskURI, diskPathRE)
	}
	return matches[1], nil
}

func getSnapshotName(snapshotURI string) (string, error) {
	matches := diskSnapshotPathRE.FindStringSubmatch(snapshotURI)
	if len(matches) != 2 {
		return "", fmt.Errorf("could not get snapshot name from %s, correct format: %s", snapshotURI, diskSnapshotPathRE)
	}
	return matches[1], nil
}

func getResourceGroupFromURI(diskURI string) (string, error) {
	fields := strings.Split(diskURI, "/")
	if len(fields) != 9 || strings.ToLower(fields[3]) != "resourcegroups" {
		return "", fmt.Errorf("invalid disk URI: %s", diskURI)
	}
	return fields[4], nil
}

func (d *Driver) checkDiskExists(ctx context.Context, diskURI string) error {
	diskName, err := getDiskName(diskURI)
	if err != nil {
		return err
	}

	resourceGroup, err := getResourceGroupFromURI(diskURI)
	if err != nil {
		return err
	}

	if _, rerr := d.cloud.DisksClient.Get(ctx, resourceGroup, diskName); rerr != nil {
		return rerr.Error()
	}

	return nil
}

func (d *Driver) checkDiskCapacity(ctx context.Context, resourceGroup, diskName string, requestGiB int) (bool, error) {
	disk, err := d.cloud.DisksClient.Get(ctx, resourceGroup, diskName)
	// Because we can not judge the reason of the error. Maybe the disk does not exist.
	// So here we do not handle the error.
	if err == nil {
		if !reflect.DeepEqual(disk, compute.Disk{}) && disk.DiskSizeGB != nil && int(*disk.DiskSizeGB) != requestGiB {
			return false, status.Errorf(codes.AlreadyExists, "the request volume already exists, but its capacity(%v) is different from (%v)", *disk.DiskProperties.DiskSizeGB, requestGiB)
		}
	}
	return true, nil
}

func isValidDiskURI(diskURI string) error {
	if isManagedDisk(diskURI) {
		if strings.Index(diskURI, "/subscriptions/") != 0 {
			return fmt.Errorf("Inavlid DiskURI: %v, correct format: %v", diskURI, diskURISupportedManaged)
		}
	} else {
		if strings.Index(diskURI, "https://") != 0 {
			return fmt.Errorf("Inavlid DiskURI: %v, correct format: %v", diskURI, diskURISupportedBlob)
		}
	}

	return nil
}

// Disk name must begin with a letter or number, end with a letter, number or underscore,
// and may contain only letters, numbers, underscores, periods, or hyphens.
// See https://docs.microsoft.com/en-us/rest/api/compute/disks/createorupdate#uri-parameters
//
//
// Snapshot name must begin with a letter or number, end with a letter, number or underscore,
// and may contain only letters, numbers, underscores, periods, or hyphens.
// See https://docs.microsoft.com/en-us/rest/api/compute/snapshots/createorupdate#uri-parameters
//
// Since the naming rule of disk is same with snapshot's, here we use the same function to handle disks and snapshots.
func getValidDiskName(volumeName string) string {
	diskName := volumeName
	if len(diskName) > diskNameMaxLength {
		diskName = diskName[0:diskNameMaxLength]
		klog.Warningf("since the maximum volume name length is %d, so it is truncated as (%q)", diskNameMaxLength, diskName)
	}
	if !checkDiskName(diskName) || len(diskName) < diskNameMinLength {
		// todo: get cluster name
		diskName = util.GenerateVolumeName("pvc-disk", uuid.NewUUID().String(), diskNameGenerateMaxLength)
		klog.Warningf("the requested volume name (%q) is invalid, so it is regenerated as (%q)", volumeName, diskName)
	}

	return diskName
}

func checkDiskName(diskName string) bool {
	length := len(diskName)

	for i, v := range diskName {
		if !(unicode.IsLetter(v) || unicode.IsDigit(v) || v == '_' || v == '.' || v == '-') ||
			(i == 0 && !(unicode.IsLetter(v) || unicode.IsDigit(v))) ||
			(i == length-1 && !(unicode.IsLetter(v) || unicode.IsDigit(v) || v == '_')) {
			return false
		}
	}

	return true
}

func getSourceVolumeId(snapshot *compute.Snapshot) string {
	if snapshot != nil &&
		snapshot.SnapshotProperties != nil &&
		snapshot.SnapshotProperties.CreationData != nil &&
		snapshot.SnapshotProperties.CreationData.SourceResourceID != nil {
		return *snapshot.SnapshotProperties.CreationData.SourceResourceID
	}
	return ""
}

func getValidCreationData(subscriptionID, resourceGroup, sourceResourceID, sourceType string) (compute.CreationData, error) {
	if sourceResourceID == "" {
		return compute.CreationData{
			CreateOption: compute.Empty,
		}, nil
	}

	switch sourceType {
	case sourceSnapshot:
		if match := diskSnapshotPathRE.FindString(sourceResourceID); match == "" {
			sourceResourceID = fmt.Sprintf(diskSnapshotPath, subscriptionID, resourceGroup, sourceResourceID)
		}

	case sourceVolume:
		if match := managedDiskPathRE.FindString(sourceResourceID); match == "" {
			sourceResourceID = fmt.Sprintf(managedDiskPath, subscriptionID, resourceGroup, sourceResourceID)
		}
	default:
		return compute.CreationData{
			CreateOption: compute.Empty,
		}, nil
	}

	splits := strings.Split(sourceResourceID, "/")
	if len(splits) > 9 {
		if sourceType == sourceSnapshot {
			return compute.CreationData{}, fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", sourceResourceID, diskSnapshotPathRE)
		} else {
			return compute.CreationData{}, fmt.Errorf("sourceResourceID(%s) is invalid, correct format: %s", sourceResourceID, managedDiskPathRE)
		}
	}
	return compute.CreationData{
		CreateOption:     compute.Copy,
		SourceResourceID: &sourceResourceID,
	}, nil
}

// isAvailabilityZone returns true if the zone is in format of <region>-<zone-id>.
func isAvailabilityZone(zone, region string) bool {
	return strings.HasPrefix(zone, fmt.Sprintf("%s-", region))
}

func IsCorruptedDir(dir string) bool {
	_, pathErr := mount.PathExists(dir)
	fmt.Printf("IsCorruptedDir(%s) returned with error: %v", dir, pathErr)
	return pathErr != nil && mount.IsCorruptedMnt(pathErr)
}
