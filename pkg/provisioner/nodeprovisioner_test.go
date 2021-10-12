/*
Copyright 2021 The Kubernetes Authors.

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

package provisioner

import (
	"context"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	testingexec "k8s.io/utils/exec/testing"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
)

var (
	sourceTestDirPath     string
	targetTestDirPath     string
	existingBlockFilePath string
	existingFilePath      string
	errorTargetPath       string
)

func TestMain(m *testing.M) {
	var err error

	sourceTestDirPath, err = ioutil.TempDir(os.TempDir(), "sourceTestDir")
	if err != nil {
		log.Fatalf("ERROR: Failed to create source test dir: %v\n", err)
	}
	defer os.RemoveAll(sourceTestDirPath)

	targetTestDirPath, err = ioutil.TempDir(os.TempDir(), "targetTestDir")
	if err != nil {
		log.Fatalf("ERROR: Failed to create target test dir: %v\n", err)
	}
	defer os.RemoveAll(targetTestDirPath)

	existingBlockFile, err := ioutil.TempFile(targetTestDirPath, "existingBlockFile-*.bin")
	if err != nil {
		log.Fatalf("ERROR: Failed to create pre-existing file: %v\n", err)
	}
	_ = existingBlockFile.Close()
	existingBlockFilePath = existingBlockFile.Name()
	defer os.Remove(existingFilePath)

	existingFile, err := ioutil.TempFile(os.TempDir(), "existingFile-*.txt")
	if err != nil {
		log.Fatalf("ERROR: Failed to create pre-existing file: %v\n", err)
	}
	_ = existingFile.Close()
	existingFilePath = existingFile.Name()
	defer os.Remove(existingFilePath)

	errorTargetPath = filepath.Join(os.TempDir(), "errorTargetPath")

	os.Exit(m.Run())
}

func TestEnsureMountPoint(t *testing.T) {
	existingMountPointPath, err := ioutil.TempDir(os.TempDir(), "existingMountPointPath-*")
	assert.NoError(t, err)
	defer os.RemoveAll(existingMountPointPath)

	errorTargetError := errors.New("fake IsLikelyNotMountPoint: fake error")

	tests := []struct {
		desc          string
		target        string
		skipOnWindows bool
		expectedMnt   bool
		expectedErr   testutil.TestError
	}{
		{
			desc:          "[Error] Mocked by IsLikelyNotMountPoint",
			target:        errorTargetPath,
			skipOnWindows: true,
			expectedErr:   testutil.TestError{DefaultError: errorTargetError},
			expectedMnt:   false,
		},
		{
			desc:          "[Error] Not a directory",
			target:        existingFilePath,
			skipOnWindows: true, // No error reported on Windows
			expectedErr: testutil.TestError{
				DefaultError: &os.PathError{Op: "mkdir", Path: existingFilePath, Err: syscall.ENOTDIR},
			},
		},
		{
			desc:          "[Success] Successful run",
			target:        targetTestDirPath,
			skipOnWindows: true,
			expectedErr:   testutil.TestError{},
			expectedMnt:   false,
		},
		{
			desc:          "[Success] Already existing mount",
			target:        existingMountPointPath,
			skipOnWindows: true,
			expectedErr:   testutil.TestError{},
			expectedMnt:   true,
		},
	}

	// Setup
	nodeProvisioner, err := NewFakeNodeProvisioner()
	assert.NoError(t, err)
	nodeProvisioner.SetMountCheckError(errorTargetPath, errorTargetError)
	err = nodeProvisioner.Mount(existingMountPointPath, existingMountPointPath, "ext4", make([]string, 0))
	assert.NoError(t, err)

	for _, test := range tests {
		if test.skipOnWindows && runtime.GOOS == "windows" {
			continue
		}
		mnt, err := nodeProvisioner.EnsureMountPointReady(test.target)
		if !testutil.AssertError(&test.expectedErr, err) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr.Error())
		}
		if err == nil {
			assert.Equal(t, test.expectedMnt, mnt)
		}
	}
}

func TestEnsureBlockTargetFile(t *testing.T) {
	newBlockFilePath := filepath.Join(targetTestDirPath, "newBlockFile.bin")
	defer os.Remove(newBlockFilePath)

	parentNotDirPath := filepath.Join(existingFilePath, "parentNotDir.bin")

	tests := []struct {
		desc        string
		req         string
		expectedErr testutil.TestError
	}{
		{
			desc:        "[Success] Block file does not already exists",
			req:         existingBlockFilePath,
			expectedErr: testutil.TestError{},
		},
		{
			desc:        "[Success] Block file already exists",
			req:         existingBlockFilePath,
			expectedErr: testutil.TestError{},
		},
		{
			desc: "[Failure] Parent of block file is file not directory",
			req:  parentNotDirPath,
			expectedErr: testutil.TestError{
				DefaultError: status.Error(codes.Internal, fmt.Sprintf("Could not mount target \"%s\": mkdir %s: not a directory", existingFilePath, existingFilePath)),
				WindowsError: status.Error(codes.Internal, fmt.Sprintf("Could not remove mount target %#v: remove %s: The system cannot find the path specified.", parentNotDirPath, parentNotDirPath)),
			},
		},
	}

	nodeProvisioner, err := NewFakeNodeProvisioner()
	assert.NoError(t, err)

	for _, test := range tests {
		err := nodeProvisioner.EnsureBlockTargetReady(test.req)
		if !testutil.AssertError(&test.expectedErr, err) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr.Error())
		}
	}
}

func TestGetBlockSizeBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping test on Windows")
	}
	notFoundErr := "exit status 1"
	unexpectedOutput := "Block size is 723327"

	tests := []struct {
		desc         string
		req          string
		outputScript testingexec.FakeAction
		expectedErr  testutil.TestError
	}{
		{
			desc: "[Success] Returns the block device size.",
			req:  existingBlockFilePath,
			outputScript: func() ([]byte, []byte, error) {
				return []byte("732237"), []byte{}, nil
			},
			expectedErr: testutil.TestError{},
		},
		{
			desc: "[Failure] File does not exist",
			req:  errorTargetPath,
			outputScript: func() ([]byte, []byte, error) {
				return []byte{}, []byte{}, errors.New(notFoundErr)
			},
			expectedErr: testutil.TestError{
				DefaultError: fmt.Errorf("error when getting size of block volume at path %s: output: , err: %s", errorTargetPath, notFoundErr),
			},
		},
		{
			desc: "[Failure] Unexpected output",
			req:  errorTargetPath,
			outputScript: func() ([]byte, []byte, error) {
				return []byte(unexpectedOutput), []byte{}, nil
			},
			expectedErr: testutil.TestError{
				DefaultError: fmt.Errorf("failed to parse size %s into a valid size", unexpectedOutput),
			},
		},
	}

	nodeProvisioner, err := NewFakeNodeProvisioner()
	assert.NoError(t, err)

	for _, test := range tests {
		nodeProvisioner.SetNextCommandOutputScripts(test.outputScript)

		_, err := nodeProvisioner.GetBlockSizeBytes(test.req)
		if !testutil.AssertError(&test.expectedErr, err) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr.Error())
		}
	}
}

func TestGetDevicePathWithLUN(t *testing.T) {

	tests := []struct {
		desc        string
		req         int
		expectedErr testutil.TestError
	}{
		{
			desc: "[Success] valid LUN test",
			req:  1,
		},
		{
			desc: "[Failure] LUN does not exist test",
			req:  237,
			expectedErr: testutil.TestError{
				DefaultError: errors.New("timed out waiting for the condition"),
				WindowsError: fmt.Errorf("azureDisk - findDiskByLun(237) failed with error(could not find disk id for lun: 237)"),
			},
		},
	}

	nodeProvisioner, err := NewFakeNodeProvisioner()
	assert.NoError(t, err)

	nodeProvisioner.SetDevicePollParameters(1*time.Second, 2*time.Second)

	for _, test := range tests {
		_, err := nodeProvisioner.GetDevicePathWithLUN(context.Background(), test.req)
		if !testutil.AssertError(&test.expectedErr, err) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
		}
	}
}

func TestGetDevicePathWithMountPath(t *testing.T) {
	cmdErr := errors.New("exit status 1")

	tests := []struct {
		desc          string
		req           string
		skipOnDarwin  bool
		skipOnWindows bool
		expectedErr   error
		outputScript  testingexec.FakeAction
	}{
		{
			desc:        "Invalid device path",
			req:         "unit-test",
			expectedErr: fmt.Errorf("could not determine device path(unit-test), error: %v", cmdErr),
			// Skip negative tests on Windows because error messages from csi-proxy are not easily predictable.
			skipOnWindows: true,
			outputScript: func() ([]byte, []byte, error) {
				return []byte{}, []byte{}, cmdErr
			},
		},
		{
			desc:          "[Success] Valid device path",
			req:           "/sys",
			skipOnDarwin:  true,
			skipOnWindows: true,
			expectedErr:   nil,
			outputScript: func() ([]byte, []byte, error) {
				return []byte("/sys"), []byte{}, nil
			},
		},
	}

	nodeProvisioner, err := NewFakeNodeProvisioner()
	assert.NoError(t, err)

	for _, test := range tests {
		if !(test.skipOnDarwin && runtime.GOOS == "darwin") && !(test.skipOnWindows && runtime.GOOS == "windows") {
			if test.outputScript != nil {
				nodeProvisioner.SetNextCommandOutputScripts(test.outputScript)
			}
			_, err := nodeProvisioner.GetDevicePathWithMountPath(test.req)
			if !testutil.IsErrorEquivalent(test.expectedErr, err) {
				t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
			}
		}
	}
}
