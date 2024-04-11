//go:build windows
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
	"context"
	"fmt"
	"strconv"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog/v2"
	"k8s.io/mount-utils"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
)

func formatAndMount(source, target, fstype string, options []string, m *mount.SafeFormatAndMount) error {
	if proxy, ok := m.Interface.(mounter.CSIProxyMounter); ok {
		return proxy.FormatAndMount(source, target, fstype, options)
	}
	return fmt.Errorf("could not cast to csi proxy class")
}

func scsiHostRescan(io azureutils.IOHandler, m *mount.SafeFormatAndMount) {
	var err error
	if proxy, ok := m.Interface.(mounter.CSIProxyMounter); ok {
		err = proxy.Rescan()
	} else {
		klog.Errorf("could not cast to csi proxy class")
	}

	if err != nil {
		klog.Errorf("Rescan failed in scsiHostRescan, error: %v", err)
	}
}

// search Windows disk number by LUN
func findDiskByLun(lun int, iohandler azureutils.IOHandler, m *mount.SafeFormatAndMount) (string, error) {
	if proxy, ok := m.Interface.(mounter.CSIProxyMounter); ok {
		return proxy.FindDiskByLun(strconv.Itoa(lun))
	}
	return "", fmt.Errorf("could not cast to csi proxy class")
}

// preparePublishPath - In case of windows, the publish code path creates a soft link
// from global stage path to the publish path. But kubelet creates the directory in advance.
// We work around this issue by deleting the publish path then recreating the link.
func preparePublishPath(path string, m *mount.SafeFormatAndMount) error {
	if proxy, ok := m.Interface.(mounter.CSIProxyMounter); ok {
		isExists, err := proxy.ExistsPath(path)
		if err != nil {
			return err
		}

		if isExists {
			klog.V(4).Infof("Removing path: %s", path)
			if err = proxy.Rmdir(path); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("could not cast to csi proxy class")
}

func CleanupMountPoint(path string, m *mount.SafeFormatAndMount, extensiveCheck bool) error {
	if proxy, ok := m.Interface.(mounter.CSIProxyMounter); ok {
		return proxy.Unmount(path)
	}
	return fmt.Errorf("could not cast to csi proxy class")
}

func getDevicePathWithMountPath(mountPath string, m *mount.SafeFormatAndMount) (string, error) {
	var devicePath string
	var err error

	if proxy, ok := m.Interface.(mounter.CSIProxyMounter); ok {
		devicePath, err = proxy.GetDeviceNameFromMount(mountPath, "")
	} else {
		return "", fmt.Errorf("could not cast to csi proxy class")
	}

	if err != nil {
		if sts, ok := status.FromError(err); ok {
			return "", fmt.Errorf(sts.Message())
		}
		return "", err
	}

	return devicePath, nil
}

func getBlockSizeBytes(devicePath string, m *mount.SafeFormatAndMount) (int64, error) {
	var sizeInBytes int64
	var err error

	if proxy, ok := m.Interface.(mounter.CSIProxyMounter); ok {
		sizeInBytes, err = proxy.GetVolumeSizeInBytes(devicePath)
	} else {
		return -1, fmt.Errorf("could not cast to csi proxy class")
	}

	if err != nil {
		if sts, ok := status.FromError(err); ok {
			return -1, fmt.Errorf(sts.Message())
		}
		return -1, err
	}

	return sizeInBytes, nil
}

func resizeVolume(devicePath, volumePath string, m *mount.SafeFormatAndMount) error {
	var err error
	if proxy, ok := m.Interface.(mounter.CSIProxyMounter); ok {
		err = proxy.ResizeVolume(devicePath)
	} else {
		return fmt.Errorf("could not cast to csi proxy class")
	}

	if err != nil {
		if sts, ok := status.FromError(err); ok {
			return fmt.Errorf(sts.Message())
		}
		return err
	}

	return nil
}

// needResizeVolume check whether device needs resize
func needResizeVolume(devicePath, volumePath string, m *mount.SafeFormatAndMount) (bool, error) {
	return false, nil
}

// rescanVolume rescan device for detecting device size expansion
func rescanVolume(io azureutils.IOHandler, devicePath string) error {
	return nil
}

// rescanAllVolumes rescan all devices
func rescanAllVolumes(io azureutils.IOHandler) error {
	return nil
}

func (d *DriverCore) GetVolumeStats(ctx context.Context, m *mount.SafeFormatAndMount, volumeID, target string, hostutil hostUtil) ([]*csi.VolumeUsage, error) {
	// check if the volume stats is cached
	cache, err := d.volStatsCache.Get(volumeID, azcache.CacheReadTypeDefault)
	if err != nil {
		return nil, status.Errorf(codes.Internal, err.Error())
	}
	if cache != nil {
		volUsage := cache.(csi.VolumeUsage)
		klog.V(6).Infof("NodeGetVolumeStats: volume stats for volume %s path %s is cached", volumeID, target)
		return []*csi.VolumeUsage{&volUsage}, nil
	}

	if proxy, ok := m.Interface.(mounter.CSIProxyMounter); ok {
		volUsage, err := proxy.GetVolumeStats(ctx, target)
		// cache the volume stats per volume
		d.volStatsCache.Set(volumeID, *volUsage)
		return []*csi.VolumeUsage{volUsage}, err
	}
	return []*csi.VolumeUsage{}, fmt.Errorf("could not cast to csi proxy class")
}
