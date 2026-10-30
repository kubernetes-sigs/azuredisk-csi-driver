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
	readErr := fmt.Errorf("read failed")
	iohandler := NewFakeIOHandlerWithOptions(FakeIOHandlerOptions{
		Dirs:   map[string][]string{"/dir": {"file"}, "/empty": {}},
		Errors: map[string]error{"/broken": readErr},
	})

	tests := []struct {
		dirname          string
		expectedDirEntry []os.DirEntry
		expectedErr      error
	}{
		{
			"/dir",
			[]os.DirEntry{
				&fakeDirEntry{
					name: "file",
				},
			},
			nil,
		},
		{
			"/empty",
			[]os.DirEntry{},
			nil,
		},
		{
			"/broken",
			nil,
			readErr,
		},
		{
			"/home/",
			nil,
			os.ErrNotExist,
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
	writeErr := fmt.Errorf("write failed")

	tests := []struct {
		writeErr           error
		expectedFileExists bool
	}{
		{
			nil,
			true,
		},
		{
			writeErr,
			false,
		},
	}

	for _, test := range tests {
		iohandler := NewFakeIOHandlerWithOptions(FakeIOHandlerOptions{
			WriteError: test.writeErr,
			OnWrite: func(dirs map[string][]string) {
				dirs["/dir"] = []string{"file"}
			},
		})
		resultErr := iohandler.WriteFile("/dir/file", []byte("data"), os.FileMode(0700))
		expectedWrites := []FakeWrite{{Path: "/dir/file", Data: "data", Perm: os.FileMode(0700)}}
		_, statErr := iohandler.Stat("/dir/file")
		if resultErr != test.writeErr || !reflect.DeepEqual(iohandler.Writes(), expectedWrites) || (statErr == nil) != test.expectedFileExists {
			t.Errorf("writeErr: %v, resultErr: %v, writes: %v, statErr: %v", test.writeErr, resultErr, iohandler.Writes(), statErr)
		}
	}
}

func TestNewFakeIOHandlerReadFile(t *testing.T) {
	readErr := fmt.Errorf("read failed")
	iohandler := NewFakeIOHandlerWithOptions(FakeIOHandlerOptions{
		Files:  map[string][]byte{"/file": []byte("data")},
		Errors: map[string]error{"/broken": readErr},
	})

	tests := []struct {
		filename      string
		expectedBytes []byte
		expectedErr   error
	}{
		{
			"/file",
			[]byte("data"),
			nil,
		},
		{
			"/broken",
			nil,
			readErr,
		},
		{
			"/missing",
			nil,
			os.ErrNotExist,
		},
	}

	for _, test := range tests {
		resultBytes, resultErr := iohandler.ReadFile(test.filename)
		if !reflect.DeepEqual(resultBytes, test.expectedBytes) || (!reflect.DeepEqual(resultErr, test.expectedErr)) {
			t.Errorf("filename: %v, resultBytes: %v, expectedBytes: %v, resultErr: %v, expectedErr: %v", test.filename, resultBytes, test.expectedBytes, resultErr, test.expectedErr)
		}
	}
}

func TestNewFakeIOHandlerStat(t *testing.T) {
	statErr := fmt.Errorf("stat failed")
	iohandler := NewFakeIOHandlerWithOptions(FakeIOHandlerOptions{
		Dirs:   map[string][]string{"/dev": {"sdd"}},
		Errors: map[string]error{"/dev/sde": statErr},
	})

	tests := []struct {
		filename    string
		expectedErr error
	}{
		{
			"/dev/sdd",
			nil,
		},
		{
			"/dev/sde",
			statErr,
		},
		{
			"/dev/sdf",
			os.ErrNotExist,
		},
	}

	for _, test := range tests {
		_, resultStatErr := iohandler.Stat(test.filename)
		resultOpenErr := iohandler.CheckOpenable(test.filename)
		if resultStatErr != test.expectedErr || resultOpenErr != test.expectedErr {
			t.Errorf("filename: %v, resultStatErr: %v, resultOpenErr: %v, expectedErr: %v", test.filename, resultStatErr, resultOpenErr, test.expectedErr)
		}
	}
}
