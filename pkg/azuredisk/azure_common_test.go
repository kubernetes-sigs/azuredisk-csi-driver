/*
Copyright 2019 The Kubernetes Authors.

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

// import (
// 	"errors"
// 	"fmt"
// 	"os"
// 	"runtime"
// 	"testing"

// 	"github.com/stretchr/testify/assert"
// 	testingexec "k8s.io/utils/exec/testing"
// 	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
// 	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
// 	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
// )

// const (
// 	lun     = 1
// 	devName = "sdd"
// )

// func TestFindDiskByLun(t *testing.T) {
// 	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
// 		t.Skipf("skip test on GOOS=%s", runtime.GOOS)
// 	}
// 	ioHandler := azureutils.NewFakeIOHandler()
// 	disk, err := findDiskByLun(lun, ioHandler, nil)
// 	if runtime.GOOS == "windows" {
// 		if err != nil {
// 			t.Errorf("no data disk found: disk %v err %v", disk, err)
// 		}
// 	} else {
// 		// if no disk matches lun, exit
// 		if disk != "/dev/"+devName || err != nil {
// 			t.Errorf("no data disk found: disk %v err %v", disk, err)
// 		}
// 	}
// }

// func TestStrFirstLetterToUpper(t *testing.T) {
// 	str := "t"

// 	if str != strFirstLetterToUpper(str) {
// 		t.Errorf("result wrong")
// 	}
// 	str = "test"
// 	if strFirstLetterToUpper(str) != "Test" {
// 		t.Errorf("result wrong")
// 	}
// 	str = "Test"
// 	if strFirstLetterToUpper(str) != "Test" {
// 		t.Errorf("result wrong")
// 	}
// }

// func TestGetBlockSizeBytes(t *testing.T) {
// 	if runtime.GOOS == "windows" {
// 		t.Skip("Skipping test on Windows")
// 	}
// 	fakeMounter, err := mounter.NewFakeSafeMounter()
// 	assert.NoError(t, err)
// 	testTarget, err := testutil.GetWorkDirPath("test")
// 	assert.NoError(t, err)

// 	notFoundErr := "exit status 1"
// 	// exception in darwin
// 	if runtime.GOOS == "darwin" {
// 		notFoundErr = "executable file not found in $PATH"
// 	} else if runtime.GOOS == "windows" {
// 		notFoundErr = "executable file not found in %PATH%"
// 	}

// 	notFoundErrAction := func() ([]byte, []byte, error) {
// 		return []byte{}, []byte{}, errors.New(notFoundErr)
// 	}

// 	tests := []struct {
// 		desc         string
// 		req          string
// 		outputScript testingexec.FakeAction
// 		expectedErr  testutil.TestError
// 	}{
// 		{
// 			desc:         "no exist path",
// 			req:          "testpath",
// 			outputScript: notFoundErrAction,
// 			expectedErr: testutil.TestError{
// 				DefaultError: fmt.Errorf("error when getting size of block volume at path testpath: output: , err: %s", notFoundErr),
// 				WindowsError: fmt.Errorf("error when getting size of block volume at path testpath: output: , err: %s", notFoundErr),
// 			},
// 		},
// 		{
// 			desc:         "invalid path",
// 			req:          testTarget,
// 			outputScript: notFoundErrAction,
// 			expectedErr: testutil.TestError{
// 				DefaultError: fmt.Errorf("error when getting size of block volume at path %s: "+
// 					"output: , err: %s", testTarget, notFoundErr),
// 				WindowsError: fmt.Errorf("error when getting size of block volume at path %s: "+
// 					"output: , err: %s", testTarget, notFoundErr),
// 			},
// 		},
// 	}
// 	for _, test := range tests {
// 		fakeMounter.Exec.(*mounter.FakeSafeMounter).SetNextCommandOutputScripts(test.outputScript)

// 		_, err := getBlockSizeBytes(test.req, fakeMounter)
// 		if !testutil.AssertError(&test.expectedErr, err) {
// 			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr.Error())
// 		}
// 	}
// 	//Setup
// 	_ = makeDir(testTarget)

// 	err = os.RemoveAll(testTarget)
// 	assert.NoError(t, err)
// }

// func TestGetDevicePathWithMountPath(t *testing.T) {
// 	fakeMounter, err := mounter.NewFakeSafeMounter()
// 	assert.NoError(t, err)
// 	err = errors.New("exit status 1")

// 	tests := []struct {
// 		desc          string
// 		req           string
// 		skipOnDarwin  bool
// 		skipOnWindows bool
// 		expectedErr   error
// 		outputScript  testingexec.FakeAction
// 	}{
// 		{
// 			desc:        "Invalid device path",
// 			req:         "unit-test",
// 			expectedErr: fmt.Errorf("could not determine device path(unit-test), error: %v", err),
// 			// Skip negative tests on Windows because error messages from csi-proxy are not easily predictable.
// 			skipOnWindows: true,
// 			outputScript: func() ([]byte, []byte, error) {
// 				return []byte{}, []byte{}, err
// 			},
// 		},
// 		{
// 			desc:          "[Success] Valid device path",
// 			req:           "/sys",
// 			skipOnDarwin:  true,
// 			skipOnWindows: true,
// 			expectedErr:   nil,
// 			outputScript: func() ([]byte, []byte, error) {
// 				return []byte("/sys"), []byte{}, nil
// 			},
// 		},
// 	}

// 	for _, test := range tests {
// 		if !(test.skipOnDarwin && runtime.GOOS == "darwin") && !(test.skipOnWindows && runtime.GOOS == "windows") {
// 			if test.outputScript != nil {
// 				fakeMounter.Exec.(*mounter.FakeSafeMounter).SetNextCommandOutputScripts(test.outputScript)
// 			}
// 			_, err := getDevicePathWithMountPath(test.req, fakeMounter)
// 			if !testutil.IsErrorEquivalent(err, test.expectedErr) {
// 				t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
// 			}
// 		}
// 	}
// }
