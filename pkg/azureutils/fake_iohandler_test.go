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
		expectedDirEntry []os.DirEntry
		expectedErr      error
	}{
		{
			"/home/",
			nil,
			fmt.Errorf("bad dir"),
		},
		{
			"/sys/bus/scsi/devices",
			[]os.DirEntry{
				&fakeDirEntry{
					name: "3:0:0:1",
				},
				&fakeDirEntry{
					name: "4:0:0:0",
				},
				&fakeDirEntry{
					name: "4:0:0:1",
				},
				&fakeDirEntry{
					name: "host1",
				},
				&fakeDirEntry{
					name: "target2:0:0",
				},
			},
			nil,
		},
		{
			"/sys/bus/scsi/devices/4:0:0:1/block",
			[]os.DirEntry{
				&fakeDirEntry{
					name: "sdd",
				},
			},
			nil,
		},
		{
			"/sys/bus/scsi/devices/3:0:0:2/block",
			[]os.DirEntry{
				&fakeDirEntry{
					name: "sde",
				},
			},
			nil,
		},
		{
			"/sys/class/scsi_host/",
			[]os.DirEntry{
				&fakeDirEntry{
					name: "host0",
				},
			},
			nil,
		},
	}

	for _, test := range tests {
		resultDirEntry, resultErr := iohandler.ReadDir(test.dirname)
		if !reflect.DeepEqual(resultDirEntry, test.expectedDirEntry) || (!reflect.DeepEqual(resultErr, test.expectedErr)) {
			t.Errorf("dirname: %v, resultDirEntry: %v, expectedDirEntry: %v, resultErr: %v, expectedErr: %v", test.dirname, resultDirEntry, test.expectedDirEntry, resultErr, test.expectedErr)
		}
		if resultErr == nil && reflect.DeepEqual(resultDirEntry, test.expectedDirEntry) {
			for num := 0; num < len(resultDirEntry); num++ {
				resultFileInfo, _ := resultDirEntry[num].Info()
				resultName := resultDirEntry[num].Name()
				resultSize := resultFileInfo.Size()
				resultMode := resultFileInfo.Mode()
				resultModTime := resultFileInfo.ModTime()
				resultIsDir := resultFileInfo.IsDir()
				resultSys := resultFileInfo.Sys()
				if resultName != test.expectedDirEntry[num].Name() || resultSize != 0 || resultMode != 777 || resultModTime.After(time.Now()) || resultIsDir != false || resultSys != nil {
					t.Errorf("Name: %v, expectedName: %v, Size: %v, Mode: %v, ModTime: %v, IsDir: %v, Sys: %v", resultName, test.expectedDirEntry[num].Name(), resultSize, resultMode, resultModTime, resultIsDir, resultSys)
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
