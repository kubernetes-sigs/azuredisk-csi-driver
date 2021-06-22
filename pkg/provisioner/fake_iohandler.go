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
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"strings"
	"time"
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
	lunStr    = "1"
	diskPath  = "4:0:0:" + lunStr
	devName   = "sdd"
	lunStr1   = "2"
	diskPath1 = "3:0:0:" + lunStr1
	devName1  = "sde"
)

type fakeIOHandler struct{}

func (handler *fakeIOHandler) ReadDir(dirname string) ([]os.FileInfo, error) {
	switch path.Clean(dirname) {
	case "/dev/disk/azure":
		return []os.FileInfo{
			&fakeFileInfo{name: "resource"},
			&fakeFileInfo{name: "resource-part1"},
			&fakeFileInfo{name: "root"},
			&fakeFileInfo{name: "root-part1"},
		}, nil

	case "/dev/disk/azure/scsi1":
		return []os.FileInfo{
			&fakeFileInfo{name: "resource"},
			&fakeFileInfo{name: "resource-part1"},
		}, nil

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

	return ioutil.ReadDir(dirname)
}

func (handler *fakeIOHandler) WriteFile(filename string, data []byte, perm os.FileMode) error {
	return nil
}

func (handler *fakeIOHandler) Readlink(name string) (string, error) {
	switch name {
	case "/dev/disk/azure/resource":
		return "../../sda", nil

	case "/dev/disk/azure/resource-part1":
		return "../../sda1", nil

	case "/dev/disk/azure/root":
		return "../../sdb", nil

	case "/dev/disk/azure/root-part1":
		return "../../sdb1", nil

	case "/dev/disk/azure/scsi1/resource":
		return "../../../sdc", nil

	case "/dev/disk/azure/scsi1/resource-part1":
		return "../../../sdc1", nil
	}

	return "", &os.PathError{Op: "readlink", Path: name, Err: os.ErrNotExist}
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
