//go:build linux
// +build linux

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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/volume"
	mount "k8s.io/mount-utils"
	utilexec "k8s.io/utils/exec"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

const (
	sysClassBlockPath = "/sys/class/block/"
	sysClassNVMePath  = "/sys/class/nvme/"
	// 'fsck' found errors and corrected them
	fsckErrorsCorrected = 1
	// 'fsck' found errors but exited without correcting them
	fsckErrorsUncorrected = 4
	// 'fsck' found operational error, e.g fresh block device
	fsckOperationalError = 8
	// FS_IOC_SHUTDOWN = _IOR('X', 125, __u32), amd64/arm64 encoding
	fsIocShutdown = 0x8004587D
	// NVME_IOCTL_ADMIN_CMD = _IOWR('N', 0x41, 72-byte nvme_passthru_cmd)
	nvmeIoctlAdminCmd = 0xC0484E41
	// Identify opcode for NVMe passthru
	nvmeAdminIdentify = 0x06
	// NVMe Identify Namespace CNS selector
	nvmeIdentifyNamespace = 0x00
	// NVMe Identify Namespace Descriptor List CNS selector
	nvmeIdentifyNamespaceDescs = 0x03
	// The offset of the NGUID in the NVMe Identify Namespace response of type struct nvme_id_ns
	nvmeIdentifyNamespaceNGUIDOffset = 104
	// NVMe Identify Namespace Descriptor types
	nvmeNamespaceDescNGUID = 0x02
	nvmeNamespaceDescUUID  = 0x03
	// NVMe status code bit set when an operation should not be retried
	nvmeStatusCodeDoNotRetry = 0x4000
)

// wholeDiskNameRegexp captures the whole-disk prefix of a SCSI (sd*) or NVMe (nvme*) device name,
// dropping any partition suffix (sdc1 -> sdc, nvme0n1p1 -> nvme0n1); empty for anything else.
var wholeDiskNameRegexp = regexp.MustCompile(`^sd[a-z]+|^nvme[0-9]+n[0-9]+`)

// nvmeControllerRegexp captures the controller of an NVMe namespace device (nvme0n1 -> nvme0).
var nvmeControllerRegexp = regexp.MustCompile(`^nvme[0-9]+`)

// exclude those used by azure as resource and OS root in /dev/disk/azure, /dev/disk/azure/scsi0
// "/dev/disk/azure/scsi0" dir is populated in Standard_DC4s/DC2s on Ubuntu 18.04
func listAzureDiskPath(io azureutils.IOHandler) []string {
	var azureDiskList []string
	azureResourcePaths := []string{"/dev/disk/azure/", "/dev/disk/azure/scsi0/"}
	for _, azureDiskPath := range azureResourcePaths {
		if dirs, err := io.ReadDir(azureDiskPath); err == nil {
			for _, f := range dirs {
				name := f.Name()
				diskPath := filepath.Join(azureDiskPath, name)
				if link, linkErr := io.Readlink(diskPath); linkErr == nil {
					sd := link[(strings.LastIndex(link, "/") + 1):]
					azureDiskList = append(azureDiskList, sd)
				}
			}
		}
	}
	klog.V(12).Infof("Azure sys disks paths: %v", azureDiskList)
	return azureDiskList
}

// getDiskLinkByDevName get disk link by device name from devLinkPath, e.g. /dev/disk/azure/, /dev/disk/by-id/
func getDiskLinkByDevName(io azureutils.IOHandler, devLinkPath, devName string) (string, error) {
	dirs, err := io.ReadDir(devLinkPath)
	klog.V(12).Infof("azureDisk - begin to find %s from %s", devName, devLinkPath)
	if err == nil {
		for _, f := range dirs {
			diskPath := devLinkPath + f.Name()
			klog.V(12).Infof("azureDisk - begin to Readlink: %s", diskPath)
			link, linkErr := io.Readlink(diskPath)
			if linkErr != nil {
				klog.Warningf("azureDisk - read link (%s) error: %v", diskPath, linkErr)
				continue
			}
			if strings.HasSuffix(link, devName) {
				return diskPath, nil
			}
		}
		return "", fmt.Errorf("device name(%s) is not found under %s", devName, devLinkPath)
	}
	return "", fmt.Errorf("read %s error: %v", devLinkPath, err)
}

func scsiHostRescan(io azureutils.IOHandler, _ *mount.SafeFormatAndMount) {
	scsiPath := "/sys/class/scsi_host/"
	if dirs, err := io.ReadDir(scsiPath); err == nil {
		for _, f := range dirs {
			name := scsiPath + f.Name() + "/scan"
			data := []byte("- - -")
			if err = io.WriteFile(name, data, 0666); err != nil {
				klog.Warningf("failed to rescan scsi host %s", name)
			}
		}
	} else {
		klog.Warningf("failed to read %s, err %v", scsiPath, err)
	}
}

func findDiskByLun(lun int, io azureutils.IOHandler, _ *mount.SafeFormatAndMount) (string, error) {
	azureDisks := listAzureDiskPath(io)
	device, err := findDiskByLunWithConstraint(lun, io, azureDisks)
	if err == nil && device != "" {
		return device, nil
	}

	devPaths := []string{
		fmt.Sprintf("/dev/disk/azure/scsi1/lun%d", lun),
		fmt.Sprintf("/dev/disk/azure/data/by-lun/%d", lun),
	}
	klog.Warningf("failed to find disk by lun %d, err %v, fall back to search in following device path: %s", lun, err, devPaths)
	for _, devPath := range devPaths {
		if _, err := os.Stat(devPath); err == nil {
			if device, err := io.Readlink(devPath); err == nil {
				klog.V(2).Infof("found device path %s linked to %s by lun %d", devPath, device, lun)
				return devPath, nil
			}
		}
	}
	return "", fmt.Errorf("failed to find disk by lun %d", lun)
}

func formatAndMount(source, target, fstype string, options []string, m *mount.SafeFormatAndMount, formatSem chan any, formatTimeout time.Duration) error {
	if newOptions, exists := azureutils.RemoveOptionIfExists(options, "directmount"); exists {
		klog.V(2).Infof("formatAndMount - skip format for %s, old options: %v, new options: %v", target, options, newOptions)
		return m.Mount(source, target, fstype, newOptions)
	}

	readOnly := false
	for _, option := range options {
		if option == "ro" {
			readOnly = true
			break
		}
	}

	// Only single source of truth to know whether the disk is formatted or not is through blkid.
	diskFSFormat, err := m.GetDiskFormat(source)
	if err != nil {
		klog.Errorf("formatAndMount - failed to get disk format for %s with error(%v)", source, err)
		return fmt.Errorf("failed to get disk format for %s with error(%v)", source, err)
	}

	newlyFormatted := false
	if diskFSFormat == "" {
		// As part of auto-recovery from mount failures, we run fsck on the disk. If the disk was pulled
		// out or a power loss occurred during a previous fsck run, the primary superblocks may have been
		// corrupted depending on the stage at which fsck was interrupted. We run fsck here to detect and
		// repair such issues before formatting.
		// Note: fsck is a no-op if the disk already has an xfs signature.
		// If filesystem was detected at this stage don't format return the error
		isFilesystemExist, err := detectFilesystemExistence(source, m)
		if err != nil {
			klog.Errorf("formatAndMount - failed to check existence of filesystem on disk %s with error(%v)", source, err)
			return fmt.Errorf("formatAndMount - failed to check existence of filesystem on disk %s with error(%v)", source, err)
		}

		if !isFilesystemExist {
			if readOnly {
				return fmt.Errorf("formatAndMount - disk %s is not formatted, but mount options specify read-only", source)
			}

			// If filesystem doesn't exist then we can format the disk with the given fstype.
			// Disk is unformatted
			args := []string{source}
			if fstype == "ext4" || fstype == "ext3" {
				args = []string{
					"-F",  // Force flag, if we reached this point then mostly it's a fresh disk without any fs signature
					"-m0", // Zero blocks reserved for super-user
					source,
				}
			} else if fstype == "xfs" {
				args = []string{
					"-f",
					source,
				}
			}

			klog.Infof("Disk %q appears to be unformatted, attempting to format as type: %q with options: %v", source, fstype, args)
			output, err := mkfsWithConcurrencyLimit(m, formatSem, formatTimeout, fstype, args)
			if err != nil {
				klog.Errorf("formatAndMount - failed to format disk %s as type %s with options %v: %v error: %v", source, fstype, args, string(output), err)
				return fmt.Errorf("failed to format disk %s as type %s with options %v: %v error: %v", source, fstype, args, string(output), err)
			}
			// Format was successful
			diskFSFormat = fstype
			newlyFormatted = true
		} else {
			// Re-read the filesystem signature to determine the existing format.
			reReadFormat, err := m.GetDiskFormat(source)
			if err != nil {
				klog.Errorf("formatAndMount - failed to re-read filesystem signature on disk %s with error: %v", source, err)
				return fmt.Errorf("failed to re-read filesystem signature on disk %s with error: %v", source, err)
			}
			klog.Infof("Disk %q contains filesystem signature post running fsck, skipping format; detected filesystem type: %q", source, reReadFormat)
			diskFSFormat = reReadFormat
		}
	}

	if !strings.EqualFold(fstype, diskFSFormat) {
		// Verify that the disk is formatted with filesystem type we are expecting
		klog.Errorf("formatAndMount - configured to mount disk %s as %s but current format is %s, things might break", source, fstype, diskFSFormat)
		return fmt.Errorf("configured to mount disk %s as %s but current format is %s, things might break", source, fstype, diskFSFormat)
	}

	// Run fsck on the disk to fix repairable issues, only do this for already formatted volumes requested as read-write.
	if !newlyFormatted && !readOnly {
		_, err := detectAndRepairFilesystem(source, []string{"-a"}, m)
		if err != nil {
			klog.Errorf("formatAndMount - failed to run fsck on disk %s with error: %v", source, err)
			return fmt.Errorf("failed to run fsck on disk %s with error: %v", source, err)
		}
	}

	klog.V(4).Infof("Attempting to mount disk %s in %s format at %s", source, fstype, target)
	options = append(options, "defaults")
	if err := m.Mount(source, target, fstype, options); err != nil {
		klog.Errorf("formatAndMount - failed to mount disk %s at %s with options %v and error: %v", source, target, options, err)
		return fmt.Errorf("failed to mount disk %s at %s with options %v and error: %v", source, target, options, err)
	}

	return nil
}

// mkfsWithConcurrencyLimit runs mkfs.<fstype> while honoring the max-concurrent-format
// semaphore. Because this driver formats the disk with a direct exec call (instead of
// mount.SafeFormatAndMount.FormatAndMount) to insert the pre-format fsck auto-recovery
// step, it must reproduce SafeFormatAndMount's WithMaxConcurrentFormat gating here to
// avoid unbounded concurrent mkfs operations under load. The logic mirrors the upstream
// (unexported) SafeFormatAndMount.format method: once formatTimeout elapses the token is
// released so another format can proceed, while the original mkfs is allowed to complete.
func mkfsWithConcurrencyLimit(m *mount.SafeFormatAndMount, formatSem chan any, formatTimeout time.Duration, fstype string, args []string) ([]byte, error) {
	if formatSem != nil {
		done := make(chan struct{})
		defer close(done)

		formatSem <- struct{}{}

		go func() {
			defer func() { <-formatSem }()

			timeout := time.NewTimer(formatTimeout)
			defer timeout.Stop()

			select {
			case <-done:
			case <-timeout.C:
			}
		}()
	}

	return m.Exec.Command("mkfs."+fstype, args...).CombinedOutput()
}

// finds a device mounted to "current" node
func findDiskByLunWithConstraint(lun int, io azureutils.IOHandler, azureDisks []string) (string, error) {
	var err error
	sysPath := "/sys/bus/scsi/devices"
	if dirs, err := io.ReadDir(sysPath); err == nil {
		for _, f := range dirs {
			name := f.Name()
			// look for path like /sys/bus/scsi/devices/3:0:0:1
			arr := strings.Split(name, ":")
			if len(arr) < 4 {
				continue
			}
			if len(azureDisks) == 0 {
				klog.V(4).Infof("/dev/disk/azure is not populated, now try to parse %v directly", name)
				target, err := strconv.Atoi(arr[0])
				if err != nil {
					klog.Errorf("failed to parse target from %v (%v), err %v", arr[0], name, err)
					continue
				}
				// as observed, targets 0-3 are used by OS disks. Skip them
				if target <= 3 {
					continue
				}
			}

			// extract LUN from the path.
			// LUN is the last index of the array, i.e. 1 in /sys/bus/scsi/devices/3:0:0:1
			l, err := strconv.Atoi(arr[3])
			if err != nil {
				// unknown path format, continue to read the next one
				klog.V(4).Infof("azure disk - failed to parse lun from %v (%v), err %v", arr[3], name, err)
				continue
			}
			if lun == l {
				// find the matching LUN
				// read vendor and model to ensure it is a VHD disk
				vendorPath := filepath.Join(sysPath, name, "vendor")
				vendorBytes, err := io.ReadFile(vendorPath)
				if err != nil {
					klog.Errorf("failed to read device vendor, err: %v", err)
					continue
				}
				vendor := strings.TrimSpace(string(vendorBytes))
				if strings.ToUpper(vendor) != "MSFT" {
					klog.V(4).Infof("vendor doesn't match VHD, got %s", vendor)
					continue
				}

				modelPath := filepath.Join(sysPath, name, "model")
				modelBytes, err := io.ReadFile(modelPath)
				if err != nil {
					klog.Errorf("failed to read device model, err: %v", err)
					continue
				}
				model := strings.TrimSpace(string(modelBytes))
				if strings.ToUpper(model) != "VIRTUAL DISK" {
					klog.V(4).Infof("model doesn't match VHD, got %s", model)
					continue
				}

				// find a disk, validate name
				dir := filepath.Join(sysPath, name, "block")
				if dev, err := io.ReadDir(dir); err == nil {
					found := false
					devName := dev[0].Name()
					for _, diskName := range azureDisks {
						klog.V(12).Infof("azureDisk - validating disk %q with sys disk %q", devName, diskName)
						if devName == diskName {
							found = true
							break
						}
					}
					if !found {
						devLinkPaths := []string{"/dev/disk/azure/scsi1/", "/dev/disk/by-id/"}
						for _, devLinkPath := range devLinkPaths {
							diskPath, err := getDiskLinkByDevName(io, devLinkPath, devName)
							if err == nil {
								klog.V(4).Infof("azureDisk - found %s by %s under %s", diskPath, devName, devLinkPath)
								return diskPath, nil
							}
							klog.Warningf("azureDisk - getDiskLinkByDevName by %s under %s failed, error: %v", devName, devLinkPath, err)
						}
						return "/dev/" + devName, nil
					}
				}
			}
		}
	}
	return "", err
}

func preparePublishPath(_ string, _ *mount.SafeFormatAndMount) error {
	return nil
}

func CleanupMountPoint(path string, m *mount.SafeFormatAndMount, extensiveCheck bool) error {
	return mount.CleanupMountPoint(path, m, extensiveCheck)
}

func getDevicePathWithMountPath(mountPath string, m *mount.SafeFormatAndMount) (string, error) {
	args := []string{"-o", "source", "--noheadings", "--mountpoint", mountPath}
	output, err := m.Exec.Command("findmnt", args...).Output()
	if err != nil {
		return "", fmt.Errorf("could not determine device path(%s), error: %v", mountPath, err)
	}

	devicePath := strings.TrimSpace(string(output))
	if len(devicePath) == 0 {
		return "", fmt.Errorf("could not get valid device for mount path: %q", mountPath)
	}

	return devicePath, nil
}

func getBlockSizeBytes(devicePath string, m *mount.SafeFormatAndMount) (int64, error) {
	output, err := m.Exec.Command("blockdev", "--getsize64", devicePath).Output()
	if err != nil {
		return -1, fmt.Errorf("error when getting size of block volume at path %s: output: %s, err: %v", devicePath, string(output), err)
	}
	strOut := strings.TrimSpace(string(output))
	gotSizeBytes, err := strconv.ParseInt(strOut, 10, 64)
	if err != nil {
		return -1, fmt.Errorf("failed to parse size %s into int a size", strOut)
	}
	return gotSizeBytes, nil
}

func resizeVolume(devicePath, volumePath string, m *mount.SafeFormatAndMount) error {
	_, err := mount.NewResizeFs(m.Exec).Resize(devicePath, volumePath)
	return err
}

// needResizeVolume check whether device needs resize
func needResizeVolume(devicePath, volumePath string, m *mount.SafeFormatAndMount) (bool, error) {
	return mount.NewResizeFs(m.Exec).NeedResize(devicePath, volumePath)
}

// rescanVolume rescan device for detecting device size expansion
// devicePath e.g. `/dev/sdc`
func rescanVolume(io azureutils.IOHandler, devicePath string) error {
	klog.V(6).Infof("rescanVolume - begin to rescan %s", devicePath)
	deviceName := filepath.Base(devicePath)
	rescanPath := filepath.Join(sysClassBlockPath, deviceName, "device/rescan")
	return io.WriteFile(rescanPath, []byte("1"), 0666)
}

// rescanAllVolumes rescan all sd* devices under /sys/class/block/sd* starting from sdc
func rescanAllVolumes(io azureutils.IOHandler) error {
	dirs, err := io.ReadDir(sysClassBlockPath)
	if err != nil {
		return err
	}
	for _, device := range dirs {
		deviceName := device.Name()
		if strings.HasPrefix(deviceName, "sd") && deviceName >= "sdc" {
			path := filepath.Join(sysClassBlockPath, deviceName)
			if err := rescanVolume(io, path); err != nil {
				klog.Warningf("rescanVolume - rescan %s failed with %v", path, err)
			}
		}
	}
	return nil
}

// detectFilesystemExistence checks whether the given device already contains a filesystem signature.
// 2. Detects filesystem signature using wipefs --no-act --noheadings --output TYPE <device>.
func detectFilesystemExistence(source string, mounter *mount.SafeFormatAndMount) (bool, error) {
	isFSExist, err := detectAndRepairFilesystem(source, []string{"-n"}, mounter)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "superblock could not be read or does not describe a valid ext2/ext3/ext4") {
			klog.Infof("Device %s may be fresh block device with no filesystem signature, fsck output: %s", source, err.Error())
		} else {
			klog.Errorf("detectFilesystemExistence - failed to check existence of filesystem on disk %s through 'fsck' with error(%v)", source, err)
			return false, fmt.Errorf("detectFilesystemExistence - failed to check existence of filesystem on disk %s through 'fsck' with error(%v)", source, err)
		}
	}
	if isFSExist {
		return true, nil
	}

	args := []string{"--no-act", "--output", "TYPE", "--noheadings", source}
	fsType, err := mounter.Exec.Command("wipefs", args...).CombinedOutput()
	if err != nil {
		klog.Errorf("detectFilesystemExistence - failed to check existence of filesystem on disk %s through 'wipefs' with error(%v)", source, err)
		return false, fmt.Errorf("detectFilesystemExistence - failed to check existence of filesystem on disk %s through 'wipefs' with error(%v)", source, err)
	}
	if len(strings.TrimSpace(string(fsType))) > 0 {
		return true, nil
	}
	return false, nil
}

// detectAndRepairFilesystem runs fsck on the given device to determine whether it already contains
// a filesystem and to repair any recoverable inconsistencies in place based on provided options.
//
// It returns:
//   - (true, nil): a filesystem is present (clean or recoverable errors corrected).
//   - (false, nil): no filesystem signature (fresh/unformatted device).
//   - (false, error): fsck could not be executed or returned an operational/ambiguous failure.
//   - (true, error): fsck found errors it could not correct.
//
// fsckOptions control fsck's behavior, e.g. "-n" for a read-only detection check or "-y"/"-E ..."
// to attempt repairs.
func detectAndRepairFilesystem(source string, fsckOptions []string, mounter *mount.SafeFormatAndMount) (bool, error) {
	klog.V(2).Infof("Checking for issues with fsck on disk: %s with options %v", source, fsckOptions)
	args := append(append([]string(nil), fsckOptions...), source)
	out, err := mounter.Exec.Command("fsck", args...).CombinedOutput()
	if err != nil {
		ee, isExitError := err.(utilexec.ExitError)
		switch {
		case err == utilexec.ErrExecutableNotFound:
			klog.Errorf("'fsck' not found on system; cannot verify filesystem signature on %s, returning error.", source)
			return false, fmt.Errorf("'fsck' not found to detect filesystem on device %s with options %v: %v", source, fsckOptions, err)
		case isExitError && ee.ExitStatus() == fsckOperationalError:
			klog.Warningf("Unable to run fsck on device %s with options %v, fsck output: %s", source, fsckOptions, string(out))
			return false, fmt.Errorf("Unable to run fsck on device %s with options %v, fsck error: %v output: %s", source, fsckOptions, err, string(out))
		case isExitError && ee.ExitStatus() == fsckErrorsCorrected:
			klog.Warningf("Device %s has errors which were corrected by fsck: %s", source, string(out))
		case isExitError && ee.ExitStatus() == fsckErrorsUncorrected:
			// Filesystem exists but fsck found errors that it could not correct
			klog.Errorf("Device %s has errors which fsck could not correct with options %v: %s", source, fsckOptions, string(out))
			return true, fmt.Errorf("'fsck' found errors on device %s with options %v but could not correct them (exit status %d)", source, fsckOptions, ee.ExitStatus())
		case isExitError && ee.ExitStatus() > fsckErrorsUncorrected:
			klog.Errorf("`fsck` error %s", string(out))
			return false, fmt.Errorf("'fsck' failed on device %s with options %v: %v", source, fsckOptions, err)
		default:
			klog.Warningf("fsck on device %s failed with error %v, output: %v", source, err, string(out))
		}
	}
	// In case if device is formatted with other filesystem, fsck will return 0
	klog.Infof("fsck on device %s completed successfully with output: %s", source, string(out))
	return true, nil
}

func (d *Driver) GetVolumeStats(_ context.Context, m *mount.SafeFormatAndMount, _, target string, hostutil hostUtil) ([]*csi.VolumeUsage, error) {
	var volUsages []*csi.VolumeUsage
	_, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, status.Errorf(codes.NotFound, "path %s does not exist", target)
		}
		return volUsages, status.Errorf(codes.Internal, "failed to stat file %s: %v", target, err)
	}

	isBlock, err := hostutil.PathIsDevice(target)
	if err != nil {
		return volUsages, status.Errorf(codes.NotFound, "failed to determine whether %s is block device: %v", target, err)
	}
	if isBlock {
		bcap, err := getBlockSizeBytes(target, m)
		if err != nil {
			return volUsages, status.Errorf(codes.Internal, "failed to get block capacity on path %s: %v", target, err)
		}
		return []*csi.VolumeUsage{
			{
				Unit:  csi.VolumeUsage_BYTES,
				Total: bcap,
			},
		}, nil
	}

	volumeMetrics, err := volume.NewMetricsStatFS(target).GetMetrics()
	if err != nil {
		return volUsages, err
	}

	available, ok := volumeMetrics.Available.AsInt64()
	if !ok {
		return volUsages, status.Errorf(codes.Internal, "failed to transform volume available size(%v)", volumeMetrics.Available)
	}
	capacity, ok := volumeMetrics.Capacity.AsInt64()
	if !ok {
		return volUsages, status.Errorf(codes.Internal, "failed to transform volume capacity size(%v)", volumeMetrics.Capacity)
	}
	used, ok := volumeMetrics.Used.AsInt64()
	if !ok {
		return volUsages, status.Errorf(codes.Internal, "failed to transform volume used size(%v)", volumeMetrics.Used)
	}

	inodesFree, ok := volumeMetrics.InodesFree.AsInt64()
	if !ok {
		return volUsages, status.Errorf(codes.Internal, "failed to transform disk inodes free(%v)", volumeMetrics.InodesFree)
	}
	inodes, ok := volumeMetrics.Inodes.AsInt64()
	if !ok {
		return volUsages, status.Errorf(codes.Internal, "failed to transform disk inodes(%v)", volumeMetrics.Inodes)
	}
	inodesUsed, ok := volumeMetrics.InodesUsed.AsInt64()
	if !ok {
		return volUsages, status.Errorf(codes.Internal, "failed to transform disk inodes used(%v)", volumeMetrics.InodesUsed)
	}

	return []*csi.VolumeUsage{
		{
			Unit:      csi.VolumeUsage_BYTES,
			Available: available,
			Total:     capacity,
			Used:      used,
		},
		{
			Unit:      csi.VolumeUsage_INODES,
			Available: inodesFree,
			Total:     inodes,
			Used:      inodesUsed,
		},
	}, nil
}

// verifyNVMeNamespaceIdentity guards against a reused NVMe namespace serving stale content: it compares
// the live controller UUID/NGUID via NVMe passthru with the kernel's cached sysfs value, and on
// mismatch rescans and fails so k8s retries with a re-enumerated device. No-op for non-NVMe disks.
func verifyNVMeNamespaceIdentity(source, lun string, io azureutils.IOHandler) error {
	devicePath, err := filepath.EvalSymlinks(source)
	if err != nil {
		devicePath = source
	}
	blockName := filepath.Base(devicePath)
	controller := nvmeControllerRegexp.FindString(blockName)
	if controller == "" {
		return nil // not an NVMe namespace
	}
	cachedID := getCachedNVMeID(io, blockName)
	lunInt, err := azureutils.GetDiskLUN(lun)
	if err != nil {
		return fmt.Errorf("verifyNVMeNamespaceIdentity - failed to parse lun %q for %s: %v", lun, blockName, err)
	}
	nsid := uint32(lunInt) + 2 // Azure NVMe data disks use NSID = LUN + 2 (see https://github.com/Azure/azure-vm-utils/blob/v0.7.0/doc/disk-identification.md?plain=1#L90)
	liveID, err := getLiveNVMeID(controller, nsid)
	if err != nil {
		return fmt.Errorf("verifyNVMeNamespaceIdentity - failed to read live identity for %s: %v", blockName, err)
	}
	if cachedID == liveID {
		return nil // kernel's cached identity is current
	}

	klog.Errorf("verifyNVMeNamespaceIdentity - namespace identity mismatch on %s (kernel %s, controller %s); rescanning %s", blockName, cachedID, liveID, controller)
	if err := io.WriteFile(filepath.Join(sysClassNVMePath, controller, "rescan_controller"), []byte("1"), 0200); err != nil {
		klog.Errorf("verifyNVMeNamespaceIdentity - rescan of %s failed: %v", controller, err)
	}
	return fmt.Errorf("NVMe namespace %s on %s is stale; rescan issued, retry staging", blockName, controller)
}

// getCachedNVMeID returns the kernel's cached namespace identifier (NGUID preferred, else UUID) from
// sysfs, or uuid.Nil if none.
func getCachedNVMeID(io azureutils.IOHandler, blockName string) uuid.UUID {
	for _, attr := range []string{"nguid", "uuid"} {
		data, err := io.ReadFile(filepath.Join(sysClassBlockPath, blockName, attr))
		if err != nil {
			continue
		}
		if id, err := uuid.Parse(strings.TrimSpace(string(data))); err == nil && id != uuid.Nil {
			return id
		}
	}
	return uuid.Nil
}

// getLiveNVMeID returns the namespace's NGUID (preferred) or UUID via an Identify admin passthru to
// the controller, reflecting the live identity rather than the kernel's cache; uuid.Nil if absent.
// A package var so tests can stub the live read.
var getLiveNVMeID = func(controller string, nsid uint32) (uuid.UUID, error) {
	fd, err := unix.Open(filepath.Join("/dev", controller), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to open /dev/%s: %v", controller, err)
	}
	defer unix.Close(fd)

	buf := make([]byte, 4096)
	cmd := nvmePassthruCmd{
		opcode:  nvmeAdminIdentify,
		nsid:    nsid,
		addr:    uint64(uintptr(unsafe.Pointer(&buf[0]))),
		dataLen: uint32(len(buf)),
		cdw10:   nvmeIdentifyNamespaceDescs,
	}
	status, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(nvmeIoctlAdminCmd), uintptr(unsafe.Pointer(&cmd)))
	runtime.KeepAlive(buf)
	if errno != 0 || status != 0 {
		if errno == 0 && status&nvmeStatusCodeDoNotRetry != 0 {
			klog.Warningf("nvme identify namespace descriptors ioctl on /dev/%s nsid %d failed with non-retriable status %v", controller, nsid, status)
		} else {
			return uuid.Nil, fmt.Errorf("nvme identify namespace descriptors ioctl on /dev/%s nsid %d failed with errno %v and status %v", controller, nsid, errno, status)
		}
	}

	var uid uuid.UUID

	if status == 0 {
		for buf := buf; len(buf) >= 4; {
			nidt, nidl := buf[0], int(buf[1])
			if nidl == 0 || 4+nidl > len(buf) {
				break
			}
			if nidt == nvmeNamespaceDescNGUID || nidt == nvmeNamespaceDescUUID {
				id, err := uuid.FromBytes(buf[4 : 4+nidl])
				if err == nil && id != uuid.Nil {
					uid = id
					if nidt == nvmeNamespaceDescNGUID {
						return uid, nil // NGUID preferred over UUID
					}
				}
			}
			buf = buf[4+nidl:]
		}
	}

	cmd.cdw10 = nvmeIdentifyNamespace
	status, _, errno = unix.Syscall(unix.SYS_IOCTL, uintptr(fd), uintptr(nvmeIoctlAdminCmd), uintptr(unsafe.Pointer(&cmd)))
	runtime.KeepAlive(buf)
	if errno != 0 || status != 0 {
		if errno == 0 && status&nvmeStatusCodeDoNotRetry != 0 {
			klog.Errorf("nvme identify namespace ioctl on /dev/%s nsid %d failed with non-retriable status %v", controller, nsid, status)
		} else {
			return uuid.Nil, fmt.Errorf("nvme identify namespace ioctl on /dev/%s nsid %d failed with errno %v and status %v", controller, nsid, errno, status)
		}
	}
	if status == 0 {
		id, err := uuid.FromBytes(buf[nvmeIdentifyNamespaceNGUIDOffset : nvmeIdentifyNamespaceNGUIDOffset+16])
		if err == nil && id != uuid.Nil {
			uid = id
		}
	}

	return uid, nil
}

// unmountAndInvalidateDevice shuts down the fs, unmounts the staging path and invalidates the backing device's caches
// so any pending changes are flushed, mitigating cache inconsistencies especially with SCSI devices.
// The fs shutdown and device cache invalidation are best-effort; only an unmount error is returned.
func unmountAndInvalidateDevice(stagingTargetPath string, io azureutils.IOHandler, m *mount.SafeFormatAndMount) error {
	// Resolve the underlying block device while still mounted.
	devicePath, _, err := mount.GetDeviceNameFromMount(m, stagingTargetPath)
	if err != nil {
		devicePath = ""
	} else if devicePath != "" {
		if resolved, err := filepath.EvalSymlinks(devicePath); err == nil {
			devicePath = resolved
		}
	}
	// Shut down the fs before unmounting so a private clone can't keep it alive and re-dirty the cache.
	if err := shutdownFilesystem(stagingTargetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		klog.Errorf("shutdownFilesystem - %v", err)
	}

	// Unmount the driver's global mount.
	if err := CleanupMountPoint(stagingTargetPath, m, true /*extensiveMountPointCheck*/); err != nil {
		return err
	}

	// Flush and invalidate the block-device caches so its synchronized for all fs types
	// and so a racing NodeStageVolume has a smaller chance of reading stale content.
	if devicePath == "" {
		return nil
	}
	wholeDiskName := wholeDiskNameRegexp.FindString(filepath.Base(devicePath))
	flushTargets := []string{devicePath}
	if wholeDiskName != "" && "/dev/"+wholeDiskName != devicePath {
		flushTargets = append(flushTargets, "/dev/"+wholeDiskName)
	}
	for _, target := range flushTargets {
		if err := flushAndInvalidateBlockDevice(target); err != nil {
			klog.Errorf("flushAndInvalidateBlockDevice - %v", err)
		}
	}

	// Delete SCSI devices so the next NodeStageVolume sees a fresh one. NVMe has no per-disk
	// equivalent and is handled by the identity check in NodeStageVolume instead.
	if strings.HasPrefix(wholeDiskName, "sd") {
		deletePath := filepath.Join(sysClassBlockPath, wholeDiskName, "device", "delete")
		if err := io.WriteFile(deletePath, []byte("1"), 0200); err != nil {
			klog.Errorf("unmountAndInvalidateDevice - failed to delete SCSI disk %s: %v", wholeDiskName, err)
		}
	}

	return nil
}

// flushAndInvalidateBlockDevice flushes and drops the raw block-device page cache so
// its synchronized for all fs types and so there's a smaller chance of the next NodeStageVolume reading stale cached content.
func flushAndInvalidateBlockDevice(devicePath string) error {
	fd, err := unix.Open(devicePath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("failed to open device %s: %v", devicePath, err)
	}
	defer unix.Close(fd)
	if err := unix.IoctlSetInt(fd, unix.BLKFLSBUF, 0); err != nil {
		return fmt.Errorf("failed to flush buffers on device %s: %v", devicePath, err)
	}
	return nil
}

// shutdownFilesystem forces an orderly (flush-first) shutdown of the mount at stagingTargetPath via
// FS_IOC_SHUTDOWN. It confirms through the open fd that the path is a real mount boundary (its fs
// differs from its parent's) first, so a racing unmount can't shut down the node-root fs instead.
// ext2/3/4 and xfs support the ioctl; others return ENOTTY/EINVAL and are a no-op.
func shutdownFilesystem(stagingTargetPath string) error {
	// O_DIRECTORY|O_NOFOLLOW also reject a file/symlink swapped in for the staging directory.
	fd, err := unix.Open(stagingTargetPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("failed to open mount %s: %w", stagingTargetPath, err)
	}
	defer unix.Close(fd)

	// Require a mount boundary. Both stats go through the fd (parent via ".."), so nothing is
	// re-resolved from a path and the check cannot race an unmount.
	var self, parent unix.Stat_t
	if err := unix.Fstat(fd, &self); err != nil {
		return fmt.Errorf("failed to stat %s: %w", stagingTargetPath, err)
	}
	if err := unix.Fstatat(fd, "..", &parent, 0); err != nil {
		return fmt.Errorf("failed to stat parent of %s: %w", stagingTargetPath, err)
	}
	if self.Dev == parent.Dev {
		return fmt.Errorf("skipping shutdown of %s: not a mountpoint (%w)", stagingTargetPath, os.ErrNotExist) // the fd is on the parent (node root) fs
	}

	if err := unix.IoctlSetPointerInt(fd, fsIocShutdown, 0); err != nil {
		if errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
			return nil // filesystem does not support shutdown (e.g. btrfs)
		}
		return fmt.Errorf("failed to shut down filesystem at %s: %w", stagingTargetPath, err)
	}
	return nil
}

// nvmePassthruCmd mirrors the NVME_IOCTL_ADMIN_CMD argument of type struct nvme_passthru_cmd.
type nvmePassthruCmd struct {
	opcode      uint8
	flags       uint8
	rsvd1       uint16
	nsid        uint32
	cdw2        uint32
	cdw3        uint32
	metadata    uint64
	addr        uint64
	metadataLen uint32
	dataLen     uint32
	cdw10       uint32
	cdw11       uint32
	cdw12       uint32
	cdw13       uint32
	cdw14       uint32
	cdw15       uint32
	timeoutMs   uint32
	result      uint32
}
