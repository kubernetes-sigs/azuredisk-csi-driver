//go:build linux
// +build linux

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
	"context"
	"os"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/kubernetes/pkg/volume"
	"k8s.io/mount-utils"
)

// NeedsResize returns true if the volume needs to be resized.
func (p *NodeProvisioner) NeedsResize(devicePath, volumePath string) (bool, error) {
	resizer := mount.NewResizeFs(p.mounter.Exec)

	return resizer.NeedResize(devicePath, volumePath)
}

// GetVolumeStats returns usage information for the specified volume.
func (p *NodeProvisioner) GetVolumeStats(ctx context.Context, target string) ([]*csi.VolumeUsage, error) {
	var volUsages []*csi.VolumeUsage
	_, err := os.Stat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, status.Errorf(codes.NotFound, "path %s does not exist", target)
		}
		return volUsages, status.Errorf(codes.Internal, "failed to stat file %s: %v", target, err)
	}

	isBlock, err := p.IsBlockDevicePath(target)
	if err != nil {
		return volUsages, status.Errorf(codes.NotFound, "failed to determine whether %s is block device: %v", target, err)
	}

	if isBlock {
		bcap, err := p.GetBlockSizeBytes(target)
		if err != nil {
			return volUsages, status.Errorf(codes.Internal, "failed to get block capacity on path %s: %v", target, err)
		}
		return []*csi.VolumeUsage{
			{
				Unit:  csi.VolumeUsage_BYTES,
				Total: bcap,
			},
		}, nil
	}

	volumeMetrics, err := volume.NewMetricsStatFS(target).GetMetrics()
	if err != nil {
		return volUsages, err
	}

	available, ok := volumeMetrics.Available.AsInt64()
	if !ok {
		return volUsages, status.Errorf(codes.Internal, "failed to transform volume available size(%v)", volumeMetrics.Available)
	}
	capacity, ok := volumeMetrics.Capacity.AsInt64()
	if !ok {
		return volUsages, status.Errorf(codes.Internal, "failed to transform volume capacity size(%v)", volumeMetrics.Capacity)
	}
	used, ok := volumeMetrics.Used.AsInt64()
	if !ok {
		return volUsages, status.Errorf(codes.Internal, "failed to transform volume used size(%v)", volumeMetrics.Used)
	}

	inodesFree, ok := volumeMetrics.InodesFree.AsInt64()
	if !ok {
		return volUsages, status.Errorf(codes.Internal, "failed to transform disk inodes free(%v)", volumeMetrics.InodesFree)
	}
	inodes, ok := volumeMetrics.Inodes.AsInt64()
	if !ok {
		return volUsages, status.Errorf(codes.Internal, "failed to transform disk inodes(%v)", volumeMetrics.Inodes)
	}
	inodesUsed, ok := volumeMetrics.InodesUsed.AsInt64()
	if !ok {
		return volUsages, status.Errorf(codes.Internal, "failed to transform disk inodes used(%v)", volumeMetrics.InodesUsed)
	}

	return []*csi.VolumeUsage{
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
	}, nil
}
