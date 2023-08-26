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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

var (
	// VolumeRegexp matches a Windows Volume
	// example: Volume{452e318a-5cde-421e-9831-b9853c521012}
	//
	// The field UniqueId has an additional prefix which is NOT included in the regex
	// however the regex can match UniqueId too
	// PS C:\disks> (Get-Disk -Number 1 | Get-Partition | Get-Volume).UniqueId
	// \\?\Volume{452e318a-5cde-421e-9831-b9853c521012}\
	VolumeRegexp = regexp.MustCompile(`Volume\{[\w-]*\}`)
)

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
func ListVolumesOnDisk(diskNumber uint32, partitionNumber uint32) (volumeIDs []string, err error) {
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

	volumeIds := strings.Split(strings.TrimSpace(string(out)), "\r\n")
	return volumeIds, nil
}

// FormatVolume - Formats a volume with the NTFS format.
func FormatVolume(volumeID string) (err error) {
	cmd := "Get-Volume -UniqueId \"$Env:volumeID\" | Format-Volume -FileSystem ntfs -Confirm:$false"
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID))
	if err != nil {
		return fmt.Errorf("error formatting volume. cmd: %s, output: %s, error: %v", cmd, string(out), err)
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
func MountVolume(volumeID, path string) error {
	cmd := "Get-Volume -UniqueId \"$Env:volumeID\" | Get-Partition | Add-PartitionAccessPath -AccessPath $Env:path"
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID), fmt.Sprintf("path=%s", path))
	if err != nil {
		return fmt.Errorf("error mount volume to path. cmd: %s, output: %s, error: %v", cmd, string(out), err)
	}
	return nil
}

// UnmountVolume - unmounts the volume path by removing the partition access path
func UnmountVolume(volumeID, path string) error {
	if err := writeCache(volumeID); err != nil {
		return err
	}
	cmd := "Get-Volume -UniqueId \"$Env:volumeID\" | Get-Partition | Remove-PartitionAccessPath -AccessPath $Env:path"
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID), fmt.Sprintf("path=%s", path))
	if err != nil {
		return fmt.Errorf("error getting driver letter to mount volume. cmd: %s, output: %s,error: %v", cmd, string(out), err)
	}
	return nil
}

// ResizeVolume - resizes a volume with the given size, if size == 0 then max supported size is used
func ResizeVolume(volumeID string, size int64) error {
	// If size is 0 then we will resize to the maximum size possible, otherwise just resize to size
	var cmd string
	var out []byte
	var err error
	var finalSize int64
	var outString string
	if size == 0 {
		cmd = "Get-Volume -UniqueId \"$Env:volumeID\" | Get-partition | Get-PartitionSupportedSize | Select SizeMax | ConvertTo-Json"
		out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID))

		if err != nil || len(out) == 0 {
			return fmt.Errorf("error getting sizemin,sizemax from mount. cmd: %s, output: %s, error: %v", cmd, string(out), err)
		}

		var getVolumeSizing map[string]int64
		outString = string(out)
		err = json.Unmarshal([]byte(outString), &getVolumeSizing)
		if err != nil {
			return fmt.Errorf("out %v outstring %v err %v", out, outString, err)
		}

		sizeMax := getVolumeSizing["SizeMax"]

		finalSize = sizeMax
	} else {
		finalSize = size
	}

	currentSize, err := getVolumeSize(volumeID)
	if err != nil {
		return fmt.Errorf("error getting the current size of volume (%s) with error (%v)", volumeID, err)
	}

	//if the partition's size is already the size we want this is a noop, just return
	if currentSize >= finalSize {
		klog.V(2).Infof("Attempted to resize volume %s to a lower size, from currentBytes=%d wantedBytes=%d", volumeID, currentSize, finalSize)
		return nil
	}

	cmd = fmt.Sprintf("Get-Volume -UniqueId \"$Env:volumeID\" | Get-Partition | Resize-Partition -Size %d", finalSize)
	out, err = azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID))
	if err != nil {
		return fmt.Errorf("error resizing volume. cmd: %s, output: %s size:%v, finalSize %v, error: %v", cmd, string(out), size, finalSize, err)
	}
	return nil
}

// GetVolumeStats - retrieves the volume stats for a given volume
func GetVolumeStats(volumeID string) (int64, int64, error) {
	// get the size and sizeRemaining for the volume
	cmd := "(Get-Volume -UniqueId \"$Env:volumeID\" | Select SizeRemaining,Size) | ConvertTo-Json"
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID))

	if err != nil {
		return -1, -1, fmt.Errorf("error getting capacity and used size of volume. cmd: %s, output: %s, error: %v", cmd, string(out), err)
	}

	var getVolume map[string]int64
	outString := string(out)
	err = json.Unmarshal([]byte(outString), &getVolume)
	if err != nil {
		return -1, -1, fmt.Errorf("out %v outstring %v err %v", out, outString, err)
	}

	volumeSize := getVolume["Size"]
	volumeSizeRemaining := getVolume["SizeRemaining"]

	volumeUsedSize := volumeSize - volumeSizeRemaining
	return volumeSize, volumeUsedSize, nil
}

// GetDiskNumberFromVolumeID - gets the disk number where the volume is.
func GetDiskNumberFromVolumeID(volumeID string) (uint32, error) {
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
func GetVolumeIDFromTargetPath(mount string) (string, error) {
	return getTarget(mount)
}

func getTarget(mount string) (string, error) {
	cmd := "(Get-Item -Path $Env:mount).Target"
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("mount=%s", mount))
	if err != nil || len(out) == 0 {
		return "", fmt.Errorf("error getting volume from mount. cmd: %s, output: %s, error: %v", cmd, string(out), err)
	}
	volumeString := strings.TrimSpace(string(out))
	if !strings.HasPrefix(volumeString, "Volume") {
		return getTarget(volumeString)
	}

	return ensureVolumePrefix(volumeString), nil
}

// GetVolumeIDFromTargetPath returns the volume id of a given target path.
func GetClosestVolumeIDFromTargetPath(targetPath string) (string, error) {
	return findClosestVolume(targetPath)
}

// findClosestVolume finds the closest volume id for a given target path
// by following symlinks and moving up in the filesystem, if after moving up in the filesystem
// we get to a DriveLetter then the volume corresponding to this drive letter is returned instead.
func findClosestVolume(path string) (string, error) {
	candidatePath := path

	// Run in a bounded loop to avoid doing an infinite loop
	// while trying to follow symlinks
	//
	// The maximum path length in Windows is 260, it could be possible to end
	// up in a sceneario where we do more than 256 iterations (e.g. by following symlinks from
	// a place high in the hierarchy to a nested sibling location many times)
	// https://docs.microsoft.com/en-us/windows/win32/fileio/naming-a-file#:~:text=In%20editions%20of%20Windows%20before,required%20to%20remove%20the%20limit.
	//
	// The number of iterations is 256, which is similar to the number of iterations in filepath-securejoin
	// https://github.com/cyphar/filepath-securejoin/blob/64536a8a66ae59588c981e2199f1dcf410508e07/join.go#L51
	for i := 0; i < 256; i++ {
		fi, err := os.Lstat(candidatePath)
		if err != nil {
			return "", err
		}
		isSymlink := fi.Mode()&os.ModeSymlink != 0

		if isSymlink {
			target, err := dereferenceSymlink(candidatePath)
			if err != nil {
				return "", err
			}
			// if it has the form Volume{volumeid} then it's a volume
			if VolumeRegexp.Match([]byte(target)) {
				// symlinks that are pointing to Volumes don't have this prefix
				return ensureVolumePrefix(target), nil
			}
			// otherwise follow the symlink
			candidatePath = target
		} else {
			// if it's not a symlink move one level up
			previousPath := candidatePath
			candidatePath = filepath.Dir(candidatePath)

			// if the new path is the same as the previous path then we reached the root path
			if previousPath == candidatePath {
				// find the volume for the root path (assuming that it's a DriveLetter)
				target, err := getVolumeForDriveLetter(candidatePath[0:1])
				if err != nil {
					return "", err
				}
				return target, nil
			}
		}

	}

	return "", fmt.Errorf("Failed to find the closest volume for path=%s", path)
}

// ensureVolumePrefix makes sure that the volume has the Volume prefix
func ensureVolumePrefix(volume string) string {
	prefix := "\\\\?\\"
	if !strings.HasPrefix(volume, prefix) {
		volume = prefix + volume
	}
	return volume
}

// dereferenceSymlink dereferences the symlink `path` and returns the stdout.
func dereferenceSymlink(path string) (string, error) {
	cmd := `(Get-Item -Path $Env:path).Target`
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("path=%s", path))
	if err != nil {
		return "", err
	}
	output := strings.TrimSpace(string(out))
	klog.V(8).Infof("Stdout: %s", output)
	return output, nil
}

// getVolumeForDriveLetter gets a volume from a drive letter (e.g. C:/).
func getVolumeForDriveLetter(path string) (string, error) {
	if len(path) != 1 {
		return "", fmt.Errorf("The path=%s is not a valid DriverLetter", path)
	}

	cmd := `(Get-Partition -DriveLetter $Env:path | Get-Volume).UniqueId`
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("path=%s", path))
	if err != nil {
		return "", err
	}
	output := strings.TrimSpace(string(out))
	klog.V(8).Infof("Stdout: %s", output)
	return output, nil
}

func writeCache(volumeID string) error {
	cmd := "Get-Volume -UniqueId \"$Env:volumeID\" | Write-Volumecache"
	out, err := azureutils.RunPowershellCmd(cmd, fmt.Sprintf("volumeID=%s", volumeID))
	if err != nil {
		return fmt.Errorf("error writing volume cache. cmd: %s, output: %s, error: %v", cmd, string(out), err)
	}
	return nil
}
