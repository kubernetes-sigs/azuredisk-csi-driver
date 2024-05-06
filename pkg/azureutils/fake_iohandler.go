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
	"io/fs"
	"os"
	"strings"
	"time"
)

type fakeDirEntry struct {
	name string
}

type fakeFileInfo struct {
	name string
}

func (f fakeFileInfo) Name() string {
	return f.name
}

func (f fakeFileInfo) Size() int64 {
	return 0
}

func (f fakeFileInfo) Mode() os.FileMode {
	return 777
}

func (f fakeFileInfo) ModTime() time.Time {
	return time.Now()
}
func (f fakeFileInfo) IsDir() bool {
	return false
}

func (f fakeFileInfo) Sys() interface{} {
	return nil
}

func (fd *fakeDirEntry) Type() fs.FileMode {
	return 777
}

func (fd *fakeDirEntry) Info() (fs.FileInfo, error) {
	return fakeFileInfo{
		name: fd.name,
	}, nil
}

func (fd *fakeDirEntry) Name() string {
	return fd.name
}

func (fd *fakeDirEntry) IsDir() bool {
	return false
}

var (
	lunStr    = "1"
	diskPath  = "4:0:0:" + lunStr
	devName   = "sdd"
	lunStr1   = "2"
	diskPath1 = "3:0:0:" + lunStr1
	devName1  = "sde"
)

type fakeIOHandler struct{}

func NewFakeIOHandler() IOHandler {
	return &fakeIOHandler{}
}

func (handler *fakeIOHandler) ReadDir(dirname string) ([]os.DirEntry, error) {
	switch dirname {
	case "/sys/bus/scsi/devices":
		f1 := &fakeDirEntry{
			name: "3:0:0:1",
		}
		f2 := &fakeDirEntry{
			name: "4:0:0:0",
		}
		f3 := &fakeDirEntry{
			name: diskPath,
		}
		f4 := &fakeDirEntry{
			name: "host1",
		}
		f5 := &fakeDirEntry{
			name: "target2:0:0",
		}
		return []os.DirEntry{f1, f2, f3, f4, f5}, nil
	case "/sys/bus/scsi/devices/" + diskPath + "/block":
		n := &fakeDirEntry{
			name: devName,
		}
		return []os.DirEntry{n}, nil
	case "/sys/bus/scsi/devices/" + diskPath1 + "/block":
		n := &fakeDirEntry{
			name: devName1,
		}
		return []os.DirEntry{n}, nil
	case "/sys/class/scsi_host/":
		n := &fakeDirEntry{
			name: "host0",
		}
		return []os.DirEntry{n}, nil
	}

	return nil, fmt.Errorf("bad dir")
}

func (handler *fakeIOHandler) WriteFile(_ string, _ []byte, _ os.FileMode) error {
	return nil
}

func (handler *fakeIOHandler) Readlink(_ string) (string, error) {
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
