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
	"strings"
	"testing"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

func TestIsValidPerfProfile(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		want    bool
	}{
		{
			name:    "none profile should return true",
			profile: "none",
			want:    true,
		},
		{
			name:    "incorrect profile should return false",
			profile: "asdas",
			want:    false,
		},
		{
			name:    "default profile should return true",
			profile: "basic",
			want:    true,
		},
		{
			name:    "advanced profile should return true",
			profile: "advanced",
			want:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidPerfProfile(tt.profile); got != tt.want {
				t.Errorf("IsValidPerfProfile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetDiskPerfAttributes(t *testing.T) {
	tests := []struct {
		name               string
		wantMode           string
		wantProfile        string
		wantAccountType    string
		wantDiskSizeGibStr string
		wantDiskIopsStr    string
		wantDiskBwMbpsStr  string
		wantErr            bool
		inAttributes       map[string]string
	}{
		{
			name:               "valid attributes should return all values",
			wantProfile:        "advanced",
			wantAccountType:    "Premium_LRS",
			wantDiskSizeGibStr: "1024",
			wantDiskIopsStr:    "100",
			wantDiskBwMbpsStr:  "500",
			wantErr:            false,
			inAttributes:       map[string]string{consts.PerfProfileField: "advanced", consts.SkuNameField: "Premium_LRS", consts.RequestedSizeGib: "1024", consts.DiskIOPSReadWriteField: "100", consts.DiskMBPSReadWriteField: "500", consts.DeviceSettingsKeyPrefix + "queue/read_ahead_kb": "8", consts.DeviceSettingsKeyPrefix + "queue/nomerges": "0"},
		},
		{
			name:               "incorrect profile should return error",
			wantProfile:        "",
			wantAccountType:    "Premium_LRS",
			wantDiskSizeGibStr: "1024",
			wantDiskIopsStr:    "100",
			wantDiskBwMbpsStr:  "500",
			wantErr:            true,
			inAttributes:       map[string]string{consts.PerfProfileField: "blah", consts.SkuNameField: "Premium_LRS", consts.RequestedSizeGib: "1024", consts.DiskIOPSReadWriteField: "100", consts.DiskMBPSReadWriteField: "500"},
		},
		{
			name:               "No profile specified should return none profile",
			wantProfile:        "none",
			wantAccountType:    "Premium_LRS",
			wantDiskSizeGibStr: "1024",
			wantDiskIopsStr:    "100",
			wantDiskBwMbpsStr:  "500",
			wantErr:            false,
			inAttributes:       map[string]string{consts.SkuNameField: "Premium_LRS", consts.RequestedSizeGib: "1024", consts.DiskIOPSReadWriteField: "100", consts.DiskMBPSReadWriteField: "500"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProfile, gotAccountType, gotDiskSizeGibStr, gotDiskIopsStr, gotDiskBwMbpsStr, settings, gotErr := GetDiskPerfAttributes(tt.inAttributes)

			if (gotErr != nil) != tt.wantErr {
				t.Errorf("GetDiskPerfAttributes() gotErr = %v, want %v", gotErr, tt.wantErr)
			}

			if !tt.wantErr {
				if strings.EqualFold(tt.wantProfile, "advanced") && len(settings) == 0 {
					t.Errorf("GetDiskPerfAttributes() setting is not parsed")
				}
				if gotProfile != tt.wantProfile {
					t.Errorf("GetDiskPerfAttributes() gotProfile = %v, want %v", gotProfile, tt.wantProfile)
				}
				if gotAccountType != tt.wantAccountType {
					t.Errorf("GetDiskPerfAttributes() gotAccountType = %v, want %v", gotAccountType, tt.wantAccountType)
				}
				if gotDiskSizeGibStr != tt.wantDiskSizeGibStr {
					t.Errorf("GetDiskPerfAttributes() gotDiskSizeGibStr = %v, want %v", gotDiskSizeGibStr, tt.wantDiskSizeGibStr)
				}
				if gotDiskIopsStr != tt.wantDiskIopsStr {
					t.Errorf("GetDiskPerfAttributes() gotDiskIopsStr = %v, want %v", gotDiskIopsStr, tt.wantDiskIopsStr)
				}
				if gotDiskBwMbpsStr != tt.wantDiskBwMbpsStr {
					t.Errorf("GetDiskPerfAttributes() gotDiskBwMbpsStr = %v, want %v", gotDiskBwMbpsStr, tt.wantDiskBwMbpsStr)
				}
			}
		})
	}
}

func TestIsPerfTuningEnabled(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		want    bool
	}{
		{
			name:    "none profile should return false",
			profile: "none",
			want:    false,
		},
		{
			name:    "default profile should return true",
			profile: "basic",
			want:    true,
		},
		{
			name:    "incorrect profile should return false",
			profile: "blah",
			want:    false,
		},
		{
			name:    "advanced profile should return true",
			profile: "advanced",
			want:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isPerfTuningEnabled(tt.profile); got != tt.want {
				t.Errorf("IsPerfTuningEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAccountSupportsPerfOptimization(t *testing.T) {
	tests := []struct {
		name        string
		accountType string
		want        bool
	}{
		{
			name:        "Premium_LRS supports optimization",
			accountType: "Premium_LRS",
			want:        true,
		},
		{
			name:        "StandardSSD_LRS supports optimization",
			accountType: "StandardSSD_LRS",
			want:        true,
		},
		{
			name:        "UltraSSD_LRS doesn't supports optimization",
			accountType: "UltraSSD_LRS",
			want:        false,
		},
		{
			name:        "Standard_LRS doesn't supports optimization",
			accountType: "Standard_LRS",
			want:        false,
		},
		{
			name:        "invalid account doesn't supports optimization",
			accountType: "asdad",
			want:        false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := accountSupportsPerfOptimization(tt.accountType); got != tt.want {
				t.Errorf("AccountSupportsPerfOptimization() = %v, want %v", got, tt.want)
			}
		})
	}
}
