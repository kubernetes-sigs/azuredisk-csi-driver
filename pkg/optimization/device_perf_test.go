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

	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
)

func TestSafeDeviceHelper_DeviceSupportsPerfOptimization(t *testing.T) {
	tests := []struct {
		name            string
		diskPerfProfile string
		diskAccountType string
		want            bool
	}{
		{
			name:            "invalid profile should return false",
			diskPerfProfile: "blah",
			diskAccountType: "premium_lrs",
			want:            false,
		},
		{
			name:            "ultrassd_lrs account should return false",
			diskPerfProfile: "basic",
			diskAccountType: "ultrassd_lrs",
			want:            false,
		},
		{
			name:            "invalid account type should return false",
			diskPerfProfile: "blah",
			diskAccountType: "premium_lrs",
			want:            false,
		},
		{
			name:            "none profile should return false",
			diskPerfProfile: "none",
			diskAccountType: "premium_lrs",
			want:            false,
		},
		{
			name:            "valid profile and account should return true",
			diskPerfProfile: "basic",
			diskAccountType: "Premium_lrs",
			want:            util.IsLinuxOS(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dh := NewSafeDeviceHelper()
			if got := dh.DeviceSupportsPerfOptimization(tt.diskPerfProfile, tt.diskAccountType); got != tt.want {
				t.Errorf("SafeDeviceHelper.DeviceSupportsPerfOptimization() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeviceHelper_OptimizeDiskPerformance(t *testing.T) {
	deviceHelper := NewSafeDeviceHelper()
	tests := []struct {
		name           string
		nodeInfo       *NodeInfo
		devicePath     string
		perfProfile    string
		accountType    string
		diskSizeGibStr string
		diskIopsStr    string
		diskBwMbpsStr  string
		wantErr        bool
	}{
		{
			name:           "nil node should return error",
			nodeInfo:       nil,
			devicePath:     "blah",
			perfProfile:    "basic",
			accountType:    "standardssd_lrs",
			diskSizeGibStr: "twenty",
			diskIopsStr:    "100",
			diskBwMbpsStr:  "100",
			wantErr:        true,
		},
		{
			name:           "invalid sku spec should return error",
			nodeInfo:       &NodeInfo{},
			devicePath:     "blah",
			perfProfile:    "basic",
			accountType:    "standardssd_lrs",
			diskSizeGibStr: "twenty",
			diskIopsStr:    "100",
			diskBwMbpsStr:  "100",
			wantErr:        true,
		},
		{
			name:           "invalid device path should return error",
			nodeInfo:       &NodeInfo{SkuName: "Standard_DS14", MaxBurstIops: 51200, MaxIops: 51200, MaxBwMbps: 512, MaxBurstBwMbps: 512},
			devicePath:     "blah",
			perfProfile:    "basic",
			accountType:    "Premium_LRS",
			diskSizeGibStr: "512",
			diskIopsStr:    "100",
			diskBwMbpsStr:  "100",
			wantErr:        true,
		},
		{
			name:           "could not set max_sectors_kb for device should return error",
			nodeInfo:       &NodeInfo{SkuName: "Standard_DS14", MaxBurstIops: 51200, MaxIops: 51200, MaxBwMbps: 512, MaxBurstBwMbps: 512},
			devicePath:     ".",
			perfProfile:    "basic",
			accountType:    "Premium_LRS",
			diskSizeGibStr: "512",
			diskIopsStr:    "100",
			diskBwMbpsStr:  "100",
			wantErr:        true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if deviceHelper.DiskSupportsPerfOptimization(tt.perfProfile, tt.accountType) {
				if err := deviceHelper.OptimizeDiskPerformance(tt.nodeInfo, tt.devicePath, tt.perfProfile, tt.accountType, tt.diskSizeGibStr, tt.diskIopsStr, tt.diskBwMbpsStr); (err != nil) != tt.wantErr {
					t.Errorf("DeviceHelper.OptimizeDiskPerformance() error = %v, wantErr %v", err, tt.wantErr)
				}
			}
		})
	}
}
