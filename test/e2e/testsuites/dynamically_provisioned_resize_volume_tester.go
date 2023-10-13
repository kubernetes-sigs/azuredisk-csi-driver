/*
Copyright 2020 The Kubernetes Authors.

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
	"strconv"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	scale "k8s.io/api/autoscaling/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/azure"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/credentials"
)

// DynamicallyProvisionedResizeVolumeTest will provision required StorageClass(es), PVC(s)
// Waiting for the PV provisioner to resize the PV
// Testing if the PV is resized successfully.
type DynamicallyProvisionedResizeVolumeTest struct {
	CSIDriver              driver.DynamicPVTestDriver
	StorageClassParameters map[string]string
	Pod                    PodDetails
	Volume                 VolumeDetails
	ResizeOffline          bool
}

func (t *DynamicallyProvisionedResizeVolumeTest) Run(ctx context.Context, client clientset.Interface, namespace *v1.Namespace) {
	tStatefulSet, cleanup := t.Pod.SetupStatefulset(ctx, client, namespace, t.CSIDriver, driver.GetParameters())
	// Defer must be called here for resources not get removed before using them
	for i := range cleanup {
		i := i
		defer cleanup[i](ctx)
	}

	ginkgo.By("deploying the statefulset")
	tStatefulSet.Create(ctx)

	ginkgo.By("checking that the pod for statefulset is running")
	tStatefulSet.WaitForPodReady(ctx)

	//Get diskURI from statefulset information
	pvcName := fmt.Sprintf("pvc-%s-%d", tStatefulSet.statefulset.ObjectMeta.Name, 0)

	pvc, err := client.CoreV1().PersistentVolumeClaims(namespace.Name).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		framework.ExpectNoError(err, fmt.Sprintf("fail to get original pvc(%s): %v", pvcName, err))
	}

	// Define a new scale for statefulset
	newScale := &scale.Scale{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tStatefulSet.statefulset.Name,
			Namespace: tStatefulSet.namespace.Name,
		},
		Spec: scale.ScaleSpec{
			Replicas: int32(0)}}

	if t.ResizeOffline {
		// Scale statefulset to 0
		_, err = client.AppsV1().StatefulSets(tStatefulSet.namespace.Name).UpdateScale(ctx, tStatefulSet.statefulset.Name, newScale, metav1.UpdateOptions{})
		framework.ExpectNoError(err)

		ginkgo.By("sleep 120s waiting for disk to detach from node")
		time.Sleep(120 * time.Second)
	}

	// Get the original requested size of the pvs and increase it by 10GB
	originalSize := pvc.Spec.Resources.Requests["storage"]
	delta := resource.Quantity{}
	delta.Set(volumehelper.GiBToBytes(10))
	originalSize.Add(delta)
	pvc.Spec.Resources.Requests["storage"] = originalSize

	ginkgo.By("resizing the pvc")
	updatedPvc, err := client.CoreV1().PersistentVolumeClaims(namespace.Name).Update(ctx, pvc, metav1.UpdateOptions{})
	if err != nil {
		framework.ExpectNoError(err, fmt.Sprintf("fail to resize pvc(%s): %v", pvcName, err))
	}
	updatedSize := updatedPvc.Spec.Resources.Requests["storage"]

	ginkgo.By("sleep 30s waiting for resize complete")
	time.Sleep(30 * time.Second)

	ginkgo.By("checking the resizing result")
	newPvc, err := client.CoreV1().PersistentVolumeClaims(namespace.Name).Get(ctx, pvcName, metav1.GetOptions{})
	if err != nil {
		framework.ExpectNoError(err, fmt.Sprintf("fail to get new pvc(%s): %v", pvcName, err))
	}
	newSize := newPvc.Spec.Resources.Requests["storage"]
	if !newSize.Equal(updatedSize) {
		framework.Failf("newSize(%+v) is not equal to updatedSize(%+v)", newSize.String(), updatedSize.String())
	}

	var newPv *v1.PersistentVolume
	var newPvSize resource.Quantity
	err = wait.PollImmediate(30*time.Second, 10*time.Minute, func() (bool, error) {
		//takes 3-6 minutes on average for dynamic resize
		ginkgo.By("checking the resizing PV result")
		newPv, _ = client.CoreV1().PersistentVolumes().Get(ctx, newPvc.Spec.VolumeName, metav1.GetOptions{})
		newPvSize = newPv.Spec.Capacity["storage"]
		if !newSize.Equal(newPvSize) {
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		ginkgo.By(fmt.Sprintf("newPVCSize(%+v) is not equal to newPVSize(%+v)", newSize.String(), newPvSize.String()))
	}

	ginkgo.By("checking the resizing azuredisk result")
	var diskURI string
	if newPv.Spec.PersistentVolumeSource.CSI != nil {
		diskURI = newPv.Spec.PersistentVolumeSource.CSI.VolumeHandle
	} else if newPv.Spec.PersistentVolumeSource.AzureDisk != nil {
		diskURI = newPv.Spec.PersistentVolumeSource.AzureDisk.DataDiskURI
	}
	ginkgo.By(fmt.Sprintf("got DiskURI: %v", diskURI))
	diskName, err := azureutils.GetDiskName(diskURI)
	framework.ExpectNoError(err, fmt.Sprintf("Error getting diskName for azuredisk %v", err))
	resourceGroup, err := azureutils.GetResourceGroupFromURI(diskURI)
	framework.ExpectNoError(err, fmt.Sprintf("Error getting resourceGroup for azuredisk %v", err))

	creds, err := credentials.CreateAzureCredentialFile()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	azureClient, err := azure.GetAzureClient(creds.Cloud, creds.SubscriptionID, creds.AADClientID, creds.TenantID, creds.AADClientSecret)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Get disk information
	disksClient, err := azureClient.GetAzureDisksClient()
	framework.ExpectNoError(err, fmt.Sprintf("Error getting client for azuredisk %v", err))
	disktest, err := disksClient.Get(ctx, resourceGroup, diskName)
	framework.ExpectNoError(err, fmt.Sprintf("Error getting disk for azuredisk %v", err))
	newdiskSize := strconv.Itoa(int(*disktest.Properties.DiskSizeGB)) + "Gi"
	if !(newSize.String() == newdiskSize) {
		framework.Failf("newPVCSize(%+v) is not equal to new azurediskSize(%+v)", newSize.String(), newdiskSize)
	}

	if t.ResizeOffline {
		// Scale the stateful set back to 1 pod
		newScale.Spec.Replicas = int32(1)

		_, err = client.AppsV1().StatefulSets(tStatefulSet.namespace.Name).UpdateScale(ctx, tStatefulSet.statefulset.Name, newScale, metav1.UpdateOptions{})
		framework.ExpectNoError(err)

		ginkgo.By("sleep 30s waiting for statefulset update complete")
		time.Sleep(30 * time.Second)
		ginkgo.By("checking that the pod for statefulset is running")
		tStatefulSet.WaitForPodReady(ctx)
	}
}
