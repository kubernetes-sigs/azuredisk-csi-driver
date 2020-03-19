// +build windows

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
	"fmt"
	"strconv"

	"k8s.io/klog"

	"k8s.io/kubernetes/pkg/util/mount"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
)

func formatAndMount(source, target, fstype string, options []string, m *mount.SafeFormatAndMount) error {
	proxy, ok := m.Interface.(*mounter.CSIProxyMounter)
	if !ok {
		return fmt.Errorf("could not cast to csi proxy class")
	}
	return proxy.FormatAndMount(source, target, fstype, options)
}

func scsiHostRescan(io ioHandler, m *mount.SafeFormatAndMount) {
	proxy, ok := m.Interface.(*mounter.CSIProxyMounter)
	if !ok {
		klog.Errorf("could not cast to csi proxy class")
		return
	}

	if err := proxy.Rescan(); err != nil {
		klog.Errorf("Rescan failed in scsiHostRescan, error: %v", err)
	}
}

// search Windows disk number by LUN
func findDiskByLun(lun int, iohandler ioHandler, m *mount.SafeFormatAndMount) (string, error) {
	proxy, ok := m.Interface.(*mounter.CSIProxyMounter)
	if !ok {
		return "", fmt.Errorf("could not cast to csi proxy class")
	}

	diskNum, err := proxy.FindDiskByLun(strconv.Itoa(lun))
	if err != nil {
		return "", err
	}
	return diskNum, err
}

// preparePublishPath - In case of windows, the publish code path creates a soft link
// from global stage path to the publish path. But kubelet creates the directory in advance.
// We work around this issue by deleting the publish path then recreating the link.
func preparePublishPath(path string, m *mount.SafeFormatAndMount) error {
	proxy, ok := m.Interface.(*mounter.CSIProxyMounter)
	if !ok {
		return fmt.Errorf("could not cast to csi proxy class")
	}

	isExists, err := proxy.ExistsPath(path)
	if err != nil {
		return err
	}

	if isExists {
		err = proxy.Rmdir(path)
		if err != nil {
			return err
		}
	}
	return nil
}

func CleanupMountPoint(path string, m *mount.SafeFormatAndMount, extensiveCheck bool) error {
	// CSI proxy alpha does not support fixing the corrupted directory. So we will
	// just do the unmount for now.
	return m.Unmount(path)
}
