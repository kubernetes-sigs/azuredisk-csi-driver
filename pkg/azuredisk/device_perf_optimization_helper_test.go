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

	constants "sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

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
			wantProfile:        "basic",
			wantAccountType:    "Premium_LRS",
			wantDiskSizeGibStr: "1024",
			wantDiskIopsStr:    "100",
			wantDiskBwMbpsStr:  "500",
			wantErr:            false,
			inAttributes:       map[string]string{constants.PerfProfileField: "basic", constants.SkuNameField: "Premium_LRS", constants.RequestedSizeGib: "1024", constants.DiskIOPSReadWriteField: "100", constants.DiskMBPSReadWriteField: "500"},
		},
		{
			name:               "incorrect profile should return error",
			wantProfile:        "",
			wantAccountType:    "Premium_LRS",
			wantDiskSizeGibStr: "1024",
			wantDiskIopsStr:    "100",
			wantDiskBwMbpsStr:  "500",
			wantErr:            true,
			inAttributes:       map[string]string{constants.PerfProfileField: "blah", constants.SkuNameField: "Premium_LRS", constants.RequestedSizeGib: "1024", constants.DiskIOPSReadWriteField: "100", constants.DiskMBPSReadWriteField: "500"},
		},
		{
			name:               "No profile specified should return none profile",
			wantProfile:        "none",
			wantAccountType:    "Premium_LRS",
			wantDiskSizeGibStr: "1024",
			wantDiskIopsStr:    "100",
			wantDiskBwMbpsStr:  "500",
			wantErr:            false,
			inAttributes:       map[string]string{constants.SkuNameField: "Premium_LRS", constants.RequestedSizeGib: "1024", constants.DiskIOPSReadWriteField: "100", constants.DiskMBPSReadWriteField: "500"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotProfile, gotAccountType, gotDiskSizeGibStr, gotDiskIopsStr, gotDiskBwMbpsStr, gotErr := getDiskPerfAttributes(tt.inAttributes)

			if (gotErr != nil) != tt.wantErr {
				t.Errorf("GetDiskPerfAttributes() gotErr = %v, want %v", gotErr, tt.wantErr)
			}

			if !tt.wantErr {
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
