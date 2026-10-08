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

	"github.com/google/uuid"
	"k8s.io/utils/exec"
	testingexec "k8s.io/utils/exec/testing"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
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
			// superblock could not be read. detectOrRepairFilesystem always surfaces an operational
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
			fsckOptions: []string{"-a"},
			fsckErr:     testingexec.FakeExitError{Status: fsckErrorsCorrected},
			wantFsExist: true,
			wantErr:     false,
		},
		{
			name:        "errors left uncorrected by fsck (exit 4)",
			fsckOptions: []string{"-a"},
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
		{
			name:        "repair mode surfaces operational error",
			fsckOptions: []string{"-a"},
			fsckErr:     testingexec.FakeExitError{Status: fsckOperationalError},
			fsckOutput:  "fsck.ext4: unable to continue",
			wantFsExist: false,
			wantErr:     true,
		},
		{
			name:        "repair mode surfaces exit status greater than uncorrected",
			fsckOptions: []string{"-a"},
			fsckErr:     testingexec.FakeExitError{Status: 16},
			fsckOutput:  "fsck.ext4: usage error",
			wantFsExist: false,
			wantErr:     true,
		},
		{
			name:        "repair mode surfaces missing fsck binary",
			fsckOptions: []string{"-a"},
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

			fsckOptions := tc.fsckOptions
			if len(fsckOptions) == 0 {
				fsckOptions = []string{"-n"}
			}
			fakeExec := fakeSafeMounter.Exec.(*testmounter.FakeSafeMounter)
			fakeExec.CommandScript = []testingexec.FakeCommandAction{
				fsckAction(t, append(append([]string(nil), fsckOptions...), "/dev/sdz"), tc.fsckErr, tc.fsckOutput),
			}

			isFilesystemExist, err := detectOrRepairFilesystem("/dev/sdz", fsckOptions, fakeSafeMounter)
			if (err != nil) != tc.wantErr {
				t.Fatalf("detectOrRepairFilesystem error = %v, wantErr %v", err, tc.wantErr)
			}
			if isFilesystemExist != tc.wantFsExist {
				t.Fatalf("isFilesystemExist = %v, want %v", isFilesystemExist, tc.wantFsExist)
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

			err = repairFilesystem("/dev/sdz", []string{"-a"}, fakeSafeMounter)
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

func TestRescanAllVolumes(t *testing.T) {
	err := rescanAllVolumes(azureutils.NewOSIOHandler())
	if err != nil {
		t.Errorf("rescanAllVolumes failed with error: %v", err)
	}
}

// fakeNVMeIOHandler is a configurable azureutils.IOHandler for the device-freshness tests: ReadFile
// is served from files, and WriteFile calls are recorded in written.
type fakeNVMeIOHandler struct {
	files   map[string]string
	written map[string][]byte
}

func (h *fakeNVMeIOHandler) ReadFile(name string) ([]byte, error) {
	if data, ok := h.files[name]; ok {
		return []byte(data), nil
	}
	return nil, os.ErrNotExist
}

func (h *fakeNVMeIOHandler) WriteFile(name string, data []byte, _ os.FileMode) error {
	if h.written == nil {
		h.written = map[string][]byte{}
	}
	h.written[name] = data
	return nil
}

func (h *fakeNVMeIOHandler) ReadDir(string) ([]os.DirEntry, error) { return nil, nil }
func (h *fakeNVMeIOHandler) Readlink(string) (string, error)       { return "", nil }

func TestNVMeControllerRegexp(t *testing.T) {
	tests := []struct {
		device string
		want   string
	}{
		{"nvme0n1", "nvme0"},
		{"nvme12n3", "nvme12"},
		{"nvme0n1p1", "nvme0"},
		{"sdc", ""},
		{"sdc1", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := nvmeControllerRegexp.FindString(tc.device); got != tc.want {
			t.Errorf("nvmeControllerRegexp.FindString(%q) = %q, want %q", tc.device, got, tc.want)
		}
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

func TestGetCachedNVMeID(t *testing.T) {
	const blockName = "nvme0n1"
	nguidPath := filepath.Join(sysClassBlockPath, blockName, "nguid")
	uuidPath := filepath.Join(sysClassBlockPath, blockName, "uuid")
	nguid := "eec2b1a5-9f0e-4d3c-8b2a-112233445566"
	diskUUID := "11112222-3333-4444-5555-666677778888"

	tests := []struct {
		name  string
		files map[string]string
		want  uuid.UUID
	}{
		{
			name:  "nguid is preferred over uuid",
			files: map[string]string{nguidPath: nguid + "\n", uuidPath: diskUUID + "\n"},
			want:  uuid.MustParse(nguid),
		},
		{
			name:  "falls back to uuid when nguid is absent",
			files: map[string]string{uuidPath: diskUUID + "\n"},
			want:  uuid.MustParse(diskUUID),
		},
		{
			name:  "falls back to uuid when nguid is all-zero",
			files: map[string]string{nguidPath: uuid.Nil.String(), uuidPath: diskUUID},
			want:  uuid.MustParse(diskUUID),
		},
		{
			name:  "skips an unparsable nguid",
			files: map[string]string{nguidPath: "not-a-uuid", uuidPath: diskUUID},
			want:  uuid.MustParse(diskUUID),
		},
		{
			name:  "returns Nil when neither is present",
			files: map[string]string{},
			want:  uuid.Nil,
		},
		{
			name:  "returns Nil when both are all-zero",
			files: map[string]string{nguidPath: uuid.Nil.String(), uuidPath: uuid.Nil.String()},
			want:  uuid.Nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := getCachedNVMeID(&fakeNVMeIOHandler{files: tc.files}, blockName); got != tc.want {
				t.Fatalf("getCachedNVMeID = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVerifyNVMeNamespaceIdentity(t *testing.T) {
	const (
		blockName  = "nvme0n1"
		controller = "nvme0"
		cachedID   = "11112222-3333-4444-5555-666677778888"
		otherID    = "99998888-7777-6666-5555-444433332211"
	)
	nvmeSource := "/dev/" + blockName
	cached := map[string]string{filepath.Join(sysClassBlockPath, blockName, "nguid"): cachedID}
	rescanPath := filepath.Join(sysClassNVMePath, controller, "rescan_controller")

	tests := []struct {
		name       string
		source     string
		lun        string
		files      map[string]string
		live       uuid.UUID
		liveErr    error
		wantErr    bool
		wantRescan bool
	}{
		{name: "non-NVMe device is skipped", source: "/dev/sdc", lun: "0"},
		{name: "both ids absent is a no-op", source: nvmeSource, lun: "0"},
		{name: "unparsable lun fails", source: nvmeSource, lun: "not-a-lun", files: cached, wantErr: true},
		{name: "live read error fails closed", source: nvmeSource, lun: "0", files: cached, liveErr: errors.New("ioctl failed"), wantErr: true},
		{name: "match is a no-op", source: nvmeSource, lun: "0", files: cached, live: uuid.MustParse(cachedID)},
		{name: "mismatch rescans and fails", source: nvmeSource, lun: "0", files: cached, live: uuid.MustParse(otherID), wantErr: true, wantRescan: true},
		{name: "cached present but live absent is a mismatch", source: nvmeSource, lun: "0", files: cached, live: uuid.Nil, wantErr: true, wantRescan: true},
		{name: "cached absent but live present is a mismatch", source: nvmeSource, lun: "0", live: uuid.MustParse(otherID), wantErr: true, wantRescan: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			defer func(orig func(string, uint32) (uuid.UUID, error)) { getLiveNVMeID = orig }(getLiveNVMeID)
			getLiveNVMeID = func(string, uint32) (uuid.UUID, error) { return tc.live, tc.liveErr }

			io := &fakeNVMeIOHandler{files: tc.files}
			err := verifyNVMeNamespaceIdentity(tc.source, tc.lun, io)

			if (err != nil) != tc.wantErr {
				t.Fatalf("verifyNVMeNamespaceIdentity error = %v, wantErr %v", err, tc.wantErr)
			}
			if _, rescanned := io.written[rescanPath]; rescanned != tc.wantRescan {
				t.Fatalf("rescan issued = %v, want %v (writes: %v)", rescanned, tc.wantRescan, io.written)
			}
		})
	}
}

func TestShutdownFilesystemGuard(t *testing.T) {
	t.Run("non-mountpoint reports os.ErrNotExist", func(t *testing.T) {
		if err := shutdownFilesystem(t.TempDir()); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("shutdownFilesystem on a non-mountpoint = %v, want an error wrapping os.ErrNotExist", err)
		}
	})

	t.Run("nonexistent path reports os.ErrNotExist", func(t *testing.T) {
		if err := shutdownFilesystem(filepath.Join(t.TempDir(), "does-not-exist")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("shutdownFilesystem on a nonexistent path = %v, want an error wrapping os.ErrNotExist", err)
		}
	})

	t.Run("non-directory errors without os.ErrNotExist", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatalf("failed to create temp file: %v", err)
		}
		if err := shutdownFilesystem(file); err == nil || errors.Is(err, os.ErrNotExist) {
			t.Fatalf("shutdownFilesystem on a non-directory = %v, want a non-nil error that is not os.ErrNotExist", err)
		}
	})
}
