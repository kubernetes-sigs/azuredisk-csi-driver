//go:build windows
// +build windows

/*
Copyright 2025 The Kubernetes Authors.

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
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/os/wmi"
)

const (
	minimumResizeSize = 100 * 1024 * 1024
)

var _ VolumeAPI = &cimVolumeAPI{}

type cimVolumeAPI struct{}

func NewCIMVolumeAPI() *cimVolumeAPI {
	return &cimVolumeAPI{}
}

// ListVolumesOnDisk - returns back list of volumes(volumeIDs) in a disk and a partition.
func (*cimVolumeAPI) ListVolumesOnDisk(diskNumber uint32, partitionNumber uint32) (volumeIDs []string, err error) {
	err = wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			partitions, err := wmi.ListPartitionsOnDisk(scope, diskNumber, partitionNumber, wmi.PartitionSelectorListObjectID)
			if err != nil {
				return fmt.Errorf("failed to list partition on disk %d: %w", diskNumber, err)
			}

			volumes, err := wmi.FindVolumesByPartition(scope, partitions)
			if err != nil {
				return fmt.Errorf("failed to list volumes on disk %d: %w", diskNumber, err)
			}
			if volumes == nil {
				return nil
			}

			err = wmi.ForEach(volumes, func(volume *wmi.COMDispatchObject) error {
				uniqueID, err := wmi.GetVolumeUniqueID(volume)
				if err != nil {
					return fmt.Errorf("failed to get unique ID for volume %v: %w", volume, err)
				}
				volumeIDs = append(volumeIDs, uniqueID)
				return nil
			})
			if err != nil {
				return err
			}

			return nil
		})
	})
	return
}

// FormatVolume - Formats a volume with the NTFS format.
func (*cimVolumeAPI) FormatVolume(volumeID string) (err error) {
	return wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			volume, err := wmi.QueryVolumeByUniqueID(scope, volumeID, nil)
			if err != nil {
				return fmt.Errorf("error querying volume (%s). error: %w", volumeID, err)
			}

			err = wmi.FormatVolume(volume,
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
			if err != nil {
				return fmt.Errorf("error formatting volume (%s). error: %w", volumeID, err)
			}
			return nil
		})
	})
}

// WriteVolumeCache - Writes the file system cache to disk with the given volume id
func (*cimVolumeAPI) WriteVolumeCache(volumeID string) (err error) {
	return writeCache(volumeID)
}

// IsVolumeFormatted - Check if the volume is formatted with the pre specified filesystem(typically ntfs).
func (*cimVolumeAPI) IsVolumeFormatted(volumeID string) (bool, error) {
	var formatted bool
	err := wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			volume, err := wmi.QueryVolumeByUniqueID(scope, volumeID, wmi.VolumeSelectorListForFileSystemType)
			if err != nil {
				return fmt.Errorf("error querying volume (%s). error: %w", volumeID, err)
			}

			fsType, err := wmi.GetVolumeFileSystemType(volume)
			if err != nil {
				return fmt.Errorf("failed to query volume file system type (%s): %w", volumeID, err)
			}

			formatted = fsType != wmi.FileSystemUnknown
			return nil
		})
	})
	return formatted, err
}

// MountVolume - mounts a volume to a path. This is done using the Add-PartitionAccessPath for presenting the volume via a path.
func (*cimVolumeAPI) MountVolume(volumeID, path string) error {
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
func (*cimVolumeAPI) UnmountVolume(volumeID, path string) error {
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
func (*cimVolumeAPI) ResizeVolume(volumeID string, size int64) error {
	return wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			var err error
			var finalSize uint64
			part, err := wmi.GetPartitionByVolumeUniqueID(scope, volumeID)
			if err != nil {
				return err
			}

			// If size is 0 then we will resize to the maximum size possible, otherwise just resize to size
			if size == 0 {
				var status string
				_, finalSize, status, err = wmi.GetPartitionSupportedSize(part)
				if err != nil {
					return fmt.Errorf("error getting sizeMin, sizeMax from volume (%s). status: %s, error: %w", volumeID, status, err)
				}

			} else {
				if size < 0 {
					return fmt.Errorf("invalid negative size %d for volume (%s)", size, volumeID)
				}
				finalSize = uint64(size)
			}

			currentSize, err := wmi.GetPartitionSize(part)
			if err != nil {
				return fmt.Errorf("error getting the current size of volume (%s) with error (%w)", volumeID, err)
			}

			//if the partition's size is already the size we want this is a noop, just return
			if currentSize >= finalSize {
				klog.V(2).Infof("Attempted to resize volume (%s) to a lower size, from currentBytes=%d wantedBytes=%d", volumeID, currentSize, finalSize)
				return nil
			}

			// only resize if finalSize - currentSize is greater than 100MB
			if finalSize-currentSize < minimumResizeSize {
				klog.V(2).Infof("minimum resize difference (100MB) not met, skipping resize. volumeID=%s currentSize=%d finalSize=%d", volumeID, currentSize, finalSize)
				return nil
			}

			_, err = wmi.ResizePartition(part, finalSize)
			if err != nil {
				return fmt.Errorf("error resizing volume (%s). size:%v, finalSize %v, error: %w", volumeID, size, finalSize, err)
			}

			diskNumber, err := wmi.GetPartitionDiskNumber(part)
			if err != nil {
				return fmt.Errorf("error parsing disk number of volume (%s). error: %w", volumeID, err)
			}

			disk, err := wmi.QueryDiskByNumber(scope, diskNumber, nil)
			if err != nil {
				return fmt.Errorf("error query disk of volume (%s). error: %w", volumeID, err)
			}

			_, err = wmi.RefreshDisk(disk)
			if err != nil {
				return fmt.Errorf("error rescan disk (%d). error: %w", diskNumber, err)
			}

			return nil
		})
	})
}

// GetDiskNumberFromVolumeID - gets the disk number where the volume is.
func (*cimVolumeAPI) GetDiskNumberFromVolumeID(volumeID string) (uint32, error) {
	var diskNumber uint32
	err := wmi.WithCOMThread(func() error {
		return wmi.WithScope(func(scope *wmi.Scope) error {
			part, err := wmi.GetPartitionByVolumeUniqueID(scope, volumeID)
			if err != nil {
				return err
			}

			diskNumber, err = wmi.GetPartitionDiskNumber(part)
			if err != nil {
				return fmt.Errorf("error query disk number of volume (%s). error: %w", volumeID, err)
			}

			return nil
		})
	})
	return diskNumber, err
}

// GetVolumeIDFromTargetPath - gets the volume ID given a mount point, the function is recursive until it find a volume or errors out
func (*cimVolumeAPI) GetVolumeIDFromTargetPath(mount string) (string, error) {
	return getTarget(mount, 5 /*max depth*/)
}
