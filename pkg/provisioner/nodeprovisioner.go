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
	"os"
	"path/filepath"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/volume/util/hostutil"
	mount "k8s.io/mount-utils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
)

const (
	defaultDevicePollInterval time.Duration = 1 * time.Second
	defaultDevicePollTimeout  time.Duration = 2 * time.Minute
)

type ioHandler interface {
	ReadDir(dirname string) ([]os.FileInfo, error)
	WriteFile(filename string, data []byte, perm os.FileMode) error
	Readlink(name string) (string, error)
	ReadFile(filename string) ([]byte, error)
}

// NodeProvisioner handles node-specific provisioning tasks.
type NodeProvisioner struct {
	mounter            *mount.SafeFormatAndMount
	host               hostutil.HostUtils
	ioHandler          ioHandler
	devicePollInterval time.Duration
	devicePollTimeout  time.Duration
}

// NewNodeProvisioner creates a new NodeProvisioner to handle node-specific provisioning tasks.
func NewNodeProvisioner() (*NodeProvisioner, error) {
	m, err := mounter.NewSafeMounter()
	if err != nil {
		return nil, err
	}

	return &NodeProvisioner{
		mounter:            m,
		host:               hostutil.NewHostUtil(),
		ioHandler:          &osIOHandler{},
		devicePollInterval: defaultDevicePollInterval,
		devicePollTimeout:  defaultDevicePollTimeout,
	}, nil
}

// SetDevicePollParameters sets the device polling parameters used when scanning the SCSI bus for devices.
func (p *NodeProvisioner) SetDevicePollParameters(interval, timeout time.Duration) {
	p.devicePollInterval = interval
	p.devicePollTimeout = timeout
}

// GetDevicePathWithLUN returns the device path for the specified LUN number.
func (p *NodeProvisioner) GetDevicePathWithLUN(lun int) (string, error) {
	p.rescanScsiHost()

	newDevicePath := ""
	err := wait.PollImmediate(p.devicePollInterval, p.devicePollTimeout, func() (bool, error) {
		var err error

		if newDevicePath, err = p.findDiskByLun(lun); err != nil {
			return false, fmt.Errorf("azureDisk - findDiskByLun(%v) failed with error(%s)", lun, err)
		}

		// did we find it?
		if newDevicePath != "" {
			return true, nil
		}

		// wait until timeout
		return false, nil
	})

	if err == nil && newDevicePath == "" {
		err = fmt.Errorf("azureDisk - findDiskByLun(%v) failed within timeout", lun)
	}

	return newDevicePath, err
}

// IsBlockDevicePath return whether the path references a block device.
func (p *NodeProvisioner) IsBlockDevicePath(path string) (bool, error) {
	return p.host.PathIsDevice(path)
}

// EnsureMountPointReady ensures that the mount point directory exists and is valid.
// It attempts to recover an invalid mount point by unmounting it.
// It returns <true, nil> if the mount point exists and is valid; otherwise, it returns <false, nil>.
func (p *NodeProvisioner) EnsureMountPointReady(target string) (bool, error) {
	notMnt, err := p.mounter.IsLikelyNotMountPoint(target)

	if err != nil && !os.IsNotExist(err) {
		if isCorruptedMount(target) {
			notMnt = false
			klog.Warningf("detected corrupted mount for targetPath [%s]", target)
		} else {
			return !notMnt, err
		}
	}

	if !notMnt {
		// testing original mount point, make sure the mount link is valid
		_, err := p.ioHandler.ReadDir(target)
		if err == nil {
			klog.V(2).Infof("already mounted to target %s", target)
			return !notMnt, nil
		}

		// mount link is invalid, now unmount and remount later
		klog.Warningf("ReadDir %s failed with %v, unmount this directory", target, err)

		if err := p.mounter.Unmount(target); err != nil {
			klog.Errorf("Unmount directory %s failed with %v", target, err)
			return !notMnt, err
		}

		notMnt = true
		return !notMnt, err
	}

	return !notMnt, p.readyMountPoint(target)
}

// EnsureBlockTargetReady ensures the mount point dir and block target files exist and are valid.
func (p *NodeProvisioner) EnsureBlockTargetReady(target string) error {
	// Since the block device target path is file, its parent directory should be ensured to be valid.
	parentDir := filepath.Dir(target)
	if _, err := p.EnsureMountPointReady(parentDir); err != nil {
		return status.Errorf(codes.Internal, "Could not mount target %q: %v", parentDir, err)
	}

	// Create the mount point as a file since bind mount device node requires it to be a file
	klog.V(2).Infof("EnsureBlockTargetReady [block]: making target file %s", target)

	err := volumehelper.MakeFile(target)
	if err != nil {
		if removeErr := os.Remove(target); removeErr != nil {
			return status.Errorf(codes.Internal, "Could not remove mount target %q: %v", target, removeErr)
		}

		return status.Errorf(codes.Internal, "Could not create file %q: %v", target, err)
	}

	return nil
}

// Mount mounts the volume at the specified path.
func (p *NodeProvisioner) Mount(source, target, fstype string, options []string) error {
	return p.mounter.Mount(source, target, fstype, options)
}

// Unmount unmounts a volume previously mounted using the Mount method.
func (p *NodeProvisioner) Unmount(target string) error {
	return p.mounter.Unmount(target)
}

func isCorruptedMount(target string) bool {
	_, pathErr := mount.PathExists(target)
	fmt.Printf("IsCorruptedDir(%s) returned with error: %v", target, pathErr)
	return pathErr != nil && mount.IsCorruptedMnt(pathErr)
}
