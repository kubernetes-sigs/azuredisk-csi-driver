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
	"encoding/json"
	"fmt"

	"os"
	"path/filepath"

	directvolume "github.com/kata-containers/kata-containers/src/runtime/pkg/direct-volume"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
)

const (
	podNameField      = "csi.storage.k8s.io/pod.name"
	podNamespaceField = "csi.storage.k8s.io/pod.namespace"

	kataRuntimeClassAnnotationKey   = "azure.csi.disk/kata-mount"
	kataRuntimeClassAnnotationValue = "direct-volume"

	kataDirectVolumeType = "directvol"
	kataVolumeRoot       = "/run/kata-containers/shared/direct-volumes"
	kataMountInfoFile    = "mountInfo.json"
	kataVolumeIDKey      = "azure.csi.disk/volume-id"
)

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

// AddMountInfo publishes a new volume without overwriting an assignment.
func (*kataDirectVolume) AddMountInfo(target string, info directvolume.MountInfo) error {
	return writeKataMountInfo(kataVolumeRoot, target, info)
}

// writeKataMountInfo exclusively creates metadata, preserving existing assignments.
// Interrupted writes fail closed during lookup until target-scoped unpublish cleanup.
func writeKataMountInfo(root, target string, info directvolume.MountInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	dir := filepath.Join(root, base64.URLEncoding.EncodeToString([]byte(target)))
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, kataMountInfoFile), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
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
	options := collectMountOptions(fsType, flags)
	if readonly {
		options = append(options, "ro")
	}
	return options
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
	if pod.Spec.RuntimeClassName == nil || *pod.Spec.RuntimeClassName == "" {
		return nil, nil
	}

	runtimeClass, err := kubeClient.NodeV1().RuntimeClasses().Get(ctx, *pod.Spec.RuntimeClassName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("get runtime class %q: %w", *pod.Spec.RuntimeClassName, err)
	}

	if runtimeClass.Annotations[kataRuntimeClassAnnotationKey] == kataRuntimeClassAnnotationValue {
		return pod, nil
	}

	return nil, nil
}
