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
	"testing"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2019-03-01/compute"
)

func TestGetCachingMode(t *testing.T) {
	tests := []struct {
		options             map[string]string
		expectedCachingMode compute.CachingTypes
		expectedError       bool
	}{
		{
			nil,
			compute.CachingTypes(defaultAzureDataDiskCachingMode),
			false,
		},
		{
			map[string]string{},
			compute.CachingTypes(defaultAzureDataDiskCachingMode),
			false,
		},
		{
			map[string]string{"cachingmode": ""},
			compute.CachingTypes(defaultAzureDataDiskCachingMode),
			false,
		},
		{
			map[string]string{"cachingmode": "None"},
			compute.CachingTypes("None"),
			false,
		},
		{
			map[string]string{"cachingmode": "ReadOnly"},
			compute.CachingTypes("ReadOnly"),
			false,
		},
		{
			map[string]string{"cachingmode": "ReadWrite"},
			compute.CachingTypes("ReadWrite"),
			false,
		},
		{
			map[string]string{"cachingmode": "WriteOnly"},
			compute.CachingTypes(""),
			true,
		},
	}

	for _, test := range tests {
		resultCachingMode, resultError := getCachingMode(test.options)
		if resultCachingMode != test.expectedCachingMode || (resultError != nil) != test.expectedError {
			t.Errorf("input: %s, getCachingMode resultCachingMode: %s, expectedCachingMode: %s, resultError: %s, expectedError: %t", test.options, resultCachingMode, test.expectedCachingMode, resultError, test.expectedError)
		}
	}
}
