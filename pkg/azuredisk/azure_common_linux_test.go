/*
Copyright 2022 The Kubernetes Authors.

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
	"runtime"
	"testing"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

func TestRescanAllVolumes(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skipf("skip test on GOOS=%s", runtime.GOOS)
	}
	err := rescanAllVolumes(azureutils.NewOSIOHandler())
	if err != nil {
		t.Errorf("rescanAllVolumes failed with error: %v", err)
	}
}

func TestRescanVolume(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skipf("skip test on GOOS=%s", runtime.GOOS)
	}

	tests := []struct {
		name       string
		devicePath string
	}{
		{
			name:       "rescan sdc device",
			devicePath: "/dev/sdc",
		},
		{
			name:       "rescan sdd device",
			devicePath: "/dev/sdd",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mockIO := azureutils.NewFakeIOHandler()
			err := rescanVolume(mockIO, test.devicePath)
			// FakeIOHandler always returns nil for WriteFile, so we expect no error
			if err != nil {
				t.Errorf("test(%s): unexpected error: %v", test.name, err)
			}
		})
	}
}
