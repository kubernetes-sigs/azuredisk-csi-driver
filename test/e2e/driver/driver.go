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

package driver

import (
	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v4/apis/volumesnapshot/v1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	VolumeSnapshotClassKind = "VolumeSnapshotClass"
	SnapshotAPIVersion      = "snapshot.storage.k8s.io/v1"
)

type PVTestDriver interface {
	GetDynamicProvisionStorageClass(parameters map[string]string, mountOptions []string, reclaimPolicy *v1.PersistentVolumeReclaimPolicy, bindingMode *storagev1.VolumeBindingMode, allowedTopologyValues []string, namespace string) *storagev1.StorageClass
	GetPersistentVolume(volumeID, fsType, size string, volumeMode v1.PersistentVolumeMode, accessMode v1.PersistentVolumeAccessMode, reclaimPolicy *v1.PersistentVolumeReclaimPolicy, namespace string, volumeContext map[string]string) *v1.PersistentVolume
	GetVolumeSnapshotClass(namespace string, parameters map[string]string) *snapshotv1.VolumeSnapshotClass
}

// DynamicPVTestDriver represents an interface for a CSI driver that supports DynamicPV
type DynamicPVTestDriver interface {
	// GetDynamicProvisionStorageClass returns a StorageClass dynamic provision Persistent Volume
	GetDynamicProvisionStorageClass(parameters map[string]string, mountOptions []string, reclaimPolicy *v1.PersistentVolumeReclaimPolicy, bindingMode *storagev1.VolumeBindingMode, allowedTopologyValues []string, namespace string) *storagev1.StorageClass
}

// PreProvisionedVolumeTestDriver represents an interface for a CSI driver that supports pre-provisioned volume
type PreProvisionedVolumeTestDriver interface {
	// GetPersistentVolume returns a PersistentVolume with pre-provisioned volumeHandle
	GetPersistentVolume(volumeID, fsType, size string, volumeMode v1.PersistentVolumeMode, accessMode v1.PersistentVolumeAccessMode, reclaimPolicy *v1.PersistentVolumeReclaimPolicy, namespace string, volumeContext map[string]string) *v1.PersistentVolume
}

type VolumeSnapshotTestDriver interface {
	GetVolumeSnapshotClass(namespace string, parameters map[string]string) *snapshotv1.VolumeSnapshotClass
}

func getStorageClass(
	generateName string,
	provisioner string,
	parameters map[string]string,
	mountOptions []string,
	reclaimPolicy *v1.PersistentVolumeReclaimPolicy,
	bindingMode *storagev1.VolumeBindingMode,
	allowedTopologies []v1.TopologySelectorTerm,
) *storagev1.StorageClass {
	if reclaimPolicy == nil {
		defaultReclaimPolicy := v1.PersistentVolumeReclaimDelete
		reclaimPolicy = &defaultReclaimPolicy
	}
	if bindingMode == nil {
		defaultBindingMode := storagev1.VolumeBindingImmediate
		bindingMode = &defaultBindingMode
	}
	allowVolumeExpansion := true
	return &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: generateName,
		},
		Provisioner:          provisioner,
		Parameters:           parameters,
		MountOptions:         mountOptions,
		ReclaimPolicy:        reclaimPolicy,
		VolumeBindingMode:    bindingMode,
		AllowedTopologies:    allowedTopologies,
		AllowVolumeExpansion: &allowVolumeExpansion,
	}
}

func getVolumeSnapshotClass(generateName, provisioner string, parameters map[string]string) *snapshotv1.VolumeSnapshotClass {
	return &snapshotv1.VolumeSnapshotClass{
		TypeMeta: metav1.TypeMeta{
			Kind:       VolumeSnapshotClassKind,
			APIVersion: SnapshotAPIVersion,
		},
		Parameters: parameters,
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: generateName,
		},
		Driver:         provisioner,
		DeletionPolicy: snapshotv1.VolumeSnapshotContentDelete,
	}
}
