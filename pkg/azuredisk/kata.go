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
	"fmt"

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
)

// kataDirectVolumer is the interface for Kata's DirectVolume API.
// This is reimplemented in tests.
type kataDirectVolumer interface {
	AddMountInfo(string, directvolume.MountInfo) error
	Remove(string) error
	IsVolumeMounted(string) (bool, error)
}

type kataDirectVolume struct{}

func (*kataDirectVolume) AddMountInfo(volumePath string, mountInfo directvolume.MountInfo) error {
	return directvolume.AddMountInfo(volumePath, mountInfo)
}

func (*kataDirectVolume) Remove(volumePath string) error {
	return directvolume.Remove(volumePath)
}

func (*kataDirectVolume) IsVolumeMounted(volumePath string) (bool, error) {
	return directvolume.IsVolumeMounted(volumePath)
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
