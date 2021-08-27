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
	"runtime"
	"testing"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

const (
	lun     = 1
	devName = "sdd"
)

func TestFindDiskByLun(t *testing.T) {
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		t.Skipf("skip test on GOOS=%s", runtime.GOOS)
	}
	ioHandler := azureutils.NewFakeIOHandler()
	disk, err := findDiskByLun(lun, ioHandler, nil)
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
