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

package azuredisk

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2019-03-01/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/klog"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/kubernetes/pkg/volume/util"
)

const (
	defaultFsType           = "ext4"
	defaultAzureVolumeLimit = 16
)

// store vm size list in current region
var vmSizeList *[]compute.VirtualMachineSize

// NodeStageVolume mount disk device to a staging path
func (d *Driver) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
	klog.V(4).Infof("NodeStageVolume: called with args %+v", *req)
	diskURI := req.GetVolumeId()
	if len(diskURI) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID not provided")
	}

	target := req.GetStagingTargetPath()
	if len(target) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Staging target not provided")
	}

	volumeCapability := req.GetVolumeCapability()
	if volumeCapability == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability not provided")
	}

	if !isValidVolumeCapabilities([]*csi.VolumeCapability{volumeCapability}) {
		return nil, status.Error(codes.InvalidArgument, "Volume capability not supported")
	}

	devicePath, ok := req.PublishContext["devicePath"]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "devicePath not provided")
	}
	lun, err := getDiskLUN(devicePath)
	if err != nil {
		return nil, err
	}

	// TODO: consider replacing IsLikelyNotMountPoint by IsNotMountPoint
	notMnt, err := d.mounter.IsLikelyNotMountPoint(target)
	if err != nil {
		if os.IsNotExist(err) {
			if errMkDir := d.mounter.MakeDir(target); errMkDir != nil {
				msg := fmt.Sprintf("could not create target dir %q: %v", target, errMkDir)
				return nil, status.Error(codes.Internal, msg)
			}
			notMnt = true
		} else {
			msg := fmt.Sprintf("could not determine if %q is valid mount point: %v", target, err)
			return nil, status.Error(codes.Internal, msg)
		}
	}

	if !notMnt {
		klog.V(2).Infof("target %q is already a valid mount point(device path: %v), skip format and mount", target, devicePath)
		// todo: check who is mounted here. No error if its us
		/*
			1) Target Path MUST be the vol referenced by vol ID
			2) VolumeCapability MUST match
			3) Readonly MUST match
		*/
		return &csi.NodeStageVolumeResponse{}, nil
	}
	// Get fsType and mountOptions that the volume will be formatted and mounted with
	fstype := defaultFsType
	options := []string{}
	if mnt := volumeCapability.GetMount(); mnt != nil {
		if mnt.FsType != "" {
			fstype = mnt.FsType
		}
		options = append(options, mnt.MountFlags...)
	}

	volContextFSType := getFStype(req.GetVolumeContext())
	if volContextFSType != "" {
		// respect "fstype" setting in storage class parameters
		fstype = volContextFSType
	}

	io := &osIOHandler{}
	scsiHostRescan(io, d.mounter.Exec)

	newDevicePath := ""
	err = wait.Poll(1*time.Second, 2*time.Minute, func() (bool, error) {
		if newDevicePath, err = findDiskByLun(int(lun), io, d.mounter.Exec); err != nil {
			return false, fmt.Errorf("azureDisk - findDiskByLun(%v) failed with error(%s)", lun, err)
		}

		// did we find it?
		if newDevicePath != "" {
			return true, nil
		}

		return false, fmt.Errorf("azureDisk - findDiskByLun(%v) failed within timeout", lun)
	})
	if err != nil {
		return nil, err
	}

	// FormatAndMount will format only if needed
	klog.V(2).Infof("NodeStageVolume: formatting %s and mounting at %s with mount options(%s)", newDevicePath, target, options)
	err = d.mounter.FormatAndMount(newDevicePath, target, fstype, options)
	if err != nil {
		msg := fmt.Sprintf("could not format %q and mount it at %q", lun, target)
		return nil, status.Error(codes.Internal, msg)
	}
	klog.V(2).Infof("NodeStageVolume: format %s and mounting at %s successfully.", newDevicePath, target)

	return &csi.NodeStageVolumeResponse{}, nil
}

// NodeUnstageVolume unmount disk device from a staging path
func (d *Driver) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	klog.V(2).Infof("NodeUnstageVolume: called with args %+v", *req)
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID not provided")
	}

	target := req.GetStagingTargetPath()
	if len(target) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Staging target not provided")
	}

	klog.V(2).Infof("NodeUnstageVolume: unmounting %s", target)
	err := d.mounter.Unmount(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Could not unmount target %q: %v", target, err)
	}
	klog.V(2).Infof("NodeUnstageVolume: unmount %s successfully", target)

	return &csi.NodeUnstageVolumeResponse{}, nil
}

// NodePublishVolume mount the volume from staging to target path
func (d *Driver) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability missing in request")
	}
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}

	source := req.GetStagingTargetPath()
	if len(source) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Staging target not provided")
	}

	target := req.GetTargetPath()
	if len(target) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Target path not provided")
	}

	notMnt, err := d.mounter.IsLikelyNotMountPoint(target)
	if err != nil && !os.IsNotExist(err) {
		klog.V(2).Infof("azureDisk - cannot validate mount point for on %s, error: %v", target, err)
		return nil, err
	}

	if !notMnt {
		// testing original mount point, make sure the mount link is valid
		_, err := ioutil.ReadDir(target)
		if err == nil {
			klog.V(2).Infof("azureDisk - already mounted to target %s", target)
			return &csi.NodePublishVolumeResponse{}, nil
		}
		// mount link is invalid, now unmount and remount later
		klog.Warningf("azureDisk - ReadDir %s failed with %v, unmount this directory", target, err)
		if err := d.mounter.Unmount(target); err != nil {
			klog.Errorf("azureDisk - Unmount directory %s failed with %v", target, err)
			return nil, err
		}
		// notMnt = true
	}

	if runtime.GOOS != "windows" {
		// in windows, we will use mklink to mount, will MkdirAll in Mount func
		if err := os.MkdirAll(target, 0750); err != nil {
			klog.Errorf("azureDisk - mkdir failed on target: %s (%v)", target, err)
			return nil, err
		}
	}

	// todo: looks like here fsType is useless since we only use "fsType" in VolumeContext
	fsType := req.GetVolumeCapability().GetMount().GetFsType()

	readOnly := req.GetReadonly()
	volumeID := req.GetVolumeId()
	attrib := req.GetVolumeContext()
	mountFlags := req.GetVolumeCapability().GetMount().GetMountFlags()

	klog.V(2).Infof("target %v\nfstype %v\n\nreadonly %v\nvolumeId %v\nContext %v\nmountflags %v\n",
		target, fsType, readOnly, volumeID, attrib, mountFlags)

	mountOptions := []string{"bind"}
	if req.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}
	mountOptions = util.JoinMountOptions(mountFlags, mountOptions)

	klog.V(2).Infof("NodePublishVolume: creating dir %s", target)
	if err := d.mounter.MakeDir(target); err != nil {
		return nil, status.Errorf(codes.Internal, "Could not create dir %q: %v", target, err)
	}

	klog.V(2).Infof("NodePublishVolume: mounting %s at %s", source, target)
	if err := d.mounter.Mount(source, target, "", mountOptions); err != nil {
		os.Remove(target)
		return nil, status.Errorf(codes.Internal, "Could not mount %q at %q: %v", source, target, err)
	}
	klog.V(2).Infof("NodePublishVolume: mount %s at %s successfully", source, target)

	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeUnpublishVolume unmount the volume from the target path
func (d *Driver) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	if len(req.GetVolumeId()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if len(req.GetTargetPath()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}
	targetPath := req.GetTargetPath()
	volumeID := req.GetVolumeId()

	klog.V(2).Infof("NodeUnpublishVolume: unmounting volume %s on %s", volumeID, targetPath)
	err := d.mounter.Unmount(req.GetTargetPath())
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	klog.V(2).Infof("NodeUnpublishVolume: unmount volume %s on %s successfully", volumeID, targetPath)

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// NodeGetCapabilities return the capabilities of the Node plugin
func (d *Driver) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	klog.V(2).Infof("Using default NodeGetCapabilities")

	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: d.NSCap,
	}, nil
}

// NodeGetInfo return info of the node on which this plugin is running
func (d *Driver) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	klog.V(5).Infof("Using default NodeGetInfo")

	instances, ok := d.cloud.Instances()
	if !ok {
		return nil, status.Error(codes.Internal, "Failed to get instances from cloud provider")
	}

	instanceType, err := instances.InstanceType(context.TODO(), types.NodeName(d.NodeID))
	if err != nil {
		klog.Warningf("Failed to get instance type from Azure cloud provider, nodeName: %v, error: %v", d.NodeID, err)
		instanceType = ""
	}

	if vmSizeList == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		result, err := d.cloud.VirtualMachineSizesClient.List(ctx, d.cloud.Location)
		if err != nil || result.Value == nil {
			return nil, status.Error(codes.Internal, "failed to list vm sizes in GetVolumeLimits from Azure cloud provider, nodeName: "+d.NodeID)
		}
		vmSizeList = result.Value
	}

	return &csi.NodeGetInfoResponse{
		NodeId:            d.NodeID,
		MaxVolumesPerNode: getMaxDataDiskCount(instanceType, vmSizeList),
	}, nil
}

func getMaxDataDiskCount(instanceType string, sizeList *[]compute.VirtualMachineSize) int64 {
	if instanceType == "" || sizeList == nil {
		return defaultAzureVolumeLimit
	}

	vmsize := strings.ToUpper(instanceType)
	for _, size := range *sizeList {
		if size.Name == nil || size.MaxDataDiskCount == nil {
			klog.Errorf("failed to get vm size in getMaxDataDiskCount")
			continue
		}
		if strings.ToUpper(*size.Name) == vmsize {
			klog.V(12).Infof("got a matching size in getMaxDataDiskCount, Name: %s, MaxDataDiskCount: %d", *size.Name, *size.MaxDataDiskCount)
			return int64(*size.MaxDataDiskCount)
		}
	}
	return defaultAzureVolumeLimit
}

func (d *Driver) NodeGetVolumeStats(ctx context.Context, in *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "")
}

// NodeExpandVolume node expand volume
func (d *Driver) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	return nil, status.Error(codes.Unimplemented, fmt.Sprintf("NodeExpandVolume is not yet implemented"))
}

func getFStype(attributes map[string]string) string {
	for k, v := range attributes {
		switch strings.ToLower(k) {
		case "fstype":
			return strings.ToLower(v)
		}
	}

	return ""
}
