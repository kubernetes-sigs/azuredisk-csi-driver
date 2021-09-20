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
	"strings"

	"k8s.io/mount-utils"
	"k8s.io/utils/exec"
	testingexec "k8s.io/utils/exec/testing"
)

// FakeSafeMounter implements a mount.Interface interface suitable for use in unit tests.
type FakeSafeMounter struct {
	mount.FakeMounter
	testingexec.FakeExec
}

// NewFakeSafeMounter creates a mount.SafeFormatAndMount instance suitable for use in unit tests.
func NewFakeSafeMounter() (*mount.SafeFormatAndMount, error) {
	fakeSafeMounter := FakeSafeMounter{}
	fakeSafeMounter.ExactOrder = true
	fakeSafeMounter.MountCheckErrors = make(map[string]error)

	return &mount.SafeFormatAndMount{
		Interface: &fakeSafeMounter,
		Exec:      &fakeSafeMounter,
	}, nil
}

// Mount overrides mount.FakeMounter.Mount.
func (f *FakeSafeMounter) Mount(source, target, fstype string, options []string) error {
	if strings.Contains(source, "error_mount") {
		return fmt.Errorf("fake Mount: source error")
	} else if strings.Contains(target, "error_mount") {
		return fmt.Errorf("fake Mount: target error")
	}

	if err, ok := f.MountCheckErrors[source]; ok {
		return err
	}

	if err, ok := f.MountCheckErrors[target]; ok {
		return err
	}

	return f.FakeMounter.Mount(source, target, fstype, options)
}

// MountSensitive overrides mount.FakeMounter.MountSensitive.
func (f *FakeSafeMounter) MountSensitive(source, target, fstype string, options, sensitiveOptions []string) error {
	if strings.Contains(source, "error_mount_sens") {
		return fmt.Errorf("fake MountSensitive: source error")
	} else if strings.Contains(target, "error_mount_sens") {
		return fmt.Errorf("fake MountSensitive: target error")
	}

	if err, ok := f.MountCheckErrors[source]; ok {
		return err
	}

	if err, ok := f.MountCheckErrors[target]; ok {
		return err
	}

	return f.FakeMounter.MountSensitive(source, target, fstype, options, sensitiveOptions)
}

// IsLikelyNotMountPoint overrides mount.FakeMounter.IsLikelyNotMountPoint.
func (f *FakeSafeMounter) IsLikelyNotMountPoint(file string) (bool, error) {
	if strings.Contains(file, "error_is_likely") {
		return false, fmt.Errorf("fake IsLikelyNotMountPoint: fake error")
	}
	if strings.Contains(file, "false_is_likely") {
		return false, nil
	}
	return f.FakeMounter.IsLikelyNotMountPoint(file)
}

// SetNextCommandOutputScripts sets the output scripts for the next sequence of command invocations.
func (f *FakeSafeMounter) SetNextCommandOutputScripts(scripts ...testingexec.FakeAction) {
	for _, script := range scripts {
		outputScripts := []testingexec.FakeAction{script}
		fakeCmdAction := func(cmd string, args ...string) exec.Cmd {
			fakeCmd := &testingexec.FakeCmd{}
			fakeCmd.OutputScript = outputScripts
			fakeCmd.CombinedOutputScript = outputScripts

			return testingexec.InitFakeCmd(fakeCmd, cmd, args...)
		}

		f.CommandScript = append(f.CommandScript, fakeCmdAction)
	}
}

// SetMountCheckError sets an error result returned by the IsLikelyNotMountPoint method.
func (f *FakeSafeMounter) SetMountCheckError(target string, err error) {
	f.MountCheckErrors[target] = err
}
