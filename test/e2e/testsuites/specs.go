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

package testsuites

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/onsi/ginkgo/v2"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	clientset "k8s.io/client-go/kubernetes"
	restclientset "k8s.io/client-go/rest"

	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
)

type PodDetails struct {
	Cmd             string
	Volumes         []VolumeDetails
	IsWindows       bool
	WinServerVer    string
	UseCMD          bool
	UseAntiAffinity bool
	ReplicaCount    int32
}

type VolumeDetails struct {
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

const (
	VolumeSnapshotKind            = "VolumeSnapshot"
	VolumePVCKind                 = "PersistentVolumeClaim"
	APIVersionv1                  = "v1"
	VolumeGroupSnapshotKind       = "VolumeGroupSnapshot"
	APIVersionv1beta1             = "v1beta1"
	SnapshotAPIVersion            = "snapshot.storage.k8s.io/" + APIVersionv1
	VolumeGroupSnapshotAPIVersion = "groupsnapshot.storage.k8s.io/" + APIVersionv1beta1
)

var (
	SnapshotAPIGroup                             = "snapshot.storage.k8s.io"
	GroupSnapshotAPIGroup                        = "groupsnapshot.storage.k8s.io"
	isAzureStackCloud                            = strings.EqualFold(os.Getenv("AZURE_CLOUD_NAME"), "AZURESTACKCLOUD")
	azurePublicCloudSupportedStorageAccountTypes = []string{"Standard_LRS", "Premium_LRS", "StandardSSD_LRS"}
	azureStackCloudSupportedStorageAccountTypes  = []string{"Standard_LRS", "Premium_LRS"}
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

func (pod *PodDetails) SetupWithDynamicVolumes(ctx context.Context, client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, storageClassParameters map[string]string) (*TestPod, []func(context.Context)) {
	tpod := NewTestPod(client, namespace, pod.Cmd, pod.IsWindows, pod.WinServerVer)
	cleanupFuncs := make([]func(context.Context), 0)
	for n, v := range pod.Volumes {
		tpvc, funcs := v.SetupDynamicPersistentVolumeClaim(ctx, client, namespace, csiDriver, storageClassParameters, nil)
		cleanupFuncs = append(cleanupFuncs, funcs...)
		ginkgo.By("setting up the pod")
		if v.VolumeMode == Block {
			tpod.SetupRawBlockVolume(tpvc.persistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeDevice.NameGenerate, n+1), v.VolumeDevice.DevicePath)
		} else {
			tpod.SetupVolume(tpvc.persistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeMount.NameGenerate, n+1), fmt.Sprintf("%s%d", v.VolumeMount.MountPathGenerate, n+1), v.VolumeMount.ReadOnly)
		}
	}
	return tpod, cleanupFuncs
}

// SetupWithDynamicMultipleVolumes each pod will be mounted with multiple volumes with different storage account types
func (pod *PodDetails) SetupWithDynamicMultipleVolumes(ctx context.Context, client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver) (*TestPod, []func(context.Context)) {
	tpod := NewTestPod(client, namespace, pod.Cmd, pod.IsWindows, pod.WinServerVer)
	cleanupFuncs := make([]func(context.Context), 0)
	supportedStorageAccountTypes := azurePublicCloudSupportedStorageAccountTypes
	if isAzureStackCloud {
		supportedStorageAccountTypes = azureStackCloudSupportedStorageAccountTypes
	}
	accountTypeCount := len(supportedStorageAccountTypes)
	for n, v := range pod.Volumes {
		storageClassParameters := map[string]string{"skuName": supportedStorageAccountTypes[n%accountTypeCount]}
		tpvc, funcs := v.SetupDynamicPersistentVolumeClaim(ctx, client, namespace, csiDriver, storageClassParameters, nil)
		cleanupFuncs = append(cleanupFuncs, funcs...)
		if v.VolumeMode == Block {
			tpod.SetupRawBlockVolume(tpvc.persistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeDevice.NameGenerate, n+1), v.VolumeDevice.DevicePath)
		} else {
			tpod.SetupVolume(tpvc.persistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeMount.NameGenerate, n+1), fmt.Sprintf("%s%d", v.VolumeMount.MountPathGenerate, n+1), v.VolumeMount.ReadOnly)
		}
	}
	return tpod, cleanupFuncs
}

func (pod *PodDetails) SetupWithDynamicVolumesWithSubpath(ctx context.Context, client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, storageClassParameters map[string]string) (*TestPod, []func(context.Context)) {
	tpod := NewTestPod(client, namespace, pod.Cmd, pod.IsWindows, pod.WinServerVer)
	cleanupFuncs := make([]func(context.Context), 0)
	for n, v := range pod.Volumes {
		tpvc, funcs := v.SetupDynamicPersistentVolumeClaim(ctx, client, namespace, csiDriver, storageClassParameters, nil)
		cleanupFuncs = append(cleanupFuncs, funcs...)
		tpod.SetupVolumeMountWithSubpath(tpvc.persistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeMount.NameGenerate, n+1), fmt.Sprintf("%s%d", v.VolumeMount.MountPathGenerate, n+1), "testSubpath", v.VolumeMount.ReadOnly)
	}
	return tpod, cleanupFuncs
}

func (pod *PodDetails) SetupWithInlineVolumes(client clientset.Interface, namespace *v1.Namespace, diskURI string, readOnly bool) (*TestPod, []func(context.Context)) {
	tpod := NewTestPod(client, namespace, pod.Cmd, pod.IsWindows, pod.WinServerVer)
	cleanupFuncs := make([]func(context.Context), 0)
	for n, v := range pod.Volumes {
		tpod.SetupInlineVolume(fmt.Sprintf("%s%d", v.VolumeMount.NameGenerate, n+1), fmt.Sprintf("%s%d", v.VolumeMount.MountPathGenerate, n+1), diskURI, readOnly)
	}
	return tpod, cleanupFuncs
}

func (pod *PodDetails) SetupWithPreProvisionedVolumes(ctx context.Context, client clientset.Interface, namespace *v1.Namespace, csiDriver driver.PreProvisionedVolumeTestDriver, volumeContext map[string]string) (*TestPod, []func(context.Context)) {
	tpod := NewTestPod(client, namespace, pod.Cmd, pod.IsWindows, pod.WinServerVer)
	cleanupFuncs := make([]func(context.Context), 0)
	for n, v := range pod.Volumes {
		tpvc, funcs := v.SetupPreProvisionedPersistentVolumeClaim(ctx, client, namespace, csiDriver, volumeContext)
		cleanupFuncs = append(cleanupFuncs, funcs...)

		if v.VolumeMode == Block {
			tpod.SetupRawBlockVolume(tpvc.persistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeDevice.NameGenerate, n+1), v.VolumeDevice.DevicePath)
		} else {
			tpod.SetupVolume(tpvc.persistentVolumeClaim, fmt.Sprintf("%s%d", v.VolumeMount.NameGenerate, n+1), fmt.Sprintf("%s%d", v.VolumeMount.MountPathGenerate, n+1), v.VolumeMount.ReadOnly)
		}
	}
	return tpod, cleanupFuncs
}

func (pod *PodDetails) SetupDeployment(ctx context.Context, client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, storageClassParameters map[string]string) (*TestDeployment, []func(context.Context)) {
	cleanupFuncs := make([]func(context.Context), 0)
	volume := pod.Volumes[0]
	ginkgo.By("setting up the StorageClass")
	storageClass := csiDriver.GetDynamicProvisionStorageClass(storageClassParameters, volume.MountOptions, volume.ReclaimPolicy, volume.VolumeBindingMode, volume.AllowedTopologyValues, namespace.Name)
	tsc := NewTestStorageClass(client, namespace, storageClass)
	createdStorageClass := tsc.Create(ctx)
	cleanupFuncs = append(cleanupFuncs, tsc.Cleanup)
	ginkgo.By("setting up the PVC")
	tpvc := NewTestPersistentVolumeClaim(client, namespace, volume.ClaimSize, volume.VolumeMode, volume.VolumeAccessMode, &createdStorageClass, nil)
	tpvc.Create(ctx)
	if volume.VolumeBindingMode == nil || *volume.VolumeBindingMode == storagev1.VolumeBindingImmediate {
		tpvc.WaitForBound(ctx)
		tpvc.ValidateProvisionedPersistentVolume(ctx)
	}
	cleanupFuncs = append(cleanupFuncs, tpvc.Cleanup)
	ginkgo.By("setting up the Deployment")
	if pod.ReplicaCount == 0 {
		pod.ReplicaCount = 1
	}
	tDeployment := NewTestDeployment(client, namespace, pod.ReplicaCount, pod.Cmd, tpvc.persistentVolumeClaim, fmt.Sprintf("%s%d", volume.VolumeMount.NameGenerate, 1), fmt.Sprintf("%s%d", volume.VolumeMount.MountPathGenerate, 1), volume.VolumeMount.ReadOnly, pod.IsWindows, pod.UseCMD, pod.UseAntiAffinity, pod.WinServerVer)

	cleanupFuncs = append(cleanupFuncs, tDeployment.Cleanup)
	return tDeployment, cleanupFuncs
}

func (pod *PodDetails) SetupDeploymentWithPreProvisionedVolumes(ctx context.Context, client clientset.Interface, namespace *v1.Namespace, csiDriver driver.PreProvisionedVolumeTestDriver, volumeContext map[string]string) (*TestDeployment, []func(context.Context)) {
	cleanupFuncs := make([]func(context.Context), 0)
	volume := pod.Volumes[0]

	ginkgo.By("setting up the PVC")
	tpvc, funcs := volume.SetupPreProvisionedPersistentVolumeClaim(ctx, client, namespace, csiDriver, volumeContext)
	cleanupFuncs = append(cleanupFuncs, funcs...)

	ginkgo.By("setting up the Deployment")
	if pod.ReplicaCount == 0 {
		pod.ReplicaCount = 1
	}
	tDeployment := NewTestDeployment(client, namespace, pod.ReplicaCount, pod.Cmd, tpvc.persistentVolumeClaim, fmt.Sprintf("%s%d", volume.VolumeMount.NameGenerate, 1), fmt.Sprintf("%s%d", volume.VolumeMount.MountPathGenerate, 1), volume.VolumeMount.ReadOnly, pod.IsWindows, pod.UseCMD, pod.UseAntiAffinity, pod.WinServerVer)

	cleanupFuncs = append(cleanupFuncs, tDeployment.Cleanup)
	return tDeployment, cleanupFuncs
}

func (pod *PodDetails) SetupStatefulset(ctx context.Context, client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, storageClassParameters map[string]string) (*TestStatefulset, []func(context.Context)) {
	cleanupFuncs := make([]func(context.Context), 0)
	volume := pod.Volumes[0]
	ginkgo.By("setting up the StorageClass")
	storageClass := csiDriver.GetDynamicProvisionStorageClass(storageClassParameters, volume.MountOptions, volume.ReclaimPolicy, volume.VolumeBindingMode, volume.AllowedTopologyValues, namespace.Name)
	tsc := NewTestStorageClass(client, namespace, storageClass)
	createdStorageClass := tsc.Create(ctx)
	cleanupFuncs = append(cleanupFuncs, tsc.Cleanup)
	ginkgo.By("setting up the PVC")
	tpvc := NewTestPersistentVolumeClaim(client, namespace, volume.ClaimSize, volume.VolumeMode, volume.VolumeAccessMode, &createdStorageClass, nil)
	storageClassName := ""
	if tpvc.storageClass != nil {
		storageClassName = tpvc.storageClass.Name
	}
	tpvc.requestedPersistentVolumeClaim = generatePVC(tpvc.namespace.Name, storageClassName, "pvc", tpvc.claimSize, tpvc.volumeMode, tpvc.accessMode, tpvc.dataSource, nil)
	ginkgo.By("setting up the statefulset")
	tStatefulset := NewTestStatefulset(client, namespace, pod.Cmd, tpvc.requestedPersistentVolumeClaim, "pvc", fmt.Sprintf("%s%d", volume.VolumeMount.MountPathGenerate, 1), volume.VolumeMount.ReadOnly, pod.IsWindows, pod.UseCMD, pod.WinServerVer)

	cleanupFuncs = append(cleanupFuncs, tStatefulset.Cleanup)
	return tStatefulset, cleanupFuncs
}

func (volume *VolumeDetails) SetupDynamicPersistentVolumeClaim(ctx context.Context, client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, storageClassParameters map[string]string, labels map[string]string) (*TestPersistentVolumeClaim, []func(context.Context)) {
	cleanupFuncs := make([]func(context.Context), 0)
	storageClass := volume.StorageClass
	if storageClass == nil {
		tsc, tscCleanup := volume.CreateStorageClass(ctx, client, namespace, csiDriver, storageClassParameters)
		cleanupFuncs = append(cleanupFuncs, tscCleanup)
		storageClass = tsc.storageClass
	}
	ginkgo.By("setting up the PVC and PV")
	var tpvc *TestPersistentVolumeClaim
	if volume.DataSource != nil {
		dataSource := &v1.TypedLocalObjectReference{
			Name: volume.DataSource.Name,
			Kind: volume.DataSource.Kind,
		}
		if volume.DataSource.Kind == VolumeSnapshotKind {
			dataSource.APIGroup = &SnapshotAPIGroup
		}
		tpvc = NewTestPersistentVolumeClaimWithDataSource(client, namespace, volume.ClaimSize, volume.VolumeMode, volume.VolumeAccessMode, storageClass, dataSource, labels)
	} else {
		tpvc = NewTestPersistentVolumeClaim(client, namespace, volume.ClaimSize, volume.VolumeMode, volume.VolumeAccessMode, storageClass, labels)
	}
	tpvc.Create(ctx)
	cleanupFuncs = append(cleanupFuncs, tpvc.Cleanup)
	// PV will not be ready until PVC is used in a pod when volumeBindingMode: WaitForFirstConsumer
	if volume.VolumeBindingMode == nil || *volume.VolumeBindingMode == storagev1.VolumeBindingImmediate {
		tpvc.WaitForBound(ctx)
		tpvc.ValidateProvisionedPersistentVolume(ctx)
	}

	return tpvc, cleanupFuncs
}

func (volume *VolumeDetails) SetupPreProvisionedPersistentVolumeClaim(ctx context.Context, client clientset.Interface, namespace *v1.Namespace, csiDriver driver.PreProvisionedVolumeTestDriver, volumeContext map[string]string) (*TestPersistentVolumeClaim, []func(context.Context)) {
	cleanupFuncs := make([]func(context.Context), 0)
	ginkgo.By("setting up the PV")
	volumeMode := v1.PersistentVolumeFilesystem
	if volume.VolumeMode == Block {
		volumeMode = v1.PersistentVolumeBlock
	}
	pv := csiDriver.GetPersistentVolume(volume.VolumeID, volume.FSType, volume.ClaimSize, volumeMode, volume.VolumeAccessMode, volume.ReclaimPolicy, namespace.Name, volumeContext)
	tpv := NewTestPreProvisionedPersistentVolume(client, pv)
	tpv.Create(ctx)
	ginkgo.By("setting up the PVC")
	tpvc := NewTestPersistentVolumeClaim(client, namespace, volume.ClaimSize, volume.VolumeMode, volume.VolumeAccessMode, nil, nil)
	tpvc.Create(ctx)
	cleanupFuncs = append(cleanupFuncs, tpvc.DeleteBoundPersistentVolume)
	cleanupFuncs = append(cleanupFuncs, tpvc.Cleanup)
	tpvc.WaitForBound(ctx)
	tpvc.ValidateProvisionedPersistentVolume(ctx)

	return tpvc, cleanupFuncs
}

func (volume *VolumeDetails) CreateStorageClass(ctx context.Context, client clientset.Interface, namespace *v1.Namespace, csiDriver driver.DynamicPVTestDriver, storageClassParameters map[string]string) (*TestStorageClass, func(context.Context)) {
	ginkgo.By("setting up the StorageClass")
	storageClass := csiDriver.GetDynamicProvisionStorageClass(storageClassParameters, volume.MountOptions, volume.ReclaimPolicy, volume.VolumeBindingMode, volume.AllowedTopologyValues, namespace.Name)
	tsc := NewTestStorageClass(client, namespace, storageClass)
	tsc.Create(ctx)
	return tsc, tsc.Cleanup
}

func CreateVolumeSnapshotClass(client restclientset.Interface, namespace *v1.Namespace, parameters map[string]string, csiDriver driver.VolumeSnapshotTestDriver) (*TestVolumeSnapshotClass, func()) {
	ginkgo.By("setting up the VolumeSnapshotClass")
	volumeSnapshotClass := csiDriver.GetVolumeSnapshotClass(namespace.Name, parameters)
	tvsc := NewTestVolumeSnapshotClass(client, namespace, volumeSnapshotClass)

	return tvsc, tvsc.Cleanup
}

func CreateVolumeGroupSnapshotClass(client restclientset.Interface, snapshotclient restclientset.Interface, namespace *v1.Namespace, parameters map[string]string, csiDriver driver.VolumeGroupSnapshotTestDriver) (*TestVolumeGroupSnapshotClass, func()) {
	ginkgo.By("setting up the VolumeGroupSnapshotClass")
	volumeSnapshotClass := csiDriver.GetVolumeGroupSnapshotClass(namespace.Name, parameters)
	tvsc := NewTestVolumeGroupSnapshotClass(client, snapshotclient, namespace, volumeSnapshotClass)

	return tvsc, tvsc.Cleanup
}
