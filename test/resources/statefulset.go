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
	"time"

	"github.com/onsi/ginkgo"
	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	e2epv "k8s.io/kubernetes/test/e2e/framework/pv"
	imageutils "k8s.io/kubernetes/test/utils/image"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	podutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/pod"
	testutils "sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
)

type TestStatefulset struct {
	Client      clientset.Interface
	Statefulset *apps.StatefulSet
	Namespace   *v1.Namespace
	PodNames    []string
	AllPods     []PodDetails
}

func NewTestStatefulset(c clientset.Interface, ns *v1.Namespace, command string, pvc []v1.PersistentVolumeClaim, volumeMount []v1.VolumeMount, isWindows, useCMD bool, schedulerName string, replicaCount int, labels map[string]string, winServerVer string) *TestStatefulset {
	generateName := "azuredisk-volume-tester-"
	if labels == nil {
		labels = make(map[string]string)
	}
	if _, exists := labels["app"]; !exists {
		labels["app"] = "azuredisk-volume-tester-" + testutils.GenerateRandomString(5)
	}
	replicas := int32(replicaCount)
	var volumeClaimTest []v1.PersistentVolumeClaim
	volumeClaimTest = append(volumeClaimTest, pvc...)
	testStatefulset := &TestStatefulset{
		Client:    c,
		Namespace: ns,
		Statefulset: &apps.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: generateName,
				Labels:       labels,
			},
			Spec: apps.StatefulSetSpec{
				PodManagementPolicy: apps.ParallelPodManagement,
				Replicas:            &replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: labels,
				},
				Template: v1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: labels,
					},
					Spec: v1.PodSpec{
						SchedulerName: schedulerName,
						NodeSelector:  map[string]string{"kubernetes.io/os": "linux"},
						Containers: []v1.Container{
							{
								Name:         "volume-tester",
								Image:        imageutils.GetE2EImage(imageutils.BusyBox),
								Command:      []string{"/bin/sh"},
								Args:         []string{"-c", command},
								VolumeMounts: volumeMount,
							},
						},
						RestartPolicy: v1.RestartPolicyAlways,
					},
				},
				VolumeClaimTemplates: volumeClaimTest,
			},
		},
	}

	if isWindows {
		testStatefulset.Statefulset.Spec.Template.Spec.NodeSelector = map[string]string{
			"kubernetes.io/os": "windows",
		}
		testStatefulset.Statefulset.Spec.Template.Spec.Containers[0].Image = "mcr.microsoft.com/windows/servercore:" + getWinImageTag(winServerVer)
		if useCMD {
			testStatefulset.Statefulset.Spec.Template.Spec.Containers[0].Command = []string{"cmd"}
			testStatefulset.Statefulset.Spec.Template.Spec.Containers[0].Args = []string{"/c", command}
		} else {
			testStatefulset.Statefulset.Spec.Template.Spec.Containers[0].Command = []string{"powershell.exe"}
			testStatefulset.Statefulset.Spec.Template.Spec.Containers[0].Args = []string{"-Command", command}
		}
	}

	return testStatefulset
}

func (t *TestStatefulset) Create() {
	var err error
	t.Statefulset, err = t.Client.AppsV1().StatefulSets(t.Namespace.Name).Create(context.TODO(), t.Statefulset, metav1.CreateOptions{})
	framework.ExpectNoError(err)
	err = t.WaitForPodReadyOrFail()
	framework.ExpectNoError(err)
	selector, err := metav1.LabelSelectorAsSelector(t.Statefulset.Spec.Selector)
	framework.ExpectNoError(err)
	options := metav1.ListOptions{LabelSelector: selector.String()}
	statefulSetPods, err := t.Client.CoreV1().Pods(t.Namespace.Name).List(context.TODO(), options)
	framework.ExpectNoError(err)
	for _, pod := range statefulSetPods.Items {
		t.PodNames = append(t.PodNames, pod.Name)
		t.updateAllPods(pod)
	}
}

func (t *TestStatefulset) CreateWithoutWaiting() {
	var err error
	t.Statefulset, err = t.Client.AppsV1().StatefulSets(t.Namespace.Name).Create(context.TODO(), t.Statefulset, metav1.CreateOptions{})
	framework.ExpectNoError(err)
	selector, err := metav1.LabelSelectorAsSelector(t.Statefulset.Spec.Selector)
	framework.ExpectNoError(err)
	options := metav1.ListOptions{LabelSelector: selector.String()}
	statefulSetPods, err := t.Client.CoreV1().Pods(t.Namespace.Name).List(context.TODO(), options)
	framework.ExpectNoError(err)
	for _, pod := range statefulSetPods.Items {
		t.PodNames = append(t.PodNames, pod.Name)
		t.updateAllPods(pod)
	}
}

func (t *TestStatefulset) updateAllPods(pod v1.Pod) {
	var podPersistentVolumes []VolumeDetails
	for _, volume := range pod.Spec.Volumes {
		if volume.VolumeSource.PersistentVolumeClaim != nil {
			pvc, err := t.Client.CoreV1().PersistentVolumeClaims(t.Namespace.Name).Get(context.TODO(), volume.VolumeSource.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
			framework.ExpectNoError(err)
			newVolume := VolumeDetails{
				PersistentVolume: &v1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: pvc.Spec.VolumeName,
					},
				},
				VolumeAccessMode: v1.ReadWriteOnce,
			}
			podPersistentVolumes = append(podPersistentVolumes, newVolume)
		}
	}
	t.AllPods = append(t.AllPods, PodDetails{Volumes: podPersistentVolumes, Name: pod.Name})
}

func (t *TestStatefulset) WaitForPodReadyOrFail() error {
	err := wait.PollImmediate(testconsts.Poll, testconsts.PollTimeout, func() (bool, error) {
		var err error
		statefulSet, err := t.Client.AppsV1().StatefulSets(t.Namespace.Name).Get(context.TODO(), t.Statefulset.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		klog.Infof("%d/%d replicas in the StatefulSet are ready", statefulSet.Status.ReadyReplicas, *statefulSet.Spec.Replicas)
		if statefulSet.Status.ReadyReplicas == *statefulSet.Spec.Replicas {
			return true, nil
		}
		return false, nil
	})
	return err
}

func (t *TestStatefulset) PollForStringInPodsExec(command []string, expectedString string) {
	pollForStringInPodsExec(t.Namespace.Name, t.PodNames, command, expectedString)
}

func (t *TestStatefulset) DeletePodAndWait() {
	ch := make(chan error, len(t.PodNames))
	for _, podName := range t.PodNames {
		e2elog.Logf("Deleting pod %q in namespace %q", podName, t.Namespace.Name)
		go func(client clientset.Interface, ns, podName string) {
			err := client.CoreV1().Pods(ns).Delete(context.TODO(), podName, metav1.DeleteOptions{})
			ch <- err
		}(t.Client, t.Namespace.Name, podName)
	}

	// Wait on all goroutines to report on pod delete
	for range t.PodNames {
		err := <-ch
		if err != nil {
			if !errors.IsNotFound(err) {
				framework.ExpectNoError(err)
			}
		}
	}
	//sleep ensure waitForPodready will not pass before old pod is deleted.
	time.Sleep(60 * time.Second)
}

func (t *TestStatefulset) Cleanup(timeout time.Duration) {
	e2elog.Logf("deleting StatefulSet %q/%q", t.Namespace.Name, t.Statefulset.Name)

	err := t.Client.AppsV1().StatefulSets(t.Namespace.Name).Delete(context.TODO(), t.Statefulset.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)

	labelSelector := metav1.LabelSelector{MatchLabels: t.Statefulset.Labels}
	listOptions := metav1.ListOptions{LabelSelector: labels.Set(labelSelector.MatchLabels).String()}
	pvcs, err := t.Client.CoreV1().PersistentVolumeClaims(t.Namespace.Name).List(context.TODO(), listOptions)
	framework.ExpectNoError(err)

	ch := make(chan error, len(pvcs.Items))
	for _, pvc := range pvcs.Items {
		pv, err := t.Client.CoreV1().PersistentVolumes().Get(context.TODO(), pvc.Spec.VolumeName, metav1.GetOptions{})
		framework.ExpectNoError(err)
		err = e2epv.DeletePersistentVolumeClaim(t.Client, pvc.Name, pvc.Namespace)
		framework.ExpectNoError(err)
		go func(client clientset.Interface, pvName, pvcName, ns string, timeout time.Duration) {
			// Wait for PV to be deleted if Reclaim Policy is Delete
			if pv.Spec.PersistentVolumeReclaimPolicy == v1.PersistentVolumeReclaimDelete {
				ginkgo.By(fmt.Sprintf("waiting for claim's PV %q to be deleted", pvName))
				err = e2epv.WaitForPersistentVolumeDeleted(client, pvName, 10*time.Second, timeout)
				if err != nil {
					ch <- err
					return
				}
			}
			// Wait for the PVC to be deleted
			err = waitForPersistentVolumeClaimDeleted(client, ns, pvcName, 10*time.Second, timeout)
			ch <- err
		}(t.Client, pvc.Spec.VolumeName, pvc.Name, t.Namespace.Name, timeout)
	}

	// Wait on all goroutines to report pv/pvc deletion
	for range pvcs.Items {
		err := <-ch
		if err != nil {
			if !errors.IsNotFound(err) {
				framework.ExpectNoError(err)
			}
		}
	}
}

func (t *TestStatefulset) Logs() (logs [][]byte, err error) {
	for _, name := range t.PodNames {
		log, err := podutil.PodLogs(t.Client, name, t.Namespace.Name)
		if err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	return
}
