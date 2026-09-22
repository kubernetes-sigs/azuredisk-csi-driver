/*
Copyright 2026 The Kubernetes Authors.

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
	"encoding/base64"
	"fmt"
	"os"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	directvolume "github.com/kata-containers/kata-containers/src/runtime/pkg/direct-volume"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
)

const (
	podNameField      = "csi.storage.k8s.io/pod.name"
	podNamespaceField = "csi.storage.k8s.io/pod.namespace"
	podUIDField       = "csi.storage.k8s.io/pod.uid"

	kataAnnotationKey   = "azure.csi.disk/kata-mount"
	kataAnnotationValue = "direct-volume"

	kataDirectVolumeType = "directvol"
	kataVolumeRoot       = "/run/kata-containers/shared/direct-volumes"
	kataVolumeIDKey      = "azure.csi.disk/volume-id"
)

// initKataNode snapshots node opt-in during driver startup, before serving CSI requests.
func (d *Driver) initKataNode(ctx context.Context) error {
	if d.NodeID == "" {
		return nil
	}
	if d.kubeClient == nil {
		return fmt.Errorf("kubeClient is nil")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	node, err := d.kubeClient.CoreV1().Nodes().Get(ctx, d.NodeID, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get node %q: %w", d.NodeID, err)
	}
	d.isKataNode = node.Annotations[kataAnnotationKey] == kataAnnotationValue
	return nil
}

// kataSupported requires both driver enablement and the startup node opt-in.
func (d *Driver) kataSupported() bool {
	return d.enableKataMount && d.isKataNode
}

// kataDirectVolumer is the interface for Kata's DirectVolume API.
// This is reimplemented in tests.
type kataDirectVolumer interface {
	AddMountInfo(string, directvolume.MountInfo) error
	Remove(string) error
	IsVolumeMounted(string) (bool, error)

	IsVolumeMountedByID(string) (bool, error)
	FindMountInfo(string) (string, error)
}

type kataDirectVolume struct{}

// AddMountInfo delegates publication to Kata's writer, which overwrites metadata.
// The driver is the sole writer for its CSI targets; kataPublished checks existing
// assignments under volumeLocks before NodePublishVolume calls this method.
func (*kataDirectVolume) AddMountInfo(target string, info directvolume.MountInfo) error {
	return directvolume.AddMountInfo(target, info)
}

// Remove finishes target-scoped cleanup, including an interrupted earlier removal.
func (*kataDirectVolume) Remove(target string) error {
	return directvolume.Remove(target)
}

// IsVolumeMounted reports whether Kata has metadata for the target.
func (*kataDirectVolume) IsVolumeMounted(target string) (bool, error) {
	return directvolume.IsVolumeMounted(target)
}

// IsVolumeMountedByID reports a metadata assignment, not an observed guest mount.
func (s *kataDirectVolume) IsVolumeMountedByID(volumeID string) (bool, error) {
	target, err := s.FindMountInfo(volumeID)
	return target != "", err
}

// FindMountInfo returns the metadata assignment target, or empty if absent; it does not observe guest mounts.
func (*kataDirectVolume) FindMountInfo(volumeID string) (string, error) {
	return kataFindMountInfo(kataVolumeRoot, volumeID, directvolume.VolumeMountInfo)
}

// kataFindMountInfo scans assignments; callers hold the CSI volume lock.
// Like directvolume.VolumeMountInfo, read must return nonnil info on success.
func kataFindMountInfo(root, volumeID string, read func(string) (*directvolume.MountInfo, error)) (string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, entry := range entries {
		path, err := base64.URLEncoding.DecodeString(entry.Name())
		if err != nil {
			return "", fmt.Errorf("invalid Kata record %q", entry.Name())
		}
		info, err := read(string(path))
		if os.IsNotExist(err) {
			// Cleanup can outlive a released record, which Kata can no longer consume.
			continue
		}
		if err != nil {
			return "", fmt.Errorf("unusable Kata record at %q: %v", path, err)
		}
		if info.Metadata[kataVolumeIDKey] == volumeID {
			return string(path), nil
		}
	}
	return "", nil
}

// kataMountOptions returns the guest mount options for a publication request.
func kataMountOptions(fsType string, flags []string, readonly bool) []string {
	options, _ := azureutils.RemoveOptionIfExists(collectMountOptions(fsType, flags), "directmount")
	if readonly {
		options = append(options, "ro")
	}
	return options
}

// kataIsMountPoint checks existing mounts without creating or repairing a target.
func (d *Driver) kataIsMountPoint(path string) (bool, error) {
	mounted, err := d.mounter.Interface.IsMountPoint(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	return mounted, err
}

// kataPublished preserves an established publication before RuntimeClass discovery.
func (d *Driver) kataPublished(req *csi.NodePublishVolumeRequest) (bool, error) {
	target, err := d.kataDirectVolume.FindMountInfo(req.VolumeId)
	if err != nil {
		return false, status.Errorf(codes.Internal, "could not check Kata assignment: %v", err)
	}
	if target == "" {
		// Unmounting a damaged target here would erase its mode before a retry.
		mounted, err := d.kataIsMountPoint(req.TargetPath)
		if err != nil {
			return false, status.Errorf(codes.Internal, "could not verify target %q: %v", req.TargetPath, err)
		}
		return mounted, nil
	}
	if target != req.GetTargetPath() {
		return false, status.Errorf(codes.FailedPrecondition, "volume %s conflicts with the Kata publication at %q", req.VolumeId, target)
	}
	return true, nil
}

// kataRestoreStaging remounts an existing filesystem after kataPublished ruled out DAV.
func (d *Driver) kataRestoreStaging(req *csi.NodePublishVolumeRequest) error {
	if mounted, err := d.kataIsMountPoint(req.StagingTargetPath); mounted || err != nil {
		return err // nil if already mounted
	}
	fsType, flags, err := resolveFSType(req.VolumeCapability, req.VolumeContext)
	if err != nil {
		return err
	}
	device, err := d.getDevicePathWithLUN(req.PublishContext[consts.LUN])
	if err != nil {
		return err
	}
	if partition, ok := req.VolumeContext[consts.VolumeAttributePartition]; ok {
		device += "-part" + partition
	}
	if err := volumehelper.MakeDir(req.StagingTargetPath); err != nil {
		return err
	}
	options, _ := azureutils.RemoveOptionIfExists(collectMountOptions(fsType, flags), "directmount")
	// A typed mount fails for an absent/wrong filesystem; it never invokes mkfs
	// or fsck. Publish Readonly belongs on the target bind, not this staging mount.
	return d.mounter.Mount(device, req.StagingTargetPath, fsType, options)
}

// kataGetMountPod returns the pod described by volumeContext if the
// pod's runtime class is annotated to use Kata mounts. Otherwise, it
// returns nil.
func kataGetMountPod(ctx context.Context, kubeClient clientset.Interface, volumeContext map[string]string) (*corev1.Pod, error) {
	if kubeClient == nil {
		return nil, fmt.Errorf("kubeClient is nil")
	}

	podName := volumeContext[podNameField]
	podNamespace := volumeContext[podNamespaceField]
	if podName == "" || podNamespace == "" {
		return nil, nil
	}

	pod, err := kubeClient.CoreV1().Pods(podNamespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get pod %s/%s: %w", podNamespace, podName, err)
	}
	if string(pod.UID) != volumeContext[podUIDField] {
		// If a pod was recreated with the same name in the meantime,
		// return a nil pod as it no longer matches the volume, but
		// don't bother returning an error.
		return nil, nil
	}
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName == "" {
		return nil, nil
	}

	runtimeClass, err := kubeClient.NodeV1().RuntimeClasses().Get(ctx, *pod.Spec.RuntimeClassName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get runtime class %q: %w", *pod.Spec.RuntimeClassName, err)
	}

	if runtimeClass.Annotations[kataAnnotationKey] == kataAnnotationValue {
		return pod, nil
	}

	return nil, nil
}
