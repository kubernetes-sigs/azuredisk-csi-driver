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
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-03-01/compute"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/azure"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/credentials"
)

// DynamicallyProvisionedAzureDiskDetach will provision required StorageClass(es), PVC(s) and Pod(s)
// Waiting for the PV provisioner to create azuredisk
// delete Pods
// Testing if disk is in unattached state
type DynamicallyProvisionedAzureDiskDetach struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pods                   []resources.PodDetails
	StorageClassParameters map[string]string
}

func (t *DynamicallyProvisionedAzureDiskDetach) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	for _, pod := range t.Pods {
		tpod, cleanupFuncs := pod.SetupWithDynamicVolumes(client, namespace, t.CSIDriver, t.StorageClassParameters, schedulerName)
		for _, cleanupFunc := range cleanupFuncs {
			defer cleanupFunc()
		}
		ginkgo.By("deploying the pod")
		tpod.Create()

		ginkgo.By("checking that the pod is running")
		tpod.WaitForRunning()

		ginkgo.By("getting azuredisk information")
		//Get diskURI from pv information
		pvcname := tpod.Pod.Spec.Volumes[0].VolumeSource.PersistentVolumeClaim.ClaimName
		pvc, err := client.CoreV1().PersistentVolumeClaims(namespace.Name).Get(context.Background(), pvcname, metav1.GetOptions{})
		framework.ExpectNoError(err, fmt.Sprintf("Error getting pvc for azuredisk %v", err))

		pvname := pvc.Spec.VolumeName
		pv, _ := client.CoreV1().PersistentVolumes().Get(context.Background(), pvname, metav1.GetOptions{})
		var diskURI string
		if pv.Spec.PersistentVolumeSource.CSI != nil {
			diskURI = pv.Spec.PersistentVolumeSource.CSI.VolumeHandle
		} else if pv.Spec.PersistentVolumeSource.AzureDisk != nil {
			diskURI = pv.Spec.PersistentVolumeSource.AzureDisk.DataDiskURI
		}
		diskName, err := azureutils.GetDiskName(diskURI)
		framework.ExpectNoError(err, fmt.Sprintf("Error getting diskName for azuredisk %v", err))
		resourceGroup, err := azureutils.GetResourceGroupFromURI(diskURI)
		framework.ExpectNoError(err, fmt.Sprintf("Error getting resourceGroup for azuredisk %v", err))

		creds, err := credentials.CreateAzureCredentialFile()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		azureClient, err := azure.GetAzureClient(creds.Cloud, creds.SubscriptionID, creds.AADClientID, creds.TenantID, creds.AADClientSecret)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		//get disk information
		disksClient, err := azureClient.GetAzureDisksClient()
		framework.ExpectNoError(err, fmt.Sprintf("Error getting client for azuredisk %v", err))
		disktest, err := disksClient.Get(context.Background(), resourceGroup, diskName)
		framework.ExpectNoError(err, fmt.Sprintf("Error getting disk for azuredisk %v", err))
		framework.ExpectEqual(compute.Attached, disktest.DiskState)

		ginkgo.By("begin to delete the pod")
		tpod.Cleanup()

		err = wait.Poll(15*time.Second, 10*time.Minute, func() (bool, error) {
			disktest, err := disksClient.Get(context.Background(), resourceGroup, diskName)
			if err != nil {
				return false, fmt.Errorf("error getting disk for azuredisk %v", err)
			}
			if disktest.DiskState == compute.Unattached {
				return true, nil
			}
			ginkgo.By(fmt.Sprintf("current disk state(%v) is not in unattached state, wait and recheck", disktest.DiskState))
			return false, nil
		})
		framework.ExpectNoError(err, fmt.Sprintf("waiting for disk detach complete returned with error: %v", err))
	}
}
