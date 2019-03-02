/*
Copyright 2017 The Kubernetes Authors.

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
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsManagedDisk(t *testing.T) {
	tests := []struct {
		options  string
		expected bool
	}{
		{
			options:  "testurl/subscriptions/12/resourceGroups/23/providers/Microsoft.Compute/disks/name",
			expected: true,
		},
		{
			options:  "test.com",
			expected: true,
		},
		{
			options:  "HTTP://test.com",
			expected: false,
		},
		{
			options:  "http://test.com/vhds/name",
			expected: false,
		},
	}

	for _, test := range tests {
		result := isManagedDisk(test.options)
		if !reflect.DeepEqual(result, test.expected) {
			t.Errorf("input: %q, isManagedDisk result: %t, expected: %t", test.options, result, test.expected)
		}
	}
}

func TestGetDiskName(t *testing.T) {
	mDiskPathRE := managedDiskPathRE
	uDiskPathRE := unmanagedDiskPathRE
	tests := []struct {
		options   string
		expected1 string
		expected2 error
	}{
		{
			options:   "testurl/subscriptions/12/resourceGroups/23/providers/Microsoft.Compute/disks/name",
			expected1: "name",
			expected2: nil,
		},
		{
			options:   "testurl/subscriptions/23/providers/Microsoft.Compute/disks/name",
			expected1: "",
			expected2: fmt.Errorf("could not get disk name from testurl/subscriptions/23/providers/Microsoft.Compute/disks/name, correct format: %s", mDiskPathRE),
		},
		{
			options:   "http://test.com/vhds/name",
			expected1: "name",
			expected2: nil,
		},
		{
			options:   "http://test.io/name",
			expected1: "",
			expected2: fmt.Errorf("could not get disk name from http://test.io/name, correct format: %s", uDiskPathRE),
		},
	}

	for _, test := range tests {
		result1, result2 := getDiskName(test.options)
		if !reflect.DeepEqual(result1, test.expected1) || !reflect.DeepEqual(result2, test.expected2) {
			t.Errorf("input: %q, getDiskName result1: %q, expected1: %q, result2: %q, expected2: %q", test.options, result1, test.expected1,
				result2, test.expected2)
		}
	}
}

func TestGetResourceGroupFromDiskURI(t *testing.T) {
	tests := []struct {
		diskURL        string
		expectedResult string
		expectError    bool
	}{
		{
			diskURL:        "/subscriptions/4be8920b-2978-43d7-axyz-04d8549c1d05/resourceGroups/azure-k8s1102/providers/Microsoft.Compute/disks/andy-mghyb1102-dynamic-pvc-f7f014c9-49f4-11e8-ab5c-000d3af7b38e",
			expectedResult: "azure-k8s1102",
			expectError:    false,
		},
		{
			diskURL:        "/4be8920b-2978-43d7-axyz-04d8549c1d05/resourceGroups/azure-k8s1102/providers/Microsoft.Compute/disks/andy-mghyb1102-dynamic-pvc-f7f014c9-49f4-11e8-ab5c-000d3af7b38e",
			expectedResult: "",
			expectError:    true,
		},
		{
			diskURL:        "",
			expectedResult: "",
			expectError:    true,
		},
	}

	for _, test := range tests {
		result, err := getResourceGroupFromDiskURI(test.diskURL)
		assert.Equal(t, result, test.expectedResult, "Expect result not equal with getResourceGroupFromDiskURI(%s) return: %q, expected: %q",
			test.diskURL, result, test.expectedResult)

		if test.expectError {
			assert.NotNil(t, err, "Expect error during getResourceGroupFromDiskURI(%s)", test.diskURL)
		} else {
			assert.Nil(t, err, "Expect error is nil during getResourceGroupFromDiskURI(%s)", test.diskURL)
		}
	}
}