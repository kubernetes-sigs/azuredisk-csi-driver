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
)

func TestIsValidPerfTuningMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want bool
	}{
		{
			name: "none mode should return true",
			mode: "none",
			want: true,
		},
		{
			name: "auto mode should return true",
			mode: "auto",
			want: true,
		},
		{
			name: "incorrent mode should return false",
			mode: "asdasd",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			if got := IsValidPerfTuningMode(tt.mode); got != tt.want {
				t.Errorf("IsValidPerfTuningMode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsValidPerfProfile(t *testing.T) {
	tests := []struct {
		name    string
		mode    string
		profile string
		want    bool
	}{
		{
			name:    "none mode should return true for any profile",
			mode:    "none",
			profile: "asdas",
			want:    true,
		},
		{
			name:    "auto mode should return false for incorrect profile",
			mode:    "auto",
			profile: "asdas",
			want:    false,
		},
		{
			name:    "auto mode should return true for default profile",
			mode:    "auto",
			profile: "default",
			want:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidPerfProfile(tt.mode, tt.profile); got != tt.want {
				t.Errorf("IsValidPerfProfile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsPerfTuningEnabled(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want bool
	}{
		{
			name: "none mode should return false",
			mode: "none",
			want: false,
		},
		{
			name: "auto mode should return true",
			mode: "auto",
			want: true,
		},
		{
			name: "incorrect mode should return false",
			mode: "blah",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPerfTuningEnabled(tt.mode); got != tt.want {
				t.Errorf("IsPerfTuningEnabled() = %v, want %v", got, tt.want)
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
		inAttributes       map[string]string
	}{
		{
			name:               "valid attributes should return all values",
			wantMode:           "none",
			wantProfile:        "default",
			wantAccountType:    "Premium_LRS",
			wantDiskSizeGibStr: "1024",
			wantDiskIopsStr:    "100",
			wantDiskBwMbpsStr:  "500",
			inAttributes:       map[string]string{perfTuningModeField: "none", perfProfileField: "default", skuNameField: "Premium_LRS", requestedSizeGib: "1024", diskIOPSReadWriteField: "100", diskMBPSReadWriteField: "500"},
		},
		{
			name:               "incorrect mode in attributes should return none mode",
			wantMode:           "none",
			wantProfile:        "default",
			wantAccountType:    "Premium_LRS",
			wantDiskSizeGibStr: "1024",
			wantDiskIopsStr:    "100",
			wantDiskBwMbpsStr:  "500",
			inAttributes:       map[string]string{perfTuningModeField: "blah", perfProfileField: "default", skuNameField: "Premium_LRS", requestedSizeGib: "1024", diskIOPSReadWriteField: "100", diskMBPSReadWriteField: "500"},
		},
		{
			name:               "incorrect profile should return default profile",
			wantMode:           "auto",
			wantProfile:        "default",
			wantAccountType:    "Premium_LRS",
			wantDiskSizeGibStr: "1024",
			wantDiskIopsStr:    "100",
			wantDiskBwMbpsStr:  "500",
			inAttributes:       map[string]string{perfTuningModeField: "auto", perfProfileField: "blah", skuNameField: "Premium_LRS", requestedSizeGib: "1024", diskIOPSReadWriteField: "100", diskMBPSReadWriteField: "500"},
		},
		{
			name:               "Empty attribute should return no value",
			wantMode:           "auto",
			wantProfile:        "default",
			wantAccountType:    "Premium_LRS",
			wantDiskSizeGibStr: "",
			wantDiskIopsStr:    "100",
			wantDiskBwMbpsStr:  "500",
			inAttributes:       map[string]string{perfTuningModeField: "auto", perfProfileField: "default", skuNameField: "Premium_LRS", requestedSizeGib: "", diskIOPSReadWriteField: "100", diskMBPSReadWriteField: "500"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMode, gotProfile, gotAccountType, gotDiskSizeGibStr, gotDiskIopsStr, gotDiskBwMbpsStr := GetDiskPerfAttributes(tt.inAttributes)
			if gotMode != tt.wantMode {
				t.Errorf("GetDiskPerfAttributes() gotMode = %v, want %v", gotMode, tt.wantMode)
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
			name:        "UltraSSD_LRS doesnt supports optimization",
			accountType: "UltraSSD_LRS",
			want:        false,
		},
		{
			name:        "Standard_LRS doesnt supports optimization",
			accountType: "Standard_LRS",
			want:        false,
		},
		{
			name:        "invalid account doesnt supports optimization",
			accountType: "asdad",
			want:        false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AccountSupportsPerfOptimization(tt.accountType); got != tt.want {
				t.Errorf("AccountSupportsPerfOptimization() = %v, want %v", got, tt.want)
			}
		})
	}
}
