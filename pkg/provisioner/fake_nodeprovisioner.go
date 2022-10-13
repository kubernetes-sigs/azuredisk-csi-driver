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

package provisioner

import (
	mount "k8s.io/mount-utils"
	testingexec "k8s.io/utils/exec/testing"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
)

// FakeNodeProvisioner is a fake node provisioner suitable for use in unit tests.
type FakeNodeProvisioner struct {
	NodeProvisioner

	blockDevicePathResults map[string]struct {
		isDevice bool
		err      error
	}
}

// NewFakeNodeProvisioner creates a fake node provisioner suitable for use in unit tests.
func NewFakeNodeProvisioner() (*FakeNodeProvisioner, error) {
	fakeSafeMounter, err := mounter.NewFakeSafeMounter()
	if err != nil {
		return nil, err
	}

	fakeHostUtil := azureutils.NewFakeHostUtil()

	return &FakeNodeProvisioner{
			NodeProvisioner: NodeProvisioner{mounter: fakeSafeMounter, host: fakeHostUtil, ioHandler: &fakeIOHandler{}},
			blockDevicePathResults: map[string]struct {
				isDevice bool
				err      error
			}{},
		},
		nil
}

// SetIsBlockDevicePathResult set the result of calling IsBlockDevicePath for the specified path.
func (fake *FakeNodeProvisioner) SetIsBlockDevicePathResult(path string, isDevice bool, err error) {
	fake.host.(*azureutils.FakeHostUtil).SetPathIsDeviceResult(path, isDevice, err)
}

// Mount mounts the volume at the specified path.
func (fake *FakeNodeProvisioner) Mount(source, target, fstype string, options []string) error {
	if err, ok := fake.mounter.Interface.(*mounter.FakeSafeMounter).MountCheckErrors[source]; ok {
		return err
	}

	if err, ok := fake.mounter.Interface.(*mounter.FakeSafeMounter).MountCheckErrors[target]; ok {
		return err
	}

	return fake.NodeProvisioner.Mount(source, target, fstype, options)
}

// CleanupMountPoint unmounts the given path and deletes the remaining directory if successful.
func (fake *FakeNodeProvisioner) CleanupMountPoint(path string, extensiveCheck bool) error {
	if err, ok := fake.mounter.Interface.(*mounter.FakeSafeMounter).MountCheckErrors[path]; ok {
		return err
	}

	return fake.NodeProvisioner.CleanupMountPoint(path, extensiveCheck)
}

// SetNextCommandOutputScripts sets the output scripts for the next sequence of command invocations.
func (fake *FakeNodeProvisioner) SetNextCommandOutputScripts(scripts ...testingexec.FakeAction) {
	fake.mounter.Exec.(*mounter.FakeSafeMounter).SetNextCommandOutputScripts(scripts...)
}

// SetMountCheckError sets an error result returned by the IsLikelyNotMountPoint method.
func (fake *FakeNodeProvisioner) SetMountCheckError(target string, err error) {
	fakeMounter, _ := fake.mounter.Interface.(*mounter.FakeSafeMounter)

	fakeMounter.SetMountCheckError(target, err)
}

func (fake *FakeNodeProvisioner) SetMounter(mounter *mount.SafeFormatAndMount) {
	fake.mounter = mounter
}
