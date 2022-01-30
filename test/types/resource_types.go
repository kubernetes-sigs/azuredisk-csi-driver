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

package testtypes

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v4/apis/volumesnapshot/v1"
	snapshotclientset "github.com/kubernetes-csi/external-snapshotter/client/v4/clientset/versioned"
	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	restclientset "k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/events"
	"k8s.io/kubernetes/test/e2e/framework"
	e2eevents "k8s.io/kubernetes/test/e2e/framework/events"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
	e2epv "k8s.io/kubernetes/test/e2e/framework/pv"
	testutils "k8s.io/kubernetes/test/utils"
	imageutils "k8s.io/kubernetes/test/utils/image"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	v1alpha1ClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/typed/azuredisk/v1alpha1"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
	podutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/pod"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

type TestStorageClass struct {
	Client       clientset.Interface
	StorageClass *storagev1.StorageClass
	Namespace    *v1.Namespace
}

func NewTestStorageClass(c clientset.Interface, ns *v1.Namespace, sc *storagev1.StorageClass) *TestStorageClass {
	return &TestStorageClass{
		Client:       c,
		StorageClass: sc,
		Namespace:    ns,
	}
}

func (t *TestStorageClass) Create() storagev1.StorageClass {
	var err error

	ginkgo.By("creating a StorageClass " + t.StorageClass.Name)
	t.StorageClass, err = t.Client.StorageV1().StorageClasses().Create(context.TODO(), t.StorageClass, metav1.CreateOptions{})
	framework.ExpectNoError(err)
	return *t.StorageClass
}

func (t *TestStorageClass) Cleanup() {
	e2elog.Logf("deleting StorageClass %s", t.StorageClass.Name)
	err := t.Client.StorageV1().StorageClasses().Delete(context.TODO(), t.StorageClass.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
}

type TestVolumeSnapshotClass struct {
	Client              restclientset.Interface
	VolumeSnapshotClass *snapshotv1.VolumeSnapshotClass
	Namespace           *v1.Namespace
}

func NewTestVolumeSnapshotClass(c restclientset.Interface, ns *v1.Namespace, vsc *snapshotv1.VolumeSnapshotClass) *TestVolumeSnapshotClass {
	return &TestVolumeSnapshotClass{
		Client:              c,
		VolumeSnapshotClass: vsc,
		Namespace:           ns,
	}
}

func (t *TestVolumeSnapshotClass) Create() {
	ginkgo.By("creating a VolumeSnapshotClass")
	var err error
	t.VolumeSnapshotClass, err = snapshotclientset.New(t.Client).SnapshotV1().VolumeSnapshotClasses().Create(context.TODO(), t.VolumeSnapshotClass, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestVolumeSnapshotClass) CreateSnapshot(pvc *v1.PersistentVolumeClaim) *snapshotv1.VolumeSnapshot {
	ginkgo.By("creating a VolumeSnapshot for " + pvc.Name)
	snapshot := &snapshotv1.VolumeSnapshot{
		TypeMeta: metav1.TypeMeta{
			Kind:       testconsts.VolumeSnapshotKind,
			APIVersion: testconsts.SnapshotAPIVersion,
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "volume-snapshot-",
			Namespace:    t.Namespace.Name,
		},
		Spec: snapshotv1.VolumeSnapshotSpec{
			VolumeSnapshotClassName: &t.VolumeSnapshotClass.Name,
			Source: snapshotv1.VolumeSnapshotSource{
				PersistentVolumeClaimName: &pvc.Name,
			},
		},
	}
	snapshot, err := snapshotclientset.New(t.Client).SnapshotV1().VolumeSnapshots(t.Namespace.Name).Create(context.TODO(), snapshot, metav1.CreateOptions{})
	framework.ExpectNoError(err)
	return snapshot
}

func (t *TestVolumeSnapshotClass) ReadyToUse(snapshot *snapshotv1.VolumeSnapshot) {
	ginkgo.By("waiting for VolumeSnapshot to be ready to use - " + snapshot.Name)
	err := wait.Poll(15*time.Second, 5*time.Minute, func() (bool, error) {
		vs, err := snapshotclientset.New(t.Client).SnapshotV1().VolumeSnapshots(t.Namespace.Name).Get(context.TODO(), snapshot.Name, metav1.GetOptions{})
		if err != nil {
			return false, fmt.Errorf("did not see ReadyToUse: %v", err)
		}
		return *vs.Status.ReadyToUse, nil
	})
	framework.ExpectNoError(err)
}

func (t *TestVolumeSnapshotClass) DeleteSnapshot(vs *snapshotv1.VolumeSnapshot) {
	ginkgo.By("deleting a VolumeSnapshot " + vs.Name)
	err := snapshotclientset.New(t.Client).SnapshotV1().VolumeSnapshots(t.Namespace.Name).Delete(context.TODO(), vs.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
}

func (t *TestVolumeSnapshotClass) Cleanup() {
	// skip deleting volume snapshot storage class otherwise snapshot e2e test will fail, details:
	// https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/260#issuecomment-583296932
	e2elog.Logf("skip deleting VolumeSnapshotClass %s", t.VolumeSnapshotClass.Name)
	//err := snapshotclientset.New(t.Client).SnapshotV1().VolumeSnapshotClasses().Delete(t.VolumeSnapshotClass.Name, nil)
	//framework.ExpectNoError(err)
}

type TestPreProvisionedPersistentVolume struct {
	client                    clientset.Interface
	persistentVolume          *v1.PersistentVolume
	requestedPersistentVolume *v1.PersistentVolume
}

func NewTestPreProvisionedPersistentVolume(c clientset.Interface, pv *v1.PersistentVolume) *TestPreProvisionedPersistentVolume {
	return &TestPreProvisionedPersistentVolume{
		client:                    c,
		requestedPersistentVolume: pv,
	}
}

func (pv *TestPreProvisionedPersistentVolume) Create() v1.PersistentVolume {
	var err error
	ginkgo.By("creating a PV")
	pv.persistentVolume, err = pv.client.CoreV1().PersistentVolumes().Create(context.TODO(), pv.requestedPersistentVolume, metav1.CreateOptions{})
	framework.ExpectNoError(err)
	return *pv.persistentVolume
}

type TestPersistentVolumeClaim struct {
	Client                         clientset.Interface
	ClaimSize                      string
	VolumeMode                     v1.PersistentVolumeMode
	AccessMode                     v1.PersistentVolumeAccessMode
	StorageClass                   *storagev1.StorageClass
	Namespace                      *v1.Namespace
	PersistentVolume               *v1.PersistentVolume
	PersistentVolumeClaim          *v1.PersistentVolumeClaim
	RequestedPersistentVolumeClaim *v1.PersistentVolumeClaim
	DataSource                     *v1.TypedLocalObjectReference
}

func NewTestPersistentVolumeClaim(c clientset.Interface, ns *v1.Namespace, claimSize string, volumeMode VolumeMode, accessMode v1.PersistentVolumeAccessMode, sc *storagev1.StorageClass) *TestPersistentVolumeClaim {
	mode := v1.PersistentVolumeFilesystem
	if volumeMode == Block {
		mode = v1.PersistentVolumeBlock
	}
	return &TestPersistentVolumeClaim{
		Client:       c,
		ClaimSize:    claimSize,
		VolumeMode:   mode,
		AccessMode:   accessMode,
		Namespace:    ns,
		StorageClass: sc,
	}
}

func NewTestPersistentVolumeClaimWithDataSource(c clientset.Interface, ns *v1.Namespace, claimSize string, volumeMode VolumeMode, accessMode v1.PersistentVolumeAccessMode, sc *storagev1.StorageClass, dataSource *v1.TypedLocalObjectReference) *TestPersistentVolumeClaim {
	mode := v1.PersistentVolumeFilesystem
	if volumeMode == Block {
		mode = v1.PersistentVolumeBlock
	}
	return &TestPersistentVolumeClaim{
		Client:       c,
		ClaimSize:    claimSize,
		VolumeMode:   mode,
		AccessMode:   accessMode,
		Namespace:    ns,
		StorageClass: sc,
		DataSource:   dataSource,
	}
}

func (t *TestPersistentVolumeClaim) Create() {
	var err error

	ginkgo.By("creating a PVC")
	storageClassName := ""
	if t.StorageClass != nil {
		storageClassName = t.StorageClass.Name
	}
	t.RequestedPersistentVolumeClaim = generatePVC(t.Namespace.Name, storageClassName, "", t.ClaimSize, t.VolumeMode, t.AccessMode, t.DataSource)
	t.PersistentVolumeClaim, err = t.Client.CoreV1().PersistentVolumeClaims(t.Namespace.Name).Create(context.TODO(), t.RequestedPersistentVolumeClaim, metav1.CreateOptions{})
	framework.ExpectNoError(err)
}

func (t *TestPersistentVolumeClaim) ValidateProvisionedPersistentVolume() {
	var err error

	// Get the bound PersistentVolume
	ginkgo.By("validating provisioned PV")
	t.PersistentVolume, err = t.Client.CoreV1().PersistentVolumes().Get(context.TODO(), t.PersistentVolumeClaim.Spec.VolumeName, metav1.GetOptions{})
	framework.ExpectNoError(err)

	// Check sizes
	expectedCapacity := t.RequestedPersistentVolumeClaim.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)]
	claimCapacity := t.PersistentVolumeClaim.Spec.Resources.Requests[v1.ResourceName(v1.ResourceStorage)]
	gomega.Expect(claimCapacity.Value()).To(gomega.Equal(expectedCapacity.Value()), "claimCapacity is not equal to requestedCapacity")

	pvCapacity := t.PersistentVolume.Spec.Capacity[v1.ResourceName(v1.ResourceStorage)]
	gomega.Expect(pvCapacity.Value()).To(gomega.Equal(expectedCapacity.Value()), "pvCapacity is not equal to requestedCapacity")

	// Check PV properties
	ginkgo.By("checking the PV")
	expectedAccessModes := t.RequestedPersistentVolumeClaim.Spec.AccessModes
	gomega.Expect(t.PersistentVolume.Spec.AccessModes).To(gomega.Equal(expectedAccessModes))
	gomega.Expect(t.PersistentVolume.Spec.ClaimRef.Name).To(gomega.Equal(t.PersistentVolumeClaim.ObjectMeta.Name))
	gomega.Expect(t.PersistentVolume.Spec.ClaimRef.Namespace).To(gomega.Equal(t.PersistentVolumeClaim.ObjectMeta.Namespace))
	// If storageClass is nil, PV was pre-provisioned with these values already set
	if t.StorageClass != nil {
		gomega.Expect(t.PersistentVolume.Spec.PersistentVolumeReclaimPolicy).To(gomega.Equal(*t.StorageClass.ReclaimPolicy))
		gomega.Expect(t.PersistentVolume.Spec.MountOptions).To(gomega.Equal(t.StorageClass.MountOptions))
		if *t.StorageClass.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer {
			gomega.Expect(t.PersistentVolume.Spec.NodeAffinity.Required.NodeSelectorTerms[0].MatchExpressions[0].Values).
				To(gomega.HaveLen(1))
		}
	}
}

func (t *TestPersistentVolumeClaim) WaitForBound() v1.PersistentVolumeClaim {
	var err error

	ginkgo.By(fmt.Sprintf("waiting for PVC to be in phase %q", v1.ClaimBound))
	err = e2epv.WaitForPersistentVolumeClaimPhase(v1.ClaimBound, t.Client, t.Namespace.Name, t.PersistentVolumeClaim.Name, framework.Poll, framework.ClaimProvisionTimeout)
	framework.ExpectNoError(err)

	ginkgo.By("checking the PVC")
	// Get new copy of the claim
	t.PersistentVolumeClaim, err = t.Client.CoreV1().PersistentVolumeClaims(t.Namespace.Name).Get(context.TODO(), t.PersistentVolumeClaim.Name, metav1.GetOptions{})
	framework.ExpectNoError(err)

	return *t.PersistentVolumeClaim
}

func generatePVC(namespace, storageClassName, name, claimSize string, volumeMode v1.PersistentVolumeMode, accessMode v1.PersistentVolumeAccessMode, dataSource *v1.TypedLocalObjectReference) *v1.PersistentVolumeClaim {
	if accessMode != v1.ReadWriteOnce && accessMode != v1.ReadOnlyMany && accessMode != v1.ReadWriteMany {
		accessMode = v1.ReadWriteOnce
	}

	pvcMeta := metav1.ObjectMeta{
		Namespace: namespace,
	}
	if name == "" {
		pvcMeta.GenerateName = "pvc-"
	} else {
		pvcMeta.Name = name
	}

	return &v1.PersistentVolumeClaim{
		ObjectMeta: pvcMeta,
		Spec: v1.PersistentVolumeClaimSpec{
			StorageClassName: &storageClassName,
			AccessModes: []v1.PersistentVolumeAccessMode{
				accessMode,
			},
			Resources: v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceName(v1.ResourceStorage): resource.MustParse(claimSize),
				},
			},
			VolumeMode: &volumeMode,
			DataSource: dataSource,
		},
	}
}

func (t *TestPersistentVolumeClaim) Cleanup() {
	// Since PV is created after pod creation when the volume binding mode is WaitForFirstConsumer,
	// we need to populate fields such as PVC and PV info in TestPersistentVolumeClaim, and valid it
	if t.StorageClass != nil && *t.StorageClass.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer {
		var err error
		t.PersistentVolumeClaim, err = t.Client.CoreV1().PersistentVolumeClaims(t.Namespace.Name).Get(context.TODO(), t.PersistentVolumeClaim.Name, metav1.GetOptions{})
		framework.ExpectNoError(err)
		t.ValidateProvisionedPersistentVolume()
	}
	e2elog.Logf("deleting PVC %q/%q", t.Namespace.Name, t.PersistentVolumeClaim.Name)
	err := e2epv.DeletePersistentVolumeClaim(t.Client, t.PersistentVolumeClaim.Name, t.Namespace.Name)
	framework.ExpectNoError(err)
	// Wait for the PV to get deleted if reclaim policy is Delete. (If it's
	// Retain, there's no use waiting because the PV won't be auto-deleted and
	// it's expected for the caller to do it.) Technically, the first few delete
	// attempts may fail, as the volume is still attached to a node because
	// kubelet is slowly cleaning up the previous pod, however it should succeed
	// in a couple of minutes.
	if t.PersistentVolume.Spec.PersistentVolumeReclaimPolicy == v1.PersistentVolumeReclaimDelete {
		ginkgo.By(fmt.Sprintf("waiting for claim's PV %q to be deleted", t.PersistentVolume.Name))
		err := e2epv.WaitForPersistentVolumeDeleted(t.Client, t.PersistentVolume.Name, 5*time.Second, 10*time.Minute)
		framework.ExpectNoError(err)
	}
	// Wait for the PVC to be deleted
	err = waitForPersistentVolumeClaimDeleted(t.Client, t.Namespace.Name, t.PersistentVolumeClaim.Name, 5*time.Second, 5*time.Minute)
	framework.ExpectNoError(err)
}

func (t *TestPersistentVolumeClaim) ReclaimPolicy() v1.PersistentVolumeReclaimPolicy {
	return t.PersistentVolume.Spec.PersistentVolumeReclaimPolicy
}

func (t *TestPersistentVolumeClaim) WaitForPersistentVolumePhase(phase v1.PersistentVolumePhase) {
	err := e2epv.WaitForPersistentVolumePhase(phase, t.Client, t.PersistentVolume.Name, 5*time.Second, 10*time.Minute)
	framework.ExpectNoError(err)
}

func (t *TestPersistentVolumeClaim) DeleteBoundPersistentVolume() {
	ginkgo.By(fmt.Sprintf("deleting PV %q", t.PersistentVolume.Name))
	err := e2epv.DeletePersistentVolume(t.Client, t.PersistentVolume.Name)
	framework.ExpectNoError(err)
	ginkgo.By(fmt.Sprintf("waiting for claim's PV %q to be deleted", t.PersistentVolume.Name))
	err = e2epv.WaitForPersistentVolumeDeleted(t.Client, t.PersistentVolume.Name, 5*time.Second, 10*time.Minute)
	framework.ExpectNoError(err)
}

func (t *TestPersistentVolumeClaim) DeleteBackingVolume(azureCloud *provider.Cloud) {
	diskURI := t.PersistentVolume.Spec.CSI.VolumeHandle
	ginkgo.By(fmt.Sprintf("deleting azuredisk volume %q", diskURI))
	err := azureCloud.DeleteManagedDisk(context.Background(), diskURI)
	if err != nil {
		ginkgo.Fail(fmt.Sprintf("could not delete volume %q: %v", diskURI, err))
	}
}

// waitForPersistentVolumeClaimDeleted waits for a PersistentVolumeClaim to be removed from the system until timeout occurs, whichever comes first.
func waitForPersistentVolumeClaimDeleted(c clientset.Interface, ns string, pvcName string, Poll, timeout time.Duration) error {
	framework.Logf("Waiting up to %v for PersistentVolumeClaim %s to be removed", timeout, pvcName)
	err := wait.PollImmediate(testconsts.Poll, testconsts.PollTimeout, func() (bool, error) {
		var err error
		_, err = c.CoreV1().PersistentVolumeClaims(ns).Get(context.TODO(), pvcName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				framework.Logf("Claim %q in namespace %q doesn't exist in the system", pvcName, ns)
				return true, nil
			}
			framework.Logf("Failed to get claim %q in namespace %q, retrying in %v. Error: %v", pvcName, ns, Poll, err)
			return false, err
		}
		return false, nil
	})

	return err
}

type TestDeployment struct {
	Client     clientset.Interface
	Deployment *apps.Deployment
	Namespace  *v1.Namespace
	PodNames   []string
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
		t.PodNames = append(t.PodNames, pod.Name)
	}
}

func (t *TestDeployment) WaitForPodReady() {
	pods, err := podutil.GetPodsForDeployment(t.Client, t.Deployment)
	framework.ExpectNoError(err)
	t.PodNames = []string{}
	for _, pod := range pods.Items {
		t.PodNames = append(t.PodNames, pod.Name)
	}
	ch := make(chan error, len(t.PodNames))
	defer close(ch)
	for _, pod := range pods.Items {
		go func(client clientset.Interface, pod v1.Pod) {
			err = e2epod.WaitForPodRunningInNamespace(t.Client, &pod)
			ch <- err
		}(t.Client, pod)
	}
	// Wait on all goroutines to report on pod ready
	var wg sync.WaitGroup
	for range t.PodNames {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := <-ch
			framework.ExpectNoError(err)
		}()
	}
	wg.Wait()
}

func (t *TestDeployment) Exec(command []string, expectedString string) {
	for _, podName := range t.PodNames {
		_, err := framework.LookForStringInPodExec(t.Namespace.Name, podName, command, expectedString, testconsts.ExecTimeout)
		framework.ExpectNoError(err)
	}
}

func (t *TestDeployment) DeletePodAndWait() {
	ch := make(chan error, len(t.PodNames))
	for _, podName := range t.PodNames {
		e2elog.Logf("Deleting pod %q in namespace %q", podName, t.Namespace.Name)
		go func(client clientset.Interface, ns, podName string) {
			err := client.CoreV1().Pods(ns).Delete(context.TODO(), podName, metav1.DeleteOptions{})
			ch <- err
		}(t.Client, t.Namespace.Name, podName)
	}
	// Wait on all goroutines to report on pod delete
	for _, podName := range t.PodNames {
		err := <-ch
		if err != nil {
			if !errors.IsNotFound(err) {
				framework.ExpectNoError(fmt.Errorf("pod %q Delete API error: %v", podName, err))
			}
		}
	}

	for _, podName := range t.PodNames {
		e2elog.Logf("Waiting for pod %q in namespace %q to be fully deleted", podName, t.Namespace.Name)
		go func(client clientset.Interface, ns, podName string) {
			err := e2epod.WaitForPodNoLongerRunningInNamespace(client, podName, ns)
			ch <- err
		}(t.Client, t.Namespace.Name, podName)
	}
	// Wait on all goroutines to report on pod terminating
	for _, podName := range t.PodNames {
		err := <-ch
		if err != nil {
			if !errors.IsNotFound(err) {
				framework.ExpectNoError(fmt.Errorf("pod %q error waiting for delete: %v", podName, err))
			}
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
			e2elog.Logf("Pod %s has the following logs: %s", t.PodNames[i], logs)
		}
	}
	err = t.Client.AppsV1().Deployments(t.Namespace.Name).Delete(context.TODO(), t.Deployment.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
}

func (t *TestDeployment) Logs() (logs [][]byte, err error) {
	for _, name := range t.PodNames {
		log, err := podutil.PodLogs(t.Client, name, t.Namespace.Name)
		if err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	return
}

type TestStatefulset struct {
	Client      clientset.Interface
	Statefulset *apps.StatefulSet
	Namespace   *v1.Namespace
	PodNames    []string
	AllPods     []PodDetails
}

func NewTestStatefulset(c clientset.Interface, ns *v1.Namespace, command string, pvc []v1.PersistentVolumeClaim, volumeMount []v1.VolumeMount, isWindows, useCMD bool, schedulerName string, replicaCount int) *TestStatefulset {
	generateName := "azuredisk-volume-tester-"
	label := "azuredisk-volume-tester"
	replicas := int32(replicaCount)
	var volumeClaimTest []v1.PersistentVolumeClaim
	volumeClaimTest = append(volumeClaimTest, pvc...)
	testStatefulset := &TestStatefulset{
		Client:    c,
		Namespace: ns,
		Statefulset: &apps.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: generateName,
			},
			Spec: apps.StatefulSetSpec{
				PodManagementPolicy: apps.ParallelPodManagement,
				Replicas:            &replicas,
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": label},
				},
				Template: v1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"app": label},
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
		testStatefulset.Statefulset.Spec.Template.Spec.Containers[0].Image = "mcr.microsoft.com/windows/servercore:ltsc2019"
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
	err = waitForStatefulSetComplete(t.Client, t.Namespace, t.Statefulset)
	framework.ExpectNoError(err)
	selector, err := metav1.LabelSelectorAsSelector(t.Statefulset.Spec.Selector)
	framework.ExpectNoError(err)
	options := metav1.ListOptions{LabelSelector: selector.String()}
	statefulSetPods, err := t.Client.CoreV1().Pods(t.Namespace.Name).List(context.TODO(), options)
	framework.ExpectNoError(err)
	for _, pod := range statefulSetPods.Items {
		t.PodNames = append(t.PodNames, pod.Name)
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
		t.AllPods = append(t.AllPods, PodDetails{Volumes: podPersistentVolumes})
	}
}

func (t *TestStatefulset) WaitForPodReady() {

	err := waitForStatefulSetComplete(t.Client, t.Namespace, t.Statefulset)
	framework.ExpectNoError(err)
}

func (t *TestStatefulset) Exec(command []string, expectedString string) {
	for _, podName := range t.PodNames {
		_, err := framework.LookForStringInPodExec(t.Namespace.Name, podName, command, expectedString, testconsts.ExecTimeout)
		framework.ExpectNoError(err)
	}
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

func (t *TestStatefulset) Cleanup() {
	e2elog.Logf("deleting StatefulSet %q/%q", t.Namespace.Name, t.Statefulset.Name)
	err := t.Client.AppsV1().StatefulSets(t.Namespace.Name).Delete(context.TODO(), t.Statefulset.Name, metav1.DeleteOptions{})
	framework.ExpectNoError(err)
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

func waitForStatefulSetComplete(cs clientset.Interface, ns *v1.Namespace, ss *apps.StatefulSet) error {
	err := wait.PollImmediate(testconsts.Poll, testconsts.PollTimeout, func() (bool, error) {
		var err error
		statefulSet, err := cs.AppsV1().StatefulSets(ns.Name).Get(context.TODO(), ss.Name, metav1.GetOptions{})
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

type TestPod struct {
	Client    clientset.Interface
	Pod       *v1.Pod
	Namespace *v1.Namespace
}

func NewTestPod(c clientset.Interface, ns *v1.Namespace, command, schedulerName string, isWindows bool) *TestPod {
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
		testPod.Pod.Spec.Containers[0].Image = "mcr.microsoft.com/windows/servercore:ltsc2019"
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

// Ideally this would be in "k8s.io/kubernetes/test/e2e/framework"
// Similar to framework.WaitForPodSuccessInNamespaceSlow
var podFailedCondition = func(pod *v1.Pod) (bool, error) {
	switch pod.Status.Phase {
	case v1.PodFailed:
		ginkgo.By("Saw pod failure")
		return true, nil
	case v1.PodSucceeded:
		return true, fmt.Errorf("pod %q successed with reason: %q, message: %q", pod.Name, pod.Status.Reason, pod.Status.Message)
	default:
		return false, nil
	}
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

func (t *TestPod) SetNodeUnschedulable(nodeName string, unschedulable bool) {
	var err error
	updateFunc := func(nodeObj *v1.Node) {
		nodeObj.Spec.Unschedulable = unschedulable
	}
	_, err = nodeutil.UpdateNodeWithRetry(t.Client, nodeName, updateFunc)
	framework.ExpectNoError(err)
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
			if ex.Key == "topology.disk.csi.azure.com/zone" && ex.Operator == v1.NodeSelectorOpIn {
				zone = ex.Values[0]
			}
		}
	}

	return zone
}

func (t *TestPod) Logs() ([]byte, error) {
	return podutil.PodLogs(t.Client, t.Pod.Name, t.Namespace.Name)
}

// should only be used for integration tests
type TestAzVolumeAttachment struct {
	Azclient             v1alpha1ClientSet.DiskV1alpha1Interface
	Namespace            string
	UnderlyingVolume     string
	PrimaryNodeName      string
	MaxMountReplicaCount int
}

// should only be used for integration tests
func NewTestAzDriverNode(azDriverNode v1alpha1ClientSet.AzDriverNodeInterface, nodeName string) *v1alpha1.AzDriverNode {
	// Delete the leftover azDriverNode from previous runs
	if _, err := azDriverNode.Get(context.Background(), nodeName, metav1.GetOptions{}); err == nil {
		err := azDriverNode.Delete(context.Background(), nodeName, metav1.DeleteOptions{})
		framework.ExpectNoError(err)
	}

	newAzDriverNode, err := azDriverNode.Create(context.Background(), &v1alpha1.AzDriverNode{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
		Spec: v1alpha1.AzDriverNodeSpec{
			NodeName: nodeName,
		},
	}, metav1.CreateOptions{})
	framework.ExpectNoError(err)

	return newAzDriverNode
}

// should only be used for integration tests
func DeleteTestAzDriverNode(azDriverNode v1alpha1ClientSet.AzDriverNodeInterface, nodeName string) {
	_ = azDriverNode.Delete(context.Background(), nodeName, metav1.DeleteOptions{})
}

// should only be used for integration tests
func NewTestAzVolumeAttachment(azVolumeAttachment v1alpha1ClientSet.AzVolumeAttachmentInterface, volumeAttachmentName, nodeName, volumeName, ns string) *v1alpha1.AzVolumeAttachment {
	// Delete leftover azVolumeAttachments from previous runs
	if _, err := azVolumeAttachment.Get(context.Background(), volumeAttachmentName, metav1.GetOptions{}); err == nil {
		err := azVolumeAttachment.Delete(context.Background(), volumeAttachmentName, metav1.DeleteOptions{})
		framework.ExpectNoError(err)
	}

	newAzVolumeAttachment, err := azVolumeAttachment.Create(context.Background(), &v1alpha1.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name: volumeAttachmentName,
		},
		Spec: v1alpha1.AzVolumeAttachmentSpec{
			UnderlyingVolume: volumeName,
			NodeName:         nodeName,
			RequestedRole:    v1alpha1.PrimaryRole,
			VolumeContext: map[string]string{
				"controller": "azdrivernode",
				"name":       volumeAttachmentName,
				"namespace":  ns,
				"partition":  "default",
			},
		},
		Status: v1alpha1.AzVolumeAttachmentStatus{
			Detail: &v1alpha1.AzVolumeAttachmentStatusDetail{
				Role:           v1alpha1.PrimaryRole,
				PublishContext: map[string]string{},
			},
			State: v1alpha1.Attached,
		},
	}, metav1.CreateOptions{})
	framework.ExpectNoError(err)

	return newAzVolumeAttachment
}

// should only be used for integration tests
func DeleteTestAzVolumeAttachment(azVolumeAttachment v1alpha1ClientSet.AzVolumeAttachmentInterface, volumeAttachmentName string) {
	_ = azVolumeAttachment.Delete(context.Background(), volumeAttachmentName, metav1.DeleteOptions{})
}

// should only be used for integration tests
func NewTestAzVolume(azVolume v1alpha1ClientSet.AzVolumeInterface, underlyingVolumeName string, maxMountReplicaCount int) *v1alpha1.AzVolume {
	// Delete leftover azVolumes from previous runs
	if _, err := azVolume.Get(context.Background(), underlyingVolumeName, metav1.GetOptions{}); err == nil {
		err := azVolume.Delete(context.Background(), underlyingVolumeName, metav1.DeleteOptions{})
		framework.ExpectNoError(err)
	}
	newAzVolume, err := azVolume.Create(context.Background(), &v1alpha1.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: underlyingVolumeName,
		},
		Spec: v1alpha1.AzVolumeSpec{
			UnderlyingVolume:     underlyingVolumeName,
			MaxMountReplicaCount: maxMountReplicaCount,
			VolumeCapability: []v1alpha1.VolumeCapability{
				{
					AccessDetails: v1alpha1.VolumeCapabilityAccessDetails{
						AccessType: v1alpha1.VolumeCapabilityAccessMount,
					},
					AccessMode: v1alpha1.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			CapacityRange: &v1alpha1.CapacityRange{
				RequiredBytes: 0,
				LimitBytes:    0,
			},
			AccessibilityRequirements: &v1alpha1.TopologyRequirement{},
		},
		Status: v1alpha1.AzVolumeStatus{
			State: v1alpha1.VolumeOperationPending,
		},
	}, metav1.CreateOptions{})
	framework.ExpectNoError(err)

	return newAzVolume
}

// should only be used for integration tests
func SetupTestAzVolumeAttachment(azclient v1alpha1ClientSet.DiskV1alpha1Interface, namespace, underlyingVolume, primaryNodeName string, maxMountReplicaCount int) *TestAzVolumeAttachment {
	return &TestAzVolumeAttachment{
		Azclient:             azclient,
		Namespace:            namespace,
		UnderlyingVolume:     underlyingVolume,
		PrimaryNodeName:      primaryNodeName,
		MaxMountReplicaCount: maxMountReplicaCount,
	}
}

// should only be used for integration tests
func (t *TestAzVolumeAttachment) Create() *v1alpha1.AzVolumeAttachment {
	// create test az volume
	azVol := t.Azclient.AzVolumes(t.Namespace)
	_ = NewTestAzVolume(azVol, t.UnderlyingVolume, t.MaxMountReplicaCount)

	// create test az volume attachment
	azAtt := t.Azclient.AzVolumeAttachments(t.Namespace)
	attName := GetAzVolumeAttachmentName(t.UnderlyingVolume, t.PrimaryNodeName)
	att := NewTestAzVolumeAttachment(azAtt, attName, t.PrimaryNodeName, t.UnderlyingVolume, t.Namespace)

	return att
}

// should only be used for integration tests
func (t *TestAzVolumeAttachment) Cleanup() {
	klog.Info("cleaning up")
	err := t.Azclient.AzVolumes(t.Namespace).Delete(context.Background(), t.UnderlyingVolume, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		framework.ExpectNoError(err)
	}

	// Delete All AzVolumeAttachments for t.UnderlyingVolume
	err = t.Azclient.AzVolumeAttachments(t.Namespace).Delete(context.Background(), GetAzVolumeAttachmentName(t.UnderlyingVolume, t.PrimaryNodeName), metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		framework.ExpectNoError(err)
	}

	nodes, err := t.Azclient.AzDriverNodes(t.Namespace).List(context.Background(), metav1.ListOptions{})
	if !errors.IsNotFound(err) {
		framework.ExpectNoError(err)
	}
	for _, node := range nodes.Items {
		err = t.Azclient.AzVolumeAttachments(t.Namespace).Delete(context.Background(), GetAzVolumeAttachmentName(t.UnderlyingVolume, node.Name), metav1.DeleteOptions{})
		if !errors.IsNotFound(err) {
			framework.ExpectNoError(err)
		}
	}
}

func GetAzVolumeAttachmentName(underlyingVolume, nodeName string) string {
	return fmt.Sprintf("%s-%s-attachment", underlyingVolume, nodeName)
}

// should only be used for integration tests
// Wait for the azVolumeAttachment object update
func (t *TestAzVolumeAttachment) WaitForAttach(timeout time.Duration) error {
	conditionFunc := func() (bool, error) {
		att, err := t.Azclient.AzVolumeAttachments(t.Namespace).Get(context.TODO(), GetAzVolumeAttachmentName(t.UnderlyingVolume, t.PrimaryNodeName), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if att.Status.Detail != nil {
			klog.Infof("volume (%s) attached to node (%s)", att.Spec.UnderlyingVolume, att.Spec.NodeName)
			return true, nil
		}
		return false, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}

// should only be used for integration tests
// Wait for the azVolumeAttachment object update
func (t *TestAzVolumeAttachment) WaitForFinalizer(timeout time.Duration) error {
	conditionFunc := func() (bool, error) {
		att, err := t.Azclient.AzVolumeAttachments(t.Namespace).Get(context.TODO(), GetAzVolumeAttachmentName(t.UnderlyingVolume, t.PrimaryNodeName), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if att.ObjectMeta.Finalizers == nil {
			return false, nil
		}
		for _, finalizer := range att.ObjectMeta.Finalizers {
			if finalizer == consts.AzVolumeAttachmentFinalizer {
				klog.Infof("finalizer (%s) found on AzVolumeAttachment object (%s)", consts.AzVolumeAttachmentFinalizer, att.Name)
				return true, nil
			}
		}
		return false, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}

// should only be used for integration tests
// Wait for the azVolumeAttachment object update
func (t *TestAzVolumeAttachment) WaitForLabels(timeout time.Duration) error {
	conditionFunc := func() (bool, error) {
		att, err := t.Azclient.AzVolumeAttachments(t.Namespace).Get(context.TODO(), GetAzVolumeAttachmentName(t.UnderlyingVolume, t.PrimaryNodeName), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if att.Labels == nil {
			return false, nil
		}
		if _, ok := att.Labels[consts.NodeNameLabel]; !ok {
			return false, nil
		}
		if _, ok := att.Labels[consts.VolumeNameLabel]; !ok {
			return false, nil
		}
		return true, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}

// should only be used for integration tests
// Wait for the azVolumeAttachment object update
func (t *TestAzVolumeAttachment) WaitForDelete(nodeName string, timeout time.Duration) error {
	attName := GetAzVolumeAttachmentName(t.UnderlyingVolume, nodeName)
	conditionFunc := func() (bool, error) {
		_, err := t.Azclient.AzVolumeAttachments(t.Namespace).Get(context.TODO(), attName, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			klog.Infof("azVolumeAttachment %s not found.", attName)
			return true, nil
		} else if err != nil {
			return false, err
		}
		return false, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}

// should only be used for integration tests
// Wait for the azVolumeAttachment object update
func (t *TestAzVolumeAttachment) WaitForPrimary(timeout time.Duration) error {
	conditionFunc := func() (bool, error) {
		attachments, err := t.Azclient.AzVolumeAttachments(t.Namespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, attachment := range attachments.Items {
			if attachment.Status.Detail == nil {
				continue
			}
			if attachment.Spec.UnderlyingVolume == t.UnderlyingVolume && attachment.Spec.RequestedRole == v1alpha1.PrimaryRole && attachment.Status.Detail.Role == v1alpha1.PrimaryRole {
				return true, nil
			}
		}
		return false, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}

// should only be used for integration tests
// Wait for the azVolumeAttachment object update
func (t *TestAzVolumeAttachment) WaitForReplicas(numReplica int, timeout time.Duration) error {
	conditionFunc := func() (bool, error) {
		attachments, err := t.Azclient.AzVolumeAttachments(t.Namespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		counter := 0
		for _, attachment := range attachments.Items {
			if attachment.Status.Detail == nil {
				continue
			}
			if attachment.Spec.UnderlyingVolume == t.UnderlyingVolume && attachment.Status.Detail.Role == v1alpha1.ReplicaRole {
				counter++
			}
		}
		klog.Infof("%d replica found for volume %s", counter, t.UnderlyingVolume)
		return counter == numReplica, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}

// should only be used for integration tests
type TestAzVolume struct {
	Azclient             v1alpha1ClientSet.DiskV1alpha1Interface
	Namespace            string
	UnderlyingVolume     string
	MaxMountReplicaCount int
}

// should only be used for integration tests
func SetupTestAzVolume(azclient v1alpha1ClientSet.DiskV1alpha1Interface, namespace string, underlyingVolume string, maxMountReplicaCount int) *TestAzVolume {
	return &TestAzVolume{
		Azclient:             azclient,
		Namespace:            namespace,
		UnderlyingVolume:     underlyingVolume,
		MaxMountReplicaCount: maxMountReplicaCount,
	}
}

// should only be used for integration tests
func (t *TestAzVolume) Create() *v1alpha1.AzVolume {
	// create test az volume
	azVolClient := t.Azclient.AzVolumes(t.Namespace)
	azVolume := NewTestAzVolume(azVolClient, t.UnderlyingVolume, t.MaxMountReplicaCount)

	return azVolume
}

// should only be used for integration tests
//Cleanup after TestAzVolume was created
func (t *TestAzVolume) Cleanup() {
	klog.Info("cleaning up TestAzVolume")
	err := t.Azclient.AzVolumes(t.Namespace).Delete(context.Background(), t.UnderlyingVolume, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		framework.ExpectNoError(err)
	}
	time.Sleep(time.Duration(1) * time.Minute)

}

// should only be used for integration tests
// Wait for the azVolume object update
func (t *TestAzVolume) WaitForFinalizer(timeout time.Duration) error {
	conditionFunc := func() (bool, error) {
		azVolume, err := t.Azclient.AzVolumes(t.Namespace).Get(context.TODO(), t.UnderlyingVolume, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if azVolume.ObjectMeta.Finalizers == nil {
			return false, nil
		}
		for _, finalizer := range azVolume.ObjectMeta.Finalizers {
			if finalizer == consts.AzVolumeFinalizer {
				klog.Infof("finalizer (%s) found on AzVolume object (%s)", consts.AzVolumeFinalizer, azVolume.Name)
				return true, nil
			}
		}
		return false, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}

// should only be used for integration tests
// Wait for the azVolume object update
func (t *TestAzVolume) WaitForDelete(timeout time.Duration) error {
	klog.Infof("Waiting for delete azVolume object")
	conditionFunc := func() (bool, error) {
		_, err := t.Azclient.AzVolumes(t.Namespace).Get(context.TODO(), t.UnderlyingVolume, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			klog.Infof("azVolume %s deleted.", t.UnderlyingVolume)
			return true, nil
		} else if err != nil {
			return false, err
		}
		return false, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}
