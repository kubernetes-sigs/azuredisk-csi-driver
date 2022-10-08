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

package resources

import (
	"context"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/pkg/kubelet/events"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eevents "k8s.io/kubernetes/test/e2e/framework/events"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	imageutils "k8s.io/kubernetes/test/utils/image"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	podutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/pod"
)

type TestPod struct {
	Client    clientset.Interface
	Pod       *v1.Pod
	Namespace *v1.Namespace
}

func NewTestPod(c clientset.Interface, ns *v1.Namespace, command, schedulerName string, isWindows bool, winServerVer string) *TestPod {
	testPod := &TestPod{
		Client:    c,
		Namespace: ns,
		Pod: &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "azuredisk-volume-tester-",
			},
			Spec: v1.PodSpec{
				SchedulerName: schedulerName,
				NodeSelector:  map[string]string{"kubernetes.io/os": "linux"},
				Containers: []v1.Container{
					{
						Name:    "volume-tester",
						Image:   imageutils.GetE2EImage(imageutils.BusyBox),
						Command: []string{"/bin/sh"},
						Args:    []string{"-c", command},
					},
				},
				RestartPolicy: v1.RestartPolicyNever,
				Volumes:       make([]v1.Volume, 0),
			},
		},
	}
	if isWindows {
		testPod.Pod.Spec.NodeSelector = map[string]string{
			"kubernetes.io/os": "windows",
		}
		testPod.Pod.Spec.Containers[0].Image = "mcr.microsoft.com/windows/servercore:" + getWinImageTag(winServerVer)
		testPod.Pod.Spec.Containers[0].Command = []string{"powershell.exe"}
		testPod.Pod.Spec.Containers[0].Args = []string{"-Command", command}
	}

	return testPod
}

func (t *TestPod) Create() {
	var err error

	t.Pod, err = t.Client.CoreV1().Pods(t.Namespace.Name).Create(context.TODO(), t.Pod, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForSuccess() {
	err := e2epod.WaitForPodSuccessInNamespaceSlow(t.Client, t.Pod.Name, t.Namespace.Name)
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForRunning() {
	err := e2epod.WaitForPodRunningInNamespace(t.Client, t.Pod)
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForRunningLong() {
	err := e2epod.WaitForPodRunningInNamespaceSlow(t.Client, t.Pod.Name, t.Namespace.Name)
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForFailedMountError() {
	err := e2eevents.WaitTimeoutForEvent(
		t.Client,
		t.Namespace.Name,
		fields.Set{"reason": events.FailedMountVolume}.AsSelector().String(),
		"MountVolume.MountDevice failed for volume",
		testconsts.PollLongTimeout)
	framework.ExpectNoError(err)
}

func (t *TestPod) WaitForFailure() {
	err := e2epod.WaitForPodCondition(t.Client, t.Namespace.Name, t.Pod.Name, testconsts.FailedConditionDescription, testconsts.SlowPodStartTimeout, podFailedCondition)
	framework.ExpectNoError(err)
}

func (t *TestPod) SetupVolume(pvc *v1.PersistentVolumeClaim, name, mountPath string, readOnly bool) {
	volumeMount := v1.VolumeMount{
		Name:      name,
		MountPath: mountPath,
		ReadOnly:  readOnly,
	}
	t.Pod.Spec.Containers[0].VolumeMounts = append(t.Pod.Spec.Containers[0].VolumeMounts, volumeMount)

	volume := v1.Volume{
		Name: name,
		VolumeSource: v1.VolumeSource{
			PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvc.Name,
			},
		},
	}
	t.Pod.Spec.Volumes = append(t.Pod.Spec.Volumes, volume)
}

func (t *TestPod) SetupInlineVolume(name, mountPath, diskURI string, readOnly bool) {
	volumeMount := v1.VolumeMount{
		Name:      name,
		MountPath: mountPath,
		ReadOnly:  readOnly,
	}
	t.Pod.Spec.Containers[0].VolumeMounts = append(t.Pod.Spec.Containers[0].VolumeMounts, volumeMount)

	kind := v1.AzureDataDiskKind("Managed")
	diskName, _ := azureutils.GetDiskName(diskURI)
	volume := v1.Volume{
		Name: name,
		VolumeSource: v1.VolumeSource{
			AzureDisk: &v1.AzureDiskVolumeSource{
				DiskName:    diskName,
				DataDiskURI: diskURI,
				ReadOnly:    &readOnly,
				Kind:        &kind,
			},
		},
	}
	t.Pod.Spec.Volumes = append(t.Pod.Spec.Volumes, volume)
}

func (t *TestPod) SetupRawBlockVolume(pvc *v1.PersistentVolumeClaim, name, devicePath string) {
	volumeDevice := v1.VolumeDevice{
		Name:       name,
		DevicePath: devicePath,
	}
	t.Pod.Spec.Containers[0].VolumeDevices = make([]v1.VolumeDevice, 0)
	t.Pod.Spec.Containers[0].VolumeDevices = append(t.Pod.Spec.Containers[0].VolumeDevices, volumeDevice)

	volume := v1.Volume{
		Name: name,
		VolumeSource: v1.VolumeSource{
			PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvc.Name,
			},
		},
	}
	t.Pod.Spec.Volumes = append(t.Pod.Spec.Volumes, volume)
}

func (t *TestPod) SetupVolumeMountWithSubpath(pvc *v1.PersistentVolumeClaim, name, mountPath string, subpath string, readOnly bool) {
	volumeMount := v1.VolumeMount{
		Name:      name,
		MountPath: mountPath,
		SubPath:   subpath,
		ReadOnly:  readOnly,
	}

	t.Pod.Spec.Containers[0].VolumeMounts = append(t.Pod.Spec.Containers[0].VolumeMounts, volumeMount)

	volume := v1.Volume{
		Name: name,
		VolumeSource: v1.VolumeSource{
			PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
				ClaimName: pvc.Name,
			},
		},
	}

	t.Pod.Spec.Volumes = append(t.Pod.Spec.Volumes, volume)
}

func (t *TestPod) SetNodeSelector(nodeSelector map[string]string) {
	t.Pod.Spec.NodeSelector = nodeSelector
}

func (t *TestPod) SetAffinity(affinity *v1.Affinity) {
	t.Pod.Spec.Affinity = affinity
}

func (t *TestPod) SetLabel(labels map[string]string) {
	t.Pod.ObjectMeta.Labels = labels
}

func (t *TestPod) SetNodeToleration(nodeTolerations ...v1.Toleration) {
	t.Pod.Spec.Tolerations = nodeTolerations
}

func (t *TestPod) Cleanup() {
	podutil.CleanupPodOrFail(t.Client, t.Pod.Name, t.Namespace.Name)
}

func (t *TestPod) GetZoneForVolume(index int) string {
	pvcSource := t.Pod.Spec.Volumes[index].VolumeSource.PersistentVolumeClaim
	if pvcSource == nil {
		return ""
	}

	pvc, err := t.Client.CoreV1().PersistentVolumeClaims(t.Namespace.Name).Get(context.TODO(), pvcSource.ClaimName, metav1.GetOptions{})
	framework.ExpectNoError(err)

	pv, err := t.Client.CoreV1().PersistentVolumes().Get(context.TODO(), pvc.Spec.VolumeName, metav1.GetOptions{})
	framework.ExpectNoError(err)

	zone := ""
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		for _, ex := range term.MatchExpressions {
			if ex.Key == testconsts.TopologyKey && ex.Operator == v1.NodeSelectorOpIn {
				zone = ex.Values[0]
			}
		}
	}

	return zone
}

func (t *TestPod) Logs() ([]byte, error) {
	return podutil.PodLogs(t.Client, t.Pod.Name, t.Namespace.Name)
}
