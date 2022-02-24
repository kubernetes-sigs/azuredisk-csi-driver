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

	"github.com/onsi/ginkgo"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"

	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
)

type PodDetails struct {
	Name            string
	Cmd             string
	Volumes         []VolumeDetails
	IsWindows       bool
	UseCMD          bool
	UseAntiAffinity bool
	ReplicaCount    int32
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
