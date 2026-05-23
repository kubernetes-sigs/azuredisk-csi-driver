//go:build windows
// +build windows

/*
Copyright 2025 The Kubernetes Authors.

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

package disk

import (
	"testing"
)

func TestGetNVMeLunFromPath(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantLUN   string
		wantError bool
	}{
		{
			name:    "valid path with NSID 1 (LUN 0)",
			path:    `\\?\scsi#disk&ven_nvme&prod_msft_nvme_accele#6&ca10229&0&000001#{53f56307-b6bf-11d0-94f2-00a0c91efb8b}`,
			wantLUN: "0",
		},
		{
			name:    "valid path with NSID 2 (LUN 1)",
			path:    `\\?\scsi#disk&ven_nvme&prod_msft_nvme_accele#6&ca10229&0&000002#{53f56307-b6bf-11d0-94f2-00a0c91efb8b}`,
			wantLUN: "1",
		},
		{
			name:    "valid path with NSID 10 (LUN 9)",
			path:    `\\?\scsi#disk&ven_nvme&prod_msft_nvme_accele#6&ca10229&0&000010#{53f56307-b6bf-11d0-94f2-00a0c91efb8b}`,
			wantLUN: "9",
		},
		{
			name:    "PNPDeviceID format (no trailing #)",
			path:    `SCSI\DISK&VEN_NVME&PROD_MSFT_NVME_ACCELE\6&CA10229&0&000001`,
			wantLUN: "0",
		},
		{
			name:    "PNPDeviceID format NSID 3 (LUN 2)",
			path:    `SCSI\DISK&VEN_NVME&PROD_MSFT_NVME_ACCELE\6&CA10229&0&000003`,
			wantLUN: "2",
		},
		{
			name:      "invalid path - no match",
			path:      `\\?\scsi#disk&ven_msft&prod_virtual_disk#1&2345&0&000000#{53f56307}`,
			wantLUN:   "",
			wantError: true,
		},
		{
			name:      "invalid path - NSID 0",
			path:      `\\?\scsi#disk&ven_nvme&prod_msft_nvme_accele#6&ca10229&0&000000#{53f56307}`,
			wantLUN:   "",
			wantError: true,
		},
		{
			name:      "empty path",
			path:      "",
			wantLUN:   "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lun, err := getNVMeLunFromPath(tt.path)
			if tt.wantError {
				if err == nil {
					t.Errorf("expected error but got lun=%s", lun)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if lun != tt.wantLUN {
				t.Errorf("got lun=%s, want %s", lun, tt.wantLUN)
			}
		})
	}
}

func TestIsNVMeDisk(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{`\\?\scsi#disk&ven_nvme&prod_msft_nvme_accele#6&ca10229&0&000001#{53f56307-b6bf-11d0-94f2-00a0c91efb8b}`, true},
		{`SCSI\DISK&VEN_NVME&PROD_MSFT_NVME_ACCELE\6&CA10229&0&000001`, true},
		{`\\?\scsi#disk&ven_msft&prod_virtual_disk#2&1f4adffe&0&000001#{53f56307-b6bf-11d0-94f2-00a0c91efb8b}`, false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isNVMeDisk(tt.path); got != tt.want {
			t.Errorf("isNVMeDisk(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
