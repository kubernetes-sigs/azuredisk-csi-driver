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

package cim

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/go-ole/go-ole"
	wmierrors "github.com/microsoft/wmi/pkg/errors"
	"github.com/pkg/errors"
	"golang.org/x/sys/windows"
	"k8s.io/klog/v2"
)

// ListVolumesOnDisk - returns back list of volumes(volumeIDs) in a disk and a partition.
func ListVolumesOnDisk(diskNumber uint32, partitionNumber uint32) (volumeIDs []string, err error) {
	partitions, err := ListPartitionsOnDisk(diskNumber, partitionNumber, []string{"ObjectId"})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list partition on disk %d", diskNumber)
	}

	volumes, err := ListVolumes([]string{"ObjectId", "UniqueId"})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list volumes")
	}

	filtered, err := FindVolumesByPartition(volumes, partitions)
	if IgnoreNotFound(err) != nil {
		return nil, errors.Wrapf(err, "failed to list volumes on disk %d", diskNumber)
	}

	for _, volume := range filtered {
		uniqueID, err := volume.GetPropertyUniqueId()
		if err != nil {
			return nil, errors.Wrapf(err, "failed to list volumes")
		}
		volumeIDs = append(volumeIDs, uniqueID)
	}

	return volumeIDs, nil
}

// FormatVolume - Formats a volume with the NTFS format.
func FormatVolume(volumeID string) (err error) {
	volume, err := QueryVolumeByUniqueID(volumeID, nil)
	if err != nil {
		return fmt.Errorf("error formatting volume (%s). error: %v", volumeID, err)
	}

	result, err := volume.InvokeMethodWithReturn(
		"Format",
		"NTFS", // Format,
		"",     // FileSystemLabel,
		nil,    // AllocationUnitSize,
		false,  // Full,
		true,   // Force
		nil,    // Compress,
		nil,    // ShortFileNameSupport,
		nil,    // SetIntegrityStreams,
		nil,    // UseLargeFRS,
		nil,    // DisableHeatGathering,
	)
	if result != 0 || err != nil {
		return fmt.Errorf("error formatting volume (%s). result: %d, error: %v", volumeID, result, err)
	}
	// TODO: Do we need to handle anything for len(out) == 0
	return nil
}

// WriteVolumeCache - Writes the file system cache to disk with the given volume id
func WriteVolumeCache(volumeID string) (err error) {
	return writeCache(volumeID)
}

// IsVolumeFormatted - Check if the volume is formatted with the pre specified filesystem(typically ntfs).
func IsVolumeFormatted(volumeID string) (bool, error) {
	volume, err := QueryVolumeByUniqueID(volumeID, []string{"FileSystemType"})
	if err != nil {
		return false, fmt.Errorf("error checking if volume (%s) is formatted. error: %v", volumeID, err)
	}

	fsType, err := volume.GetProperty("FileSystemType")
	if err != nil {
		return false, fmt.Errorf("failed to query volume file system type (%s): %w", volumeID, err)
	}

	const FileSystemUnknown = 0
	return fsType.(int32) != FileSystemUnknown, nil
}

// MountVolume - mounts a volume to a path. This is done using the Add-PartitionAccessPath for presenting the volume via a path.
func MountVolume(volumeID, path string) error {
	mountPoint := path
	if !strings.HasSuffix(mountPoint, "\\") {
		mountPoint += "\\"
	}
	utf16MountPath, _ := windows.UTF16PtrFromString(mountPoint)
	utf16VolumeID, _ := windows.UTF16PtrFromString(volumeID)
	err := windows.SetVolumeMountPoint(utf16MountPath, utf16VolumeID)
	if err != nil {
		if errors.Is(windows.GetLastError(), windows.ERROR_DIR_NOT_EMPTY) {
			targetVolumeID, err := getTarget(path, 5 /*max depth*/)
			if err != nil {
				return fmt.Errorf("error get target volume (%s) to path %s. error: %v", volumeID, path, err)
			}

			if volumeID == targetVolumeID {
				return nil
			}
		}

		return fmt.Errorf("error mount volume (%s) to path %s. error: %v", volumeID, path, err)
	}

	return nil
}

// UnmountVolume - unmounts the volume path by removing the partition access path
func UnmountVolume(volumeID, path string) error {
	if err := writeCache(volumeID); err != nil {
		return err
	}

	mountPoint := path
	if !strings.HasSuffix(mountPoint, "\\") {
		mountPoint += "\\"
	}
	utf16MountPath, _ := windows.UTF16PtrFromString(mountPoint)
	err := windows.DeleteVolumeMountPoint(utf16MountPath)
	if err != nil {
		return fmt.Errorf("error umount volume (%s) from path %s. error: %v", volumeID, path, err)
	}
	return nil
}

// ResizeVolume - resizes a volume with the given size, if size == 0 then max supported size is used
func ResizeVolume(volumeID string, size int64) error {
	var err error
	var finalSize int64
	part, err := GetPartitionByVolumeUniqueID(volumeID, nil)
	if err != nil {
		return err
	}

	// If size is 0 then we will resize to the maximum size possible, otherwise just resize to size
	if size == 0 {
		var sizeMin, sizeMax ole.VARIANT
		var status string
		result, err := part.InvokeMethodWithReturn("GetSupportedSize", &sizeMin, &sizeMax, &status)
		if result != 0 || err != nil {
			return fmt.Errorf("error getting sizemin, sizemax from volume (%s). result: %d, error: %v", volumeID, result, err)
		}

		finalSizeStr := sizeMax.ToString()
		finalSize, err = strconv.ParseInt(finalSizeStr, 10, 64)
		if err != nil {
			return fmt.Errorf("error parsing the sizeMax of volume (%s) with error (%v)", volumeID, err)
		}
	} else {
		finalSize = size
	}

	currentSizeVal, err := part.GetProperty("Size")
	if err != nil {
		return fmt.Errorf("error getting the current size of volume (%s) with error (%v)", volumeID, err)
	}

	currentSize, err := strconv.ParseInt(currentSizeVal.(string), 10, 64)
	if err != nil {
		return fmt.Errorf("error parsing the current size of volume (%s) with error (%v)", volumeID, err)
	}

	// only resize if finalSize - currentSize is greater than 100MB
	if finalSize-currentSize < 100*1024*1024 {
		klog.V(2).Infof("minimum resize difference(1GB) not met, skipping resize. volumeID=%s currentSize=%d finalSize=%d", volumeID, currentSize, finalSize)
		return nil
	}

	//if the partition's size is already the size we want this is a noop, just return
	if currentSize >= finalSize {
		klog.V(2).Infof("Attempted to resize volume (%s) to a lower size, from currentBytes=%d wantedBytes=%d", volumeID, currentSize, finalSize)
		return nil
	}

	var status string
	result, err := part.InvokeMethodWithReturn("Resize", strconv.Itoa(int(finalSize)), &status)

	if result != 0 || err != nil {
		return fmt.Errorf("error resizing volume (%s). size:%v, finalSize %v, error: %v", volumeID, size, finalSize, err)
	}

	diskNumber, err := GetPartitionDiskNumber(part)
	if err != nil {
		return fmt.Errorf("error parsing disk number of volume (%s). error: %v", volumeID, err)
	}

	disk, err := QueryDiskByNumber(diskNumber, nil)
	if err != nil {
		return fmt.Errorf("error parsing disk number of volume (%s). error: %v", volumeID, err)
	}

	result, err = disk.InvokeMethodWithReturn("Refresh", &status)
	if result != 0 || err != nil {
		return fmt.Errorf("error rescan disk (%d). result %d, error: %v", diskNumber, result, err)
	}

	return nil
}

// GetDiskNumberFromVolumeID - gets the disk number where the volume is.
func GetDiskNumberFromVolumeID(volumeID string) (uint32, error) {
	// get the size and sizeRemaining for the volume
	part, err := GetPartitionByVolumeUniqueID(volumeID, []string{"DiskNumber"})
	if err != nil {
		return 0, err
	}

	diskNumber, err := part.GetProperty("DiskNumber")
	if err != nil {
		return 0, fmt.Errorf("error query disk number of volume (%s). error: %v", volumeID, err)
	}

	return uint32(diskNumber.(int32)), nil
}

// GetVolumeIDFromTargetPath - gets the volume ID given a mount point, the function is recursive until it find a volume or errors out
func GetVolumeIDFromTargetPath(mount string) (string, error) {
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
	volume, err := QueryVolumeByUniqueID(volumeID, []string{})
	if err != nil && !wmierrors.IsNotFound(err) {
		return fmt.Errorf("error writing volume (%s) cache. error: %v", volumeID, err)
	}

	result, err := volume.Flush()
	if result != 0 || err != nil {
		return fmt.Errorf("error writing volume (%s) cache. result: %d, error: %v", volumeID, result, err)
	}
	return nil
}
