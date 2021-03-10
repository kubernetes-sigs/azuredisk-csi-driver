// +build windows

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
	"fmt"
	"strconv"

	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
)

// FormatAndMount formats the volume and mounts it at the specified path.
func (p *NodeProvisioner) FormatAndMount(source, target, fstype string, options []string) error {
	proxy, ok := p.mounter.Interface.(mounter.CSIProxyMounter)
	if !ok {
		return fmt.Errorf("could not cast to csi proxy class")
	}

	return proxy.FormatAndMount(source, target, fstype, options)
}

// PreparePublishPath readies the mount point for mount.
func (p *NodeProvisioner) PreparePublishPath(path string) error {
	// In Windows, the publish code path creates a soft link from the global stage path to the publish path, but kubelet
	// creates the directory in advance. We work around this issue by deleting the publish path and then recreating the link
	// when Mount is called.
	proxy, ok := p.mounter.Interface.(mounter.CSIProxyMounter)
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

// CleanupMountPoint unmounts the given path and deletes the remaining directory if successful.
func (p *NodeProvisioner) CleanupMountPoint(path string, extensiveCheck bool) error {
	// TODO - Is this OS-specific code needed anymore?

	// CSI proxy alpha does not support fixing the corrupted directory. So we will
	// just do the unmount for now.
	return p.mounter.Unmount(path)
}

// readyMountPoint readies the mount point for mount.
func (p *NodeProvisioner) readyMountPoint(path string) error {
	return nil
}

func (p *NodeProvisioner) rescanScsiHost() {
	proxy, ok := p.mounter.Interface.(mounter.CSIProxyMounter)
	if !ok {
		klog.Errorf("could not cast to csi proxy class")
		return
	}

	if err := proxy.Rescan(); err != nil {
		klog.Errorf("Rescan failed in scsiHostRescan, error: %v", err)
	}
}

// search Windows disk number by LUN
func (p *NodeProvisioner) findDiskByLun(lun int) (string, error) {
	proxy, ok := p.mounter.Interface.(mounter.CSIProxyMounter)
	if !ok {
		return "", fmt.Errorf("could not cast to csi proxy class")
	}

	diskNum, err := proxy.FindDiskByLun(strconv.Itoa(lun))
	if err != nil {
		return "", err
	}
	return diskNum, err
}
