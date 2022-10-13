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
	"reflect"
	"testing"
)

func TestNewFakeHostUtil(t *testing.T) {
	fakeHostUtil := NewFakeHostUtil()
	fakeHostUtil.SetPathIsDeviceResult("/home/", true, nil)

	tests := []struct {
		path         string
		expectedResp bool
		expectedErr  error
	}{
		{
			"/home/",
			true,
			nil,
		},
		{
			"/hame/",
			false,
			fmt.Errorf("path %q does not exist", "/hame/"),
		},
		{
			".",
			false,
			nil,
		},
	}

	for _, test := range tests {
		resultResp, resultErr := fakeHostUtil.PathIsDevice(test.path)
		if !reflect.DeepEqual(resultResp, test.expectedResp) || (!reflect.DeepEqual(resultErr, test.expectedErr)) {
			t.Errorf("path: %v, resultResp: %v, expectedResp: %v, resultErr: %v, expectedErr: %v", test.path, resultResp, test.expectedResp, resultErr, test.expectedErr)
		}
	}
}
