//go:build linux
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

package optimization

import (
	"os"
	"path"
	"strings"
	"testing"
)

func Test_OptimizeDiskPerformance(t *testing.T) {
	deviceHelper := NewDeviceHelper()
	tests := []struct {
		name                string
		nodeInfo            *NodeInfo
		devicePath          string
		perfProfile         string
		accountType         string
		diskSizeGibStr      string
		diskIopsStr         string
		diskBwMbpsStr       string
		wantErr             bool
		blockDeviceRootPath string
		mkdirPath           []string
	}{
		{
			name:                "could not have queue dir for device should return error",
			nodeInfo:            &NodeInfo{SkuName: "Standard_DS14", MaxBurstIops: 51200, MaxIops: 51200, MaxBwMbps: 512, MaxBurstBwMbps: 512},
			devicePath:          "sda1",
			perfProfile:         "basic",
			accountType:         "Premium_LRS",
			diskSizeGibStr:      "512",
			diskIopsStr:         "100",
			diskBwMbpsStr:       "100",
			wantErr:             true,
			blockDeviceRootPath: ".",
			mkdirPath:           []string{""},
		},
		{
			name:                "could not have queue/iosched dir for device should return error",
			nodeInfo:            &NodeInfo{SkuName: "Standard_DS14", MaxBurstIops: 51200, MaxIops: 51200, MaxBwMbps: 512, MaxBurstBwMbps: 512},
			devicePath:          "sda2",
			perfProfile:         "basic",
			accountType:         "Premium_LRS",
			diskSizeGibStr:      "512",
			diskIopsStr:         "100",
			diskBwMbpsStr:       "100",
			wantErr:             true,
			blockDeviceRootPath: ".",
			mkdirPath:           []string{"queue"},
		},
		{
			name:                "could not have device dir for device should return error",
			nodeInfo:            &NodeInfo{SkuName: "Standard_DS14", MaxBurstIops: 51200, MaxIops: 51200, MaxBwMbps: 512, MaxBurstBwMbps: 512},
			devicePath:          "sda3",
			perfProfile:         "basic",
			accountType:         "Premium_LRS",
			diskSizeGibStr:      "512",
			diskIopsStr:         "100",
			diskBwMbpsStr:       "100",
			wantErr:             true,
			blockDeviceRootPath: ".",
			mkdirPath:           []string{"queue/iosched"},
		},
		{
			name:                "valid device",
			nodeInfo:            &NodeInfo{SkuName: "Standard_DS14", MaxBurstIops: 51200, MaxIops: 51200, MaxBwMbps: 512, MaxBurstBwMbps: 512},
			devicePath:          "sda4",
			perfProfile:         "basic",
			accountType:         "Premium_LRS",
			diskSizeGibStr:      "512",
			diskIopsStr:         "100",
			diskBwMbpsStr:       "100",
			wantErr:             false,
			blockDeviceRootPath: ".",
			mkdirPath:           []string{"queue/iosched", "device"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.blockDeviceRootPath != "" {
				deviceHelper.blockDeviceRootPath = tt.blockDeviceRootPath
				for _, mkdir := range tt.mkdirPath {
					err := os.MkdirAll(path.Join(tt.blockDeviceRootPath, tt.devicePath, mkdir), os.ModePerm)
					if err != nil {
						t.Errorf("mkdir err: %v", err)
					} else {
						defer os.RemoveAll(path.Join(tt.blockDeviceRootPath, tt.devicePath))
					}
				}
			}
			if deviceHelper.DiskSupportsPerfOptimization(tt.perfProfile, tt.accountType) {
				if err := deviceHelper.OptimizeDiskPerformance(tt.nodeInfo, tt.devicePath, tt.perfProfile, tt.accountType, tt.diskSizeGibStr, tt.diskIopsStr, tt.diskBwMbpsStr); (err != nil) != tt.wantErr {
					t.Errorf("DeviceHelper.OptimizeDiskPerformance() error = %v, wantErr %v", err, tt.wantErr)
				}
			}
		})
	}
}

func Test_getOptimalDeviceSettings(t *testing.T) {
	accountType := "Premium_LRS"
	tier := "Premium"
	sizeP20 := "P20"
	sizeP30 := "P30"
	diskSkus := make(map[string]map[string]DiskSkuInfo)
	diskSkus[strings.ToLower(accountType)] = map[string]DiskSkuInfo{}
	diskSkus[strings.ToLower(accountType)][strings.ToLower(sizeP20)] = DiskSkuInfo{StorageAccountType: accountType, StorageTier: tier, DiskSize: sizeP20, MaxIops: 100, MaxBurstIops: 100, MaxBwMbps: 500, MaxBurstBwMbps: 500, MaxSizeGiB: 1024}
	diskSkus[strings.ToLower(accountType)][strings.ToLower(sizeP30)] = DiskSkuInfo{StorageAccountType: accountType, StorageTier: tier, DiskSize: sizeP30, MaxIops: 200, MaxBurstIops: 200, MaxBwMbps: 1000, MaxBurstBwMbps: 1000, MaxSizeGiB: 4096}
	skuName := "Standard_DS14"
	nodeInfo := &NodeInfo{SkuName: skuName, MaxBurstIops: 51200, MaxIops: 51200, MaxBwMbps: 512, MaxBurstBwMbps: 512}
	nodeInfoNoCapabilityVM := &NodeInfo{SkuName: skuName, MaxBurstIops: 0, MaxIops: 0, MaxBwMbps: 0, MaxBurstBwMbps: 0}

	tests := []struct {
		name             string
		perfProfile      string
		accountType      string
		DiskSizeGibStr   string
		diskIopsStr      string
		diskBwMbpsStr    string
		wantQueueDepth   string
		wantNrRequests   string
		wantScheduler    string
		wantMaxSectorsKb string
		wantReadAheadKb  string
		wantErr          bool
		node             *NodeInfo
	}{
		{
			name:           "Should return valid disk perf settings",
			perfProfile:    "basic",
			accountType:    "Premium_LRS",
			DiskSizeGibStr: "512",
			diskIopsStr:    "100",
			diskBwMbpsStr:  "100",
			wantScheduler:  "mq-deadline",
			wantErr:        false,
			node:           nodeInfo,
		},
		{
			name:           "Should return valid disk perf settings with no capability published VM",
			perfProfile:    "basic",
			accountType:    "Premium_LRS",
			DiskSizeGibStr: "512",
			diskIopsStr:    "100",
			diskBwMbpsStr:  "100",
			wantScheduler:  "mq-deadline",
			wantErr:        false,
			node:           nodeInfoNoCapabilityVM,
		},
		{
			name:           "Should return error if matching disk sku is not found",
			perfProfile:    "basic",
			accountType:    "Premium_LRS",
			DiskSizeGibStr: "512123123123123213123",
			diskIopsStr:    "100",
			diskBwMbpsStr:  "100",
			wantScheduler:  "mq-deadline",
			wantErr:        true,
			node:           nodeInfo,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotQueueDepth, gotNrRequests, gotScheduler, gotMaxSectorsKb, gotReadAheadKb, err := getOptimalDeviceSettings(tt.node, diskSkus, tt.perfProfile, tt.accountType, tt.DiskSizeGibStr, tt.diskIopsStr, tt.diskBwMbpsStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("getOptimalDeviceSettings() error = %v, wantErr %v", err, tt.wantErr)
				return
			} else if !tt.wantErr {
				if gotQueueDepth == "" {
					t.Errorf("getOptimalDeviceSettings() failed for gotQueueDepth")
				}
				if gotNrRequests == "" {
					t.Errorf("getOptimalDeviceSettings() failed for gotNrRequests")
				}
				if gotScheduler != tt.wantScheduler {
					t.Errorf("getOptimalDeviceSettings() failed for gotScheduler = %v", gotScheduler)
				}
				if gotMaxSectorsKb == "" {
					t.Errorf("getOptimalDeviceSettings() failed for gotMaxSectorsKb")
				}
				if gotReadAheadKb == "" {
					t.Errorf("getOptimalDeviceSettings() failed for gotReadAheadKb")
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
	diskSkus[strings.ToLower(accountType)][strings.ToLower(sizeP20)] = DiskSkuInfo{StorageAccountType: accountType, StorageTier: tier, DiskSize: sizeP20, MaxIops: 100, MaxBurstIops: 100, MaxBwMbps: 500, MaxBurstBwMbps: 500, MaxSizeGiB: 1024}
	diskSkus[strings.ToLower(accountType)][strings.ToLower(sizeP30)] = DiskSkuInfo{StorageAccountType: accountType, StorageTier: tier, DiskSize: sizeP30, MaxIops: 200, MaxBurstIops: 200, MaxBwMbps: 1000, MaxBurstBwMbps: 1000, MaxSizeGiB: 4096}

	tests := []struct {
		name           string
		accountType    string
		DiskSizeGibStr string
		diskIopsStr    string
		diskBwMbpsStr  string
		wantDiskSize   string
		wantErr        bool
		skus           map[string]map[string]DiskSkuInfo
	}{
		{
			name:           "Should get matching sku when request is less that sku size",
			accountType:    "Premium_LRS",
			DiskSizeGibStr: "1500",
			diskIopsStr:    "150",
			diskBwMbpsStr:  "750",
			wantDiskSize:   sizeP30,
			wantErr:        false,
			skus:           diskSkus,
		},
		{
			name:           "Should get smaller sku when multiple skus match",
			accountType:    "Premium_LRS",
			DiskSizeGibStr: "500",
			diskIopsStr:    "50",
			diskBwMbpsStr:  "50",
			wantDiskSize:   sizeP20,
			wantErr:        false,
			skus:           diskSkus,
		},
		{
			name:           "Should get error if disk size is invalid",
			accountType:    "Premium_LRS",
			DiskSizeGibStr: "Gib",
			diskIopsStr:    "50",
			diskBwMbpsStr:  "50",
			wantDiskSize:   sizeP20,
			wantErr:        true,
			skus:           diskSkus,
		},
		{
			name:           "Should get smatching sku if iops and bw are not provided",
			accountType:    "Premium_LRS",
			DiskSizeGibStr: "500",
			diskIopsStr:    "blah",
			diskBwMbpsStr:  "blah",
			wantDiskSize:   sizeP20,
			wantErr:        false,
			skus:           diskSkus,
		},
		{
			name:           "Should get error when no skus are passed.",
			accountType:    "Premium_LRS",
			DiskSizeGibStr: "500",
			diskIopsStr:    "blah",
			diskBwMbpsStr:  "blah", wantDiskSize: sizeP20,
			wantErr: true,
			skus:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMatchingSku, err := getMatchingDiskSku(tt.skus, tt.accountType, tt.DiskSizeGibStr, tt.diskIopsStr, tt.diskBwMbpsStr)
			if (err != nil) != tt.wantErr {
				t.Errorf("getMatchingDiskSku() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if !tt.wantErr && !strings.EqualFold(gotMatchingSku.DiskSize, tt.wantDiskSize) {
				t.Errorf("getMatchingDiskSku() = %s, want %s", gotMatchingSku.DiskSize, tt.wantDiskSize)
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
		DiskSizeGb int
		diskIops   int
		diskBwMbps int
		want       bool
		sku        *DiskSkuInfo
	}{
		{
			name:       "Sku should match demand which is same as limits",
			DiskSizeGb: 1023,
			diskIops:   99,
			diskBwMbps: 499,
			want:       true,
			sku:        &DiskSkuInfo{StorageAccountType: accountType, StorageTier: tier, DiskSize: size, MaxIops: 100, MaxBwMbps: 500, MaxSizeGiB: 1024},
		},
		{
			name:       "Sku should match demand which is less than limits",
			DiskSizeGb: 1024,
			diskIops:   100,
			diskBwMbps: 500,
			want:       true,
			sku:        &DiskSkuInfo{StorageAccountType: accountType, StorageTier: tier, DiskSize: size, MaxIops: 100, MaxBwMbps: 500, MaxSizeGiB: 1024},
		},
		{
			name:       "Sku should  not match demand which is more than limits",
			DiskSizeGb: 1025,
			diskIops:   101,
			diskBwMbps: 501,
			want:       false,
			sku:        &DiskSkuInfo{StorageAccountType: accountType, StorageTier: tier, DiskSize: size, MaxIops: 100, MaxBwMbps: 500, MaxSizeGiB: 1024},
		},
		{
			name:       "nil Sku should return false",
			DiskSizeGb: 1025,
			diskIops:   101,
			diskBwMbps: 501,
			want:       false,
			sku:        nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := meetsRequest(tt.sku, tt.DiskSizeGb, tt.diskIops, tt.diskBwMbps); got != tt.want {
				t.Errorf("meetsRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getDeviceName(t *testing.T) {
	tests := []struct {
		name    string
		lunPath string
		wantErr bool
	}{
		{
			name:    "return error for invalid file",
			lunPath: "blah",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := getDeviceName(tt.lunPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("getDeviceName() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
		})
	}
}

func Test_echoToFile(t *testing.T) {
	filePath := "fake-echo-file"
	defer os.Remove(filePath)
	tests := []struct {
		name     string
		content  string
		filePath string
		wantErr  bool
	}{
		{
			name:     "echo should succeed",
			content:  "10",
			filePath: filePath,
			wantErr:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := echoToFile(tt.content, tt.filePath); (err != nil) != tt.wantErr {
				t.Errorf("echoToFile() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
