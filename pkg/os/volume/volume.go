//go:build windows
// +build windows

/*
Copyright 2023 The Kubernetes Authors.

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

package volume

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

var _ VolumeAPI = &powerShellVolumeAPI{}

type powerShellVolumeAPI struct{}

func NewPowerShellVolumeAPI() *powerShellVolumeAPI {
	return &powerShellVolumeAPI{}
}

type VolumeAPI interface {
	// ListVolumesOnDisk lists volumes on a disk identified by a `diskNumber` and optionally a partition identified by `partitionNumber`.
	ListVolumesOnDisk(diskNumber uint32, partitionNumber uint32) (volumeIDs []string, err error)
	// MountVolume mounts the volume at the requested global staging target path.
	MountVolume(volumeID, targetPath string) error
	// UnmountVolume gracefully dismounts a volume.
	UnmountVolume(volumeID, targetPath string) error
	// IsVolumeFormatted checks if a volume is formatted with NTFS.
	IsVolumeFormatted(volumeID string) (bool, error)
	// FormatVolume formats a volume with the NTFS format.
	FormatVolume(volumeID string) error
	// ResizeVolume performs resizing of the partition and file system for a block based volume.
	ResizeVolume(volumeID string, sizeBytes int64) error
	// GetDiskNumberFromVolumeID returns the disk number for a given volumeID.
	GetDiskNumberFromVolumeID(volumeID string) (uint32, error)
	// GetVolumeIDFromTargetPath returns the volume id of a given target path.
	GetVolumeIDFromTargetPath(targetPath string) (string, error)
	// WriteVolumeCache writes the volume `volumeID`'s cache to disk.
	WriteVolumeCache(volumeID string) error
}

func getVolumeSize(volumeID string) (int64, error) {
	cmd := "(Get-Volume -UniqueId \"$Env:volumeID\" | Get-partition).Size"
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID))

	if err != nil || len(out) == 0 {
		return -1, fmt.Errorf("error getting size of the partition from mount. cmd %s, output: %s, error: %v", cmd, string(out), err)
	}

	outString := strings.TrimSpace(string(out))
	volumeSize, err := strconv.ParseInt(outString, 10, 64)
	if err != nil {
		return -1, fmt.Errorf("error parsing size of volume %s received %v trimmed to %v err %v", volumeID, out, outString, err)
	}

	return volumeSize, nil
}

// ListVolumesOnDisk - returns back list of volumes(volumeIDs) in a disk and a partition.
func (*powerShellVolumeAPI) ListVolumesOnDisk(diskNumber uint32, partitionNumber uint32) (volumeIDs []string, err error) {
	var cmd string
	if partitionNumber == 0 {
		// 0 means that the partitionNumber wasn't set so we list all the partitions
		cmd = fmt.Sprintf("(Get-Disk -Number %d | Get-Partition | Get-Volume).UniqueId", diskNumber)
	} else {
		cmd = fmt.Sprintf("(Get-Disk -Number %d | Get-Partition -PartitionNumber %d | Get-Volume).UniqueId", diskNumber, partitionNumber)
	}
	out, err := azureutils.RunPowershellCmd(cmd)
	if err != nil {
		return []string{}, fmt.Errorf("error list volumes on disk. cmd: %s, output: %s, error: %v", cmd, string(out), err)
	}

	volumeIDs = strings.Split(strings.TrimSpace(string(out)), "\r\n")
	return volumeIDs, nil
}

// FormatVolume - Formats a volume with the NTFS format.
func (*powerShellVolumeAPI) FormatVolume(volumeID string) (err error) {
	cmd := "Get-Volume -UniqueId \"$Env:volumeID\" | Format-Volume -FileSystem ntfs -Confirm:$false"
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID))
	if err != nil {
		return fmt.Errorf("error formatting volume. cmd: %s, output: %s, error: %v", cmd, string(out), err)
	}
	// TODO: Do we need to handle anything for len(out) == 0
	return nil
}

// WriteVolumeCache - Writes the file system cache to disk with the given volume id
func (*powerShellVolumeAPI) WriteVolumeCache(volumeID string) (err error) {
	return writeCache(volumeID)
}

// IsVolumeFormatted - Check if the volume is formatted with the pre specified filesystem(typically ntfs).
func (*powerShellVolumeAPI) IsVolumeFormatted(volumeID string) (bool, error) {
	cmd := "(Get-Volume -UniqueId \"$Env:volumeID\" -ErrorAction Stop).FileSystemType"
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID))
	if err != nil {
		return false, fmt.Errorf("error checking if volume is formatted. cmd: %s, output: %s, error: %v", cmd, string(out), err)
	}
	stringOut := strings.TrimSpace(string(out))
	if len(stringOut) == 0 || strings.EqualFold(stringOut, "Unknown") {
		return false, nil
	}
	return true, nil
}

// MountVolume - mounts a volume to a path. This is done using the Add-PartitionAccessPath for presenting the volume via a path.
func (*powerShellVolumeAPI) MountVolume(volumeID, path string) error {
	cmd := "Get-Volume -UniqueId \"$Env:volumeID\" | Get-Partition | Add-PartitionAccessPath -AccessPath $Env:path"
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID), fmt.Sprintf("path=%s", path))
	if err != nil {
		return fmt.Errorf("error mount volume to path. cmd: %s, output: %s, error: %v", cmd, string(out), err)
	}
	return nil
}

// UnmountVolume - unmounts the volume path by removing the partition access path
func (*powerShellVolumeAPI) UnmountVolume(volumeID, path string) error {
	if err := writeCache(volumeID); err != nil {
		return err
	}
	cmd := "Get-Volume -UniqueId \"$Env:volumeID\" | Get-Partition | Remove-PartitionAccessPath -AccessPath $Env:path"
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID), fmt.Sprintf("path=%s", path))
	if err != nil {
		return fmt.Errorf("failed to mount volume. cmd: %s, output: %s,error: %v", cmd, string(out), err)
	}
	return nil
}

// ResizeVolume - resizes a volume with the given size, if size == 0 then max supported size is used
func (*powerShellVolumeAPI) ResizeVolume(volumeID string, size int64) error {
	// if size is 0 then we will resize to the maximum size possible, otherwise just resize to size
	var finalSize int64
	if size == 0 {
		cmd := "Get-Volume -UniqueId \"$Env:volumeID\" | Get-partition | Get-PartitionSupportedSize | Select SizeMax | ConvertTo-Json"
		out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID))

		if err != nil || len(out) == 0 {
			return fmt.Errorf("error getting sizemin,sizemax from mount. cmd: %s, output: %s, error: %v", cmd, string(out), err)
		}

		var getVolumeSizing map[string]int64
		outString := string(out)
		if err = json.Unmarshal([]byte(outString), &getVolumeSizing); err != nil {
			return fmt.Errorf("out %v outstring %v err %v", out, outString, err)
		}
		finalSize = getVolumeSizing["SizeMax"]
	} else {
		finalSize = size
	}

	currentSize, err := getVolumeSize(volumeID)
	if err != nil {
		return fmt.Errorf("error getting the current size of volume (%s) with error (%v)", volumeID, err)
	}

	// only resize if finalSize - currentSize is greater than 100MB
	if finalSize-currentSize < 100*1024*1024 {
		klog.V(2).Infof("minimum resize difference(1GB) not met, skipping resize. volumeID=%s currentSize=%d finalSize=%d", volumeID, currentSize, finalSize)
		return nil
	}

	cmd := fmt.Sprintf("Get-Volume -UniqueId \"$Env:volumeID\" | Get-Partition | Resize-Partition -Size %d", finalSize)
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID))
	if err != nil {
		return fmt.Errorf("error resizing volume. cmd: %s, output: %s size:%v, finalSize %v, error: %v", cmd, string(out), size, finalSize, err)
	}
	return nil
}

// GetDiskNumberFromVolumeID - gets the disk number where the volume is.
func (*powerShellVolumeAPI) GetDiskNumberFromVolumeID(volumeID string) (uint32, error) {
	// get the size and sizeRemaining for the volume
	cmd := "(Get-Volume -UniqueId \"$Env:volumeID\" | Get-Partition).DiskNumber"
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID))

	if err != nil || len(out) == 0 {
		return 0, fmt.Errorf("error getting disk number. cmd: %s, output: %s, error: %v", cmd, string(out), err)
	}

	reg, err := regexp.Compile("[^0-9]+")
	if err != nil {
		return 0, fmt.Errorf("error compiling regex. err: %v", err)
	}
	diskNumberOutput := reg.ReplaceAllString(string(out), "")

	diskNumber, err := strconv.ParseUint(diskNumberOutput, 10, 32)

	if err != nil {
		return 0, fmt.Errorf("error parsing disk number. cmd: %s, output: %s, error: %v", cmd, diskNumberOutput, err)
	}

	return uint32(diskNumber), nil
}

// GetVolumeIDFromTargetPath - gets the volume ID given a mount point, the function is recursive until it find a volume or errors out
func (*powerShellVolumeAPI) GetVolumeIDFromTargetPath(mount string) (string, error) {
	return getTarget(mount, 5 /*max depth*/)
}

func getTarget(mount string, depth int) (string, error) {
	if depth == 0 {
		return "", fmt.Errorf("maximum depth reached on mount %s", mount)
	}
	target, err := os.Readlink(mount)
	if err != nil {
		return "", fmt.Errorf("error reading link for mount %s. target %s err: %v", mount, target, err)
	}
	volumeString := strings.TrimSpace(target)
	if !strings.HasPrefix(volumeString, "Volume") && !strings.HasPrefix(volumeString, "\\\\?\\Volume") {
		return getTarget(volumeString, depth-1)
	}

	return ensureVolumePrefix(volumeString), nil
}

// ensureVolumePrefix makes sure that the volume has the Volume prefix
func ensureVolumePrefix(volume string) string {
	prefix := "\\\\?\\"
	if !strings.HasPrefix(volume, prefix) {
		volume = prefix + volume
	}
	return volume
}

func writeCache(volumeID string) error {
	cmd := "Get-Volume -UniqueId \"$Env:volumeID\" | Write-Volumecache"
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID))
	if err != nil {
		return fmt.Errorf("error writing volume cache. cmd: %s, output: %s, error: %v", cmd, string(out), err)
	}
	return nil
}
