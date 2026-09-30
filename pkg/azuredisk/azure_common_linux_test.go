//go:build linux
// +build linux

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

package azuredisk

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	mount "k8s.io/mount-utils"
	"k8s.io/utils/exec"
	testingexec "k8s.io/utils/exec/testing"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	testmounter "sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
)

func TestFormatAndMountFormatsUnformattedDisk(t *testing.T) {
	fakeSafeMounter, err := testmounter.NewFakeSafeMounter()
	if err != nil {
		t.Fatalf("NewFakeSafeMounter failed: %v", err)
	}

	fakeExec := fakeSafeMounter.Exec.(*testmounter.FakeSafeMounter)
	fakeExec.CommandScript = []testingexec.FakeCommandAction{
		blkidAction(t, "/dev/sdz", testingexec.FakeExitError{Status: 2}, ""),
		// fsck reports a fresh block device (exit status 8), so no filesystem exists yet.
		fsckAction(t, []string{"-n", "/dev/sdz"}, testingexec.FakeExitError{Status: fsckOperationalError}, "fsck.ext4: Superblock could not be read or does not describe a valid ext2/ext3/ext4 filesystem"),
		// wipefs finds no filesystem signature, confirming the disk is unformatted.
		wipefsAction(t, "/dev/sdz", nil, ""),
		mkfsAction(t),
	}

	if err := formatAndMount("/dev/sdz", "/mnt/test", "ext4", nil, fakeSafeMounter, nil, 0); err != nil {
		t.Fatalf("formatAndMount returned error: %v", err)
	}

	if got, want := fakeExec.CommandCalls, 4; got != want {
		t.Fatalf("unexpected command count: got %d, want %d", got, want)
	}
}

// TestFormatAndMountSkipsFormatWhenFsckDetectsFilesystem verifies that when blkid reports no
// filesystem but fsck finds/repairs an existing filesystem, the disk is not reformatted.
func TestFormatAndMountSkipsFormatWhenFsckDetectsFilesystem(t *testing.T) {
	fakeSafeMounter, err := testmounter.NewFakeSafeMounter()
	if err != nil {
		t.Fatalf("NewFakeSafeMounter failed: %v", err)
	}

	fakeExec := fakeSafeMounter.Exec.(*testmounter.FakeSafeMounter)
	fakeExec.CommandScript = []testingexec.FakeCommandAction{
		blkidAction(t, "/dev/sdz", testingexec.FakeExitError{Status: 2}, ""),
		// fsck exits cleanly, meaning a filesystem already exists on the disk.
		fsckAction(t, []string{"-n", "/dev/sdz"}, nil, ""),
		// The existing filesystem is re-read to determine its format instead of reformatting.
		blkidAction(t, "/dev/sdz", nil, "TYPE=ext4\n"),
		// fsck runs once more with "-a" to repair any issues before mounting.
		fsckAction(t, []string{"-a", "/dev/sdz"}, nil, ""),
	}

	if err := formatAndMount("/dev/sdz", "/mnt/test", "ext4", nil, fakeSafeMounter, nil, 0); err != nil {
		t.Fatalf("formatAndMount returned error: %v", err)
	}

	// blkid, fsck, the re-read blkid, and the pre-mount fsck should run; mkfs must be skipped.
	if got, want := fakeExec.CommandCalls, 4; got != want {
		t.Fatalf("unexpected command count: got %d, want %d", got, want)
	}
}

func TestFormatAndMountDoesNotReformatWhenAlreadyFormatted(t *testing.T) {
	fakeSafeMounter, err := testmounter.NewFakeSafeMounter()
	if err != nil {
		t.Fatalf("NewFakeSafeMounter failed: %v", err)
	}

	fakeExec := fakeSafeMounter.Exec.(*testmounter.FakeSafeMounter)
	fakeExec.CommandScript = []testingexec.FakeCommandAction{
		// First call: disk is unformatted.
		blkidAction(t, "/dev/sdz", testingexec.FakeExitError{Status: 2}, ""),
		// First call: fsck reports a fresh block device, so the disk gets formatted.
		fsckAction(t, []string{"-n", "/dev/sdz"}, testingexec.FakeExitError{Status: fsckOperationalError}, "fsck.ext4: Superblock could not be read or does not describe a valid ext2/ext3/ext4 filesystem"),
		// First call: wipefs finds no filesystem signature.
		wipefsAction(t, "/dev/sdz", nil, ""),
		// First call: mkfs succeeds.
		mkfsAction(t),
		// Second call: disk is already ext4, so it should skip the detection fsck/mkfs path.
		blkidAction(t, "/dev/sdz", nil, "TYPE=ext4\n"),
		// Second call: fsck runs with "-a" to repair any issues before mounting.
		fsckAction(t, []string{"-a", "/dev/sdz"}, nil, ""),
	}

	if err := formatAndMount("/dev/sdz", "/mnt/test", "ext4", nil, fakeSafeMounter, nil, 0); err != nil {
		t.Fatalf("first formatAndMount returned error: %v", err)
	}

	if err := formatAndMount("/dev/sdz", "/mnt/test", "ext4", nil, fakeSafeMounter, nil, 0); err != nil {
		t.Fatalf("second formatAndMount returned error: %v", err)
	}

	if got, want := fakeExec.CommandCalls, 6; got != want {
		t.Fatalf("unexpected command count: got %d, want %d", got, want)
	}
}

// TestDetectAndRepairFilesystem verifies detectAndRepairFilesystem's handling of the various fsck
// exit codes using a fake mounter, which lets us deterministically simulate each outcome without
// requiring root privileges or real loopback/block devices.
func TestDetectAndRepairFilesystem(t *testing.T) {
	tests := []struct {
		name        string
		fsckErr     error
		fsckOutput  string
		wantFsExist bool
		wantErr     bool
	}{
		{
			// fsck exits 0 on a healthy ext4/xfs filesystem (xfs check is effectively a no-op).
			name:        "healthy filesystem",
			fsckErr:     nil,
			wantFsExist: true,
			wantErr:     false,
		},
		{
			// fsck exits 8 (operational error) on a fresh block device: its output reports that the
			// superblock could not be read. detectAndRepairFilesystem always surfaces an operational
			// error; interpreting that output as "no filesystem signature" is the caller's job (see
			// TestDetectFilesystemExistence).
			name:        "operational error on a fresh block device",
			fsckErr:     testingexec.FakeExitError{Status: fsckOperationalError},
			fsckOutput:  "fsck.ext4: Superblock could not be read or does not describe a valid ext2/ext3/ext4 filesystem",
			wantFsExist: false,
			wantErr:     true,
		},
		{
			// fsck exits 8 (operational error) without the "no filesystem" signature: we cannot rule
			// out a filesystem, so surface the error instead of assuming a fresh device.
			name:        "operational error with a filesystem present",
			fsckErr:     testingexec.FakeExitError{Status: fsckOperationalError},
			fsckOutput:  "fsck.ext4: unable to set superblock flags",
			wantFsExist: false,
			wantErr:     true,
		},
		{
			name:        "errors corrected by fsck (exit 1)",
			fsckErr:     testingexec.FakeExitError{Status: fsckErrorsCorrected},
			wantFsExist: true,
			wantErr:     false,
		},
		{
			name:        "errors left uncorrected by fsck (exit 4)",
			fsckErr:     testingexec.FakeExitError{Status: fsckErrorsUncorrected},
			wantFsExist: true,
			wantErr:     true,
		},
		{
			name:        "fsck exit status greater than uncorrected (exit 16)",
			fsckErr:     testingexec.FakeExitError{Status: 16},
			wantFsExist: false,
			wantErr:     true,
		},
		{
			// When fsck is unavailable we cannot detect a filesystem, so surface an error rather
			// than making an assumption about the device's contents.
			name:        "fsck binary not found",
			fsckErr:     exec.ErrExecutableNotFound,
			wantFsExist: false,
			wantErr:     true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeSafeMounter, err := testmounter.NewFakeSafeMounter()
			if err != nil {
				t.Fatalf("NewFakeSafeMounter failed: %v", err)
			}

			fakeExec := fakeSafeMounter.Exec.(*testmounter.FakeSafeMounter)
			fakeExec.CommandScript = []testingexec.FakeCommandAction{
				fsckAction(t, []string{"-y", "/dev/sdz"}, tc.fsckErr, tc.fsckOutput),
			}

			isFilesystemExist, err := detectAndRepairFilesystem("/dev/sdz", []string{"-y"}, fakeSafeMounter)
			if (err != nil) != tc.wantErr {
				t.Fatalf("detectAndRepairFilesystem error = %v, wantErr %v", err, tc.wantErr)
			}
			if isFilesystemExist != tc.wantFsExist {
				t.Fatalf("isFilesystemExist = %v, want %v", isFilesystemExist, tc.wantFsExist)
			}
		})
	}
}

// TestDetectFilesystemExistence verifies that detectFilesystemExistence interprets an fsck
// operational error whose output reports an unreadable superblock as a fresh block device and
// falls back to wipefs, while still surfacing other fsck failures.
func TestDetectFilesystemExistence(t *testing.T) {
	const superblockOutput = "fsck.ext4: Superblock could not be read or does not describe a valid ext2/ext3/ext4 filesystem"

	tests := []struct {
		name         string
		fsckErr      error
		fsckOutput   string
		expectWipefs bool
		wipefsErr    error
		wipefsOutput string
		wantFsExist  bool
		wantErr      bool
	}{
		{
			// fsck exits 0, so a filesystem already exists and wipefs is not consulted.
			name:        "fsck reports an existing filesystem",
			fsckErr:     nil,
			wantFsExist: true,
			wantErr:     false,
		},
		{
			// fsck exits 8 with the unreadable-superblock signature: treated as a fresh device, so
			// wipefs is consulted and reports no signature.
			name:         "fresh block device confirmed by wipefs",
			fsckErr:      testingexec.FakeExitError{Status: fsckOperationalError},
			fsckOutput:   superblockOutput,
			expectWipefs: true,
			wipefsErr:    nil,
			wipefsOutput: "",
			wantFsExist:  false,
			wantErr:      false,
		},
		{
			// fsck reports an unreadable superblock but wipefs still finds a filesystem signature.
			name:         "unreadable superblock but wipefs finds a signature",
			fsckErr:      testingexec.FakeExitError{Status: fsckOperationalError},
			fsckOutput:   superblockOutput,
			expectWipefs: true,
			wipefsErr:    nil,
			wipefsOutput: "ext4\n",
			wantFsExist:  true,
			wantErr:      false,
		},
		{
			// fsck fails with an operational error unrelated to a missing superblock: surface it.
			name:        "operational error unrelated to superblock",
			fsckErr:     testingexec.FakeExitError{Status: fsckOperationalError},
			fsckOutput:  "fsck.ext4: unable to set superblock flags",
			wantFsExist: false,
			wantErr:     true,
		},
		{
			// fsck reports a fresh device but wipefs itself fails: surface the wipefs error.
			name:         "wipefs failure is surfaced",
			fsckErr:      testingexec.FakeExitError{Status: fsckOperationalError},
			fsckOutput:   superblockOutput,
			expectWipefs: true,
			wipefsErr:    testingexec.FakeExitError{Status: 1},
			wipefsOutput: "",
			wantFsExist:  false,
			wantErr:      true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeSafeMounter, err := testmounter.NewFakeSafeMounter()
			if err != nil {
				t.Fatalf("NewFakeSafeMounter failed: %v", err)
			}

			fakeExec := fakeSafeMounter.Exec.(*testmounter.FakeSafeMounter)
			script := []testingexec.FakeCommandAction{
				fsckAction(t, []string{"-n", "/dev/sdz"}, tc.fsckErr, tc.fsckOutput),
			}
			if tc.expectWipefs {
				script = append(script, wipefsAction(t, "/dev/sdz", tc.wipefsErr, tc.wipefsOutput))
			}
			fakeExec.CommandScript = script

			isFilesystemExist, err := detectFilesystemExistence("/dev/sdz", fakeSafeMounter)
			if (err != nil) != tc.wantErr {
				t.Fatalf("detectFilesystemExistence error = %v, wantErr %v", err, tc.wantErr)
			}
			if isFilesystemExist != tc.wantFsExist {
				t.Fatalf("isFilesystemExist = %v, want %v", isFilesystemExist, tc.wantFsExist)
			}

			wantCalls := 1
			if tc.expectWipefs {
				wantCalls = 2
			}
			if got := fakeExec.CommandCalls; got != wantCalls {
				t.Fatalf("unexpected command count: got %d, want %d", got, wantCalls)
			}
		})
	}
}

// TestFormatAndMountHonorsConcurrentFormatSemaphore verifies that when a max-concurrent-format
// semaphore is configured, formatAndMount acquires a token before running mkfs and releases it
// afterwards, so subsequent formats are not permanently blocked.
func TestFormatAndMountHonorsConcurrentFormatSemaphore(t *testing.T) {
	fakeSafeMounter, err := testmounter.NewFakeSafeMounter()
	if err != nil {
		t.Fatalf("NewFakeSafeMounter failed: %v", err)
	}

	fakeExec := fakeSafeMounter.Exec.(*testmounter.FakeSafeMounter)
	fakeExec.CommandScript = []testingexec.FakeCommandAction{
		blkidAction(t, "/dev/sdz", testingexec.FakeExitError{Status: 2}, ""),
		fsckAction(t, []string{"-n", "/dev/sdz"}, testingexec.FakeExitError{Status: fsckOperationalError}, "fsck.ext4: Superblock could not be read or does not describe a valid ext2/ext3/ext4 filesystem"),
		wipefsAction(t, "/dev/sdz", nil, ""),
		mkfsAction(t),
		fsckAction(t, []string{"-a", "/dev/sdz"}, nil, ""),
	}

	formatSem := make(chan any, 1)
	if err := formatAndMount("/dev/sdz", "/mnt/test", "ext4", nil, fakeSafeMounter, formatSem, 30*time.Second); err != nil {
		t.Fatalf("formatAndMount returned error: %v", err)
	}

	// The concurrency token must be released after formatting so the semaphore is drained.
	// Release happens in a background goroutine, so poll briefly to avoid a race.
	released := false
	for i := 0; i < 200; i++ {
		if len(formatSem) == 0 {
			released = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !released {
		t.Fatalf("format semaphore not released: got %d tokens held, want 0", len(formatSem))
	}
}

// blkidAction returns a fake command action asserting a GetDiskFormat (blkid) invocation and
// returning the given combined output and error.
func blkidAction(t *testing.T, source string, err error, output string) testingexec.FakeCommandAction {
	return func(cmd string, args ...string) exec.Cmd {
		expectedArgs := []string{"-p", "-s", "TYPE", "-s", "PTTYPE", "-o", "export", source}
		if cmd != "blkid" || !reflect.DeepEqual(args, expectedArgs) {
			t.Fatalf("unexpected blkid command: %s %v", cmd, args)
		}

		fakeCmd := &testingexec.FakeCmd{CombinedOutputScript: []testingexec.FakeAction{
			func() ([]byte, []byte, error) {
				return []byte(output), []byte{}, err
			},
		}}
		return testingexec.InitFakeCmd(fakeCmd, cmd, args...)
	}
}

// fsckAction returns a fake command action asserting an fsck invocation with the expected args and
// returning the given combined output and error.
func fsckAction(t *testing.T, expectedArgs []string, err error, output string) testingexec.FakeCommandAction {
	return func(cmd string, args ...string) exec.Cmd {
		if cmd != "fsck" || !reflect.DeepEqual(args, expectedArgs) {
			t.Fatalf("unexpected fsck command: %s %v", cmd, args)
		}

		fakeCmd := &testingexec.FakeCmd{CombinedOutputScript: []testingexec.FakeAction{
			func() ([]byte, []byte, error) {
				return []byte(output), []byte{}, err
			},
		}}
		return testingexec.InitFakeCmd(fakeCmd, cmd, args...)
	}
}

// wipefsAction returns a fake command action asserting a wipefs --no-act invocation used to detect
// an existing filesystem signature, returning the given combined output and error.
func wipefsAction(t *testing.T, source string, err error, output string) testingexec.FakeCommandAction {
	return func(cmd string, args ...string) exec.Cmd {
		expectedArgs := []string{"--no-act", "--output", "TYPE", "--noheadings", source}
		if cmd != "wipefs" || !reflect.DeepEqual(args, expectedArgs) {
			t.Fatalf("unexpected wipefs command: %s %v", cmd, args)
		}

		fakeCmd := &testingexec.FakeCmd{CombinedOutputScript: []testingexec.FakeAction{
			func() ([]byte, []byte, error) {
				return []byte(output), []byte{}, err
			},
		}}
		return testingexec.InitFakeCmd(fakeCmd, cmd, args...)
	}
}

// mkfsAction returns a fake command action asserting a successful mkfs.ext4 invocation.
func mkfsAction(t *testing.T) testingexec.FakeCommandAction {
	return func(cmd string, args ...string) exec.Cmd {
		expectedArgs := []string{"-F", "-m0", "/dev/sdz"}
		if cmd != "mkfs.ext4" || !reflect.DeepEqual(args, expectedArgs) {
			t.Fatalf("unexpected mkfs command: %s %v", cmd, args)
		}

		fakeCmd := &testingexec.FakeCmd{CombinedOutputScript: []testingexec.FakeAction{
			func() ([]byte, []byte, error) {
				return []byte{}, []byte{}, nil
			},
		}}
		return testingexec.InitFakeCmd(fakeCmd, cmd, args...)
	}
}

func TestRescanAllVolumes(t *testing.T) {
	err := rescanAllVolumes(azureutils.NewOSIOHandler())
	if err != nil {
		t.Errorf("rescanAllVolumes failed with error: %v", err)
	}
}

func TestWholeDiskNameRegexp(t *testing.T) {
	tests := []struct {
		device string
		want   string
	}{
		{"sdc", "sdc"},
		{"sdc1", "sdc"},
		{"sdaa", "sdaa"},
		{"sdaa12", "sdaa"},
		{"nvme0n1", "nvme0n1"},
		{"nvme0n1p1", "nvme0n1"},
		{"nvme12n3", "nvme12n3"},
		{"dm-0", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := wholeDiskNameRegexp.FindString(tc.device); got != tc.want {
			t.Errorf("wholeDiskNameRegexp.FindString(%q) = %q, want %q", tc.device, got, tc.want)
		}
	}
}

func TestShutdownFilesystem(t *testing.T) {
	t.Run("non-mountpoint directory reports os.ErrNotExist", func(t *testing.T) {
		if err := shutdownFilesystem(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("shutdownFilesystem on a non-mountpoint = %v, want an error wrapping os.ErrNotExist", err)
		}
	})

	t.Run("non-existent path reports os.ErrNotExist", func(t *testing.T) {
		if err := shutdownFilesystem(filepath.Join(t.TempDir(), "does-not-exist")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("shutdownFilesystem on a non-existent path = %v, want an error wrapping os.ErrNotExist", err)
		}
	})

	t.Run("symlink to a directory is rejected with ENOTDIR", func(t *testing.T) {
		dir := t.TempDir()
		realDir := filepath.Join(dir, "real")
		if err := os.Mkdir(realDir, 0o755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
		link := filepath.Join(dir, "link")
		if err := os.Symlink(realDir, link); err != nil {
			t.Fatalf("failed to create symlink: %v", err)
		}
		if err := shutdownFilesystem(link); !errors.Is(err, unix.ENOTDIR) {
			t.Fatalf("shutdownFilesystem on a symlink = %v, want an error wrapping unix.ENOTDIR", err)
		}
	})

	t.Run("unsupported filesystem (sysfs) swallows ENOTTY", func(t *testing.T) {
		if err := shutdownFilesystem("/sys"); err != nil {
			t.Fatalf("shutdownFilesystem(/sys) = %v, want nil (sysfs does not support FS_IOC_SHUTDOWN, so ENOTTY must be swallowed)", err)
		}
	})

	t.Run("mountpoint with stubbed ioctl behaves as expected", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			ret     error
			wantErr bool
		}{
			{"success returns nil", nil, false},
			{"ENOTTY is swallowed", unix.ENOTTY, false},
			{"EINVAL is swallowed", unix.EINVAL, false},
			{"EOPNOTSUPP is swallowed", unix.EOPNOTSUPP, false},
			{"other error (EIO) is returned", unix.EIO, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				orig := ioctlSetPointerInt
				t.Cleanup(func() { ioctlSetPointerInt = orig })
				callCount := 0
				ioctlSetPointerInt = func(_ int, req uint, val int) error {
					if req != 0x8004587D || val != 0 {
						t.Errorf("ioctl req = 0x%X, val = 0x%X; want req = FS_IOC_SHUTDOWN (0x8004587D), val = FS_SHUTDOWN_FLAGS_DEFAULT (0x0)", req, val)
					}
					callCount++
					return tc.ret
				}
				err := shutdownFilesystem("/sys")
				if tc.wantErr {
					if !errors.Is(err, tc.ret) {
						t.Fatalf("shutdownFilesystem = %v, want an error wrapping %v", err, tc.ret)
					}
				} else if err != nil {
					t.Fatalf("shutdownFilesystem = %v, want nil", err)
				}
				if callCount != 1 {
					t.Fatalf("ioctlSetPointerInt call count = %d, want 1", callCount)
				}
			})
		}
	})
}

func TestFlushAndInvalidateBlockDevice(t *testing.T) {
	t.Run("calls BLKFLSBUF as expected and returns nil", func(t *testing.T) {
		orig := ioctlSetInt
		t.Cleanup(func() { ioctlSetInt = orig })
		callCount := 0
		ioctlSetInt = func(_ int, req uint, val int) error {
			if req != unix.BLKFLSBUF || val != 0 {
				t.Errorf("ioctl req = 0x%X, val = 0x%X, want req = BLKFLSBUF (0x%X), val = 0x0", req, val, unix.BLKFLSBUF)
			}
			callCount++
			return nil
		}
		if err := flushAndInvalidateBlockDevice("/sys"); err != nil {
			t.Fatalf("flushAndInvalidateBlockDevice = %v, want nil", err)
		}
		if callCount != 1 {
			t.Fatalf("ioctlSetInt call count = %d, want 1", callCount)
		}
	})

	t.Run("regular file returns ENOTTY", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "notablockdevice")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}
		err := flushAndInvalidateBlockDevice(file)
		if err == nil {
			t.Fatalf("flushAndInvalidateBlockDevice on a regular file = nil, want an error")
		}
		if !errors.Is(err, unix.ENOTTY) {
			t.Fatalf("flushAndInvalidateBlockDevice on a regular file = %v, want an error wrapping unix.ENOTTY", err)
		}
	})

	t.Run("non-existent path returns os.ErrNotExist", func(t *testing.T) {
		err := flushAndInvalidateBlockDevice(filepath.Join(t.TempDir(), "does-not-exist"))
		if err == nil {
			t.Fatalf("flushAndInvalidateBlockDevice on a non-existent path = nil, want an error")
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("flushAndInvalidateBlockDevice on a non-existent path = %v, want an error wrapping os.ErrNotExist", err)
		}
	})
}

// recordingIOHandler is an azureutils.IOHandler that records WriteFile calls so tests can assert the
// SCSI device-delete side effect of unmountAndInvalidateDevice.
type recordingIOHandler struct {
	writes   []ioWrite
	writeErr error
}

type ioWrite struct {
	path string
	data string
	perm os.FileMode
}

func (h *recordingIOHandler) WriteFile(filename string, data []byte, perm os.FileMode) error {
	h.writes = append(h.writes, ioWrite{path: filename, data: string(data), perm: perm})
	return h.writeErr
}
func (h *recordingIOHandler) ReadDir(string) ([]os.DirEntry, error) { return nil, nil }
func (h *recordingIOHandler) Readlink(string) (string, error)       { return "", nil }
func (h *recordingIOHandler) ReadFile(string) ([]byte, error)       { return nil, nil }

// installUnmountSeams overrides the unmountAndInvalidateDevice indirection seams (restoring them on
// cleanup) so the destructive sequence can be driven deterministically without real devices or root.
// It records the shutdown/unmount/flush steps, in order, into *events.
func installUnmountSeams(t *testing.T, devicePath string, devErr, cleanupErr, flushErr error, events *[]string) {
	t.Helper()
	origGet := getDeviceNameFromMount
	origShutdown := shutdownFilesystem
	origCleanup := cleanupMountPoint
	origFlush := flushAndInvalidateBlockDevice
	t.Cleanup(func() {
		getDeviceNameFromMount = origGet
		shutdownFilesystem = origShutdown
		cleanupMountPoint = origCleanup
		flushAndInvalidateBlockDevice = origFlush
	})
	getDeviceNameFromMount = func(_ mount.Interface, _ string) (string, int, error) {
		return devicePath, 1, devErr
	}
	shutdownFilesystem = func(string) error {
		*events = append(*events, "shutdown")
		return nil
	}
	cleanupMountPoint = func(_ string, _ mount.Interface, _ bool) error {
		*events = append(*events, "unmount")
		return cleanupErr
	}
	flushAndInvalidateBlockDevice = func(target string) error {
		*events = append(*events, "flush:"+target)
		return flushErr
	}
}

// TestUnmountAndInvalidateDeviceSequence verifies that unmountAndInvalidateDevice
// performs shutdown -> unmount -> flush -> delete as expected for various kinds of inputs.
func TestUnmountAndInvalidateDeviceSequence(t *testing.T) {
	const staging = "/var/lib/kubelet/plugins/kubernetes.io/csi/pv/globalmount"

	newMounter := func(t *testing.T) *mount.SafeFormatAndMount {
		m, err := testmounter.NewFakeSafeMounter()
		if err != nil {
			t.Fatalf("NewFakeSafeMounter failed: %v", err)
		}
		return m
	}

	t.Run("successful cleanup flushes partition+whole-disk and deletes the SCSI device", func(t *testing.T) {
		var events []string
		installUnmountSeams(t, "/dev/sdzz1", nil, nil, nil, &events)
		io := &recordingIOHandler{}

		if err := unmountAndInvalidateDevice(staging, io, newMounter(t)); err != nil {
			t.Fatalf("unmountAndInvalidateDevice returned error: %v", err)
		}
		want := []string{"shutdown", "unmount", "flush:/dev/sdzz1", "flush:/dev/sdzz"}
		if !reflect.DeepEqual(events, want) {
			t.Fatalf("sequence = %v, want %v", events, want)
		}
		if len(io.writes) != 1 {
			t.Fatalf("delete writes = %d, want 1 (%+v)", len(io.writes), io.writes)
		}
		w := io.writes[0]
		if want := "/sys/class/block/sdzz/device/delete"; w.path != want {
			t.Errorf("delete path = %q, want %q", w.path, want)
		}
		if w.data != "1" {
			t.Errorf("delete data = %q, want %q", w.data, "1")
		}
		if w.perm != 0o200 {
			t.Errorf("delete perm = %#o, want 0200", w.perm)
		}
	})

	t.Run("unmount failure prevents flush and delete", func(t *testing.T) {
		var events []string
		unmountErr := errors.New("boom: unmount failed")
		installUnmountSeams(t, "/dev/sdzz1", nil, unmountErr, nil, &events)
		io := &recordingIOHandler{}

		err := unmountAndInvalidateDevice(staging, io, newMounter(t))
		if !errors.Is(err, unmountErr) {
			t.Fatalf("err = %v, want %v", err, unmountErr)
		}
		if want := []string{"shutdown", "unmount"}; !reflect.DeepEqual(events, want) {
			t.Fatalf("sequence = %v, want %v (flush/delete must not run after unmount failure)", events, want)
		}
		if len(io.writes) != 0 {
			t.Fatalf("delete writes = %d, want 0 after unmount failure", len(io.writes))
		}
	})

	t.Run("nvme device is flushed but never deleted", func(t *testing.T) {
		var events []string
		installUnmountSeams(t, "/dev/nvme9n9", nil, nil, nil, &events)
		io := &recordingIOHandler{}

		if err := unmountAndInvalidateDevice(staging, io, newMounter(t)); err != nil {
			t.Fatalf("unmountAndInvalidateDevice returned error: %v", err)
		}
		if want := []string{"shutdown", "unmount", "flush:/dev/nvme9n9"}; !reflect.DeepEqual(events, want) {
			t.Fatalf("sequence = %v, want %v", events, want)
		}
		if len(io.writes) != 0 {
			t.Fatalf("NVMe must not be deleted; got writes %+v", io.writes)
		}
	})

	t.Run("already-unstaged path is idempotent (no device, no flush/delete)", func(t *testing.T) {
		var events []string
		// A failed device lookup (not mounted) yields an empty device -> early return after unmount.
		installUnmountSeams(t, "", errors.New("not mounted"), nil, nil, &events)
		io := &recordingIOHandler{}

		for i := 0; i < 2; i++ { // retry-safe: a second call behaves identically
			if err := unmountAndInvalidateDevice(staging, io, newMounter(t)); err != nil {
				t.Fatalf("call %d returned error: %v", i, err)
			}
		}
		if want := []string{"shutdown", "unmount", "shutdown", "unmount"}; !reflect.DeepEqual(events, want) {
			t.Fatalf("sequence = %v, want %v", events, want)
		}
		if len(io.writes) != 0 {
			t.Fatalf("no device -> no delete; got writes %+v", io.writes)
		}
	})

	t.Run("non-sd/non-nvme device is flushed once and never deleted", func(t *testing.T) {
		var events []string
		// A device-mapper path matches neither sd* nor nvme*, so wholeDiskName is empty: no extra
		// whole-disk flush target is appended, and the SCSI delete is skipped.
		installUnmountSeams(t, "/dev/mapper/test-vol", nil, nil, nil, &events)
		io := &recordingIOHandler{}

		if err := unmountAndInvalidateDevice(staging, io, newMounter(t)); err != nil {
			t.Fatalf("unmountAndInvalidateDevice returned error: %v", err)
		}
		if want := []string{"shutdown", "unmount", "flush:/dev/mapper/test-vol"}; !reflect.DeepEqual(events, want) {
			t.Fatalf("sequence = %v, want %v", events, want)
		}
		if len(io.writes) != 0 {
			t.Fatalf("non-sd/non-nvme must not be deleted; got writes %+v", io.writes)
		}
	})

	t.Run("flush errors do not skip the SCSI device delete", func(t *testing.T) {
		var events []string
		installUnmountSeams(t, "/dev/sdzz1", nil, nil, errors.New("flush boom"), &events)
		io := &recordingIOHandler{}

		if err := unmountAndInvalidateDevice(staging, io, newMounter(t)); err != nil {
			t.Fatalf("unmountAndInvalidateDevice returned error: %v", err)
		}
		// Flushing is best-effort: both targets are still attempted and, crucially, the delete still
		// happens -- a failed flush must never block the removal that prevents stale-cache reuse.
		if want := []string{"shutdown", "unmount", "flush:/dev/sdzz1", "flush:/dev/sdzz"}; !reflect.DeepEqual(events, want) {
			t.Fatalf("sequence = %v, want %v", events, want)
		}
		if len(io.writes) != 1 || io.writes[0].path != "/sys/class/block/sdzz/device/delete" {
			t.Fatalf("delete must still happen despite flush errors; writes = %+v", io.writes)
		}
	})

	t.Run("SCSI device delete failure is swallowed (returns nil)", func(t *testing.T) {
		var events []string
		installUnmountSeams(t, "/dev/sdzz1", nil, nil, nil, &events)
		io := &recordingIOHandler{writeErr: errors.New("permission denied")}

		// A failed SCSI delete is best-effort: it is logged, not returned.
		if err := unmountAndInvalidateDevice(staging, io, newMounter(t)); err != nil {
			t.Fatalf("unmountAndInvalidateDevice returned error: %v, want nil (delete failure is best-effort)", err)
		}
		if len(io.writes) != 1 {
			t.Fatalf("expected one delete attempt, got %+v", io.writes)
		}
	})
}
