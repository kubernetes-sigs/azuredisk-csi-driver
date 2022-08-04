/*
Copyright 2020 The Kubernetes Authors.

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

package mounter

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	testingexec "k8s.io/utils/exec/testing"
)

var (
	sourceTest string
	targetTest string
)

func TestMain(m *testing.M) {
	var err error
	sourceTest, err = ioutil.TempDir(os.TempDir(), "source_test")
	if err != nil {
		log.Printf("failed to get source test path: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(sourceTest)

	targetTest, err = ioutil.TempDir(os.TempDir(), "target_test")
	if err != nil {
		log.Printf("failed to get target test path: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(targetTest)

	_ = m.Run()

}

func TestNewFakeSafeMounter(t *testing.T) {
	resp, err := NewFakeSafeMounter()
	assert.NotNil(t, resp)
	assert.Nil(t, err)
}

func TestMount(t *testing.T) {
	tests := []struct {
		desc        string
		source      string
		target      string
		expectedErr error
	}{
		{
			desc:        "[Error] Mocked source error",
			source:      "./error_mount_source",
			target:      targetTest,
			expectedErr: fmt.Errorf("fake Mount: source error"),
		},
		{
			desc:        "[Error] Mocked target error",
			source:      sourceTest,
			target:      "./error_mount_target",
			expectedErr: fmt.Errorf("fake Mount: target error"),
		},
		{
			desc:        "[Success] Successful run",
			source:      sourceTest,
			target:      targetTest,
			expectedErr: nil,
		},
	}

	fakeMounter := &FakeSafeMounter{}

	for _, test := range tests {
		err := fakeMounter.Mount(test.source, test.target, "", nil)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
		}
	}
}

func TestMountSensitive(t *testing.T) {
	tests := []struct {
		desc        string
		source      string
		target      string
		expectedErr error
	}{
		{
			desc:        "[Error] Mocked source error",
			source:      "./error_mount_sens_source",
			target:      targetTest,
			expectedErr: fmt.Errorf("fake MountSensitive: source error"),
		},
		{
			desc:        "[Error] Mocked target error",
			source:      sourceTest,
			target:      "./error_mount_sens_target",
			expectedErr: fmt.Errorf("fake MountSensitive: target error"),
		},
		{
			desc:        "[Success] Successful run",
			source:      sourceTest,
			target:      targetTest,
			expectedErr: nil,
		},
	}

	fakeMounter := &FakeSafeMounter{}

	for _, test := range tests {
		err := fakeMounter.MountSensitive(test.source, test.target, "", nil, nil)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
		}
	}
}

func TestIsLikelyNotMountPoint(t *testing.T) {
	tests := []struct {
		desc        string
		file        string
		expectedErr error
	}{
		{
			desc:        "[Error] Mocked file error",
			file:        "./error_is_likely_target",
			expectedErr: fmt.Errorf("fake IsLikelyNotMountPoint: fake error"),
		},
		{desc: "[Success] Successful run",
			file:        targetTest,
			expectedErr: nil,
		},
		{
			desc:        "[Success] Successful run not a mount",
			file:        "./false_is_likely_target",
			expectedErr: nil,
		},
	}

	fakeMounter := &FakeSafeMounter{}

	for _, test := range tests {
		_, err := fakeMounter.IsLikelyNotMountPoint(test.file)
		if !reflect.DeepEqual(err, test.expectedErr) {
			t.Errorf("Unexpected error: %v", err)
		}
	}
}

func TestSetNextCommandOutputScripts(t *testing.T) {
	findmntAction := func() ([]byte, []byte, error) {
		return []byte("test"), []byte{}, nil
	}
	blkidAction := func() ([]byte, []byte, error) {
		return []byte("DEVICE=test\nTYPE=ext4"), []byte{}, nil
	}
	resize2fsAction := func() ([]byte, []byte, error) {
		return []byte{}, []byte{}, nil
	}

	stdout := [][]byte{}

	tests := []struct {
		scripts                 []testingexec.FakeAction
		cmd                     string
		args                    []string
		expectedCmdOutputStdout [][]byte
		expectedCmdOutputErr    []error
	}{
		{
			scripts:                 []testingexec.FakeAction{},
			cmd:                     "cd .",
			args:                    []string{"args"},
			expectedCmdOutputStdout: stdout,
			expectedCmdOutputErr:    []error{},
		},
		{
			scripts:                 []testingexec.FakeAction{findmntAction},
			cmd:                     "cd .",
			args:                    []string{"args"},
			expectedCmdOutputStdout: append(stdout, []byte("test")),
			expectedCmdOutputErr:    []error{nil},
		},
		{
			scripts:                 []testingexec.FakeAction{findmntAction, blkidAction, resize2fsAction},
			cmd:                     "cd .",
			args:                    []string{"args", "arg"},
			expectedCmdOutputStdout: append(stdout, []byte("test"), []byte("DEVICE=test\nTYPE=ext4"), []byte{}),
			expectedCmdOutputErr:    []error{nil, nil, nil},
		},
	}

	for _, test := range tests {
		fakeMounter := &FakeSafeMounter{}
		fakeMounter.SetNextCommandOutputScripts(test.scripts...)
		if fakeMounter.CommandScript != nil {
			for num := 0; num < len(fakeMounter.CommandScript); num++ {
				resultCmd := fakeMounter.CommandScript[num](test.cmd, test.args...)
				resultCmdOutputStdout, resultCmdOutputErr := resultCmd.Output()
				if !reflect.DeepEqual(resultCmdOutputStdout, test.expectedCmdOutputStdout[num]) || !reflect.DeepEqual(resultCmdOutputErr, test.expectedCmdOutputErr[num]) {
					t.Errorf("resultCmdOutputStdout: %v, expectedCmdOutputStdout: %v, resultCmdOutputErr: %v, expectedCmdOutputErr: %v", resultCmdOutputStdout, test.expectedCmdOutputStdout[num], resultCmdOutputErr, test.expectedCmdOutputErr[num])
				}
			}
		}
	}
}
