//go:build windows
// +build windows

/*
Copyright 2022 The Kubernetes Authors.

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

package mounter

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/sys/windows"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	mount "k8s.io/mount-utils"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/os/disk"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/os/filesystem"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/os/volume"
)

var _ CSIProxyMounter = &winMounter{}

type winMounter struct {
	volAPI  volume.VolumeAPI
	diskAPI disk.DiskAPI
}

func NewWinMounter(useWinCIMAPI bool) *winMounter {
	var volAPI volume.VolumeAPI
	var diskAPI disk.DiskAPI
	if useWinCIMAPI {
		volAPI = volume.NewCIMVolumeAPI()
		diskAPI = disk.NewCIMDiskAPI()
	} else {
		volAPI = volume.NewPowerShellVolumeAPI()
		diskAPI = disk.NewPowerShellDiskAPI()
	}
	return &winMounter{
		volAPI:  volAPI,
		diskAPI: diskAPI,
	}
}

// Mount just creates a soft link at target pointing to source.
func (mounter *winMounter) Mount(source, target, _ string, _ []string) error {
	return os.Symlink(normalizeWindowsPath(source), normalizeWindowsPath(target))
}

// Rmdir - delete the given directory
func (mounter *winMounter) Rmdir(path string) error {
	return filesystem.Rmdir(normalizeWindowsPath(path), true)
}

// Unmount - Removes the directory - equivalent to unmount on Linux.
func (mounter *winMounter) Unmount(target string) error {
	volumeID, err := mounter.GetDeviceNameFromMount(target, "")
	if err != nil {
		return err
	}
	klog.V(2).Infof("Unmounting volume %s from %s", volumeID, target)
	if err = mounter.volAPI.UnmountVolume(volumeID, normalizeWindowsPath(target)); err != nil {
		return err
	}
	return mounter.Rmdir(target)
}

func (mounter *winMounter) List() ([]mount.MountPoint, error) {
	return []mount.MountPoint{}, fmt.Errorf("List not implemented for CSIProxyMounter")
}

func (mounter *winMounter) IsMountPoint(file string) (bool, error) {
	isNotMnt, err := mounter.IsLikelyNotMountPoint(file)
	if err != nil {
		return false, err
	}
	return !isNotMnt, nil
}

func (mounter *winMounter) IsMountPointMatch(mp mount.MountPoint, dir string) bool {
	return mp.Path == dir
}

// IsLikelyMountPoint - If the directory does not exists, the function will return os.ErrNotExist error.
// If the path exists, will check if its a link, if its a link then existence of target path is checked.
func (mounter *winMounter) IsLikelyNotMountPoint(path string) (bool, error) {
	isExists, err := mounter.ExistsPath(path)
	if err != nil {
		return false, err
	}
	if !isExists {
		return true, os.ErrNotExist
	}

	response, err := filesystem.IsMountPoint(normalizeWindowsPath(path))
	if err != nil {
		return false, err
	}
	return !response, nil
}

// MakeDir - Creates a directory.
// Currently the make dir is only used from the staging code path, hence we call it
// with Plugin context..
func (mounter *winMounter) MakeDir(path string) error {
	return os.MkdirAll(normalizeWindowsPath(path), 0755)
}

// ExistsPath - Checks if a path exists. Unlike util ExistsPath, this call does not perform follow link.
func (mounter *winMounter) ExistsPath(path string) (bool, error) {
	return filesystem.PathExists(normalizeWindowsPath(path))
}

func (mounter *winMounter) MountSensitive(source string, target string, fstype string, options []string, sensitiveOptions []string) error {
	return fmt.Errorf("MountSensitive not implemented for winMounter")
}

func (mounter *winMounter) MountSensitiveWithoutSystemd(source string, target string, fstype string, options []string, sensitiveOptions []string) error {
	return fmt.Errorf("MountSensitiveWithoutSystemd not implemented for winMounter")
}

func (mounter *winMounter) MountSensitiveWithoutSystemdWithMountFlags(source string, target string, fstype string, options []string, sensitiveOptions []string, mountFlags []string) error {
	return mounter.MountSensitive(source, target, fstype, options, sensitiveOptions /* sensitiveOptions */)
}

func (mounter *winMounter) GetMountRefs(pathname string) ([]string, error) {
	return []string{}, fmt.Errorf("GetMountRefs not implemented for winMounter")
}

func (mounter *winMounter) EvalHostSymlinks(pathname string) (string, error) {
	return "", fmt.Errorf("EvalHostSymlinks not implemented for winMounter")
}

func (mounter *winMounter) GetFSGroup(pathname string) (int64, error) {
	return -1, fmt.Errorf("GetFSGroup not implemented for winMounter")
}

func (mounter *winMounter) GetSELinuxSupport(pathname string) (bool, error) {
	return false, fmt.Errorf("GetSELinuxSupport not implemented for winMounter")
}

func (mounter *winMounter) GetMode(pathname string) (os.FileMode, error) {
	return 0, fmt.Errorf("GetMode not implemented for winMounter")
}

// GetAPIVersions returns the versions of the client APIs this mounter is using.
func (mounter *winMounter) GetAPIVersions() string {
	return ""
}

func (mounter *winMounter) CanSafelySkipMountPointCheck() bool {
	return false
}

// FormatAndMount - accepts the source disk number, target path to mount, the fstype to format with and options to be used.
func (mounter *winMounter) FormatAndMount(source, target, fstype string, options []string) error {
	diskNum, err := strconv.Atoi(source)
	if err != nil {
		return fmt.Errorf("parse %s failed with error: %v", source, err)
	}

	// set disk as online and clear readonly flag if there is any.
	if err := mounter.diskAPI.SetDiskState(uint32(diskNum), true); err != nil {
		// only log the error since SetDiskState is only needed in cloned volume
		klog.Errorf("SetDiskState on disk(%d) failed with %v", diskNum, err)
	}

	// Call PartitionDisk CSI proxy call to partition the disk and return the volume id
	if err := mounter.diskAPI.PartitionDisk(uint32(diskNum)); err != nil {
		return err
	}

	// List the volumes on the given disk.
	volumeIds, err := mounter.volAPI.ListVolumesOnDisk(uint32(diskNum), 0)
	if err != nil {
		return err
	}

	if len(volumeIds) == 0 {
		return fmt.Errorf("no volumes found on disk %d", diskNum)
	}

	// TODO: consider partitions and choose the right partition.
	// For now just choose the first volume.
	volumeID := volumeIds[0]

	// Check if the volume is formatted.
	formatted, err := mounter.volAPI.IsVolumeFormatted(volumeID)
	if err != nil {
		return err
	}

	// If the volume is not formatted, then format it, else proceed to mount.
	if !formatted {
		if err := mounter.volAPI.FormatVolume(volumeID); err != nil {
			return err
		}
	}

	// Mount the volume by calling the CSI proxy call.
	return mounter.volAPI.MountVolume(volumeID, normalizeWindowsPath(target))
}

// Rescan would trigger an update storage cache via the CSI proxy.
func (mounter *winMounter) Rescan() error {
	// Call Rescan from disk APIs of CSI Proxy.
	return mounter.diskAPI.Rescan()
}

// FindDiskByLun - given a lun number, find out the corresponding disk
func (mounter *winMounter) FindDiskByLun(lun string) (diskNum string, err error) {
	diskLocations, err := mounter.diskAPI.ListDiskLocations()
	if err != nil {
		return "", err
	}
	// List all disk locations and match the lun id being requested for.
	// If match is found then return back the disk number.
	for diskID, location := range diskLocations {
		if strings.EqualFold(location.LUNID, lun) {
			return strconv.Itoa(int(diskID)), nil
		}
	}

	klog.Warningf("ListDiskLocations failed to find lun %s, falling back to ListDisksUsingCIM", lun)
	diskLocations, err = disk.ListDisksUsingCIM()
	if err != nil {
		return "", err
	}

	// List all disk locations and match the lun id being requested for.
	// If match is found then return back the disk number.
	for diskID, location := range diskLocations {
		if strings.EqualFold(location.LUNID, lun) {
			return strconv.Itoa(int(diskID)), nil
		}
	}
	return "", fmt.Errorf("could not find disk id for lun: %s", lun)
}

// GetDeviceNameFromMount returns the volume ID for a mount path.
func (mounter *winMounter) GetDeviceNameFromMount(mountPath, pluginMountDir string) (string, error) {
	return mounter.volAPI.GetVolumeIDFromTargetPath(normalizeWindowsPath(mountPath))
}

// GetVolumeSizeInBytes returns the size of the volume in bytes.
func (mounter *winMounter) GetVolumeSizeInBytes(volumePath string) (int64, error) {
	_, totalBytes, _, err := GetFreeSpace(volumePath)
	return totalBytes, err
}

// ResizeVolume resizes the volume to the maximum available size.
// source could be the disk number or the volume id.
func (mounter *winMounter) ResizeVolume(source string) error {
	var volumeID string

	diskNum, err := strconv.Atoi(source)
	if err == nil {
		// List the volumes on the given disk.
		volumeIds, err := mounter.volAPI.ListVolumesOnDisk(uint32(diskNum), 0)
		if err != nil {
			return err
		}
		if len(volumeIds) == 0 {
			return fmt.Errorf("no volumes found on disk %d", diskNum)
		}

		// TODO: consider partitions and choose the right partition.
		// For now just choose the first volume.
		volumeID = volumeIds[0]
	} else {
		klog.V(2).Infof("parse %s failed with error: %v", source, err)
		volumeID = source
	}

	return mounter.volAPI.ResizeVolume(volumeID, 0)
}

// GetFreeSpace returns the free space of the volume in bytes, total size of the volume in bytes and the used space of the volume in bytes
func GetFreeSpace(path string) (int64, int64, int64, error) {
	var totalNumberOfBytes, totalNumberOfFreeBytes uint64
	dirName := windows.StringToUTF16Ptr(path)
	err := windows.GetDiskFreeSpaceEx(dirName, nil, &totalNumberOfBytes, &totalNumberOfFreeBytes)
	return int64(totalNumberOfFreeBytes), int64(totalNumberOfBytes), int64(totalNumberOfBytes - totalNumberOfFreeBytes), err
}

// GetVolumeStats get volume usage
func (mounter *winMounter) GetVolumeStats(ctx context.Context, path string) (*csi.VolumeUsage, error) {
	freeBytesAvailable, totalBytes, totalBytesUsed, err := GetFreeSpace(path)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get free space on path %s: %v", path, err)
	}

	volUsage := &csi.VolumeUsage{
		Unit:      csi.VolumeUsage_BYTES,
		Available: freeBytesAvailable,
		Total:     totalBytes,
		Used:      totalBytesUsed,
	}
	return volUsage, nil
}
