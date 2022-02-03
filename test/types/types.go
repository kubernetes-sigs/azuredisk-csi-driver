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

	"github.com/onsi/ginkgo"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
	restclientset "k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"

	e2elog "k8s.io/kubernetes/test/e2e/framework/log"

	v1alpha1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
)

type TestCmd struct {
	Command  string
	Args     []string
	StartLog string
	EndLog   string
}

type PodDetails struct {
	Name            string
	Cmd             string
	Volumes         []VolumeDetails
	IsWindows       bool
	UseCMD          bool
	UseAntiAffinity bool
	ReplicaCount    int32
}

type VolumeDetails struct {
	VolumeType            string
	FSType                string
	Encrypted             bool
	MountOptions          []string
	ClaimSize             string
	ReclaimPolicy         *v1.PersistentVolumeReclaimPolicy
	VolumeBindingMode     *storagev1.VolumeBindingMode
	AllowedTopologyValues []string
	VolumeMode            VolumeMode
	VolumeMount           VolumeMountDetails
	VolumeDevice          VolumeDeviceDetails
	VolumeAccessMode      v1.PersistentVolumeAccessMode
	// Optional, used to get AzVolumeAttachments
	PersistentVolume *v1.PersistentVolume
	// Optional, used with pre-provisioned volumes
	VolumeID string
	// Optional, used with PVCs created from snapshots or pvc
	DataSource *DataSource
	// Optional, used with specified StorageClass
	StorageClass *storagev1.StorageClass
}

type VolumeMode int

const (
	FileSystem VolumeMode = iota
	Block
)

type VolumeMountDetails struct {
	NameGenerate      string
	MountPathGenerate string
	ReadOnly          bool
}

type VolumeDeviceDetails struct {
	NameGenerate string
	DevicePath   string
}

type DataSource struct {
	Kind string
	Name string
}

func (pod *PodDetails) SetupWithDynamicVolumes(client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, storageClassParameters map[string]string, schedulerName string) (*TestPod, []func()) {
	tpod := NewTestPod(client, namespace, pod.Cmd, schedulerName, pod.IsWindows)
	cleanupFuncs := make([]func(), 0)
	for n, v := range pod.Volumes {
		tpvc, funcs := v.SetupDynamicPersistentVolumeClaim(client, namespace, csiDriver, storageClassParameters)
		cleanupFuncs = append(cleanupFuncs, funcs...)
		ginkgo.By("setting up the pod")
		if v.VolumeMode == Block {
			tpod.SetupRawBlockVolume(tpvc.PersistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeDevice.NameGenerate, n+1), v.VolumeDevice.DevicePath)
		} else {
			tpod.SetupVolume(tpvc.PersistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeMount.NameGenerate, n+1), fmt.Sprintf("%s%d", v.VolumeMount.MountPathGenerate, n+1), v.VolumeMount.ReadOnly)
		}
		if tpvc.PersistentVolume != nil {
			klog.Infof("adding PV (%s) to pod (%s)", tpvc.PersistentVolume.Name, tpod.Pod.Name)
			pod.Volumes[n].PersistentVolume = tpvc.PersistentVolume.DeepCopy()
		}
	}
	return tpod, cleanupFuncs
}

// SetupWithDynamicMultipleVolumes each pod will be mounted with multiple volumes with different storage account types
func (pod *PodDetails) SetupWithDynamicMultipleVolumes(client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, schedulerName string) (*TestPod, []func()) {
	tpod := NewTestPod(client, namespace, pod.Cmd, schedulerName, pod.IsWindows)
	cleanupFuncs := make([]func(), 0)
	supportedStorageAccountTypes := testconsts.AzurePublicCloudSupportedStorageAccountTypes
	if testconsts.IsAzureStackCloud {
		supportedStorageAccountTypes = testconsts.AzureStackCloudSupportedStorageAccountTypes
	}
	accountTypeCount := len(supportedStorageAccountTypes)
	for n, v := range pod.Volumes {
		storageClassParameters := map[string]string{"skuName": supportedStorageAccountTypes[n%accountTypeCount]}
		tpvc, funcs := v.SetupDynamicPersistentVolumeClaim(client, namespace, csiDriver, storageClassParameters)
		cleanupFuncs = append(cleanupFuncs, funcs...)
		if v.VolumeMode == Block {
			tpod.SetupRawBlockVolume(tpvc.PersistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeDevice.NameGenerate, n+1), v.VolumeDevice.DevicePath)
		} else {
			tpod.SetupVolume(tpvc.PersistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeMount.NameGenerate, n+1), fmt.Sprintf("%s%d", v.VolumeMount.MountPathGenerate, n+1), v.VolumeMount.ReadOnly)
		}
	}
	return tpod, cleanupFuncs
}

func (pod *PodDetails) SetupWithDynamicVolumesWithSubpath(client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, storageClassParameters map[string]string, schedulerName string) (*TestPod, []func()) {
	tpod := NewTestPod(client, namespace, pod.Cmd, schedulerName, pod.IsWindows)
	cleanupFuncs := make([]func(), 0)
	for n, v := range pod.Volumes {
		tpvc, funcs := v.SetupDynamicPersistentVolumeClaim(client, namespace, csiDriver, storageClassParameters)
		cleanupFuncs = append(cleanupFuncs, funcs...)
		tpod.SetupVolumeMountWithSubpath(tpvc.PersistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeMount.NameGenerate, n+1), fmt.Sprintf("%s%d", v.VolumeMount.MountPathGenerate, n+1), "testSubpath", v.VolumeMount.ReadOnly)
	}
	return tpod, cleanupFuncs
}

func (pod *PodDetails) SetupWithInlineVolumes(client clientset.Interface, namespace *v1.Namespace, csiDriver driver.PreProvisionedVolumeTestDriver, diskURI string, readOnly bool, schedulerName string) (*TestPod, []func()) {
	tpod := NewTestPod(client, namespace, pod.Cmd, schedulerName, pod.IsWindows)
	cleanupFuncs := make([]func(), 0)
	for n, v := range pod.Volumes {
		tpod.SetupInlineVolume(fmt.Sprintf("%s%d", v.VolumeMount.NameGenerate, n+1), fmt.Sprintf("%s%d", v.VolumeMount.MountPathGenerate, n+1), diskURI, readOnly)
	}
	return tpod, cleanupFuncs
}

func (pod *PodDetails) SetupWithPreProvisionedVolumes(client clientset.Interface, namespace *v1.Namespace, csiDriver driver.PreProvisionedVolumeTestDriver, volumeContext map[string]string, schedulerName string) (*TestPod, []func()) {
	tpod := NewTestPod(client, namespace, pod.Cmd, schedulerName, pod.IsWindows)
	cleanupFuncs := make([]func(), 0)
	for n, v := range pod.Volumes {
		tpvc, funcs := v.SetupPreProvisionedPersistentVolumeClaim(client, namespace, csiDriver, volumeContext)
		cleanupFuncs = append(cleanupFuncs, funcs...)

		if v.VolumeMode == Block {
			tpod.SetupRawBlockVolume(tpvc.PersistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeDevice.NameGenerate, n+1), v.VolumeDevice.DevicePath)
		} else {
			tpod.SetupVolume(tpvc.PersistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeMount.NameGenerate, n+1), fmt.Sprintf("%s%d", v.VolumeMount.MountPathGenerate, n+1), v.VolumeMount.ReadOnly)
		}
	}
	return tpod, cleanupFuncs
}

func (pod *PodDetails) SetupDeployment(client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, schedulerName string, storageClassParameters map[string]string) (*TestDeployment, []func()) {
	cleanupFuncs := make([]func(), 0)
	var volumes []v1.Volume

	volumeMounts := make([]v1.VolumeMount, 0)
	volumeDevices := make([]v1.VolumeDevice, 0)

	for n, volume := range pod.Volumes {
		ginkgo.By("setting up the StorageClass")
		storageClass := csiDriver.GetDynamicProvisionStorageClass(storageClassParameters, volume.MountOptions, volume.ReclaimPolicy, volume.VolumeBindingMode, volume.AllowedTopologyValues, namespace.Name)
		tsc := NewTestStorageClass(client, namespace, storageClass)
		createdStorageClass := tsc.Create()
		cleanupFuncs = append(cleanupFuncs, tsc.Cleanup)
		ginkgo.By("setting up the PVC")
		tpvc := NewTestPersistentVolumeClaim(client, namespace, volume.ClaimSize, volume.VolumeMode, volume.VolumeAccessMode, &createdStorageClass)
		tpvc.Create()
		if volume.VolumeBindingMode == nil || *volume.VolumeBindingMode == storagev1.VolumeBindingImmediate {
			tpvc.WaitForBound()
			tpvc.ValidateProvisionedPersistentVolume()
		}
		cleanupFuncs = append(cleanupFuncs, tpvc.Cleanup)

		newVolumeName := fmt.Sprintf("%s%d", volume.VolumeMount.NameGenerate, n+1)
		newMountPath := fmt.Sprintf("%s%d", volume.VolumeMount.MountPathGenerate, n+1)
		pvc := tpvc.PersistentVolumeClaim

		if pvc.Spec.VolumeMode == nil || *pvc.Spec.VolumeMode == v1.PersistentVolumeFilesystem {
			newVolumeMount := v1.VolumeMount{
				Name:      newVolumeName,
				MountPath: newMountPath,
				ReadOnly:  volume.VolumeMount.ReadOnly,
			}
			volumeMounts = append(volumeMounts, newVolumeMount)
		} else {
			newVolumeDevices := v1.VolumeDevice{
				Name:       newVolumeName,
				DevicePath: newMountPath,
			}
			volumeDevices = append(volumeDevices, newVolumeDevices)
		}

		newVolume := v1.Volume{
			Name: newVolumeName,
			VolumeSource: v1.VolumeSource{
				PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvc.Name,
				},
			},
		}
		volumes = append(volumes, newVolume)

		pod.Volumes[n].PersistentVolume = tpvc.PersistentVolume
	}

	ginkgo.By("setting up the Deployment")
	if pod.ReplicaCount == 0 {
		pod.ReplicaCount = 1
	}
	tDeployment := NewTestDeployment(client, namespace, pod.Cmd, volumeMounts, volumeDevices, volumes, pod.ReplicaCount, pod.IsWindows, pod.UseCMD, pod.UseAntiAffinity, schedulerName)

	cleanupFuncs = append(cleanupFuncs, tDeployment.Cleanup)
	return tDeployment, cleanupFuncs
}

func (pod *PodDetails) SetupDeploymentWithPreProvisionedVolumes(client clientset.Interface, namespace *v1.Namespace, csiDriver driver.PreProvisionedVolumeTestDriver, volumeContext map[string]string, schedulerName string) (*TestDeployment, []func()) {
	cleanupFuncs := make([]func(), 0)
	var volumes []v1.Volume

	volumeMounts := make([]v1.VolumeMount, 0)
	volumeDevices := make([]v1.VolumeDevice, 0)

	for n, volume := range pod.Volumes {
		ginkgo.By("setting up the PVC")
		tpvc, funcs := volume.SetupPreProvisionedPersistentVolumeClaim(client, namespace, csiDriver, volumeContext)
		cleanupFuncs = append(cleanupFuncs, funcs...)

		newVolumeName := fmt.Sprintf("%s%d", volume.VolumeMount.NameGenerate, n+1)
		newMountPath := fmt.Sprintf("%s%d", volume.VolumeMount.MountPathGenerate, n+1)
		pvc := tpvc.PersistentVolumeClaim

		if pvc.Spec.VolumeMode == nil || *pvc.Spec.VolumeMode == v1.PersistentVolumeFilesystem {
			newVolumeMount := v1.VolumeMount{
				Name:      newVolumeName,
				MountPath: newMountPath,
				ReadOnly:  volume.VolumeMount.ReadOnly,
			}
			volumeMounts = append(volumeMounts, newVolumeMount)
		} else {
			newVolumeDevices := v1.VolumeDevice{
				Name:       newVolumeName,
				DevicePath: newMountPath,
			}
			volumeDevices = append(volumeDevices, newVolumeDevices)
		}

		newVolume := v1.Volume{
			Name: newVolumeName,
			VolumeSource: v1.VolumeSource{
				PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvc.Name,
				},
			},
		}
		volumes = append(volumes, newVolume)

		pod.Volumes[n].PersistentVolume = tpvc.PersistentVolume
	}

	ginkgo.By("setting up the Deployment")
	if pod.ReplicaCount == 0 {
		pod.ReplicaCount = 1
	}
	tDeployment := NewTestDeployment(client, namespace, pod.Cmd, volumeMounts, volumeDevices, volumes, pod.ReplicaCount, pod.IsWindows, pod.UseCMD, pod.UseAntiAffinity, schedulerName)

	cleanupFuncs = append(cleanupFuncs, tDeployment.Cleanup)
	return tDeployment, cleanupFuncs
}

func (pod *PodDetails) CreateStorageClass(client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, storageClassParameters map[string]string) (storagev1.StorageClass, func()) {
	ginkgo.By("setting up the StorageClass")
	var allowedTopologyValues []string
	var volumeBindingMode storagev1.VolumeBindingMode

	nodes, err := client.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	framework.ExpectNoError(err)
	allowedTopologyValuesMap := make(map[string]bool)
	for _, node := range nodes.Items {
		if zone, ok := node.Labels[testconsts.TopologyKey]; ok {
			allowedTopologyValuesMap[zone] = true
		}
	}
	for k := range allowedTopologyValuesMap {
		allowedTopologyValues = append(allowedTopologyValues, k)

		volumeBindingMode = storagev1.VolumeBindingWaitForFirstConsumer
	}

	reclaimPolicy := v1.PersistentVolumeReclaimDelete
	storageClass := csiDriver.GetDynamicProvisionStorageClass(storageClassParameters, []string{}, &reclaimPolicy, &volumeBindingMode, allowedTopologyValues, namespace.Name)
	tsc := NewTestStorageClass(client, namespace, storageClass)
	createdStorageClass := tsc.Create()
	return createdStorageClass, tsc.Cleanup
}

func (pod *PodDetails) SetupStatefulset(client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, schedulerName string, replicaCount int, storageClassParameters map[string]string, storageClass *storagev1.StorageClass) (*TestStatefulset, []func()) {
	cleanupFuncs := make([]func(), 0)
	var pvcs []v1.PersistentVolumeClaim
	var volumeMounts []v1.VolumeMount
	for n, volume := range pod.Volumes {
		ginkgo.By("setting up the PVC")
		tpvc := NewTestPersistentVolumeClaim(client, namespace, volume.ClaimSize, volume.VolumeMode, volume.VolumeAccessMode, storageClass)
		storageClassName := ""
		if tpvc.StorageClass != nil {
			storageClassName = tpvc.StorageClass.Name
		}
		tvolumeMount := v1.VolumeMount{
			Name:      fmt.Sprintf("%s%d", volume.VolumeMount.NameGenerate, n+1),
			MountPath: fmt.Sprintf("%s%d", volume.VolumeMount.MountPathGenerate, n+1),
			ReadOnly:  volume.VolumeMount.ReadOnly,
		}
		volumeMounts = append(volumeMounts, tvolumeMount)

		tpvc.RequestedPersistentVolumeClaim = generatePVC(tpvc.Namespace.Name, storageClassName, tvolumeMount.Name, tpvc.ClaimSize, tpvc.VolumeMode, tpvc.AccessMode, tpvc.DataSource)
		pvcs = append(pvcs, *tpvc.RequestedPersistentVolumeClaim)
	}
	ginkgo.By("setting up the statefulset")
	tStatefulset := NewTestStatefulset(client, namespace, pod.Cmd, pvcs, volumeMounts, pod.IsWindows, pod.UseCMD, schedulerName, replicaCount)

	cleanupFuncs = append(cleanupFuncs, tStatefulset.Cleanup)
	return tStatefulset, cleanupFuncs
}

func (volume *VolumeDetails) SetupDynamicPersistentVolumeClaim(client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, storageClassParameters map[string]string) (*TestPersistentVolumeClaim, []func()) {
	cleanupFuncs := make([]func(), 0)
	storageClass := volume.StorageClass
	if storageClass == nil {
		tsc, tscCleanup := volume.CreateStorageClass(client, namespace, csiDriver, storageClassParameters)
		cleanupFuncs = append(cleanupFuncs, tscCleanup)
		storageClass = tsc.StorageClass
	}
	ginkgo.By("setting up the PVC and PV")
	var tpvc *TestPersistentVolumeClaim
	if volume.DataSource != nil {
		dataSource := &v1.TypedLocalObjectReference{
			Name: volume.DataSource.Name,
			Kind: volume.DataSource.Kind,
		}
		if volume.DataSource.Kind == testconsts.VolumeSnapshotKind {
			apiGroup := testconsts.SnapshotAPIGroup
			dataSource.APIGroup = &apiGroup
		}
		tpvc = NewTestPersistentVolumeClaimWithDataSource(client, namespace, volume.ClaimSize, volume.VolumeMode, volume.VolumeAccessMode, storageClass, dataSource)
	} else {
		tpvc = NewTestPersistentVolumeClaim(client, namespace, volume.ClaimSize, volume.VolumeMode, volume.VolumeAccessMode, storageClass)
	}
	tpvc.Create()
	cleanupFuncs = append(cleanupFuncs, tpvc.Cleanup)
	// PV will not be ready until PVC is used in a pod when volumeBindingMode: WaitForFirstConsumer
	if volume.VolumeBindingMode == nil || *volume.VolumeBindingMode == storagev1.VolumeBindingImmediate {
		tpvc.WaitForBound()
		tpvc.ValidateProvisionedPersistentVolume()
	}

	return tpvc, cleanupFuncs
}

func (volume *VolumeDetails) SetupPreProvisionedPersistentVolumeClaim(client clientset.Interface, namespace *v1.Namespace, csiDriver driver.PreProvisionedVolumeTestDriver, volumeContext map[string]string) (*TestPersistentVolumeClaim, []func()) {
	cleanupFuncs := make([]func(), 0)
	ginkgo.By("setting up the PV")
	volumeMode := v1.PersistentVolumeFilesystem
	if volume.VolumeMode == Block {
		volumeMode = v1.PersistentVolumeBlock
	}
	pv := csiDriver.GetPersistentVolume(volume.VolumeID, volume.FSType, volume.ClaimSize, volumeMode, volume.VolumeAccessMode, volume.ReclaimPolicy, namespace.Name, volumeContext)
	tpv := NewTestPreProvisionedPersistentVolume(client, pv)
	tpv.Create()
	ginkgo.By("setting up the PVC")
	tpvc := NewTestPersistentVolumeClaim(client, namespace, volume.ClaimSize, volume.VolumeMode, volume.VolumeAccessMode, nil)
	tpvc.Create()
	cleanupFuncs = append(cleanupFuncs, tpvc.DeleteBoundPersistentVolume)
	cleanupFuncs = append(cleanupFuncs, tpvc.Cleanup)
	tpvc.WaitForBound()
	tpvc.ValidateProvisionedPersistentVolume()

	return tpvc, cleanupFuncs
}

func (volume *VolumeDetails) CreateStorageClass(client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, storageClassParameters map[string]string) (*TestStorageClass, func()) {
	ginkgo.By("setting up the StorageClass")
	storageClass := csiDriver.GetDynamicProvisionStorageClass(storageClassParameters, volume.MountOptions, volume.ReclaimPolicy, volume.VolumeBindingMode, volume.AllowedTopologyValues, namespace.Name)
	tsc := NewTestStorageClass(client, namespace, storageClass)
	tsc.Create()
	return tsc, tsc.Cleanup
}

func CreateVolumeSnapshotClass(client restclientset.Interface, namespace *v1.Namespace, csiDriver driver.VolumeSnapshotTestDriver) (*TestVolumeSnapshotClass, func()) {
	ginkgo.By("setting up the VolumeSnapshotClass")
	volumeSnapshotClass := csiDriver.GetVolumeSnapshotClass(namespace.Name)
	tvsc := NewTestVolumeSnapshotClass(client, namespace, volumeSnapshotClass)

	return tvsc, tvsc.Cleanup
}

func VerifySuccessfulReplicaAzVolumeAttachments(pod PodDetails, azDiskClient *azDiskClientSet.Clientset, storageClassParameters map[string]string, client clientset.Interface, namespace *v1.Namespace) (bool, *v1alpha1.AzVolumeAttachmentList, error) {
	if storageClassParameters["maxShares"] == "" {
		return true, nil, nil
	}

	var expectedNumberOfReplicas int
	nodes := GetSchedulableNodes(azDiskClient, client, pod, namespace)
	nodesAvailableForReplicas := len(nodes) - 1

	for _, volume := range pod.Volumes {
		if volume.PersistentVolume != nil {
			_, maxMountReplicas := azureutils.GetMaxSharesAndMaxMountReplicaCount(storageClassParameters, volume.VolumeMode == Block)
			if nodesAvailableForReplicas >= maxMountReplicas {
				expectedNumberOfReplicas = maxMountReplicas
			} else {
				expectedNumberOfReplicas = nodesAvailableForReplicas
			}

			replicaAttachments, err := GetReplicaAttachments(volume.PersistentVolume, client, namespace, azDiskClient)
			framework.ExpectNoError(err)
			numReplicaAttachments := len(replicaAttachments.Items)

			if numReplicaAttachments != expectedNumberOfReplicas {
				e2elog.Logf("expected %d replica attachments, found %d", expectedNumberOfReplicas, numReplicaAttachments)
				return false, nil, nil
			}

			failedReplicaAttachments := v1alpha1.AzVolumeAttachmentList{}

			for _, replica := range replicaAttachments.Items {
				if replica.Status.State != "Attached" {
					e2elog.Logf("found replica attachment %s, currently not attached", replica.Name)
					failedReplicaAttachments.Items = append(failedReplicaAttachments.Items, replica)
				} else {
					e2elog.Logf("found replica attachment %s in attached state", replica.Name)
				}
			}
			if len(failedReplicaAttachments.Items) > 0 {
				return false, &failedReplicaAttachments, nil
			}
		}
	}
	return true, nil, nil
}

func GetReplicaAttachments(persistentVolume *v1.PersistentVolume, client clientset.Interface, namespace *v1.Namespace, azDiskClient *azDiskClientSet.Clientset) (*v1alpha1.AzVolumeAttachmentList, error) {
	pv, err := client.CoreV1().PersistentVolumes().Get(context.TODO(), persistentVolume.Name, metav1.GetOptions{})
	if err != nil {
		ginkgo.Fail("failed to get persistent volume")
	}
	diskname, err := azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
	if err != nil {
		ginkgo.Fail("failed to get persistent volume diskname")
	}
	azVolumeAttachmentsReplica, err := azDiskClient.DiskV1alpha1().AzVolumeAttachments(azureconstants.DefaultAzureDiskCrdNamespace).List(context.Background(), metav1.ListOptions{LabelSelector: labels.Set(map[string]string{azureconstants.RoleLabel: "Replica", azureconstants.VolumeNameLabel: diskname}).String()})
	if err != nil {
		ginkgo.Fail("failed while getting replica attachments")
	}
	return azVolumeAttachmentsReplica, nil
}

func GetSchedulableNodes(azDiskClient *azDiskClientSet.Clientset, client clientset.Interface, pod PodDetails, namespace *v1.Namespace) []*v1.Node {
	nodes := nodeutil.ListAzDriverNodeNames(azDiskClient.DiskV1alpha1().AzDriverNodes(azureconstants.DefaultAzureDiskCrdNamespace))
	var availableNodes []*v1.Node
	schedulableNodes, err := client.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{FieldSelector: fields.Set{
		"spec.unschedulable": "false",
	}.AsSelector().String()})

	if err != nil {
		ginkgo.Fail("failed while getting schedulable nodes list")
	}

	podObj, err := client.CoreV1().Pods(namespace.Name).Get(context.TODO(), pod.Name, metav1.GetOptions{})
	if err != nil {
		ginkgo.Fail("failed while getting pod")
	}

	for _, nodeName := range nodes {
		for i, schedulableNode := range schedulableNodes.Items {
			if nodeName == schedulableNode.Name {
				//Check if node has any taints making it unschedulable

				nodeDetails, err := client.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
				framework.ExpectNoError(err)

				tolerable := true
				for _, taint := range nodeDetails.Spec.Taints {
					for _, podToleration := range podObj.Spec.Tolerations {
						if !podToleration.ToleratesTaint(&taint) {
							tolerable = false
						}
					}
				}
				if tolerable {
					availableNodes = append(availableNodes, &schedulableNodes.Items[i])
				}
			}
		}
	}

	return availableNodes
}
