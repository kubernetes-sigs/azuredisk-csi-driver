/*
Copyright 2026 The Kubernetes Authors.

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

func TestParseDiskLocation(t *testing.T) {
	tests := []struct {
		name     string
		location string
		wantOK   bool
		want     Location
	}{
		{
			name:     "OS disk on adapter 0 is skipped even though later fields are valid",
			location: "PCI Slot 3 : Adapter 0 : Port 0 : Target 0 : LUN 0",
			wantOK:   false,
		},
		{
			name:     "temp disk on adapter 0 is skipped",
			location: "PCI Slot 3 : Adapter 0 : Port 0 : Target 1 : LUN 0",
			wantOK:   false,
		},
		{
			name:     "Gen1 IDE OS disk on adapter 0 is skipped",
			location: "Integrated : Adapter 0 : Port 0 : Target 0 : LUN 0",
			wantOK:   false,
		},
		{
			name:     "data disk at LUN 0",
			location: "PCI Slot 0 : Adapter 1 : Port 0 : Target 0 : LUN 0",
			wantOK:   true,
			want:     Location{Adapter: "1", Target: "0", LUNID: "0"},
		},
		{
			name:     "data disk at LUN 5",
			location: "PCI Slot 0 : Adapter 1 : Port 0 : Target 0 : LUN 5",
			wantOK:   true,
			want:     Location{Adapter: "1", Target: "0", LUNID: "5"},
		},
		{
			name:     "fewer than five fields",
			location: "Adapter 1 : Target 0 : LUN 0",
			wantOK:   false,
		},
		{
			name:     "no key/value fields",
			location: "a b c : d e f : g h i : j k l : m n o",
			wantOK:   false,
		},
		{
			name:     "empty location",
			location: "",
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseDiskLocation(0, tt.location)
			if ok != tt.wantOK {
				t.Fatalf("parseDiskLocation(%q) ok = %v, want %v (location %+v)", tt.location, ok, tt.wantOK, got)
			}
			if ok && got != tt.want {
				t.Errorf("parseDiskLocation(%q) = %+v, want %+v", tt.location, got, tt.want)
			}
		})
	}
}
