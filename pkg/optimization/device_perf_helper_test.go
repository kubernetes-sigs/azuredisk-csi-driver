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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsValidPerfProfile(tt.profile); got != tt.want {
				t.Errorf("IsValidPerfProfile() = %v, want %v", got, tt.want)
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
			if got := accountSupportsPerfOptimization(tt.accountType); got != tt.want {
				t.Errorf("AccountSupportsPerfOptimization() = %v, want %v", got, tt.want)
			}
		})
	}
}
