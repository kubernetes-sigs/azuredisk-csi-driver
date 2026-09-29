//go:build windows
// +build windows

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

package volume

import "testing"

func TestUnallocatedAfter(t *testing.T) {
	const gib = uint64(1 << 30)
	tests := []struct {
		name                   string
		offset, partSize, disk uint64
		want                   uint64
		skipsGetSupportedSize  bool
	}{
		{
			// Values from a 128 GiB data disk whose partition already fills the disk:
			// only the GPT backup header area is left.
			name: "partition fills disk", offset: 16777216, partSize: 137421127680, disk: 128 * gib,
			want: 128*gib - 16777216 - 137421127680, skipsGetSupportedSize: true,
		},
		{
			name: "disk expanded from 128 to 256 GiB", offset: 16777216, partSize: 137421127680, disk: 256 * gib,
			want: 256*gib - 16777216 - 137421127680, skipsGetSupportedSize: false,
		},
		{
			name: "just under the minimum resize size", offset: 0, partSize: 10 * gib, disk: 10*gib + minimumResizeSize - 1,
			want: minimumResizeSize - 1, skipsGetSupportedSize: true,
		},
		{
			name: "exactly the minimum resize size", offset: 0, partSize: 10 * gib, disk: 10*gib + minimumResizeSize,
			want: minimumResizeSize, skipsGetSupportedSize: false,
		},
		{
			name: "partition end past reported disk size", offset: 1024, partSize: 10 * gib, disk: 10 * gib,
			want: 0, skipsGetSupportedSize: true,
		},
	}
	for _, test := range tests {
		got := unallocatedAfter(test.offset, test.partSize, test.disk)
		if got != test.want {
			t.Errorf("%s: unallocatedAfter() = %d, want %d", test.name, got, test.want)
		}
		if skips := got < minimumResizeSize; skips != test.skipsGetSupportedSize {
			t.Errorf("%s: skips GetSupportedSize = %v, want %v", test.name, skips, test.skipsGetSupportedSize)
		}
	}
}
