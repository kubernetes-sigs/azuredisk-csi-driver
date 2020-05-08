/*
Copyright 2019 The Kubernetes Authors.

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
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"

	"k8s.io/kubernetes/pkg/util/mount"
)

func TestGetFStype(t *testing.T) {
	tests := []struct {
		options  map[string]string
		expected string
	}{
		{
			nil,
			"",
		},
		{
			map[string]string{},
			"",
		},
		{
			map[string]string{"fstype": ""},
			"",
		},
		{
			map[string]string{"fstype": "xfs"},
			"xfs",
		},
		{
			map[string]string{"FSType": "xfs"},
			"xfs",
		},
		{
			map[string]string{"fstype": "EXT4"},
			"ext4",
		},
	}

	for _, test := range tests {
		result := getFStype(test.options)
		if result != test.expected {
			t.Errorf("input: %q, getFStype result: %s, expected: %s", test.options, result, test.expected)
		}
	}
}

func TestGetMaxDataDiskCount(t *testing.T) {
	tests := []struct {
		instanceType string
		expectResult int64
	}{
		{
			instanceType: "standard_d2_v2",
			expectResult: 8,
		},
		{
			instanceType: "Standard_DS14_V2",
			expectResult: 64,
		},
		{
			instanceType: "NOT_EXISTING",
			expectResult: defaultAzureVolumeLimit,
		},
		{
			instanceType: "",
			expectResult: defaultAzureVolumeLimit,
		},
	}

	for _, test := range tests {
		result := getMaxDataDiskCount(test.instanceType)
		assert.Equal(t, test.expectResult, result)
	}
}

func TestEnsureMountPoint(t *testing.T) {
	mntPoint, err := ioutil.TempDir(os.TempDir(), "azuredisk-csi-mount-test")
	if err != nil {
		t.Fatalf("failed to create tmp dir: %v", err)
	}
	defer os.RemoveAll(mntPoint)

	fakeExecCallback := func(cmd string, args ...string) ([]byte, error) {
		return nil, nil
	}

	d, err := NewFakeDriver(t)
	if err != nil {
		t.Fatalf("Error getting driver: %v", err)
	}

	tests := []struct {
		desc          string
		target        string
		mountCheckErr error
		expectedErr   string
	}{
		{
			desc:          "success with NotExist dir",
			target:        "/tmp/NotExist",
			mountCheckErr: os.ErrNotExist,
			expectedErr:   "",
		},
		{
			desc:          "success with already mounted dir",
			target:        mntPoint,
			mountCheckErr: nil,
			expectedErr:   "",
		},
		{
			desc:          "success with invalid link, then unmount",
			target:        "/tmp/InvalidLink",
			mountCheckErr: nil,
			expectedErr:   "",
		},
		{
			desc:          "fail with non-NotExist error",
			target:        "/tmp/noPermission",
			mountCheckErr: os.ErrPermission,
			expectedErr:   os.ErrPermission.Error(),
		},
	}

	for _, test := range tests {
		mountCheckErrors := map[string]error{
			test.target: test.mountCheckErr,
		}

		fakeMounter := &mount.FakeMounter{
			MountPoints:      []mount.MountPoint{{Path: test.target}},
			MountCheckErrors: mountCheckErrors,
		}

		d.mounter = &mount.SafeFormatAndMount{
			Interface: fakeMounter,
			Exec:      mount.NewFakeExec(fakeExecCallback),
		}

		result := d.ensureMountPoint(test.target)
		if (result == nil && test.expectedErr != "") || (result != nil && (result.Error() != test.expectedErr)) {
			t.Errorf("input: (%+v), result: %v, expectedErr: %v", test, result, test.expectedErr)
		}
	}
}
