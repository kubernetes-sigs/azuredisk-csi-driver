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
	"strings"

	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/azure"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/credentials"

	"github.com/onsi/ginkgo"
	"github.com/pborman/uuid"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	restclientset "k8s.io/client-go/rest"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
)

// DynamicallyProvisionedVolumeSnapshotTest will provision required StorageClass(es),VolumeSnapshotClass(es), PVC(s) and Pod(s)
// Waiting for the PV provisioner to create a new PV
// Testing if the Pod(s) can write and read to mounted volumes
// Create a snapshot, validate the data is still on the disk, and then write and read to it again
// And finally delete the snapshot
// This test only supports a single volume
type DynamicallyProvisionedVolumeSnapshotTest struct {
	CSIDriver              driver.PVTestDriver
	Pod                    PodDetails
	ShouldOverwrite        bool
	PodOverwrite           PodDetails
	PodWithSnapshot        PodDetails
	StorageClassParameters map[string]string
}

func (t *DynamicallyProvisionedVolumeSnapshotTest) Run(client clientset.Interface, restclient restclientset.Interface, namespace *v1.Namespace, schedulerName string) {
	tpod := NewTestPod(client, namespace, t.Pod.Cmd, schedulerName, t.Pod.IsWindows)
	volume := t.Pod.Volumes[0]
	ctx := context.Background()

	tpvc, pvcCleanup := volume.SetupDynamicPersistentVolumeClaim(client, namespace, t.CSIDriver, t.StorageClassParameters)
	for i := range pvcCleanup {
		defer pvcCleanup[i]()
	}
	tpod.SetupVolume(tpvc.persistentVolumeClaim, volume.VolumeMount.NameGenerate+"1", volume.VolumeMount.MountPathGenerate+"1", volume.VolumeMount.ReadOnly)
	ginkgo.By("deploying the pod")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod's command exits with no error")
	tpod.WaitForSuccess()

	// delete pod and wait for volume to be unpublished to ensure filesystem cache is flushed
	tpod.Cleanup()
	err := wait.PollImmediate(poll, pollTimeout, func() (bool, error) {
		volumeAttachments, err := client.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		notFound := true
		for _, volumeAttachment := range volumeAttachments.Items {
			if volumeAttachment.Spec.Source.PersistentVolumeName != nil && *volumeAttachment.Spec.Source.PersistentVolumeName == tpvc.persistentVolumeClaim.Spec.VolumeName {
				notFound = false
			}
		}
		return notFound, nil
	})
	framework.ExpectNoError(err)

	ginkgo.By("Checking Prow test resource group")
	creds, err := credentials.CreateAzureCredentialFile()
	framework.ExpectNoError(err, fmt.Sprintf("Error getting creds for AzurePublicCloud %v", err))
	defer func() {
		err := credentials.DeleteAzureCredentialFile()
		framework.ExpectNoError(err)
	}()

	ginkgo.By("Prow test resource group: " + creds.ResourceGroup)

	azureClient, err := azure.GetAzureClient(creds.Cloud, creds.SubscriptionID, creds.AADClientID, creds.TenantID, creds.AADClientSecret)
	framework.ExpectNoError(err)

	//create external resource group
	externalRG := credentials.ResourceGroupPrefix + uuid.NewUUID().String()
	ginkgo.By("Creating external resource group: " + externalRG)
	_, err = azureClient.EnsureResourceGroup(ctx, externalRG, creds.Location, nil)
	framework.ExpectNoError(err)
	defer func() {
		// Only delete resource group the test created
		if strings.HasPrefix(externalRG, credentials.ResourceGroupPrefix) {
			e2elog.Logf("Deleting resource group %s", externalRG)
			err := azureClient.DeleteResourceGroup(ctx, externalRG)
			framework.ExpectNoError(err)
		}
	}()

	ginkgo.By("creating volume snapshot class with external rg " + externalRG)
	tvsc, cleanup := CreateVolumeSnapshotClass(restclient, namespace, t.CSIDriver)
	mp := map[string]string{
		"resourceGroup": externalRG,
	}
	tvsc.volumeSnapshotClass.Parameters = mp
	tvsc.Create()
	defer cleanup()

	ginkgo.By("taking snapshots")
	snapshot := tvsc.CreateSnapshot(tpvc.persistentVolumeClaim)

	defer tvsc.DeleteSnapshot(snapshot)
	tvsc.ReadyToUse(snapshot)

	if t.ShouldOverwrite {
		tpod = NewTestPod(client, namespace, t.PodOverwrite.Cmd, schedulerName, t.PodOverwrite.IsWindows)

		tpod.SetupVolume(tpvc.persistentVolumeClaim, volume.VolumeMount.NameGenerate+"1", volume.VolumeMount.MountPathGenerate+"1", volume.VolumeMount.ReadOnly)
		ginkgo.By("deploying a new pod to overwrite pv data")
		tpod.Create()
		defer tpod.Cleanup()
		ginkgo.By("checking that the pod's command exits with no error")
		tpod.WaitForSuccess()
	}

	snapshotVolume := volume
	snapshotVolume.DataSource = &DataSource{
		Kind: VolumeSnapshotKind,
		Name: snapshot.Name,
	}
	t.PodWithSnapshot.Volumes = []VolumeDetails{snapshotVolume}
	tPodWithSnapshot, tPodWithSnapshotCleanup := t.PodWithSnapshot.SetupWithDynamicVolumes(client, namespace, t.CSIDriver, t.StorageClassParameters, schedulerName)
	for i := range tPodWithSnapshotCleanup {
		defer tPodWithSnapshotCleanup[i]()
	}
	ginkgo.By("deploying a pod with a volume restored from the snapshot")
	tPodWithSnapshot.Create()
	defer tPodWithSnapshot.Cleanup()
	ginkgo.By("checking that the pod's command exits with no error")
	tPodWithSnapshot.WaitForSuccess()

}
