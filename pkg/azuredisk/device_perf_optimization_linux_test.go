// +build linux

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
	"strings"
	"testing"
)

func Test_getOptimalDeviceSettings(t *testing.T) {

	accountType := "Premium_LRS"
	tier := "Premium"
	sizeP20 := "P20"
	sizeP30 := "P30"
	diskSkus := make(map[string]map[string]DiskSkuInfo)
	diskSkus[strings.ToLower(accountType)] = map[string]DiskSkuInfo{}
	diskSkus[strings.ToLower(accountType)][strings.ToLower(sizeP20)] = DiskSkuInfo{storageAccountType: &accountType, storageTier: &tier, diskSize: &sizeP20, maxIops: 100, maxBurstIops: 100, maxBwMbps: 500, maxBurstBwMbps: 500, maxSizeGiB: 1024}
	diskSkus[strings.ToLower(accountType)][strings.ToLower(sizeP30)] = DiskSkuInfo{storageAccountType: &accountType, storageTier: &tier, diskSize: &sizeP30, maxIops: 200, maxBurstIops: 200, maxBwMbps: 1000, maxBurstBwMbps: 1000, maxSizeGiB: 4096}
	skuName := "Standard_DS14"
	zone := "1"
	region := "eastus"
	nodeInfo := &NodeInfo{skuName: &skuName, zone: &zone, region: &region, maxBurstIops: 51200, maxIops: 51200, maxBwMbps: 512, maxBurstBwMbps: 512}

	tests := []struct {
		name             string
		tuningMode       string
		perfProfile      string
		accountType      string
		diskSizeGibStr   string
		diskIopsStr      string
		diskBwMbpsStr    string
		wantQueueDepth   string
		wantNrRequests   string
		wantScheduler    string
		wantMaxSectorsKb string
		wantReadAheadKb  string
		wantErr          bool
	}{
		{
			name:           "Should return valid disk perf settings",
			tuningMode:     "auto",
			perfProfile:    "default",
			accountType:    "Premium_LRS",
			diskSizeGibStr: "512",
			diskIopsStr:    "100",
			diskBwMbpsStr:  "100",
			wantScheduler:  "mq-deadline",
			wantErr:        false,
		},
		{
			name:           "Should return error if matching disk sku is not found",
			tuningMode:     "auto",
			perfProfile:    "default",
			accountType:    "Premium_LRS",
			diskSizeGibStr: "512123123123123213123",
			diskIopsStr:    "100",
			diskBwMbpsStr:  "100",
			wantScheduler:  "mq-deadline",
			wantErr:        true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotQueueDepth, gotNrRequests, gotScheduler, gotMaxSectorsKb, gotReadAheadKb, err := getOptimalDeviceSettings(nodeInfo, diskSkus, tt.tuningMode, tt.perfProfile, tt.accountType, tt.diskSizeGibStr, tt.diskIopsStr, tt.diskBwMbpsStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("getOptimalDeviceSettings() error = %v, wantErr %v", err, tt.wantErr)
				return
			} else if !tt.wantErr {
				if gotQueueDepth == "" {
					t.Errorf("getOptimalDeviceSettings() gotQueueDepth")
				}
				if gotNrRequests == "" {
					t.Errorf("getOptimalDeviceSettings() gotNrRequests")
				}
				if gotScheduler != tt.wantScheduler {
					t.Errorf("getOptimalDeviceSettings() gotScheduler = %v", gotScheduler)
				}
				if gotMaxSectorsKb == "" {
					t.Errorf("getOptimalDeviceSettings() gotMaxSectorsKb")
				}
				if gotReadAheadKb == "" {
					t.Errorf("getOptimalDeviceSettings() gotReadAheadKb")
				}
			}
		})
	}
}

func Test_getMatchingDiskSku(t *testing.T) {
	accountType := "Premium_LRS"
	tier := "Premium"
	sizeP20 := "P20"
	sizeP30 := "P30"
	diskSkus := make(map[string]map[string]DiskSkuInfo)
	diskSkus[strings.ToLower(accountType)] = map[string]DiskSkuInfo{}
	diskSkus[strings.ToLower(accountType)][strings.ToLower(sizeP20)] = DiskSkuInfo{storageAccountType: &accountType, storageTier: &tier, diskSize: &sizeP20, maxIops: 100, maxBurstIops: 100, maxBwMbps: 500, maxBurstBwMbps: 500, maxSizeGiB: 1024}
	diskSkus[strings.ToLower(accountType)][strings.ToLower(sizeP30)] = DiskSkuInfo{storageAccountType: &accountType, storageTier: &tier, diskSize: &sizeP30, maxIops: 200, maxBurstIops: 200, maxBwMbps: 1000, maxBurstBwMbps: 1000, maxSizeGiB: 4096}

	tests := []struct {
		name           string
		accountType    string
		diskSizeGibStr string
		diskIopsStr    string
		diskBwMbpsStr  string
		wantDiskSize   string
		wantErr        bool
		skus           map[string]map[string]DiskSkuInfo
	}{
		{
			name:           "Should get matching sku when request is less that sku size",
			accountType:    "Premium_LRS",
			diskSizeGibStr: "1500",
			diskIopsStr:    "150",
			diskBwMbpsStr:  "750",
			wantDiskSize:   sizeP30,
			wantErr:        false,
			skus:           diskSkus,
		},
		{
			name:           "Should get smaller sku when multiple skus match",
			accountType:    "Premium_LRS",
			diskSizeGibStr: "500",
			diskIopsStr:    "50",
			diskBwMbpsStr:  "50",
			wantDiskSize:   sizeP20,
			wantErr:        false,
			skus:           diskSkus,
		},
		{
			name:           "Should get error if disk size is invalid",
			accountType:    "Premium_LRS",
			diskSizeGibStr: "Gib",
			diskIopsStr:    "50",
			diskBwMbpsStr:  "50",
			wantDiskSize:   sizeP20,
			wantErr:        true,
			skus:           diskSkus,
		},
		{
			name:           "Should get smatching sku if iops and bw are not provided",
			accountType:    "Premium_LRS",
			diskSizeGibStr: "500",
			diskIopsStr:    "blah",
			diskBwMbpsStr:  "blah",
			wantDiskSize:   sizeP20,
			wantErr:        false,
			skus:           diskSkus,
		},
		{
			name:           "Should get error when no skus are passed.",
			accountType:    "Premium_LRS",
			diskSizeGibStr: "500",
			diskIopsStr:    "blah",
			diskBwMbpsStr:  "blah", wantDiskSize: sizeP20,
			wantErr: true,
			skus:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMatchingSku, err := getMatchingDiskSku(tt.skus, tt.accountType, tt.diskSizeGibStr, tt.diskIopsStr, tt.diskBwMbpsStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("getMatchingDiskSku() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && !strings.EqualFold(*gotMatchingSku.diskSize, tt.wantDiskSize) {
				t.Errorf("getMatchingDiskSku() = %s, want %s", *gotMatchingSku.diskSize, tt.wantDiskSize)
			}
		})
	}
}

func Test_meetsRequest(t *testing.T) {

	accountType := "Premium_LRS"
	tier := "Premium"
	size := "P20"

	tests := []struct {
		name       string
		diskSizeGb int
		diskIops   int
		diskBwMbps int
		want       bool
		sku        *DiskSkuInfo
	}{
		{
			name:       "Sku should match demand which is same as limits",
			diskSizeGb: 1023,
			diskIops:   99,
			diskBwMbps: 499,
			want:       true,
			sku:        &DiskSkuInfo{storageAccountType: &accountType, storageTier: &tier, diskSize: &size, maxIops: 100, maxBwMbps: 500, maxSizeGiB: 1024},
		},
		{
			name:       "Sku should match demand which is less than limits",
			diskSizeGb: 1024,
			diskIops:   100,
			diskBwMbps: 500,
			want:       true,
			sku:        &DiskSkuInfo{storageAccountType: &accountType, storageTier: &tier, diskSize: &size, maxIops: 100, maxBwMbps: 500, maxSizeGiB: 1024},
		},
		{
			name:       "Sku should  not match demand which is more than limits",
			diskSizeGb: 1025,
			diskIops:   101,
			diskBwMbps: 501,
			want:       false,
			sku:        &DiskSkuInfo{storageAccountType: &accountType, storageTier: &tier, diskSize: &size, maxIops: 100, maxBwMbps: 500, maxSizeGiB: 1024},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := meetsRequest(tt.sku, tt.diskSizeGb, tt.diskIops, tt.diskBwMbps); got != tt.want {
				t.Errorf("meetsRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}
