// +build azurediskv2

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
	"os"

	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/volume"
)

// NodeProvisioner defines the methods required to manage staging and publishing of mount points.
type NodeProvisioner interface {
	GetDevicePathWithLUN(lun int) (string, error)
	GetDevicePathWithMountPath(mountPath string) (string, error)
	IsBlockDevicePath(path string) (bool, error)
	PreparePublishPath(target string) error
	EnsureMountPointReady(target string) (bool, error)
	EnsureBlockTargetReady(target string) error
	FormatAndMount(source, target, fstype string, options []string) error
	Mount(source, target, fstype string, options []string) error
	Unmount(target string) error
	CleanupMountPoint(path string, extensiveCheck bool) error
	Resize(source, target string) error
	GetBlockSizeBytes(devicePath string) (int64, error)
}

// NodeStageVolume mount disk device to a staging path
func (d *DriverV2) NodeStageVolume(ctx context.Context, req *csi.NodeStageVolumeRequest) (*csi.NodeStageVolumeResponse, error) {
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

	if acquired := d.volumeLocks.TryAcquire(diskURI); !acquired {
		return nil, status.Errorf(codes.Aborted, volumeOperationAlreadyExistsFmt, diskURI)
	}
	defer d.volumeLocks.Release(diskURI)

	lunStr, ok := req.PublishContext[LUN]
	if !ok {
		return nil, status.Error(codes.InvalidArgument, "lun not provided")
	}

	lun, err := getDiskLUN(lunStr)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to find disk on lun %s. %v", lunStr, err)
	}

	source, err := d.nodeProvisioner.GetDevicePathWithLUN(lun)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to find disk on lun %v. %v", lun, err)
	}

	// If perf optimizations are enabled
	// tweak device settings to enhance performance
	if d.getPerfOptimizationEnabled() {
		profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr, err := getDiskPerfAttributes(req.GetVolumeContext())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to get perf attributes for %s. Error: %v", source, err)
		}

		if d.getDeviceHelper().DiskSupportsPerfOptimization(profile, accountType) {
			if err := d.getDeviceHelper().OptimizeDiskPerformance(d.getNodeInfo(), source, profile, accountType,
				diskSizeGibStr, diskIopsStr, diskBwMbpsStr); err != nil {
				return nil, status.Errorf(codes.Internal, "Failed to optimize device performance for target(%s) error(%s)", source, err)
			}
		} else {
			klog.V(2).Infof("NodeStageVolume: perf optimization is disabled for %s. perfProfile %s accountType %s", source, profile, accountType)
		}
	}

	// If the access type is block, do nothing for stage
	switch req.GetVolumeCapability().GetAccessType().(type) {
	case *csi.VolumeCapability_Block:
		return &csi.NodeStageVolumeResponse{}, nil
	}

	mnt, err := d.nodeProvisioner.EnsureMountPointReady(target)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Could not mount target %q: %v", target, err)
	}
	if mnt {
		klog.V(2).Infof("NodeStageVolume: already mounted on target %s", target)
		return &csi.NodeStageVolumeResponse{}, nil
	}

	// Get fsType and mountOptions that the volume will be formatted and mounted with
	fstype := getDefaultFsType()
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

	// If partition is specified, should mount it only instead of the entire disk.
	if partition, ok := req.GetVolumeContext()[volumeAttributePartition]; ok {
		source = source + "-part" + partition
	}

	// FormatAndMount will format only if needed
	klog.V(2).Infof("NodeStageVolume: formatting %s and mounting at %s with mount options(%s)", source, target, options)
	err = d.nodeProvisioner.FormatAndMount(source, target, fstype, options)
	if err != nil {
		msg := fmt.Sprintf("could not format %q(lun: %q), and mount it at %q", source, lun, target)
		return nil, status.Error(codes.Internal, msg)
	}
	klog.V(2).Infof("NodeStageVolume: format %s and mounting at %s successfully.", source, target)

	// if resize is required, resize filesystem
	if required, ok := req.GetVolumeContext()[resizeRequired]; ok && required == "true" {
		klog.V(2).Infof("NodeStageVolume: fs resize initiating on target(%s) volumeid(%s)", target, diskURI)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "NodeStageVolume: Could not get volume path for %s: %v", target, err)
		}

		if err := d.nodeProvisioner.Resize(source, target); err != nil {
			return nil, status.Errorf(codes.Internal, "NodeStageVolume: Could not resize volume %q (%q):  %v", diskURI, source, err)
		}

		klog.V(2).Infof("NodeStageVolume: fs resize successful on target(%s) volumeid(%s).", target, diskURI)
	}

	return &csi.NodeStageVolumeResponse{}, nil
}

// NodeUnstageVolume unmount disk device from a staging path
func (d *DriverV2) NodeUnstageVolume(ctx context.Context, req *csi.NodeUnstageVolumeRequest) (*csi.NodeUnstageVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID not provided")
	}

	stagingTargetPath := req.GetStagingTargetPath()
	if len(stagingTargetPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Staging target not provided")
	}

	if acquired := d.volumeLocks.TryAcquire(volumeID); !acquired {
		return nil, status.Errorf(codes.Aborted, volumeOperationAlreadyExistsFmt, volumeID)
	}
	defer d.volumeLocks.Release(volumeID)

	klog.V(2).Infof("NodeUnstageVolume: unmounting %s", stagingTargetPath)
	err := d.nodeProvisioner.CleanupMountPoint(stagingTargetPath, false)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount staging target %q: %v", stagingTargetPath, err)
	}
	klog.V(2).Infof("NodeUnstageVolume: unmount %s successfully", stagingTargetPath)

	return &csi.NodeUnstageVolumeResponse{}, nil
}

// NodePublishVolume mount the volume from staging to target path
func (d *DriverV2) NodePublishVolume(ctx context.Context, req *csi.NodePublishVolumeRequest) (*csi.NodePublishVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	if req.GetVolumeCapability() == nil {
		return nil, status.Error(codes.InvalidArgument, "Volume capability missing in request")
	}
	if len(volumeID) == 0 {
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

	err := d.nodeProvisioner.PreparePublishPath(target)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("Target path could not be prepared: %v", err))
	}

	if acquired := d.volumeLocks.TryAcquire(volumeID); !acquired {
		return nil, status.Errorf(codes.Aborted, volumeOperationAlreadyExistsFmt, volumeID)
	}
	defer d.volumeLocks.Release(volumeID)

	mountOptions := []string{"bind"}
	if req.GetReadonly() {
		mountOptions = append(mountOptions, "ro")
	}

	switch req.GetVolumeCapability().GetAccessType().(type) {
	case *csi.VolumeCapability_Block:
		lunStr, ok := req.PublishContext[LUN]
		if !ok {
			return nil, status.Error(codes.InvalidArgument, "lun not provided")
		}

		lun, err := getDiskLUN(lunStr)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to find device path with lun %s. %v", lunStr, err)
		}

		source, err = d.nodeProvisioner.GetDevicePathWithLUN(lun)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to find device path with lun %v. %v", lun, err)
		}

		klog.V(2).Infof("NodePublishVolume [block]: found device path %s with lun %v", source, lun)

		err = d.nodeProvisioner.EnsureBlockTargetReady(target)
		if err != nil {
			return nil, err
		}

	case *csi.VolumeCapability_Mount:
		mnt, err := d.nodeProvisioner.EnsureMountPointReady(target)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "Could not mount target %q: %v", target, err)
		}
		if mnt {
			klog.V(2).Infof("NodePublishVolume: already mounted on target %s", target)
			return &csi.NodePublishVolumeResponse{}, nil
		}
	}

	klog.V(2).Infof("NodePublishVolume: mounting %s at %s", source, target)
	if err := d.nodeProvisioner.Mount(source, target, "", mountOptions); err != nil {
		return nil, status.Errorf(codes.Internal, "Could not mount %q at %q: %v", source, target, err)
	}

	klog.V(2).Infof("NodePublishVolume: mount %s at %s successfully", source, target)

	return &csi.NodePublishVolumeResponse{}, nil
}

// NodeUnpublishVolume unmount the volume from the target path
func (d *DriverV2) NodeUnpublishVolume(ctx context.Context, req *csi.NodeUnpublishVolumeRequest) (*csi.NodeUnpublishVolumeResponse, error) {
	targetPath := req.GetTargetPath()
	volumeID := req.GetVolumeId()

	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing in request")
	}
	if len(targetPath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Target path missing in request")
	}

	klog.V(2).Infof("NodeUnpublishVolume: unmounting volume %s on %s", volumeID, targetPath)
	err := d.nodeProvisioner.CleanupMountPoint(targetPath, false)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmount target %q: %v", targetPath, err)
	}

	if acquired := d.volumeLocks.TryAcquire(volumeID); !acquired {
		return nil, status.Errorf(codes.Aborted, volumeOperationAlreadyExistsFmt, volumeID)
	}
	defer d.volumeLocks.Release(volumeID)

	klog.V(2).Infof("NodeUnpublishVolume: unmount volume %s on %s successfully", volumeID, targetPath)

	return &csi.NodeUnpublishVolumeResponse{}, nil
}

// NodeGetCapabilities return the capabilities of the Node plugin
func (d *DriverV2) NodeGetCapabilities(ctx context.Context, req *csi.NodeGetCapabilitiesRequest) (*csi.NodeGetCapabilitiesResponse, error) {
	return &csi.NodeGetCapabilitiesResponse{
		Capabilities: d.NSCap,
	}, nil
}

// NodeGetInfo return info of the node on which this plugin is running
func (d *DriverV2) NodeGetInfo(ctx context.Context, req *csi.NodeGetInfoRequest) (*csi.NodeGetInfoResponse, error) {
	instances, ok := d.cloudProvisioner.GetCloud().Instances()
	if !ok {
		return nil, status.Error(codes.Internal, "Failed to get instances from cloud provider")
	}

	instanceType, err := instances.InstanceType(context.TODO(), types.NodeName(d.NodeID))
	if err != nil {
		klog.Warningf("Failed to get instance type from Azure cloud provider, nodeName: %v, error: %v", d.NodeID, err)
		instanceType = ""
	}

	topology := &csi.Topology{
		Segments: map[string]string{topologyKey: ""},
	}
	zone, err := d.cloudProvisioner.GetCloud().GetZone(ctx)
	if err != nil {
		klog.Warningf("Failed to get zone from Azure cloud provider, nodeName: %v, error: %v", d.NodeID, err)
	} else {
		if isAvailabilityZone(zone.FailureDomain, d.cloudProvisioner.GetCloud().Location) {
			topology.Segments[topologyKey] = zone.FailureDomain
			topology.Segments[WellKnownTopologyKey] = zone.FailureDomain
			klog.V(2).Infof("NodeGetInfo, nodeName: %v, zone: %v", d.NodeID, zone.FailureDomain)
		}
	}

	maxDataDiskCount := d.VolumeAttachLimit
	if maxDataDiskCount < 0 {
		maxDataDiskCount = getMaxDataDiskCount(instanceType)
	}

	return &csi.NodeGetInfoResponse{
		NodeId:             d.NodeID,
		MaxVolumesPerNode:  maxDataDiskCount,
		AccessibleTopology: topology,
	}, nil
}

func (d *DriverV2) NodeGetVolumeStats(ctx context.Context, req *csi.NodeGetVolumeStatsRequest) (*csi.NodeGetVolumeStatsResponse, error) {
	if len(req.VolumeId) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodeGetVolumeStats volume ID was empty")
	}
	if len(req.VolumePath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "NodeGetVolumeStats volume path was empty")
	}

	_, err := os.Stat(req.VolumePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, status.Errorf(codes.NotFound, "path %s does not exist", req.VolumePath)
		}
		return nil, status.Errorf(codes.Internal, "failed to stat file %s: %v", req.VolumePath, err)
	}

	isBlock, err := d.nodeProvisioner.IsBlockDevicePath(req.VolumePath)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to determine whether %s is block device: %v", req.VolumePath, err)
	}
	if isBlock {
		bcap, err := d.nodeProvisioner.GetBlockSizeBytes(req.VolumePath)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get block capacity on path %s: %v", req.VolumePath, err)
		}
		return &csi.NodeGetVolumeStatsResponse{
			Usage: []*csi.VolumeUsage{
				{
					Unit:  csi.VolumeUsage_BYTES,
					Total: bcap,
				},
			},
		}, nil
	}

	volumeMetrics, err := volume.NewMetricsStatFS(req.VolumePath).GetMetrics()
	if err != nil {
		return nil, err
	}

	available, ok := volumeMetrics.Available.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal, "failed to transform volume available size(%v)", volumeMetrics.Available)
	}
	capacity, ok := volumeMetrics.Capacity.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal, "failed to transform volume capacity size(%v)", volumeMetrics.Capacity)
	}
	used, ok := volumeMetrics.Used.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal, "failed to transform volume used size(%v)", volumeMetrics.Used)
	}

	inodesFree, ok := volumeMetrics.InodesFree.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal, "failed to transform disk inodes free(%v)", volumeMetrics.InodesFree)
	}
	inodes, ok := volumeMetrics.Inodes.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal, "failed to transform disk inodes(%v)", volumeMetrics.Inodes)
	}
	inodesUsed, ok := volumeMetrics.InodesUsed.AsInt64()
	if !ok {
		return nil, status.Errorf(codes.Internal, "failed to transform disk inodes used(%v)", volumeMetrics.InodesUsed)
	}

	return &csi.NodeGetVolumeStatsResponse{
		Usage: []*csi.VolumeUsage{
			{
				Unit:      csi.VolumeUsage_BYTES,
				Available: available,
				Total:     capacity,
				Used:      used,
			},
			{
				Unit:      csi.VolumeUsage_INODES,
				Available: inodesFree,
				Total:     inodes,
				Used:      inodesUsed,
			},
		},
	}, nil
}

// NodeExpandVolume node expand volume
func (d *DriverV2) NodeExpandVolume(ctx context.Context, req *csi.NodeExpandVolumeRequest) (*csi.NodeExpandVolumeResponse, error) {
	volumeID := req.GetVolumeId()
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID not provided")
	}
	capacityBytes := req.GetCapacityRange().GetRequiredBytes()
	volSizeBytes := int64(capacityBytes)
	requestGiB := volumehelper.RoundUpGiB(volSizeBytes)

	volumePath := req.GetVolumePath()
	if len(volumePath) == 0 {
		return nil, status.Error(codes.InvalidArgument, "volume path must be provided")
	}

	isBlock, err := d.nodeProvisioner.IsBlockDevicePath(volumePath)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "failed to determine device path for volumePath [%v]: %v", volumePath, err)
	}
	if isBlock {
		// Noop for Block NodeExpandVolume
		klog.V(4).Infof("NodeExpandVolume succeeded on %v to %s, path check is block so this is a no-op", volumeID, volumePath)
		return &csi.NodeExpandVolumeResponse{}, nil
	}

	volumeCapability := req.GetVolumeCapability()
	if volumeCapability != nil {
		// VolumeCapability is optional, if specified, validate it
		caps := []*csi.VolumeCapability{volumeCapability}
		if !isValidVolumeCapabilities(caps) {
			return nil, status.Error(codes.InvalidArgument, "VolumeCapability is invalid.")
		}

		if blk := volumeCapability.GetBlock(); blk != nil {
			// Noop for Block NodeExpandVolume
			// This should not be executed but if somehow it is set to Block we should be cautious
			klog.Warningf("NodeExpandVolume succeeded on %v to %s, capability is block but block check failed to identify it", volumeID, volumePath)
			return &csi.NodeExpandVolumeResponse{}, nil
		}
	}

	devicePath, err := d.nodeProvisioner.GetDevicePathWithMountPath(volumePath)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, err.Error())
	}

	if err := d.nodeProvisioner.Resize(devicePath, volumePath); err != nil {
		return nil, status.Errorf(codes.Internal, "Could not resize volume %q (%q):  %v", volumeID, devicePath, err)
	}

	gotBlockSizeBytes, err := d.nodeProvisioner.GetBlockSizeBytes(devicePath)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("Could not get size of block volume at path %s: %v", devicePath, err))
	}
	gotBlockGiB := volumehelper.RoundUpGiB(gotBlockSizeBytes)
	if gotBlockGiB < requestGiB {
		// Because size was rounded up, getting more size than requested will be a success.
		return nil, status.Errorf(codes.Internal, "resize requested for %v, but after resizing volume size was %v", requestGiB, gotBlockGiB)
	}
	klog.V(2).Infof("NodeExpandVolume succeeded on resizing volume %v to %v", volumeID, gotBlockSizeBytes)

	return &csi.NodeExpandVolumeResponse{
		CapacityBytes: gotBlockSizeBytes,
	}, nil
}

// ensureMountPoint: create mount point if not exists
// return <true, nil> if it's already a mounted point otherwise return <false, nil>
func (d *DriverV2) ensureMountPoint(target string) (bool, error) {
	klog.Warning("ensureMountPoint method is deprecated.")
	return d.nodeProvisioner.EnsureMountPointReady(target)
}
