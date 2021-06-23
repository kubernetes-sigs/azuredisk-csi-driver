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

package optimization

import (
	"testing"

	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

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

func TestNewNodeInfo(t *testing.T) {
	cloud := &azure.Cloud{}

	defer func() {
		if r := recover(); r == nil {
			t.Errorf("NewNodeInfo did not panic when cloud was not initialized.")
		}
	}()

	_, _ = NewNodeInfo(cloud, "test")
}

func Test_newNodeInfoInternal(t *testing.T) {
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
			name:     "Should fail to populate an invalid VM sku",
			instance: "blah",
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newNodeInfoInternal(tt.instance, "testZone", "testRegion")
			if (err != nil) != tt.wantErr {
				t.Errorf("NewNodeInfoInternal() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
