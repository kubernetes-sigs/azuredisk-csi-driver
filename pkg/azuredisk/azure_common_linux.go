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
	"strconv"
	"strings"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"golang.org/x/sys/unix"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/volume"
	mount "k8s.io/mount-utils"
	utilexec "k8s.io/utils/exec"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	csiMetrics "sigs.k8s.io/azuredisk-csi-driver/pkg/metrics"
)

const (
	sysClassBlockPath   = "/sys/class/block/"
	sysScsiScanModePath = "/sys/module/scsi_mod/parameters/scan"
	// Azure attaches SCSI data disks to this controller (the GUID is a fixed constant)
	sysScsiDataDiskControllerPath = "/sys/bus/vmbus/devices/f8b3781b-1e82-4818-a1c3-63d806ec15bb"
	// 'fsck' found errors and corrected them
	fsckErrorsCorrected = 1
	// 'fsck' found errors but exited without correcting them
	fsckErrorsUncorrected = 4
	// 'fsck' found operational error, e.g fresh block device, device in-use
	fsckOperationalError = 8
	// FS_IOC_SHUTDOWN = _IOR('X', 125, __u32)
	fsIocShutdown = 0x8004587D
)

var (
	getDeviceNameFromMount = mount.GetDeviceNameFromMount
	cleanupMountPoint      = mount.CleanupMountPoint
	ioctlSetInt            = unix.IoctlSetInt
	ioctlSetPointerInt     = unix.IoctlSetPointerInt
	// wholeDiskNameRegexp captures the whole-disk prefix of a SCSI (sd*) or NVMe (nvme*) device name,
	// dropping any partition suffix (sdc1 -> sdc, nvme0n1p1 -> nvme0n1); empty for anything else.
	wholeDiskNameRegexp = regexp.MustCompile(`^sd[a-z]+|^nvme[0-9]+n[0-9]+`)
)

// scsiHostRescan does nothing on Linux because findDiskByLun scans only the LUN it needs, and only when it's missing.
func scsiHostRescan(_ azureutils.IOHandler, _ *mount.SafeFormatAndMount) {}

// setSCSIScanMode sets the kernel's SCSI scan mode to newMode or manual if newMode is empty or unsupported.
// Manual stops the kernel from scanning for new SCSI disks on its own to avoid inappropriately timed disk removals.
func setSCSIScanMode(io azureutils.IOHandler, newMode string) error {
	newMode = strings.TrimSpace(strings.ToLower(newMode))
	switch newMode {
	case "":
		newMode = "manual"
	case "manual", "async", "sync":
	case "unchanged":
		klog.V(2).Infof("leaving SCSI scan mode unchanged")
		return nil
	default:
		klog.Errorf("interpreting unsupported SCSI scan mode %q as manual (supported values: manual, async, sync, unchanged)", newMode)
		newMode = "manual"
	}
	data, err := io.ReadFile(sysScsiScanModePath)
	if err != nil {
		return fmt.Errorf("failed to read SCSI scan mode from %s: %w", sysScsiScanModePath, err)
	}
	mode := strings.TrimSpace(string(data))
	if mode == newMode {
		klog.V(2).Infof("SCSI scan mode is already %s", mode)
		return nil
	}
	if err := io.WriteFile(sysScsiScanModePath, []byte(newMode), 0644); err != nil {
		return fmt.Errorf("failed to change SCSI scan mode from %q to %q: %w", mode, newMode, err)
	}
	klog.Infof("changed SCSI scan mode from %q to %q", mode, newMode)
	return nil
}

func findDiskByLun(lun int, io azureutils.IOHandler, _ *mount.SafeFormatAndMount) (string, error) {
	// Scanning SCSI hosts on NVMe VM SKUs takes over 2 seconds on average,
	// so avoid it if the node doesn't have a SCSI data disk controller.
	dataDiskControllerEntries, err := io.ReadDir(sysScsiDataDiskControllerPath)
	if errors.Is(err, os.ErrNotExist) {
		byLunPath := fmt.Sprintf("/dev/disk/azure/data/by-lun/%d", lun)
		if _, err := io.Stat(byLunPath); errors.Is(err, os.ErrNotExist) {
			return "", nil
		} else if err != nil {
			return "", fmt.Errorf("failed to check existence of block device %s: %w", byLunPath, err)
		}
		klog.V(2).Infof("found %s for lun %d", byLunPath, lun)
		return byLunPath, nil
	} else if err != nil {
		return "", fmt.Errorf("failed to read SCSI data disk controller: %w", err)
	}

	scsiHostName := ""
	for _, entry := range dataDiskControllerEntries {
		if strings.HasPrefix(entry.Name(), "host") {
			scsiHostName = entry.Name()
			break
		}
	}
	if scsiHostName == "" {
		return "", fmt.Errorf("found no SCSI host for the data disk controller")
	}

	// Azure data disks are at channel 0, target 0 of the controller's SCSI host.
	// Scan only for the LUN that we need to ensure orderly disk additions and removals.
	scsiHostID := strings.TrimPrefix(scsiHostName, "host")
	scsiDevice := filepath.Join(sysScsiDataDiskControllerPath, scsiHostName, "target"+scsiHostID+":0:0", fmt.Sprintf("%s:0:0:%d", scsiHostID, lun))
	_, err = io.Stat(scsiDevice)
	if errors.Is(err, os.ErrNotExist) {
		scanPath := filepath.Join(sysScsiDataDiskControllerPath, scsiHostName, "scsi_host", scsiHostName, "scan")
		if err := io.WriteFile(scanPath, []byte(fmt.Sprintf("0 0 %d", lun)), 0200); err != nil {
			return "", fmt.Errorf("failed to rescan SCSI host %s for lun %d: %w", scsiHostID, lun, err)
		}
		// The kernel creates a new disk's SCSI device entry as part of the scan,
		// so if it didn't appear, return to the caller to allow a later rescan on re-entry.
		if _, err = io.Stat(scsiDevice); errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("failed to check existence of SCSI device entry for lun %d: %w", lun, err)
	}

	// It takes a small amount of time after a SCSI scan for the block device to become ready.
	// As such, we wait up to 180ms in total, in incrementally longer intervals (5ms, 10ms, 15ms, ..., 40ms), for the device to appear
	// before returning to the caller and resorting to its much longer exponential backoff in the order of seconds.
	for i := time.Duration(0); i <= 8; i++ {
		time.Sleep(5 * i * time.Millisecond)
		disks, err := io.ReadDir(filepath.Join(scsiDevice, "block"))
		if errors.Is(err, os.ErrNotExist) || (err == nil && len(disks) == 0) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("failed to retrieve block device name for lun %d from %s: %w", lun, scsiDevice, err)
		}
		devicePath := "/dev/" + disks[0].Name()
		if err := io.CheckOpenable(devicePath); err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ENXIO) {
				continue
			}
			return "", fmt.Errorf("failed to open block device %s for lun %d: %w", devicePath, lun, err)
		}
		klog.V(2).Infof("block device %s for lun %d is ready", devicePath, lun)
		return devicePath, nil
	}
	return "", nil
}

func formatAndMount(source, target, fstype string, options []string, m *mount.SafeFormatAndMount, formatSem chan any, formatTimeout time.Duration) error {
	if newOptions, exists := azureutils.RemoveOptionIfExists(options, "directmount"); exists {
		klog.V(2).Infof("formatAndMount - skip format for %s, old options: %v, new options: %v", target, options, newOptions)
		return mountWithMetrics(source, target, fstype, newOptions, m)
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
		// blkid found no filesystem signature, so fall back to fsck/wipefs detection before formatting.
		csiMetrics.NewCSIMetricContext("blkid_no_filesystem_signature").
			ObserveFormatAndMountWithLabels(true, csiMetrics.FsType, fstype)

		// As part of auto-recovery from mount failures, we run fsck on the disk. If the disk was pulled
		// out or a power loss occurred during a previous fsck run, the primary superblocks may have been
		// corrupted depending on the stage at which fsck was interrupted. We run fsck here to detect and
		// repair such issues before formatting.
		// Note: fsck is a no-op if the disk already has an xfs signature.
		// If filesystem was detected at this stage don't format return the error
		isFilesystemExist, err := detectFilesystemExistence(source, fstype, m)
		if err != nil {
			klog.Errorf("formatAndMount - failed to check existence of filesystem on disk %s with error(%v)", source, err)
			return fmt.Errorf("formatAndMount - failed to check existence of filesystem on disk %s with error(%v)", source, err)
		}

		if !isFilesystemExist {
			if readOnly {
				return fmt.Errorf("formatAndMount - disk %s is not formatted, but mount options specify read-only", source)
			}

			// blkid, fsck and wipefs all report no signature, so formatting is permitted.
			csiMetrics.NewCSIMetricContext("no_filesystem_signature_confirmed").
				ObserveFormatAndMountWithLabels(true, csiMetrics.FsType, fstype)

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
			mkfsMetric := csiMetrics.NewCSIMetricContext("mkfs")
			output, err := mkfsWithConcurrencyLimit(m, formatSem, formatTimeout, fstype, args)
			mkfsMetric.ObserveFormatAndMountWithLabels(err == nil, csiMetrics.FsType, fstype)
			if err != nil {
				klog.Errorf("formatAndMount - failed to format disk %s as type %s with options %v: %v error: %v", source, fstype, args, string(output), err)
				return fmt.Errorf("failed to format disk %s as type %s with options %v: %v error: %v", source, fstype, args, string(output), err)
			}
			// Format was successful
			diskFSFormat = fstype
			newlyFormatted = true
		} else {
			// blkid was empty, but fsck or wipefs found a filesystem; the disk was saved from a reformat.
			csiMetrics.NewCSIMetricContext("filesystem_detected_after_blkid_miss").
				ObserveFormatAndMountWithLabels(true, csiMetrics.FsType, fstype)

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
		csiMetrics.NewCSIMetricContext("filesystem_type_mismatch").
			ObserveFormatAndMountWithLabels(false, csiMetrics.FsType, fstype)
		klog.Errorf("formatAndMount - configured to mount disk %s as %s but current format is %s, things might break", source, fstype, diskFSFormat)
		return fmt.Errorf("configured to mount disk %s as %s but current format is %s, things might break", source, fstype, diskFSFormat)
	}

	// Run fsck on the disk to fix repairable issues, only do this for already formatted volumes requested as read-write.
	if !newlyFormatted && !readOnly {
		err := repairFilesystem(source, fstype, []string{"-a"}, m)
		if err != nil {
			klog.Errorf("formatAndMount - failed to run fsck on disk %s with error: %v", source, err)
			return fmt.Errorf("failed to run fsck on disk %s with error: %v", source, err)
		}
	}

	klog.V(4).Infof("Attempting to mount disk %s in %s format at %s", source, fstype, target)
	options = append(options, "defaults")
	return mountWithMetrics(source, target, fstype, options, m)
}

func mountWithMetrics(source, target, fstype string, options []string, m *mount.SafeFormatAndMount) error {
	mountMetric := csiMetrics.NewCSIMetricContext("mount")
	err := m.Mount(source, target, fstype, options)
	mountMetric.ObserveFormatAndMountWithLabels(err == nil, csiMetrics.FsType, fstype)
	if err != nil {
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

func preparePublishPath(_ string, _ *mount.SafeFormatAndMount) error {
	return nil
}

func CleanupMountPoint(path string, m *mount.SafeFormatAndMount, extensiveCheck bool) error {
	return cleanupMountPoint(path, m, extensiveCheck)
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
// It falls back to wipefs when fsck reports that the superblock is unreadable or invalid.
func detectFilesystemExistence(source, fsType string, mounter *mount.SafeFormatAndMount) (bool, error) {
	klog.Infof("Checking filesystem existence on device %s through 'fsck -n'", source)
	isFSExist, err := detectOrRepairFilesystem(source, fsType, []string{"-n"}, mounter)
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

	klog.Infof("Checking filesystem existence on device %s through 'wipefs'", source)
	args := []string{"--no-act", "--output", "TYPE", "--noheadings", source}
	wipefsMetric := csiMetrics.NewCSIMetricContext("wipefs_check")
	detectedFsType, err := mounter.Exec.Command("wipefs", args...).CombinedOutput()
	wipefsMetric.ObserveFormatAndMountWithLabels(err == nil, csiMetrics.FsType, fsType)
	if err != nil {
		klog.Errorf("detectFilesystemExistence - failed to check existence of filesystem on disk %s through 'wipefs' with error(%v)", source, err)
		return false, fmt.Errorf("detectFilesystemExistence - failed to check existence of filesystem on disk %s through 'wipefs' with error(%v)", source, err)
	}
	if len(strings.TrimSpace(string(detectedFsType))) > 0 {
		return true, nil
	}
	return false, nil
}

func repairFilesystem(source, fsType string, options []string, mounter *mount.SafeFormatAndMount) error {
	filesystemExists, err := detectOrRepairFilesystem(source, fsType, options, mounter)
	if err == nil {
		return nil
	}

	if filesystemExists {
		// The filesystem exists, but fsck could not correct its errors.
		return err
	}

	// Match mount-utils best-effort repair behavior.
	klog.Warningf("Unable to run fsck on disk %s; continuing with mount: %v", source, err)
	return nil
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
func detectOrRepairFilesystem(source, fsType string, fsckOptions []string, mounter *mount.SafeFormatAndMount) (bool, error) {
	klog.V(2).Infof("Checking for issues with fsck on disk: %s with options %v", source, fsckOptions)
	args := append(append([]string(nil), fsckOptions...), source)
	readOnlyDetection := false
	for _, option := range fsckOptions {
		if option == "-n" {
			readOnlyDetection = true
			break
		}
	}

	operation := "fsck_repair"
	if readOnlyDetection {
		operation = "fsck_read_only_check"
	}
	mc := csiMetrics.NewCSIMetricContext(operation)
	outcome := "clean"
	success := true
	defer func() {
		mc.ObserveFormatAndMountWithLabels(success, csiMetrics.FsType, fsType, csiMetrics.FsckOutcome, outcome)
	}()

	out, err := mounter.Exec.Command("fsck", args...).CombinedOutput()
	if err != nil {
		ee, isExitError := err.(utilexec.ExitError)
		switch {
		case err == utilexec.ErrExecutableNotFound:
			outcome, success = "not_found", false
			klog.Errorf("'fsck' not found on system; cannot verify filesystem signature on %s, returning error.", source)
			return false, fmt.Errorf("'fsck' not found to detect filesystem on device %s with options %v: %v", source, fsckOptions, err)
		case isExitError && ee.ExitStatus() >= 0 && ee.ExitStatus()&fsckOperationalError != 0:
			outcome, success = "operational_error", false
			klog.Errorf("Unable to run fsck on device %s with options %v, fsck output: %s", source, fsckOptions, string(out))
			return false, fmt.Errorf("Unable to run fsck on device %s with options %v, fsck error: %v output: %s", source, fsckOptions, err, string(out))
		case isExitError && ee.ExitStatus() >= 0 && ee.ExitStatus()&fsckErrorsUncorrected != 0:
			// Filesystem exists but fsck found errors that it could not correct
			outcome, success = "errors_uncorrected", false
			klog.Errorf("Device %s has errors which fsck could not correct with options %v: %s", source, fsckOptions, string(out))
			return true, fmt.Errorf("'fsck' found errors on device %s with options %v but could not correct them output: %s (exit status %d)", source, fsckOptions, string(out), ee.ExitStatus())
		case isExitError && ee.ExitStatus() >= 0 && ee.ExitStatus()&fsckErrorsCorrected != 0:
			outcome, success = "errors_corrected", true
			klog.Warningf("Device %s has errors which were corrected by fsck: %s", source, string(out))
		case isExitError && ee.ExitStatus() > fsckErrorsUncorrected:
			outcome, success = "fatal_error", false
			klog.Errorf("`fsck` error %s", string(out))
			return false, fmt.Errorf("'fsck' failed on device %s with options %v: %v", source, fsckOptions, err)
		default:
			success = true
			if isExitError {
				success = ee.ExitStatus() >= 0
				outcome = fmt.Sprintf("unknown_exit_%d", ee.ExitStatus())
				klog.Warningf("fsck on device %s failed with unknown exit status %d, output: %v", source, ee.ExitStatus(), string(out))
			} else {
				outcome = "unknown"
				klog.Warningf("fsck on device %s failed with error %v, output: %v", source, err, string(out))
			}
		}
	}
	klog.Infof("fsck on device %s completed with output: %s", source, string(out))
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

// unmountAndInvalidateDevice shuts down the fs, unmounts the staging path and invalidates the backing device's caches
// so any pending changes are flushed, mitigating cache inconsistencies especially with SCSI devices.
// The fs shutdown and device cache invalidation are best-effort; only an unmount error is returned.
func unmountAndInvalidateDevice(stagingTargetPath string, io azureutils.IOHandler, m *mount.SafeFormatAndMount) error {
	// Resolve the underlying block device while still mounted.
	devicePath, _, err := getDeviceNameFromMount(m, stagingTargetPath)
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
	if err := cleanupMountPoint(stagingTargetPath, m, true /*extensiveMountPointCheck*/); err != nil {
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
	// equivalent and will be handled by an identity check in NodeStageVolume instead (ToDo).
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
var flushAndInvalidateBlockDevice = func(devicePath string) error {
	fd, err := unix.Open(devicePath, unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("failed to open device %s: %w", devicePath, err)
	}
	defer unix.Close(fd)
	if err := ioctlSetInt(fd, unix.BLKFLSBUF, 0); err != nil {
		return fmt.Errorf("failed to flush buffers on device %s: %w", devicePath, err)
	}
	return nil
}

// shutdownFilesystem forces an orderly (flush-first) shutdown of the mount at stagingTargetPath via
// FS_IOC_SHUTDOWN. It confirms through the open fd that the path is a real mount boundary (its fs
// differs from its parent's) first, so a racing unmount can't shut down the node-root fs instead.
// ext2/3/4 and xfs support the ioctl; others return ENOTTY/EINVAL and are a no-op.
var shutdownFilesystem = func(stagingTargetPath string) error {
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

	if err := ioctlSetPointerInt(fd, fsIocShutdown, 0); err != nil {
		if errors.Is(err, unix.ENOTTY) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
			return nil // filesystem does not support shutdown (e.g. btrfs)
		}
		return fmt.Errorf("failed to shut down filesystem at %s: %w", stagingTargetPath, err)
	}
	return nil
}
