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
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	restclientset "k8s.io/client-go/rest"
)

// DynamicallyProvisionedVolumeSnapshotTest will provision required StorageClass(es),VolumeSnapshotClass(es), PVC(s) and Pod(s)
// Waiting for the PV provisioner to create a new PV
// Testing if the Pod(s) can write and read to mounted volumes
// Create a snapshot, validate the data is still on the disk, and then write and read to it again
// And finally delete the snapshot
// This test only supports a single volume
type DynamicallyProvisionedVolumeSnapshotTest struct {
	CSIDriver       driver.PVTestDriver
	Pod             PodDetails
	PodWithSnapshot PodDetails
}

func (t *DynamicallyProvisionedVolumeSnapshotTest) Run(client clientset.Interface, restclient restclientset.Interface, namespace *v1.Namespace) {
	tpod := NewTestPod(client, namespace, t.Pod.Cmd, t.Pod.IsWindows)
	volume := t.Pod.Volumes[0]
	tpvc, pvcCleanup := volume.SetupDynamicPersistentVolumeClaim(client, namespace, t.CSIDriver)
	for i := range pvcCleanup {
		defer pvcCleanup[i]()
	}
	tpod.SetupVolume(tpvc.persistentVolumeClaim, volume.VolumeMount.NameGenerate+"1", volume.VolumeMount.MountPathGenerate+"1", volume.VolumeMount.ReadOnly)

	ginkgo.By("deploying the pod")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod's command exits with no error")
	tpod.WaitForSuccess()

	ginkgo.By("creating volume snapshot class")
	tvsc, cleanup := CreateVolumeSnapshotClass(restclient, namespace, t.CSIDriver)
	defer cleanup()

	ginkgo.By("taking snapshots")
	snapshot := tvsc.CreateSnapshot(tpvc.persistentVolumeClaim)
	defer tvsc.DeleteSnapshot(snapshot)
	tvsc.ReadyToUse(snapshot)

	snapshotVolume := volume
	snapshotVolume.DataSource = &DataSource{
		Kind: VolumeSnapshotKind,
		Name: snapshot.Name,
	}
	t.PodWithSnapshot.Volumes = []VolumeDetails{snapshotVolume}
	tPodWithSnapshot, tPodWithSnapshotCleanup := t.PodWithSnapshot.SetupWithDynamicVolumes(client, namespace, t.CSIDriver)
	for i := range tPodWithSnapshotCleanup {
		defer tPodWithSnapshotCleanup[i]()
	}
	ginkgo.By("deploying a second pod with a volume restored from the snapshot")
	tPodWithSnapshot.Create()
	defer tPodWithSnapshot.Cleanup()
	ginkgo.By("checking that the pod's command exits with no error")
	tPodWithSnapshot.WaitForSuccess()
}
