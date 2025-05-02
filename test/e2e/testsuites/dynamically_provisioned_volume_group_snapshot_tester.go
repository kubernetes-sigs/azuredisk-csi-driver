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
	"strings"
	"time"

	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/azure"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/credentials"

	"github.com/onsi/ginkgo/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/uuid"

	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	restclientset "k8s.io/client-go/rest"
	"k8s.io/kubernetes/test/e2e/framework"
)

// DynamicallyProvisionedVolumeGroupSnapshotTest will provision required StorageClass(es),VolumeGroupSnapshotClass(es), PVC(s) and Pod(s)
// Waiting for the PV provisioner to create a new PV
// Testing if the Pod(s) can write and read to mounted volumes
// Create a volume group snapshot, validate the data is still on the disk, and then write and read to it again
// And finally delete the volume group snapshot
// This test only supports a single volume
type DynamicallyProvisionedVolumeGroupSnapshotTest struct {
	CSIDriver                           driver.PVTestDriver
	GroupPods                           []PodDetails
	MatchLabels                         map[string]string
	ShouldOverwrite                     bool
	PodsOverwrite                       []PodDetails
	PodsWithSnapshot                    []PodDetails
	StorageClassParameters              map[string]string
	GroupSnapshotStorageClassParameters map[string]string
	IsWindowsHPCDeployment              bool
	PodCheck                            []*PodExecCheck
}

func (t *DynamicallyProvisionedVolumeGroupSnapshotTest) Run(ctx context.Context, client clientset.Interface, restclient restclientset.Interface, restsnapshotclient restclientset.Interface, namespace *v1.Namespace) {
	volumes := []VolumeDetails{}
	tpods := []*TestPod{}
	tpvcs := []*TestPersistentVolumeClaim{}
	for i := range t.GroupPods {
		ginkgo.By("Running test for pod " + strconv.Itoa(i))
		tpod := NewTestPod(client, namespace, t.GroupPods[i].Cmd, t.GroupPods[i].IsWindows, t.GroupPods[i].WinServerVer)
		volume := t.GroupPods[i].Volumes[0]
		tpvc, pvcCleanup := volume.SetupDynamicPersistentVolumeClaim(ctx, client, namespace, t.CSIDriver, t.StorageClassParameters, t.MatchLabels)
		for i := range pvcCleanup {
			defer pvcCleanup[i](ctx)
		}
		tpod.SetupVolume(tpvc.persistentVolumeClaim, volume.VolumeMount.NameGenerate+"1", volume.VolumeMount.MountPathGenerate+"1", volume.VolumeMount.ReadOnly)
		ginkgo.By("deploying the pod")
		tpod.Create(ctx)
		defer tpod.Cleanup(ctx)
		ginkgo.By("checking that the pod's command exits with no error")
		tpod.WaitForSuccess(ctx)
		ginkgo.By("sleep 10s to make sure the data is written to the disk")
		time.Sleep(time.Millisecond * 10000)
		volumes = append(volumes, volume)
		tpods = append(tpods, tpod)
		tpvcs = append(tpvcs, tpvc)
	}

	ginkgo.By("Checking Prow test resource group")
	creds, err := credentials.CreateAzureCredentialFile()
	framework.ExpectNoError(err, fmt.Sprintf("Error getting creds for AzurePublicCloud %v", err))
	defer func() {
		err := credentials.DeleteAzureCredentialFile()
		framework.ExpectNoError(err)
	}()

	ginkgo.By("Prow test resource group: " + creds.ResourceGroup)

	azureClient, err := azure.GetAzureClient(creds.Cloud, creds.SubscriptionID, creds.AADClientID, creds.TenantID, creds.AADClientSecret, creds.AADFederatedTokenFile)
	framework.ExpectNoError(err)

	//create external resource group
	externalRG := credentials.ResourceGroupPrefix + string(uuid.NewUUID())
	ginkgo.By("Creating external resource group: " + externalRG)
	_, err = azureClient.EnsureResourceGroup(ctx, externalRG, creds.Location, nil)
	framework.ExpectNoError(err)
	defer func() {
		// Only delete resource group the test created
		if strings.HasPrefix(externalRG, credentials.ResourceGroupPrefix) {
			framework.Logf("Deleting resource group %s", externalRG)
			err := azureClient.DeleteResourceGroup(ctx, externalRG)
			framework.ExpectNoError(err)
		}
	}()

	ginkgo.By("creating volume group snapshot class with external rg " + externalRG)
	tvsc, cleanup := CreateVolumeGroupSnapshotClass(restclient, restsnapshotclient, namespace, t.GroupSnapshotStorageClassParameters, t.CSIDriver)
	if tvsc.volumeGroupSnapshotClass.Parameters == nil {
		tvsc.volumeGroupSnapshotClass.Parameters = map[string]string{}
	}
	tvsc.volumeGroupSnapshotClass.Parameters["resourceGroup"] = externalRG
	tvsc.Create(ctx)
	defer cleanup()

	ginkgo.By("taking volume group snapshots")
	volumeGroupSnapshot := tvsc.CreateVolumeGroupSnapshot(ctx, t.MatchLabels)

	if t.ShouldOverwrite {
		for idx := range t.PodsOverwrite {
			tpods[idx] = NewTestPod(client, namespace, t.PodsOverwrite[idx].Cmd, t.PodsOverwrite[idx].IsWindows, t.GroupPods[idx].WinServerVer)
			tpods[idx].SetupVolume(tpvcs[idx].persistentVolumeClaim, volumes[idx].VolumeMount.NameGenerate+"1", volumes[idx].VolumeMount.MountPathGenerate+"1", volumes[idx].VolumeMount.ReadOnly)
			tpods[idx].SetLabel(map[string]string{
				testLabelKey + strconv.Itoa(idx): testLabelValue + strconv.Itoa(idx),
			})
			ginkgo.By("deploying a new pod to overwrite pv data")
			tpods[idx].Create(ctx)
			defer tpods[idx].Cleanup(ctx)
			ginkgo.By("checking that the pod is running")
			tpods[idx].WaitForRunning(ctx)
		}
	}

	defer tvsc.DeleteVolumeGroupSnapshot(ctx, volumeGroupSnapshot)
	tvsc.ReadyToUse(ctx, volumeGroupSnapshot)

	snapshotItems, err := tvsc.ListSnapshot(ctx, volumeGroupSnapshot)
	framework.ExpectNoError(err, fmt.Sprintf("Error getting snapshots in volume group snapshot %v", err))
	if len(snapshotItems) == 0 {
		framework.Failf("No snapshots found in volume group snapshot %s", volumeGroupSnapshot.Name)
	}
	for _, snapshot := range snapshotItems {
		ginkgo.By("Matching snapshot " + snapshot.Name + " with PVCs")
		index := -1
		for i := range tpvcs {
			if tpvcs[i].persistentVolumeClaim.Name == *snapshot.Spec.Source.PersistentVolumeClaimName {
				index = i
				break
			}
		}
		if index == -1 {
			framework.Failf("No matching PVC found for snapshot %s", snapshot.Name)
		}
		ginkgo.By("Snapshot " + snapshot.Name + " matched with PVC " + tpvcs[index].persistentVolumeClaim.Name)
		snapshotVolume := volumes[index]
		snapshotVolume.DataSource = &DataSource{
			Kind: VolumeSnapshotKind,
			Name: snapshot.Name,
		}
		t.PodsWithSnapshot[index].Volumes = []VolumeDetails{snapshotVolume}
		tPodWithSnapshot, tPodWithSnapshotCleanup := t.PodsWithSnapshot[index].SetupWithDynamicVolumes(ctx, client, namespace, t.CSIDriver, t.StorageClassParameters)
		for i := range tPodWithSnapshotCleanup {
			defer tPodWithSnapshotCleanup[i](ctx)
		}

		if t.ShouldOverwrite && !t.IsWindowsHPCDeployment {
			// 	TODO: add test case which will schedule the original disk and the copied disk on the same node once the conflicting UUID issue is fixed.
			ginkgo.By("Set pod anti-affinity to make sure two pods are scheduled on different nodes")
			testPodAntiAffinity := v1.Affinity{
				PodAntiAffinity: &v1.PodAntiAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: []v1.PodAffinityTerm{
						{
							LabelSelector: &metav1.LabelSelector{MatchLabels: map[string]string{
								testLabelKey + strconv.Itoa(index): testLabelValue + strconv.Itoa(index),
							}},
							TopologyKey: HostNameLabel,
						}},
				},
			}
			tPodWithSnapshot.SetAffinity(&testPodAntiAffinity)
		}

		ginkgo.By("deploying a pod with a volume restored from the snapshot")
		tPodWithSnapshot.Create(ctx)
		defer tPodWithSnapshot.Cleanup(ctx)
		ginkgo.By("checking that the pod's command exits with no error")
		tPodWithSnapshot.WaitForRunning(ctx)
		if t.PodCheck != nil {
			ginkgo.By("check pod exec")
			tPodWithSnapshot.PollForStringInPodsExec(t.PodCheck[index].Cmd, t.PodCheck[index].ExpectedString)
		}
	}

}
