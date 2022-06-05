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

package azuredisk

import (
	"context"

	v1 "k8s.io/api/core/v1"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

type CrdProvisioner interface {
	RegisterDriverNode(ctx context.Context, node *v1.Node, nodePartition string, nodeID string) error
	CreateVolume(ctx context.Context, volumeName string, capacityRange *azdiskv1beta2.CapacityRange,
		volumeCapabilities []azdiskv1beta2.VolumeCapability, parameters map[string]string,
		secrets map[string]string, volumeContentSource *azdiskv1beta2.ContentVolumeSource,
		accessibilityReq *azdiskv1beta2.TopologyRequirement) (*azdiskv1beta2.AzVolumeStatusDetail, error)
	DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error
	PublishVolume(ctx context.Context, volumeID string, nodeID string, volumeCapability *azdiskv1beta2.VolumeCapability,
		readOnly bool, secrets map[string]string, volumeContext map[string]string) (map[string]string, error)
	WaitForAttach(ctx context.Context, volume, node string) (*azdiskv1beta2.AzVolumeAttachment, error)
	UnpublishVolume(ctx context.Context, volumeID string, nodeID string, secrets map[string]string, mode consts.UnpublishMode) error
	WaitForDetach(ctx context.Context, volume, node string) error
	GetAzVolumeAttachment(ctx context.Context, volumeID string, nodeID string) (*azdiskv1beta2.AzVolumeAttachment, error)
	ExpandVolume(ctx context.Context, volumeID string, capacityRange *azdiskv1beta2.CapacityRange, secrets map[string]string) (*azdiskv1beta2.AzVolumeStatusDetail, error)
	GetDiskClientSet() azdisk.Interface
}

// NodeProvisioner defines the methods required to manage staging and publishing of mount points.
type NodeProvisioner interface {
	GetDevicePathWithLUN(ctx context.Context, lun int) (string, error)
	GetDevicePathWithMountPath(mountPath string) (string, error)
	IsBlockDevicePath(path string) (bool, error)
	PreparePublishPath(target string) error
	EnsureMountPointReady(target string) (bool, error)
	EnsureBlockTargetReady(target string) error
	FormatAndMount(source, target, fstype string, options []string) error
	Mount(source, target, fstype string, options []string) error
	Unmount(target string) error
	CleanupMountPoint(path string, extensiveCheck bool) error
	RescanVolume(devicePath string) error
	Resize(source, target string) error
	GetBlockSizeBytes(devicePath string) (int64, error)
}
