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

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk"
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
	Volume                 VolumeDetails
	IsTestingMigration     bool
}

func (t *DynamicallyProvisionedResizeVolumeTest) Run(client clientset.Interface, namespace *v1.Namespace) {
	// Force volume binding mode to immediate so the PV can be provisioned without a pod
	volumeBindingMode := storagev1.VolumeBindingImmediate
	t.Volume.VolumeBindingMode = &volumeBindingMode
	tpvc, _ := t.Volume.SetupDynamicPersistentVolumeClaim(client, namespace, t.CSIDriver, t.StorageClassParameters)

	pvcName := tpvc.persistentVolumeClaim.Name
	pvc, err := client.CoreV1().PersistentVolumeClaims(namespace.Name).Get(context.TODO(), pvcName, metav1.GetOptions{})
	if err != nil {
		framework.ExpectNoError(err, fmt.Sprintf("fail to get original pvc(%s): %v", pvcName, err))
	}

	originalSize := pvc.Spec.Resources.Requests["storage"]
	delta := resource.Quantity{}
	delta.Set(volumehelper.GiBToBytes(1))
	originalSize.Add(delta)
	pvc.Spec.Resources.Requests["storage"] = originalSize

	ginkgo.By("resizing the pvc")
	updatedPvc, err := client.CoreV1().PersistentVolumeClaims(namespace.Name).Update(context.TODO(), pvc, metav1.UpdateOptions{})
	if err != nil {
		framework.ExpectNoError(err, fmt.Sprintf("fail to resize pvc(%s): %v", pvcName, err))
	}
	updatedSize := updatedPvc.Spec.Resources.Requests["storage"]

	ginkgo.By("sleep 30s waiting for resize complete")
	time.Sleep(30 * time.Second)

	ginkgo.By("checking the resizing result")
	newPvc, err := client.CoreV1().PersistentVolumeClaims(namespace.Name).Get(context.TODO(), pvcName, metav1.GetOptions{})
	if err != nil {
		framework.ExpectNoError(err, fmt.Sprintf("fail to get new pvc(%s): %v", pvcName, err))
	}
	newSize := newPvc.Spec.Resources.Requests["storage"]
	if !newSize.Equal(updatedSize) {
		framework.Failf("newSize(%+v) is not equal to updatedSize(%+v)", newSize.String(), updatedSize.String())
	}

	ginkgo.By("checking the resizing PV result")
	newPv, _ := client.CoreV1().PersistentVolumes().Get(context.Background(), newPvc.Spec.VolumeName, metav1.GetOptions{})
	newPvSize := newPv.Spec.Capacity["storage"]
	if !newSize.Equal(newPvSize) && !t.IsTestingMigration {
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
	diskName, err := azuredisk.GetDiskName(diskURI)
	framework.ExpectNoError(err, fmt.Sprintf("Error getting diskName for azuredisk %v", err))
	resourceGroup, err := azuredisk.GetResourceGroupFromURI(diskURI)
	framework.ExpectNoError(err, fmt.Sprintf("Error getting resourceGroup for azuredisk %v", err))

	creds, err := credentials.CreateAzureCredentialFile(false)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	azureClient, err := azure.GetAzureClient(creds.Cloud, creds.SubscriptionID, creds.AADClientID, creds.TenantID, creds.AADClientSecret)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	//get disk information
	disksClient, err := azureClient.GetAzureDisksClient()
	framework.ExpectNoError(err, fmt.Sprintf("Error getting client for azuredisk %v", err))
	disktest, err := disksClient.Get(context.Background(), resourceGroup, diskName)
	framework.ExpectNoError(err, fmt.Sprintf("Error getting disk for azuredisk %v", err))
	newdiskSize := strconv.Itoa(int(*disktest.DiskSizeGB)) + "Gi"
	if !(newSize.String() == newdiskSize) && !t.IsTestingMigration {
		framework.Failf("newPVCSize(%+v) is not equal to new azurediskSize(%+v)", newSize.String(), newdiskSize)
	}

	// will delete the PVC
	// will also wait for PV to be deleted when reclaimPolicy=Delete
	tpvc.Cleanup()
	// first check PV stills exists, then manually delete it
	if tpvc.ReclaimPolicy() == v1.PersistentVolumeReclaimRetain {
		tpvc.WaitForPersistentVolumePhase(v1.VolumeReleased)
		tpvc.DeleteBoundPersistentVolume()
	}
}
