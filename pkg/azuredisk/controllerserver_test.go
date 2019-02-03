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

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2018-10-01/compute"
)

func TestGetCachingMode(t *testing.T) {
	tests := []struct {
		options   map[string]string
		expected1 compute.CachingTypes
		expected2 error
	}{
		{
			nil,
			compute.CachingTypes(defaultAzureDataDiskCachingMode),
			nil,
		},
		{
			map[string]string{},
			compute.CachingTypes(defaultAzureDataDiskCachingMode),
			nil,
		},
		{
			map[string]string{"cachingmode": ""},
			compute.CachingTypes(defaultAzureDataDiskCachingMode),
			nil,
		},
		{
			map[string]string{"cachingmode": "None"},
			compute.CachingTypes("None"),
			nil,
		},
		{
			map[string]string{"cachingmode": "ReadOnly"},
			compute.CachingTypes("ReadOnly"),
			nil,
		},
		{
			map[string]string{"cachingmode": "ReadWrite"},
			compute.CachingTypes("ReadWrite"),
			nil,
		},
		{
			map[string]string{"cachingmode": "WriteOnly"},
			compute.CachingTypes(""),
			fmt.Errorf("azureDisk - %s is not supported cachingmode. Supported values are %s", "WriteOnly", supportedCachingModes.List()),
		},
	}

	for _, test := range tests {
		result1, result2 := getCachingMode(test.options)
		if !reflect.DeepEqual(result1, test.expected1) || !reflect.DeepEqual(result2, test.expected2) {
			t.Errorf("input: %q, getCachingMode result1: %q, expected1: %q, result2: %q, expected2: %q", test.options, result1, test.expected1, result2, test.expected2)
		}
	}
}
