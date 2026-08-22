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
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"k8s.io/utils/exec"
	testingexec "k8s.io/utils/exec/testing"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	testmounter "sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
)

func TestSyncFilesystemAtMountPoint(t *testing.T) {
	fakeMounter, err := testmounter.NewFakeSafeMounter()
	if err != nil {
		t.Fatalf("NewFakeSafeMounter failed: %v", err)
	}

	newMountPoint := func(t *testing.T) string {
		t.Helper()
		mountPoint := filepath.Join(t.TempDir(), "false_is_likely_mount")
		if err := os.Mkdir(mountPoint, 0755); err != nil {
			t.Fatalf("Mkdir() error = %v", err)
		}
		return mountPoint
	}

	t.Run("mounted filesystem", func(t *testing.T) {
		originalOpenMountPoint := openMountPoint
		originalfsync := fsync
		t.Cleanup(func() {
			openMountPoint = originalOpenMountPoint
			fsync = originalfsync
		})

		openMountPoint = func(path string, flags int, perm uint32) (int, error) {
			if flags&unix.O_PATH != 0 {
				t.Errorf("openMountPoint() flags contain O_PATH: %#x", flags)
			}
			if flags&unix.O_DIRECTORY == 0 {
				t.Errorf("openMountPoint() flags do not contain O_DIRECTORY: %#x", flags)
			}
			return unix.Open(path, flags, perm)
		}

		called := false
		fsync = func(_ int) error {
			called = true
			return nil
		}

		if err := syncFilesystemAtMountPoint(newMountPoint(t), fakeMounter); err != nil {
			t.Fatalf("syncFilesystemAtMountPoint() error = %v", err)
		}
		if !called {
			t.Error("syncFilesystemAtMountPoint() did not call fsync")
		}
	})

	t.Run("sync failure", func(t *testing.T) {
		originalfsync := fsync
		t.Cleanup(func() {
			fsync = originalfsync
		})

		syncErr := syscall.EIO
		fsync = func(_ int) error {
			return syncErr
		}

		err := syncFilesystemAtMountPoint(newMountPoint(t), fakeMounter)
		if !errors.Is(err, syncErr) {
			t.Fatalf("syncFilesystemAtMountPoint() error = %v, want %v", err, syncErr)
		}
	})

	t.Run("existing non-mount path", func(t *testing.T) {
		if err := syncFilesystemAtMountPoint(t.TempDir(), fakeMounter); err != nil {
			t.Fatalf("syncFilesystemAtMountPoint() error = %v", err)
		}
	})

	t.Run("missing mount point", func(t *testing.T) {
		mountPoint := filepath.Join(t.TempDir(), "missing")
		if err := syncFilesystemAtMountPoint(mountPoint, fakeMounter); err != nil {
			t.Fatalf("syncFilesystemAtMountPoint() error = %v", err)
		}
	})
}

func TestShouldSyncFilesystem(t *testing.T) {
	fakeMounter, err := testmounter.NewFakeSafeMounter()
	if err != nil {
		t.Fatalf("NewFakeSafeMounter failed: %v", err)
	}

	tests := []struct {
		name       string
		mountPoint string
		wantSync   bool
		wantErr    bool
	}{
		{
			name:       "healthy mount point",
			mountPoint: "/mnt/valid_mount",
			wantSync:   true,
		},
		{
			name:       "existing non-mount path",
			mountPoint: "/mnt/non-mount",
		},
		{
			name:       "corrupted mount point",
			mountPoint: "/mnt/corrupted",
		},
		{
			name:       "unexpected mount check error",
			mountPoint: "/mnt/error_is_likely",
			wantErr:    true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotSync, err := shouldSyncFilesystem(test.mountPoint, fakeMounter)
			if (err != nil) != test.wantErr {
				t.Fatalf("shouldSyncFilesystem() error = %v, wantErr %t", err, test.wantErr)
			}
			if gotSync != test.wantSync {
				t.Errorf("shouldSyncFilesystem() = %t, want %t", gotSync, test.wantSync)
			}
		})
	}
}

func TestJournalEntryGlob(t *testing.T) {
	tests := []struct {
		name       string
		fsType     string
		deviceName string
		want       string
	}{
		{
			name:       "ext4 journal",
			fsType:     "ext4",
			deviceName: "nvme0n2",
			want:       "/proc/fs/jbd2/nvme0n2-*",
		},
		{
			name:       "xfs has no jbd2 journal",
			fsType:     "xfs",
			deviceName: "nvme0n2",
		},
		{
			name:       "unknown filesystem has no jbd2 journal",
			deviceName: "nvme0n2",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := journalEntryGlob(test.fsType, test.deviceName); got != test.want {
				t.Errorf("journalEntryGlob() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFilesystemInstanceName(t *testing.T) {
	t.Run("uses device name for xfs", func(t *testing.T) {
		got, err := filesystemInstanceName("/dev/nvme0n2", "xfs", nil)
		if err != nil {
			t.Fatalf("filesystemInstanceName() error = %v", err)
		}
		if got != "nvme0n2" {
			t.Errorf("filesystemInstanceName() = %q, want %q", got, "nvme0n2")
		}
	})

	t.Run("uses btrfs FSID", func(t *testing.T) {
		fakeMounter, err := testmounter.NewFakeSafeMounter()
		if err != nil {
			t.Fatalf("NewFakeSafeMounter failed: %v", err)
		}
		const fsID = "d21d0da4-6af5-4b0f-9d43-91a60c72b6f7"
		fakeExec := fakeMounter.Exec.(*testmounter.FakeSafeMounter)
		fakeExec.CommandScript = []testingexec.FakeCommandAction{
			func(cmd string, args ...string) exec.Cmd {
				expectedArgs := []string{"-s", "UUID", "-o", "value", "/dev/sdc"}
				if cmd != "blkid" || !reflect.DeepEqual(args, expectedArgs) {
					t.Fatalf("unexpected blkid command: %s %v", cmd, args)
				}
				fakeCmd := &testingexec.FakeCmd{OutputScript: []testingexec.FakeAction{
					func() ([]byte, []byte, error) {
						return []byte(fsID + "\n"), nil, nil
					},
				}}
				return testingexec.InitFakeCmd(fakeCmd, cmd, args...)
			},
		}

		got, err := filesystemInstanceName("/dev/sdc", "btrfs", fakeMounter)
		if err != nil {
			t.Fatalf("filesystemInstanceName() error = %v", err)
		}
		if got != fsID {
			t.Errorf("filesystemInstanceName() = %q, want %q", got, fsID)
		}
	})
}

func TestWaitFSShutdownWithRoots(t *testing.T) {
	t.Run("collects filesystem and journal errors before diagnostics", func(t *testing.T) {
		root := t.TempDir()
		sysFSRoot := filepath.Join(root, "sys", "fs")
		jbd2Root := filepath.Join(root, "proc", "fs", "jbd2")
		sysFSEntry := filepath.Join(sysFSRoot, "ext4", "test-device")
		journalEntry := filepath.Join(jbd2Root, "test-device-8")
		for _, path := range []string{sysFSEntry, journalEntry} {
			if err := os.MkdirAll(path, 0755); err != nil {
				t.Fatalf("MkdirAll(%q) error = %v", path, err)
			}
		}

		errorMessages := waitFSShutdownWithRoots(
			"/dev/test-device",
			"ext4",
			"test-device",
			0,
			sysFSRoot,
			jbd2Root)
		if len(errorMessages) != 2 {
			t.Fatalf("waitFSShutdownWithRoots() returned %d errors, want 2: %v", len(errorMessages), errorMessages)
		}
	})

	t.Run("skips diagnostics after successful shutdown", func(t *testing.T) {
		diagnosticCalled := false
		errorMessages := waitFSShutdownWithRoots(
			"/dev/test-device",
			"xfs",
			"test-device",
			0,
			filepath.Join(t.TempDir(), "sys", "fs"),
			filepath.Join(t.TempDir(), "proc", "fs", "jbd2"),
		)
		if len(errorMessages) != 0 {
			t.Fatalf("waitFSShutdownWithRoots() returned errors: %v", errorMessages)
		}
		if diagnosticCalled {
			t.Error("waitFSShutdownWithRoots() ran diagnostics after successful shutdown")
		}
	})

	t.Run("waits for btrfs FSID entry", func(t *testing.T) {
		root := t.TempDir()
		sysFSRoot := filepath.Join(root, "sys", "fs")
		fsID := "d21d0da4-6af5-4b0f-9d43-91a60c72b6f7"
		if err := os.MkdirAll(filepath.Join(sysFSRoot, "btrfs", fsID), 0755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}

		errorMessages := waitFSShutdownWithRoots(
			"/dev/test-device",
			"btrfs",
			fsID,
			0,
			sysFSRoot,
			filepath.Join(root, "proc", "fs", "jbd2"),
		)
		if len(errorMessages) != 1 {
			t.Fatalf("waitFSShutdownWithRoots() returned %d errors, want 1: %v", len(errorMessages), errorMessages)
		}
	})
}

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
		// fsck runs once more with "-a" to repair any issues before mounting.
		fsckAction(t, []string{"-a", "/dev/sdz"}, nil, ""),
	}

	if err := formatAndMount("/dev/sdz", "/mnt/test", "ext4", nil, fakeSafeMounter, nil, 0); err != nil {
		t.Fatalf("formatAndMount returned error: %v", err)
	}

	if got, want := fakeExec.CommandCalls, 5; got != want {
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
		// First call: fsck runs with "-a" to repair any issues before mounting.
		fsckAction(t, []string{"-a", "/dev/sdz"}, nil, ""),
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

	if got, want := fakeExec.CommandCalls, 7; got != want {
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
