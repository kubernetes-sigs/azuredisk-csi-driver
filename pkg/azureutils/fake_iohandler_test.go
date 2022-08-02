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

package azureutils

import (
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestNewFakeIOHandlerReadDir(t *testing.T) {
	iohandler := NewFakeIOHandler()

	tests := []struct {
		dirname          string
		expectedFileInfo []os.FileInfo
		expectedErr      error
	}{
		{
			"/home/",
			nil,
			fmt.Errorf("bad dir"),
		},
		{
			"/sys/bus/scsi/devices",
			[]os.FileInfo{
				&fakeFileInfo{
					name: "3:0:0:1",
				},
				&fakeFileInfo{
					name: "4:0:0:0",
				},
				&fakeFileInfo{
					name: "4:0:0:1",
				},
				&fakeFileInfo{
					name: "host1",
				},
				&fakeFileInfo{
					name: "target2:0:0",
				},
			},
			nil,
		},
		{
			"/sys/bus/scsi/devices/4:0:0:1/block",
			[]os.FileInfo{
				&fakeFileInfo{
					name: "sdd",
				},
			},
			nil,
		},
		{
			"/sys/bus/scsi/devices/3:0:0:2/block",
			[]os.FileInfo{
				&fakeFileInfo{
					name: "sde",
				},
			},
			nil,
		},
		{
			"/sys/class/scsi_host/",
			[]os.FileInfo{
				&fakeFileInfo{
					name: "host0",
				},
			},
			nil,
		},
	}

	for _, test := range tests {
		resultFileInfo, resultErr := iohandler.ReadDir(test.dirname)
		if !reflect.DeepEqual(resultFileInfo, test.expectedFileInfo) || (!reflect.DeepEqual(resultErr, test.expectedErr)) {
			t.Errorf("dirname: %v, resultFileInfo: %v, expectedFileInfo: %v, resultErr: %v, expectedErr: %v", test.dirname, resultFileInfo, test.expectedFileInfo, resultErr, test.expectedErr)
		}
		if resultErr == nil && reflect.DeepEqual(resultFileInfo, test.expectedFileInfo) {
			for num := 0; num < len(resultFileInfo); num++ {
				resultName := resultFileInfo[num].Name()
				resultSize := resultFileInfo[num].Size()
				resultMode := resultFileInfo[num].Mode()
				resultModTime := resultFileInfo[num].ModTime()
				resultIsDir := resultFileInfo[num].IsDir()
				resultSys := resultFileInfo[num].Sys()
				if resultName != test.expectedFileInfo[num].Name() || resultSize != 0 || resultMode != 777 || resultModTime.After(time.Now()) || resultIsDir != false || resultSys != nil {
					t.Errorf("Name: %v, expectedName: %v, Size: %v, Mode: %v, ModTime: %v, IsDir: %v, Sys: %v", resultName, test.expectedFileInfo[num].Name(), resultSize, resultMode, resultModTime, resultIsDir, resultSys)
				}
			}
		}
	}
}

func TestNewFakeIOHandlerWriteFile(t *testing.T) {
	iohandler := NewFakeIOHandler()

	tests := []struct {
		filename    string
		data        []byte
		perm        os.FileMode
		expectedErr error
	}{
		{
			"file",
			nil,
			os.FileMode(0700),
			nil,
		},
	}

	for _, test := range tests {
		resultErr := iohandler.WriteFile(test.filename, test.data, test.perm)
		if !reflect.DeepEqual(resultErr, test.expectedErr) {
			t.Errorf("filename: %v, data: %v, perm: %v, resultErr: %v, expectedErr: %v", test.filename, test.data, test.perm, resultErr, test.expectedErr)
		}
	}
}

func TestNewFakeIOHandlerReadLink(t *testing.T) {
	iohandler := NewFakeIOHandler()

	tests := []struct {
		name         string
		expectedLink string
		expectedErr  error
	}{
		{
			"file",
			"/dev/azure/disk/sda",
			nil,
		},
	}

	for _, test := range tests {
		resultLink, resultErr := iohandler.Readlink(test.name)
		if !reflect.DeepEqual(resultLink, test.expectedLink) || !reflect.DeepEqual(resultErr, test.expectedErr) {
			t.Errorf("name: %v, resultLink: %v, expectedLink: %v, resultErr: %v, expectedErr: %v", test.name, resultLink, test.expectedLink, resultErr, test.expectedErr)
		}
	}
}

func TestNewFakeIOHandlerReadFile(t *testing.T) {
	iohandler := NewFakeIOHandler()

	tests := []struct {
		filename      string
		expectedBytes []byte
		expectedErr   error
	}{
		{
			"file.unknown",
			nil,
			fmt.Errorf("unknown file"),
		},
		{
			"file.vendor",
			[]byte("Msft    \n"),
			nil,
		},
		{
			"file.model",
			[]byte("Virtual Disk \n"),
			nil,
		},
	}

	for _, test := range tests {
		resultBytes, resultErr := iohandler.ReadFile(test.filename)
		if !reflect.DeepEqual(resultBytes, test.expectedBytes) || (!reflect.DeepEqual(resultErr, test.expectedErr)) {
			t.Errorf("filename: %v, resultBytes: %v, expectedBytes: %v, resultErr: %v, expectedErr: %v", test.filename, resultBytes, test.expectedBytes, resultErr, test.expectedErr)
		}
	}
}
