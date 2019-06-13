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

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2018-10-01/compute"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/stretchr/testify/assert"
)

func TestGetFStype(t *testing.T) {
	tests := []struct {
		options  map[string]string
		expected string
	}{
		{
			nil,
			"",
		},
		{
			map[string]string{},
			"",
		},
		{
			map[string]string{"fstype": ""},
			"",
		},
		{
			map[string]string{"fstype": "xfs"},
			"xfs",
		},
		{
			map[string]string{"FSType": "xfs"},
			"xfs",
		},
		{
			map[string]string{"fstype": "EXT4"},
			"ext4",
		},
	}

	for _, test := range tests {
		result := getFStype(test.options)
		if result != test.expected {
			t.Errorf("input: %q, getFStype result: %s, expected: %s", test.options, result, test.expected)
		}
	}
}

func TestGetMaxDataDiskCount(t *testing.T) {
	tests := []struct {
		instanceType string
		sizeList     *[]compute.VirtualMachineSize
		expectResult int64
	}{
		{
			instanceType: "standard_d2_v2",
			sizeList: &[]compute.VirtualMachineSize{
				{Name: to.StringPtr("Standard_D2_V2"), MaxDataDiskCount: to.Int32Ptr(8)},
				{Name: to.StringPtr("Standard_D3_V2"), MaxDataDiskCount: to.Int32Ptr(16)},
			},
			expectResult: 8,
		},
		{
			instanceType: "NOT_EXISTING",
			sizeList: &[]compute.VirtualMachineSize{
				{Name: to.StringPtr("Standard_D2_V2"), MaxDataDiskCount: to.Int32Ptr(8)},
			},
			expectResult: defaultAzureVolumeLimit,
		},
		{
			instanceType: "",
			sizeList:     &[]compute.VirtualMachineSize{},
			expectResult: defaultAzureVolumeLimit,
		},
	}

	for _, test := range tests {
		result := getMaxDataDiskCount(test.instanceType, test.sizeList)
		assert.Equal(t, test.expectResult, result)
	}
}
