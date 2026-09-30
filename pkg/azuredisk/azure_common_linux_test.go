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
	"reflect"
	"testing"
	"time"

	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/utils/exec"
	testingexec "k8s.io/utils/exec/testing"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	csiMetrics "sigs.k8s.io/azuredisk-csi-driver/pkg/metrics"
	testmounter "sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
)

const unreadableSuperblockError = "fsck.ext4: Superblock could not be read or does not describe a valid ext2/ext3/ext4 filesystem"

type fsckExitError struct {
	status  int
	message string
}

func (e fsckExitError) String() string  { return e.message }
func (e fsckExitError) Error() string   { return e.message }
func (e fsckExitError) Exited() bool    { return true }
func (e fsckExitError) ExitStatus() int { return e.status }

func TestFormatAndMountFormatsUnformattedDisk(t *testing.T) {
	fakeSafeMounter, err := testmounter.NewFakeSafeMounter()
	if err != nil {
		t.Fatalf("NewFakeSafeMounter failed: %v", err)
	}
	operations := []string{
		"blkid_no_filesystem_signature",
		"no_filesystem_signature_confirmed",
		"mkfs",
		"mount",
	}
	countsBefore := make(map[string]float64, len(operations))
	for _, operation := range operations {
		countsBefore[operation] = getFormatAndMountOperationCount(t, operation, "true", "ext4", "")
	}
	detectedAfterBlkidMissBefore := getFormatAndMountOperationCount(t, "filesystem_detected_after_blkid_miss", "true", "ext4", "")

	fakeExec := fakeSafeMounter.Exec.(*testmounter.FakeSafeMounter)
	fakeExec.CommandScript = []testingexec.FakeCommandAction{
		blkidAction(t, "/dev/sdz", testingexec.FakeExitError{Status: 2}, ""),
		// fsck reports a fresh block device (exit status 8), so no filesystem exists yet.
		fsckAction(t, []string{"-n", "/dev/sdz"}, fsckExitError{status: fsckOperationalError, message: unreadableSuperblockError}, unreadableSuperblockError),
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
	for _, operation := range operations {
		countAfter := getFormatAndMountOperationCount(t, operation, "true", "ext4", "")
		if got := countAfter - countsBefore[operation]; got != 1 {
			t.Errorf("%s metric increment = %v, want 1", operation, got)
		}
	}
	detectedAfterBlkidMissAfter := getFormatAndMountOperationCount(t, "filesystem_detected_after_blkid_miss", "true", "ext4", "")
	if got, want := detectedAfterBlkidMissAfter-detectedAfterBlkidMissBefore, float64(0); got != want {
		t.Fatalf("unexpected filesystem-detected-after-blkid-miss metric delta: got %v, want %v", got, want)
	}
}

// TestFormatAndMountSkipsFormatWhenFsckDetectsFilesystem verifies that when blkid reports no
// filesystem but fsck finds/repairs an existing filesystem, the disk is not reformatted.
func TestFormatAndMountSkipsFormatWhenFsckDetectsFilesystem(t *testing.T) {
	fakeSafeMounter, err := testmounter.NewFakeSafeMounter()
	if err != nil {
		t.Fatalf("NewFakeSafeMounter failed: %v", err)
	}
	before := getFormatAndMountOperationCount(t, "filesystem_detected_after_blkid_miss", "true", "ext4", "")

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
	after := getFormatAndMountOperationCount(t, "filesystem_detected_after_blkid_miss", "true", "ext4", "")
	if got, want := after-before, float64(1); got != want {
		t.Fatalf("unexpected filesystem-detected-after-blkid-miss metric delta: got %v, want %v", got, want)
	}
}

func TestFormatAndMountMountMetrics(t *testing.T) {
	tests := []struct {
		name      string
		direct    bool
		target    string
		wantError bool
	}{
		{
			name:   "normal mount succeeds",
			target: "/mnt/test",
		},
		{
			name:      "normal mount fails",
			target:    "/mnt/error_mount",
			wantError: true,
		},
		{
			name:   "direct mount succeeds",
			direct: true,
			target: "/mnt/test",
		},
		{
			name:      "direct mount fails",
			direct:    true,
			target:    "/mnt/error_mount",
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fakeSafeMounter, err := testmounter.NewFakeSafeMounter()
			if err != nil {
				t.Fatalf("NewFakeSafeMounter failed: %v", err)
			}

			options := []string(nil)
			if test.direct {
				options = []string{"directmount"}
			} else {
				fakeExec := fakeSafeMounter.Exec.(*testmounter.FakeSafeMounter)
				fakeExec.CommandScript = []testingexec.FakeCommandAction{
					blkidAction(t, "/dev/sdz", nil, "TYPE=ext4\n"),
					fsckAction(t, []string{"-a", "/dev/sdz"}, nil, ""),
				}
			}

			success := "true"
			if test.wantError {
				success = "false"
			}
			before := getFormatAndMountOperationCount(t, "mount", success, "ext4", "")

			err = formatAndMount("/dev/sdz", test.target, "ext4", options, fakeSafeMounter, nil, 0)
			if test.wantError && err == nil {
				t.Fatal("formatAndMount returned nil error, want mount error")
			}
			if !test.wantError && err != nil {
				t.Fatalf("formatAndMount returned error: %v", err)
			}

			after := getFormatAndMountOperationCount(t, "mount", success, "ext4", "")
			if got, want := after-before, float64(1); got != want {
				t.Fatalf("unexpected mount metric delta: got %v, want %v", got, want)
			}
		})
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
		fsckAction(t, []string{"-n", "/dev/sdz"}, fsckExitError{status: fsckOperationalError, message: unreadableSuperblockError}, unreadableSuperblockError),
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

// TestDetectOrRepairFilesystem verifies detectOrRepairFilesystem's handling of the various fsck
// exit codes using a fake mounter, which lets us deterministically simulate each outcome without
// requiring root privileges or real loopback/block devices.
func TestDetectOrRepairFilesystem(t *testing.T) {
	tests := []struct {
		name        string
		fsckOptions []string
		fsckErr     error
		fsckOutput  string
		wantFsExist bool
		wantErr     bool
		wantSuccess string
		wantOutcome string
	}{
		{
			// fsck exits 0 on a healthy ext4/xfs filesystem (xfs check is effectively a no-op).
			name:        "healthy filesystem",
			fsckErr:     nil,
			wantFsExist: true,
			wantErr:     false,
			wantSuccess: "true",
			wantOutcome: "clean",
		},
		{
			// fsck exits 8 (operational error) on a fresh block device: its output reports that the
			// superblock could not be read. detectOrRepairFilesystem always surfaces an operational
			// error; interpreting that output as "no filesystem signature" is the caller's job (see
			// TestDetectFilesystemExistence).
			name:        "operational error on a fresh block device",
			fsckOptions: []string{"-n"},
			fsckErr:     testingexec.FakeExitError{Status: fsckOperationalError},
			fsckOutput:  "fsck.ext4: Superblock could not be read or does not describe a valid ext2/ext3/ext4 filesystem",
			wantFsExist: false,
			wantErr:     true,
			wantSuccess: "false",
			wantOutcome: "operational_error",
		},
		{
			// During repair, the same output aborts the mount and must be reported as a failure.
			name:        "unreadable superblock during repair",
			fsckOptions: []string{"-a"},
			fsckErr:     testingexec.FakeExitError{Status: fsckOperationalError},
			fsckOutput:  "fsck.ext4: Superblock could not be read or does not describe a valid ext2/ext3/ext4 filesystem",
			wantFsExist: false,
			wantErr:     true,
			wantSuccess: "false",
			wantOutcome: "operational_error",
		},
		{
			// fsck exits 8 (operational error) without the "no filesystem" signature: we cannot rule
			// out a filesystem, so surface the error instead of assuming a fresh device.
			name:        "operational error with a filesystem present",
			fsckErr:     testingexec.FakeExitError{Status: fsckOperationalError},
			fsckOutput:  "fsck.ext4: unable to set superblock flags",
			wantFsExist: false,
			wantErr:     true,
			wantSuccess: "false",
			wantOutcome: "operational_error",
		},
		{
			name:        "errors corrected by fsck (exit 1)",
			fsckOptions: []string{"-a"},
			fsckErr:     testingexec.FakeExitError{Status: fsckErrorsCorrected},
			wantFsExist: true,
			wantErr:     false,
			wantSuccess: "true",
			wantOutcome: "errors_corrected",
		},
		{
			name:        "unknown fsck exit error",
			fsckErr:     testingexec.FakeExitError{Status: 0},
			wantFsExist: true,
			wantErr:     false,
			wantSuccess: "true",
			wantOutcome: "unknown_exit_0",
		},
		{
			name:        "unknown fsck execution error",
			fsckErr:     errors.New("unknown execution error"),
			wantFsExist: true,
			wantErr:     false,
			wantSuccess: "true",
			wantOutcome: "unknown",
		},
		{
			name:        "errors left uncorrected by fsck (exit 4)",
			fsckOptions: []string{"-a"},
			fsckErr:     testingexec.FakeExitError{Status: fsckErrorsUncorrected},
			wantFsExist: true,
			wantErr:     true,
			wantSuccess: "false",
			wantOutcome: "errors_uncorrected",
		},
		{
			name:        "fsck exit status greater than uncorrected (exit 16)",
			fsckErr:     testingexec.FakeExitError{Status: 16},
			wantFsExist: false,
			wantErr:     true,
			wantSuccess: "false",
			wantOutcome: "fatal_error",
		},
		{
			// When fsck is unavailable we cannot detect a filesystem, so surface an error rather
			// than making an assumption about the device's contents.
			name:        "fsck binary not found",
			fsckErr:     exec.ErrExecutableNotFound,
			wantFsExist: false,
			wantErr:     true,
			wantSuccess: "false",
			wantOutcome: "not_found",
		},
		{
			name:        "repair mode surfaces operational error",
			fsckOptions: []string{"-a"},
			fsckErr:     testingexec.FakeExitError{Status: fsckOperationalError},
			fsckOutput:  "fsck.ext4: unable to continue",
			wantFsExist: false,
			wantErr:     true,
			wantSuccess: "false",
			wantOutcome: "operational_error",
		},
		{
			name:        "repair mode surfaces exit status greater than uncorrected",
			fsckOptions: []string{"-a"},
			fsckErr:     testingexec.FakeExitError{Status: 16},
			fsckOutput:  "fsck.ext4: usage error",
			wantFsExist: false,
			wantErr:     true,
			wantSuccess: "false",
			wantOutcome: "fatal_error",
		},
		{
			name:        "repair mode surfaces missing fsck binary",
			fsckOptions: []string{"-a"},
			fsckErr:     exec.ErrExecutableNotFound,
			wantFsExist: false,
			wantErr:     true,
			wantSuccess: "false",
			wantOutcome: "not_found",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fakeSafeMounter, err := testmounter.NewFakeSafeMounter()
			if err != nil {
				t.Fatalf("NewFakeSafeMounter failed: %v", err)
			}

			fsckOptions := tc.fsckOptions
			if len(fsckOptions) == 0 {
				fsckOptions = []string{"-n"}
			}
			fakeExec := fakeSafeMounter.Exec.(*testmounter.FakeSafeMounter)
			fakeExec.CommandScript = []testingexec.FakeCommandAction{
				fsckAction(t, append(append([]string(nil), fsckOptions...), "/dev/sdz"), tc.fsckErr, tc.fsckOutput),
			}

			wantOperation := "fsck_repair"
			if reflect.DeepEqual(fsckOptions, []string{"-n"}) {
				wantOperation = "fsck_read_only_check"
			}
			countBefore := getFormatAndMountOperationCount(t, wantOperation, tc.wantSuccess, "ext4", tc.wantOutcome)

			isFilesystemExist, err := detectOrRepairFilesystem("/dev/sdz", "ext4", fsckOptions, fakeSafeMounter)
			if (err != nil) != tc.wantErr {
				t.Fatalf("detectOrRepairFilesystem error = %v, wantErr %v", err, tc.wantErr)
			}
			if isFilesystemExist != tc.wantFsExist {
				t.Fatalf("isFilesystemExist = %v, want %v", isFilesystemExist, tc.wantFsExist)
			}

			countAfter := getFormatAndMountOperationCount(t, wantOperation, tc.wantSuccess, "ext4", tc.wantOutcome)
			if got := countAfter - countBefore; got != 1 {
				t.Errorf("format_and_mount_operations_total increment = %v, want 1", got)
			}
		})
	}
}

func TestRepairFilesystem(t *testing.T) {
	tests := []struct {
		name       string
		fsckErr    error
		fsckOutput string
		wantErr    bool
	}{
		{
			name: "healthy filesystem",
		},
		{
			name:    "errors corrected by fsck",
			fsckErr: testingexec.FakeExitError{Status: fsckErrorsCorrected},
		},
		{
			name:       "errors left uncorrected by fsck",
			fsckErr:    testingexec.FakeExitError{Status: fsckErrorsUncorrected},
			fsckOutput: "filesystem errors remain",
			wantErr:    true,
		},
		{
			name:       "operational error is ignored",
			fsckErr:    testingexec.FakeExitError{Status: fsckOperationalError},
			fsckOutput: "fsck.ext4: unable to continue",
		},
		{
			name:       "exit status greater than uncorrected is ignored",
			fsckErr:    testingexec.FakeExitError{Status: 16},
			fsckOutput: "fsck.ext4: usage error",
		},
		{
			name:    "missing fsck binary is ignored",
			fsckErr: exec.ErrExecutableNotFound,
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
				fsckAction(t, []string{"-a", "/dev/sdz"}, tc.fsckErr, tc.fsckOutput),
			}

			err = repairFilesystem("/dev/sdz", "ext4", []string{"-a"}, fakeSafeMounter)
			if (err != nil) != tc.wantErr {
				t.Fatalf("repairFilesystem error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

// TestDetectFilesystemExistence verifies that detectFilesystemExistence falls back to wipefs when
// fsck reports an unreadable superblock.
func TestDetectFilesystemExistence(t *testing.T) {
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
			fsckErr:      fsckExitError{status: fsckOperationalError, message: unreadableSuperblockError},
			fsckOutput:   unreadableSuperblockError,
			expectWipefs: true,
			wipefsErr:    nil,
			wipefsOutput: "",
			wantFsExist:  false,
			wantErr:      false,
		},
		{
			// fsck reports an unreadable superblock but wipefs still finds a filesystem signature.
			name:         "unreadable superblock but wipefs finds a signature",
			fsckErr:      fsckExitError{status: fsckOperationalError, message: unreadableSuperblockError},
			fsckOutput:   unreadableSuperblockError,
			expectWipefs: true,
			wipefsErr:    nil,
			wipefsOutput: "ext4\n",
			wantFsExist:  true,
			wantErr:      false,
		},
		{
			// Operational errors without the unreadable-superblock signature are surfaced because
			// filesystem detection is inconclusive.
			name:        "operational error unrelated to superblock",
			fsckErr:     testingexec.FakeExitError{Status: fsckOperationalError},
			fsckOutput:  "fsck.ext4: unable to set superblock flags",
			wantFsExist: false,
			wantErr:     true,
		},
		{
			// fsck reports a fresh device but wipefs itself fails: surface the wipefs error.
			name:         "wipefs failure is surfaced",
			fsckErr:      fsckExitError{status: fsckOperationalError, message: unreadableSuperblockError},
			fsckOutput:   unreadableSuperblockError,
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

			wantWipefsSuccess := "true"
			if tc.wipefsErr != nil {
				wantWipefsSuccess = "false"
			}
			wipefsCountBefore := 0.0
			if tc.expectWipefs {
				wipefsCountBefore = getFormatAndMountOperationCount(t, "wipefs_check", wantWipefsSuccess, "ext4", "")
			}

			isFilesystemExist, err := detectFilesystemExistence("/dev/sdz", "ext4", fakeSafeMounter)
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

			if tc.expectWipefs {
				wipefsCountAfter := getFormatAndMountOperationCount(t, "wipefs_check", wantWipefsSuccess, "ext4", "")
				if got := wipefsCountAfter - wipefsCountBefore; got != 1 {
					t.Errorf("wipefs_check counter increment = %v, want 1", got)
				}
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
		fsckAction(t, []string{"-n", "/dev/sdz"}, fsckExitError{status: fsckOperationalError, message: unreadableSuperblockError}, unreadableSuperblockError),
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

func getFormatAndMountOperationCount(t *testing.T, operation, success, fsType, fsckOutcome string) float64 {
	t.Helper()

	families, err := legacyregistry.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("failed to gather metrics: %v", err)
	}

	for _, family := range families {
		if family.GetName() != "azuredisk_csi_driver_format_and_mount_operations_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, label := range metric.GetLabel() {
				labels[label.GetName()] = label.GetValue()
			}
			if labels["operation"] == operation &&
				labels["success"] == success &&
				labels[csiMetrics.FsType] == fsType &&
				labels[csiMetrics.FsckOutcome] == fsckOutcome {
				return metric.GetCounter().GetValue()
			}
		}
	}

	return 0
}

func TestRescanAllVolumes(t *testing.T) {
	err := rescanAllVolumes(azureutils.NewOSIOHandler())
	if err != nil {
		t.Errorf("rescanAllVolumes failed with error: %v", err)
	}
}
