// +build windows

/*
Copyright 2017 The Kubernetes Authors.

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
	"os"

	utilexec "k8s.io/utils/exec"
	"k8s.io/utils/mount"
)

var _ mount.Interface = &CSIProxyMounter{}

type CSIProxyMounter struct {
}

func (mounter *CSIProxyMounter) Mount(source string, target string, fstype string, options []string) error {
	return fmt.Errorf("Mount not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) Unmount(target string) error {
	return fmt.Errorf("Unmount not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) List() ([]mount.MountPoint, error) {
	return []mount.MountPoint{}, fmt.Errorf("List not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) IsMountPointMatch(mp mount.MountPoint, dir string) bool {
	return mp.Path == dir
}

func (mounter *CSIProxyMounter) IsLikelyNotMountPoint(file string) (bool, error) {
	return false, fmt.Errorf("IsLikelyNotMountPoint not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) PathIsDevice(pathname string) (bool, error) {
	return false, fmt.Errorf("PathIsDevice not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) DeviceOpened(pathname string) (bool, error) {
	return false, fmt.Errorf("DeviceOpened not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) GetDeviceNameFromMount(mountPath, pluginMountDir string) (string, error) {
	return "", fmt.Errorf("GetDeviceNameFromMount not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) MakeRShared(path string) error {
	return fmt.Errorf("MakeRShared not implemented for CSIProxyMounter")
}

/*
func (mounter *CSIProxyMounter) GetFileType(pathname string) (mount.FileType, error) {
	return mount.FileType("fake"), fmt.Errorf("GetFileType not implemented for CSIProxyMounter")
}
*/

func (mounter *CSIProxyMounter) MakeFile(pathname string) error {
	return fmt.Errorf("MakeFile not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) MakeDir(pathname string) error {
	return fmt.Errorf("MakeDir not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) ExistsPath(pathname string) (bool, error) {
	return false, fmt.Errorf("ExistsPath not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) EvalHostSymlinks(pathname string) (string, error) {
	return "", fmt.Errorf("EvalHostSymlinks not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) GetMountRefs(pathname string) ([]string, error) {
	return []string{}, fmt.Errorf("GetMountRefs not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) GetFSGroup(pathname string) (int64, error) {
	return -1, fmt.Errorf("GetFSGroup not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) GetSELinuxSupport(pathname string) (bool, error) {
	return false, fmt.Errorf("GetSELinuxSupport not implemented for CSIProxyMounter")
}

func (mounter *CSIProxyMounter) GetMode(pathname string) (os.FileMode, error) {
	return 0, fmt.Errorf("GetMode not implemented for CSIProxyMounter")
}

func NewSafeMounter() *mount.SafeFormatAndMount {
	CSIProxyMounter := &CSIProxyMounter{}
	return &mount.SafeFormatAndMount{
		Interface: CSIProxyMounter,
		Exec:      utilexec.New(),
	}
}
