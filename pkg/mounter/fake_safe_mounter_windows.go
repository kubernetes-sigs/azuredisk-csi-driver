//go:build windows
// +build windows

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
	"flag"
	"fmt"
	"os"
	"strings"

	"k8s.io/mount-utils"
)

var _ CSIProxyMounter = &FakeSafeMounter{}
var useCSIProxy = flag.Bool("use-csi-proxy", true, "Specifies whether to use CSI Proxy or internal fake when running unit tests.")

// Returns whether FakeSafeMounter is using CSI Proxy.
func IsFakeUsingCSIProxy() bool {
	return *useCSIProxy
}

// NewFakeSafeMounter creates a mount.SafeFormatAndMount instance suitable for use in unit tests.
func NewFakeSafeMounter() (*mount.SafeFormatAndMount, error) {
	if *useCSIProxy {
		return NewSafeMounter(true)
	}

	fakeSafeMounter := FakeSafeMounter{}
	fakeSafeMounter.ExactOrder = true
	fakeSafeMounter.MountCheckErrors = make(map[string]error)

	return &mount.SafeFormatAndMount{
		Interface: &fakeSafeMounter,
		Exec:      &fakeSafeMounter,
	}, nil
}

// FormatAndMount - accepts the source disk number, target path to mount, the fstype to format with and options to be used.
func (fake *FakeSafeMounter) FormatAndMount(source, target, fstype string, options []string) error {
	return fake.Mount(source, target, fstype, options)
}

// ExistsPath return whether or not the path is valid and exists.
func (fake *FakeSafeMounter) ExistsPath(path string) (bool, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

// Rmdir deletes the specified directory.
func (fake *FakeSafeMounter) Rmdir(path string) error {
	if err := os.RemoveAll(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return err
	}

	return nil
}

// Rescan triggers a rescan of the SCSI bus.
func (fake *FakeSafeMounter) Rescan() error {
	return nil
}

// FindDiskByLun returns the disk for the specified LUN.
func (fake *FakeSafeMounter) FindDiskByLun(lun string) (string, error) {
	if lun == "1" {
		return "1", nil
	}

	return "", fmt.Errorf("could not find disk id for lun: %s", lun)
}

// GetDeviceNameFromMount returns the volume ID for a mount path.
func (fake *FakeSafeMounter) GetDeviceNameFromMount(mountPath, pluginMountDir string) (string, error) {
	cmd := fmt.Sprintf("(Get-Item -Path %s).Target", mountPath)
	output, err := fake.Command("powershell", cmd).Output()
	if err != nil {
		windowsMountPath := strings.Replace(mountPath, "/", "\\", -1)
		return "", fmt.Errorf("error getting the volume for the mount %s, internal error error getting volume from mount. cmd: (Get-Item -Path %s).Target, output: , error: <nil>", windowsMountPath, windowsMountPath)
	}

	return string(output), nil
}

// GetVolumeSizeInBytes returns the size of the volume in bytes.
func (fake *FakeSafeMounter) GetVolumeSizeInBytes(devicePath string) (int64, error) {
	return -1, fmt.Errorf("error getting the volume for the mount %s, internal error error getting volume from mount. cmd: (Get-Item -Path %s).Target, output: , error: <nil>", devicePath, devicePath)
}

// ResizeVolume resizes the volume to the maximum available size.
func (fake *FakeSafeMounter) ResizeVolume(devicePath string) error {
	return nil
}

func (fake *FakeSafeMounter) GetAPIVersions() string {
	return ""
}
