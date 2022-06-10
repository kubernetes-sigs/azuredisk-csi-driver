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

	"k8s.io/kubernetes/pkg/volume/util/hostutil"
)

type FakeHostUtil struct {
	hostutil.FakeHostUtil
	pathIsDeviceResult map[string]struct {
		isDevice bool
		err      error
	}
}

// NewFakeHostUtil returns a FakeHostUtil object suitable for use in unit tests.
func NewFakeHostUtil() *FakeHostUtil {
	return &FakeHostUtil{
		pathIsDeviceResult: make(map[string]struct {
			isDevice bool
			err      error
		}),
	}
}

// PathIsDevice return whether the path references a block device.
func (f *FakeHostUtil) PathIsDevice(path string) (bool, error) {
	if result, ok := f.pathIsDeviceResult[path]; ok {
		return result.isDevice, result.err
	}

	_, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, fmt.Errorf("path %q does not exist", path)
	}

	return false, err
}

// SetPathIsDeviceResult set the result of calling IsBlockDevicePath for the specified path.
func (f *FakeHostUtil) SetPathIsDeviceResult(path string, isDevice bool, err error) {
	result := struct {
		isDevice bool
		err      error
	}{isDevice, err}

	f.pathIsDeviceResult[path] = result
}
