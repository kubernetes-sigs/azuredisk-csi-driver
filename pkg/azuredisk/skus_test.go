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

package azuredisk

import (
	"testing"

	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

func Test_populateSkuMap(t *testing.T) {
	skuName := "Standard_DS14"
	zone := "1"
	region := "eastus"
	nodeInfo := &NodeInfo{SkuName: skuName, Zone: zone, Region: region}
	nodeInfoInvalid := &NodeInfo{SkuName: "blah", Zone: zone, Region: region}
	tests := []struct {
		name    string
		driver  *Driver
		wantErr bool
	}{
		{
			name:    "Invalid sku should return error",
			driver:  &Driver{DriverCore: DriverCore{nodeInfo: nodeInfoInvalid}},
			wantErr: true,
		},
		{
			name:    "Valid sku should return success",
			driver:  &Driver{DriverCore: DriverCore{nodeInfo: nodeInfo}},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := populateSkuMap(&tt.driver.DriverCore)
			if (err != nil) != tt.wantErr {
				t.Errorf("populateSkuMap() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !tt.wantErr && tt.driver.getNodeInfo() == nil {
				t.Errorf("populateSkuMap() getNodeInfo() returns nil")
			}
		})
	}
}

func TestDiskSkuInfo_GetLatencyTest(t *testing.T) {
	for _, skuInfo := range DiskSkuMap["premium_lrs"] {
		t.Run(skuInfo.StorageTier, func(t *testing.T) {
			if got := skuInfo.GetRandomIOLatencyInSec(); got <= 0 {
				t.Errorf("DiskSkuInfo.GetRandomIOLatencyInSec() = %v, want > 0", got)
			}
			if got := skuInfo.GetSequentialOLatencyInSec(); got <= 0 {
				t.Errorf("DiskSkuInfo.GetSequentialOLatencyInSec() = %v, want > 0", got)
			}
		})
	}
	for _, skuInfo := range DiskSkuMap["standardssd_lrs"] {
		t.Run(skuInfo.StorageTier, func(t *testing.T) {
			if got := skuInfo.GetRandomIOLatencyInSec(); got <= 0 {
				t.Errorf("DiskSkuInfo.GetRandomIOLatencyInSec() = %v, want > 0", got)
			}
			if got := skuInfo.GetSequentialOLatencyInSec(); got <= 0 {
				t.Errorf("DiskSkuInfo.GetSequentialOLatencyInSec() = %v, want > 0", got)
			}
		})
	}
}

func TestPopulateNodeAndSkuInfo(t *testing.T) {
	d := &DriverCore{}
	d.cloud = &azure.Cloud{}

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("PopulateNodeAndSkuInfo did not panic when cloud was not initialized.")
		}
	}()

	_ = PopulateNodeAndSkuInfo(d)
}

func Test_populateNodeAndSkuInfoInternal(t *testing.T) {
	d, _ := NewFakeDriver(t)
	dCore := d.getDriverCore()
	tests := []struct {
		name     string
		instance string
		wantErr  bool
	}{
		{
			name:     "Should be able to populate valid VM sku",
			instance: "Standard_DS14",
			wantErr:  false,
		},
		{
			name:     "Should fail to populate valid VM sku",
			instance: "blah",
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := populateNodeAndSkuInfoInternal(&dCore, tt.instance, "testZone", "testRegion")
			if (err != nil) != tt.wantErr {
				t.Errorf("populateNodeAndSkuInfoInternal() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
