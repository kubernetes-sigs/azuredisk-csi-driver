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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
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
	instanceType := "Standard_DS14"

	tests := []struct {
		description  string
		instanceType string
		wantErr      bool
	}{
		{
			description:  "[Success] Should succeed for an existing node.",
			instanceType: instanceType,
			wantErr:      false,
		},
		{
			description:  "[Failure] Should return an error for a unknown SKU.",
			instanceType: "unknown-sku",
			wantErr:      true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			nodeInfo, err := NewNodeInfo(context.Background(), tt.instanceType)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewNodeInfoInternal() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				assert.NotNil(t, nodeInfo)
				assert.Equal(t, instanceType, nodeInfo.SkuName)
				assert.NotEqual(t, 0, nodeInfo.MaxBurstBwMbps)
				assert.NotEqual(t, 0, nodeInfo.MaxBurstIops)
				assert.NotEqual(t, 0, nodeInfo.MaxBwMbps)
				assert.NotEqual(t, 0, nodeInfo.MaxIops)
				assert.NotEqual(t, 0, nodeInfo.MaxDataDiskCount)
				assert.NotEqual(t, 0, nodeInfo.VCpus)
			}
		})
	}
}
