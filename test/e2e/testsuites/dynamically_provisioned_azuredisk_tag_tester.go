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

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/azure"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/credentials"
)

// DynamicallyProvisionedAzureDiskWithTag will provision required StorageClass(es), PVC(s) and Pod(s)
// Waiting for the PV provisioner to create azuredisk
// Testing if azuredisk contains tag
type DynamicallyProvisionedAzureDiskWithTag struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pods                   []PodDetails
	StorageClassParameters map[string]string
	Tags                   string
}

func (t *DynamicallyProvisionedAzureDiskWithTag) Run(ctx context.Context, client clientset.Interface, namespace *v1.Namespace) {
	for _, pod := range t.Pods {
		tpod, cleanup := pod.SetupWithDynamicVolumes(ctx, client, namespace, t.CSIDriver, t.StorageClassParameters)
		//defer must be called here for resources not get removed before using them
		for i := range cleanup {
			defer cleanup[i](ctx)
		}
		ginkgo.By("deploying the pod")
		tpod.Create(ctx)
		defer tpod.Cleanup(ctx)

		ginkgo.By("checking that the pod is running")
		tpod.WaitForRunning(ctx)

		ginkgo.By("checking whether azuredisk contains tag")

		//Get diskURI from pv information
		pvcname := tpod.pod.Spec.Volumes[0].VolumeSource.PersistentVolumeClaim.ClaimName
		pvc, err := client.CoreV1().PersistentVolumeClaims(namespace.Name).Get(ctx, pvcname, metav1.GetOptions{})
		framework.ExpectNoError(err, fmt.Sprintf("Error getting pvc for azuredisk %v", err))

		pvname := pvc.Spec.VolumeName
		pv, _ := client.CoreV1().PersistentVolumes().Get(ctx, pvname, metav1.GetOptions{})
		diskURI := pv.Spec.PersistentVolumeSource.CSI.VolumeHandle
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
		disktest, err := disksClient.Get(ctx, resourceGroup, diskName)
		framework.ExpectNoError(err, fmt.Sprintf("Error getting disk for azuredisk %v", err))
		test, err := util.ConvertTagsToMap(t.Tags)
		framework.ExpectNoError(err, fmt.Sprintf("Error getting tag %v", err))
		test["k8s-azure-created-by"] = "kubernetes-azure-dd"

		for k, v := range test {
			_, ok := disktest.Tags[k]
			framework.ExpectEqual(ok, true)
			if ok {
				framework.ExpectEqual(*disktest.Tags[k], v)
			}
		}
		tag, ok := disktest.Tags["kubernetes.io-created-for-pv-name"]
		framework.ExpectEqual(ok, true)
		framework.ExpectEqual(tag != nil, true)
		if tag != nil {
			ginkgo.By(fmt.Sprintf("kubernetes.io-created-for-pv-name: %s", *tag))
		}

		tag, ok = disktest.Tags["kubernetes.io-created-for-pvc-name"]
		framework.ExpectEqual(ok, true)
		framework.ExpectEqual(tag != nil, true)
		if tag != nil {
			ginkgo.By(fmt.Sprintf("kubernetes.io-created-for-pvc-name: %s", *tag))
		}

		tag, ok = disktest.Tags["kubernetes.io-created-for-pvc-namespace"]
		framework.ExpectEqual(ok, true)
		framework.ExpectEqual(tag != nil, true)
		if tag != nil {
			ginkgo.By(fmt.Sprintf("kubernetes.io-created-for-pvc-namespace: %s", *tag))
		}
	}
}
