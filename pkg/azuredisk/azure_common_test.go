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

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-06-30/compute"
	"github.com/stretchr/testify/assert"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"

	v1 "k8s.io/api/core/v1"
	testingexec "k8s.io/utils/exec/testing"
)

type fakeFileInfo struct {
	name string
}

func (fi *fakeFileInfo) Name() string {
	return fi.name
}

func (fi *fakeFileInfo) Size() int64 {
	return 0
}

func (fi *fakeFileInfo) Mode() os.FileMode {
	return 777
}

func (fi *fakeFileInfo) ModTime() time.Time {
	return time.Now()
}
func (fi *fakeFileInfo) IsDir() bool {
	return false
}

func (fi *fakeFileInfo) Sys() interface{} {
	return nil
}

var (
	lun       = 1
	lunStr    = "1"
	diskPath  = "4:0:0:" + lunStr
	devName   = "sdd"
	lunStr1   = "2"
	diskPath1 = "3:0:0:" + lunStr1
	devName1  = "sde"
)

type fakeIOHandler struct{}

func (handler *fakeIOHandler) ReadDir(dirname string) ([]os.FileInfo, error) {
	switch dirname {
	case "/sys/bus/scsi/devices":
		f1 := &fakeFileInfo{
			name: "3:0:0:1",
		}
		f2 := &fakeFileInfo{
			name: "4:0:0:0",
		}
		f3 := &fakeFileInfo{
			name: diskPath,
		}
		f4 := &fakeFileInfo{
			name: "host1",
		}
		f5 := &fakeFileInfo{
			name: "target2:0:0",
		}
		return []os.FileInfo{f1, f2, f3, f4, f5}, nil
	case "/sys/bus/scsi/devices/" + diskPath + "/block":
		n := &fakeFileInfo{
			name: devName,
		}
		return []os.FileInfo{n}, nil
	case "/sys/bus/scsi/devices/" + diskPath1 + "/block":
		n := &fakeFileInfo{
			name: devName1,
		}
		return []os.FileInfo{n}, nil
	}
	return nil, fmt.Errorf("bad dir")
}

func (handler *fakeIOHandler) WriteFile(filename string, data []byte, perm os.FileMode) error {
	return nil
}

func (handler *fakeIOHandler) Readlink(name string) (string, error) {
	return "/dev/azure/disk/sda", nil
}

func (handler *fakeIOHandler) ReadFile(filename string) ([]byte, error) {
	if strings.HasSuffix(filename, "vendor") {
		return []byte("Msft    \n"), nil
	}
	if strings.HasSuffix(filename, "model") {
		return []byte("Virtual Disk \n"), nil
	}
	return nil, fmt.Errorf("unknown file")
}

func TestIoHandler(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skipf("skip test on GOOS=%s", runtime.GOOS)
	}
	disk, err := findDiskByLun(lun, &fakeIOHandler{}, nil)
	if runtime.GOOS == "windows" {
		if err != nil {
			t.Errorf("no data disk found: disk %v err %v", disk, err)
		}
	} else {
		// if no disk matches lun, exit
		if disk != "/dev/"+devName || err != nil {
			t.Errorf("no data disk found: disk %v err %v", disk, err)
		}
	}
}

func TestNormalizeStorageAccountType(t *testing.T) {
	tests := []struct {
		cloud                  string
		storageAccountType     string
		disableAzureStackCloud bool
		expectedAccountType    compute.DiskStorageAccountTypes
		expectError            bool
	}{
		{
			cloud:                  azurePublicCloud,
			storageAccountType:     "",
			disableAzureStackCloud: false,
			expectedAccountType:    compute.StandardSSDLRS,
			expectError:            false,
		},
		{
			cloud:                  azureStackCloud,
			storageAccountType:     "",
			disableAzureStackCloud: false,
			expectedAccountType:    compute.StandardLRS,
			expectError:            false,
		},
		{
			cloud:                  azurePublicCloud,
			storageAccountType:     "NOT_EXISTING",
			disableAzureStackCloud: false,
			expectedAccountType:    "",
			expectError:            true,
		},
		{
			cloud:                  azurePublicCloud,
			storageAccountType:     "Standard_LRS",
			disableAzureStackCloud: false,
			expectedAccountType:    compute.StandardLRS,
			expectError:            false,
		},
		{
			cloud:                  azurePublicCloud,
			storageAccountType:     "Premium_LRS",
			disableAzureStackCloud: false,
			expectedAccountType:    compute.PremiumLRS,
			expectError:            false,
		},
		{
			cloud:                  azurePublicCloud,
			storageAccountType:     "StandardSSD_LRS",
			disableAzureStackCloud: false,
			expectedAccountType:    compute.StandardSSDLRS,
			expectError:            false,
		},
		{
			cloud:                  azurePublicCloud,
			storageAccountType:     "UltraSSD_LRS",
			disableAzureStackCloud: false,
			expectedAccountType:    compute.UltraSSDLRS,
			expectError:            false,
		},
		{
			cloud:                  azureStackCloud,
			storageAccountType:     "UltraSSD_LRS",
			disableAzureStackCloud: false,
			expectedAccountType:    "",
			expectError:            true,
		},
		{
			cloud:                  azureStackCloud,
			storageAccountType:     "UltraSSD_LRS",
			disableAzureStackCloud: true,
			expectedAccountType:    compute.UltraSSDLRS,
			expectError:            false,
		},
	}

	for _, test := range tests {
		result, err := normalizeStorageAccountType(test.storageAccountType, test.cloud, test.disableAzureStackCloud)
		assert.Equal(t, result, test.expectedAccountType)
		assert.Equal(t, err != nil, test.expectError, fmt.Sprintf("error msg: %v", err))
	}
}

func TestNormalizeCachingMode(t *testing.T) {
	tests := []struct {
		desc          string
		req           v1.AzureDataDiskCachingMode
		expectedErr   error
		expectedValue v1.AzureDataDiskCachingMode
	}{
		{
			desc:          "CachingMode not exist",
			req:           "",
			expectedErr:   nil,
			expectedValue: v1.AzureDataDiskCachingReadOnly,
		},
		{
			desc:          "Not supported CachingMode",
			req:           "WriteOnly",
			expectedErr:   fmt.Errorf("azureDisk - WriteOnly is not supported cachingmode. Supported values are [None ReadOnly ReadWrite]"),
			expectedValue: "",
		},
		{
			desc:          "Valid CachingMode",
			req:           "ReadOnly",
			expectedErr:   nil,
			expectedValue: "ReadOnly",
		},
	}
	for _, test := range tests {
		value, err := normalizeCachingMode(test.req)
		assert.Equal(t, value, test.expectedValue)
		assert.Equal(t, err, test.expectedErr, fmt.Sprintf("error msg: %v", err))
	}
}
func TestGetDiskLUN(t *testing.T) {
	tests := []struct {
		deviceInfo  string
		expectedLUN int
		expectError bool
	}{
		{
			deviceInfo:  "0",
			expectedLUN: 0,
			expectError: false,
		},
		{
			deviceInfo:  "10",
			expectedLUN: 10,
			expectError: false,
		},
		{
			deviceInfo:  "11d",
			expectedLUN: -1,
			expectError: true,
		},
		{
			deviceInfo:  "999",
			expectedLUN: -1,
			expectError: true,
		},
		{
			deviceInfo:  "",
			expectedLUN: -1,
			expectError: true,
		},
		{
			deviceInfo:  "/dev/disk/azure/scsi1/lun2",
			expectedLUN: 2,
			expectError: false,
		},
		{
			deviceInfo:  "/dev/disk/azure/scsi0/lun12",
			expectedLUN: 12,
			expectError: false,
		},
		{
			deviceInfo:  "/devhost/disk/azure/scsi0/lun13",
			expectedLUN: 13,
			expectError: false,
		},
		{
			deviceInfo:  "/dev/disk/by-id/scsi1/lun2",
			expectedLUN: -1,
			expectError: true,
		},
	}

	for _, test := range tests {
		result, err := getDiskLUN(test.deviceInfo)
		assert.Equal(t, result, test.expectedLUN)
		assert.Equal(t, err != nil, test.expectError, fmt.Sprintf("error msg: %v", err))
	}
}

func TestStrFirstLetterToUpper(t *testing.T) {
	str := "t"

	if str != strFirstLetterToUpper(str) {
		t.Errorf("result wrong")
	}
	str = "test"
	if strFirstLetterToUpper(str) != "Test" {
		t.Errorf("result wrong")
	}
	str = "Test"
	if strFirstLetterToUpper(str) != "Test" {
		t.Errorf("result wrong")
	}
}

func TestGetBlockSizeBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping test on Windows")
	}
	fakeMounter, err := mounter.NewFakeSafeMounter()
	assert.NoError(t, err)
	testTarget, err := testutil.GetWorkDirPath("test")
	assert.NoError(t, err)

	notFoundErr := "exit status 1"
	// exception in darwin
	if runtime.GOOS == "darwin" {
		notFoundErr = "executable file not found in $PATH"
	} else if runtime.GOOS == "windows" {
		notFoundErr = "executable file not found in %PATH%"
	}

	notFoundErrAction := func() ([]byte, []byte, error) {
		return []byte{}, []byte{}, errors.New(notFoundErr)
	}

	tests := []struct {
		desc         string
		req          string
		outputScript testingexec.FakeAction
		expectedErr  testutil.TestError
	}{
		{
			desc:         "no exist path",
			req:          "testpath",
			outputScript: notFoundErrAction,
			expectedErr: testutil.TestError{
				DefaultError: fmt.Errorf("error when getting size of block volume at path testpath: output: , err: %s", notFoundErr),
				WindowsError: fmt.Errorf("error when getting size of block volume at path testpath: output: , err: %s", notFoundErr),
			},
		},
		{
			desc:         "invalid path",
			req:          testTarget,
			outputScript: notFoundErrAction,
			expectedErr: testutil.TestError{
				DefaultError: fmt.Errorf("error when getting size of block volume at path %s: "+
					"output: , err: %s", testTarget, notFoundErr),
				WindowsError: fmt.Errorf("error when getting size of block volume at path %s: "+
					"output: , err: %s", testTarget, notFoundErr),
			},
		},
	}
	for _, test := range tests {
		fakeMounter.Exec.(*mounter.FakeSafeMounter).SetNextCommandOutputScripts(test.outputScript)

		_, err := getBlockSizeBytes(test.req, fakeMounter)
		if !testutil.AssertError(&test.expectedErr, err) {
			t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr.Error())
		}
	}
	//Setup
	_ = makeDir(testTarget)

	err = os.RemoveAll(testTarget)
	assert.NoError(t, err)
}

func TestGetDevicePathWithMountPath(t *testing.T) {
	fakeMounter, err := mounter.NewFakeSafeMounter()
	assert.NoError(t, err)
	err = errors.New("exit status 1")

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
			expectedErr: fmt.Errorf("could not determine device path(unit-test), error: %v", err),
			// Skip negative tests on Windows because error messages from csi-proxy are not easily predictable.
			skipOnWindows: true,
			outputScript: func() ([]byte, []byte, error) {
				return []byte{}, []byte{}, err
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

	for _, test := range tests {
		if !(test.skipOnDarwin && runtime.GOOS == "darwin") && !(test.skipOnWindows && runtime.GOOS == "windows") {
			if test.outputScript != nil {
				fakeMounter.Exec.(*mounter.FakeSafeMounter).SetNextCommandOutputScripts(test.outputScript)
			}
			_, err := getDevicePathWithMountPath(test.req, fakeMounter)
			if !reflect.DeepEqual(err, test.expectedErr) {
				t.Errorf("desc: %s\n actualErr: (%v), expectedErr: (%v)", test.desc, err, test.expectedErr)
			}
		}
	}
}
