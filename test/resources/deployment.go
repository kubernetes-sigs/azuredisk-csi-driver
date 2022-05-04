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
	"fmt"
	"math/rand"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	testutils "k8s.io/kubernetes/test/utils"
	imageutils "k8s.io/kubernetes/test/utils/image"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	podutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/pod"
)

type TestDeployment struct {
	Client     clientset.Interface
	Deployment *apps.Deployment
	Namespace  *v1.Namespace
	Pods       []PodDetails
}

func NewTestDeployment(c clientset.Interface, ns *v1.Namespace, command string, volumeMounts []v1.VolumeMount, volumeDevices []v1.VolumeDevice, volumes []v1.Volume, replicaCount int32, isWindows, useCMD, useAntiAffinity bool, schedulerName string) *TestDeployment {
	generateName := "azuredisk-volume-tester-"
	selectorValue := fmt.Sprintf("%s%d", generateName, rand.Int())

	testDeployment := &TestDeployment{
		Client:    c,
		Namespace: ns,
		Deployment: &apps.Deployment{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: generateName,
			},
			Spec: apps.DeploymentSpec{
				Replicas: &replicaCount,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": selectorValue},
				},
				Template: v1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": selectorValue},
					},
					Spec: v1.PodSpec{
						SchedulerName: schedulerName,
						NodeSelector:  map[string]string{"kubernetes.io/os": "linux"},
						Containers: []v1.Container{
							{
								Name:          "volume-tester",
								Image:         imageutils.GetE2EImage(imageutils.BusyBox),
								Command:       []string{"/bin/sh"},
								Args:          []string{"-c", command},
								VolumeMounts:  volumeMounts,
								VolumeDevices: volumeDevices,
							},
						},
						RestartPolicy: v1.RestartPolicyAlways,
						Volumes:       volumes,
					},
				},
			},
		},
	}

	if useAntiAffinity {
		affinity := &v1.Affinity{
			PodAntiAffinity: &v1.PodAntiAffinity{
				RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
					{
						LabelSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"app": selectorValue},
						},
						TopologyKey: testconsts.TopologyKey,
					},
				},
			},
		}
		testDeployment.Deployment.Spec.Template.Spec.Affinity = affinity
	}

	if isWindows {
		testDeployment.Deployment.Spec.Template.Spec.NodeSelector = map[string]string{
			"kubernetes.io/os": "windows",
		}
		testDeployment.Deployment.Spec.Template.Spec.Containers[0].Image = "mcr.microsoft.com/windows/servercore:ltsc2019"
		if useCMD {
			testDeployment.Deployment.Spec.Template.Spec.Containers[0].Command = []string{"cmd"}
			testDeployment.Deployment.Spec.Template.Spec.Containers[0].Args = []string{"/c", command}
		} else {
			testDeployment.Deployment.Spec.Template.Spec.Containers[0].Command = []string{"powershell.exe"}
			testDeployment.Deployment.Spec.Template.Spec.Containers[0].Args = []string{"-Command", command}
		}
	}

	return testDeployment
}

func (t *TestDeployment) Create() {
	var err error
	t.Deployment, err = t.Client.AppsV1().Deployments(t.Namespace.Name).Create(context.TODO(), t.Deployment, metav1.CreateOptions{})
	framework.ExpectNoError(err)
	err = testutils.WaitForDeploymentComplete(t.Client, t.Deployment, e2elog.Logf, testconsts.Poll, testconsts.PollLongTimeout)
	framework.ExpectNoError(err)
	pods, err := podutil.GetPodsForDeployment(t.Client, t.Deployment)
	framework.ExpectNoError(err)
	for _, pod := range pods.Items {
		t.Pods = append(t.Pods, PodDetails{Name: pod.Name})
	}
}

func (t *TestDeployment) WaitForPodReady() {
	pods, err := podutil.GetPodsForDeployment(t.Client, t.Deployment)
	framework.ExpectNoError(err)
	t.Pods = []PodDetails{}
	for _, pod := range pods.Items {
		var podPersistentVolumes []VolumeDetails
		for _, volume := range pod.Spec.Volumes {
			if volume.VolumeSource.PersistentVolumeClaim != nil {
				pvc, err := t.Client.CoreV1().PersistentVolumeClaims(t.Namespace.Name).Get(context.TODO(), volume.VolumeSource.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
				framework.ExpectNoError(err)
				accessMode := v1.ReadWriteOnce
				if len(pvc.Spec.AccessModes) > 0 {
					accessMode = pvc.Spec.AccessModes[0]
				}
				newVolume := VolumeDetails{
					PersistentVolume: &v1.PersistentVolume{
						ObjectMeta: metav1.ObjectMeta{
							Name: pvc.Spec.VolumeName,
						},
					},
					VolumeAccessMode: accessMode,
				}
				podPersistentVolumes = append(podPersistentVolumes, newVolume)
			}
		}
		t.Pods = append(t.Pods, PodDetails{Name: pod.Name, Volumes: podPersistentVolumes})
	}
	ch := make(chan error, len(t.Pods))
	defer close(ch)
	for _, pod := range pods.Items {
		go func(client clientset.Interface, pod v1.Pod) {
			err = e2epod.WaitForPodRunningInNamespace(t.Client, &pod)
			ch <- err
		}(t.Client, pod)
	}
	// Wait on all goroutines to report on pod ready
	for range t.Pods {
		err := <-ch
		framework.ExpectNoError(err)
	}
}

func (t *TestDeployment) Exec(command []string, expectedString string) {
	for _, pod := range t.Pods {
		_, err := framework.LookForStringInPodExec(t.Namespace.Name, pod.Name, command, expectedString, testconsts.ExecTimeout)
		framework.ExpectNoError(err)
	}
}

func (t *TestDeployment) DeletePodAndWait() {
	ch := make(chan error, len(t.Pods))
	for _, pod := range t.Pods {
		e2elog.Logf("Deleting pod %q in namespace %q", pod.Name, t.Namespace.Name)
		go func(client clientset.Interface, ns, podName string) {
			err := client.CoreV1().Pods(ns).Delete(context.TODO(), podName, metav1.DeleteOptions{})
			ch <- err
		}(t.Client, t.Namespace.Name, pod.Name)
	}
	// Wait on all goroutines to report on pod delete
	for _, pod := range t.Pods {
		err := <-ch
		if err != nil {
			if !errors.IsNotFound(err) {
				framework.ExpectNoError(fmt.Errorf("pod %q Delete API error: %v", pod.Name, err))
			}
		}
	}

	conditionFunc := func(podName string, ch chan error) {
		e2elog.Logf("Waiting for pod %q in namespace %q to be fully deleted", podName, t.Namespace.Name)
		err := e2epod.WaitForPodNoLongerRunningInNamespace(t.Client, podName, t.Namespace.Name)
		ch <- err
	}
	t.WaitForPodStatus(conditionFunc)
}

func (t *TestDeployment) ForceDeletePod(podName string) error {
	err := t.Client.CoreV1().Pods(t.Deployment.Namespace).Delete(context.Background(), podName, *metav1.NewDeleteOptions(0))
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	// remove pod from the deployment's pod list
	pods := make([]PodDetails, len(t.Pods)-1)
	i := 0
	for _, tPod := range t.Pods {
		if tPod.Name == podName {
			continue
		}
		pods[i] = tPod
		i++
	}
	t.Pods = pods
	return nil
}

func (t *TestDeployment) WaitForPodTerminating(timeout time.Duration) {
	conditionFunc := func(podName string, ch chan error) {
		e2elog.Logf("Waiting for pod %q in namespace %q to start terminating", podName, t.Namespace.Name)
		err := wait.PollImmediate(time.Duration(15)*time.Second, timeout, func() (bool, error) {
			podObj, err := t.Client.CoreV1().Pods(t.Namespace.Name).Get(context.Background(), podName, metav1.GetOptions{})
			if err != nil && !errors.IsNotFound(err) {
				return false, err
			}
			return !podObj.DeletionTimestamp.IsZero(), nil
		})
		ch <- err
	}
	t.WaitForPodStatus(conditionFunc)
}

func (t *TestDeployment) WaitForPodStatus(conditionFunc func(podName string, ch chan error)) {
	ch := make(chan error, len(t.Pods))

	for _, pod := range t.Pods {
		go conditionFunc(pod.Name, ch)
	}

	for _, pod := range t.Pods {
		err := <-ch
		if err != nil && !errors.IsNotFound(err) {
			framework.ExpectNoError(fmt.Errorf("pod %q error waiting for delete: %v", pod.Name, err))
		}
	}
}

func (t *TestDeployment) Cleanup() {
	e2elog.Logf("deleting Deployment %q/%q", t.Namespace.Name, t.Deployment.Name)
	body, err := t.Logs()
	if err != nil {
		e2elog.Logf("Error getting logs for %s: %v", t.Deployment.Name, err)
	} else {
		for i, logs := range body {
			e2elog.Logf("Pod %s has the following logs: %s", t.Pods[i], logs)
		}
	}
	err = t.Client.AppsV1().Deployments(t.Namespace.Name).Delete(context.TODO(), t.Deployment.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
}

func (t *TestDeployment) Logs() (logs [][]byte, err error) {
	for _, pod := range t.Pods {
		log, err := podutil.PodLogs(t.Client, pod.Name, t.Namespace.Name)
		if err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	return
}
