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
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	clientset "k8s.io/client-go/kubernetes"
)

// DynamicallyProvisionedVolumeCloningTest will provision required StorageClass(es), PVC(s) and Pod(s)
type DynamicallyProvisionedVolumeCloningTest struct {
	CSIDriver driver.DynamicPVTestDriver
	Pod       PodDetails
	Volume    VolumeDetails
}

func (t *DynamicallyProvisionedVolumeCloningTest) Run(client clientset.Interface, namespace *v1.Namespace) {
	// create the storageClass
	tsc, tscCleanup := t.Pod.Volumes[0].CreateStorageClass(client, namespace, t.CSIDriver)
	defer tscCleanup()

	// create the pod
	t.Pod.Volumes[0].StorageClass = tsc.storageClass
	tpod, cleanups := t.Pod.SetupWithDynamicVolumes(client, namespace, t.CSIDriver)
	for i := range cleanups {
		defer cleanups[i]()
	}
	if t.Pod.Volumes[0].VolumeBindingMode != nil && *t.Pod.Volumes[0].VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer {
		ginkgo.By("deploying the pod")
		tpod.Create()
		defer tpod.Cleanup()
		tpod.WaitForRunning()
	}

	ginkgo.By("creating the pvc from an existing pvc")
	t.Volume.DataSource.Name = tpod.pod.Spec.Volumes[0].VolumeSource.PersistentVolumeClaim.ClaimName
	t.Volume.StorageClass = tsc.storageClass
	tpvc, cleanups := t.Volume.SetupDynamicPersistentVolumeClaim(client, namespace, t.CSIDriver)
	for i := range cleanups {
		defer cleanups[i]()
	}

	ginkgo.By("validating the cloned volume")
	if t.Volume.VolumeBindingMode != nil && *t.Volume.VolumeBindingMode == storagev1.VolumeBindingWaitForFirstConsumer {
		podWithClonedVolume := NewTestPod(client, namespace, t.Pod.Cmd)
		podWithClonedVolume.SetupVolume(tpvc.persistentVolumeClaim, t.Pod.Volumes[0].VolumeMount.NameGenerate+"0", t.Pod.Volumes[0].VolumeMount.MountPathGenerate+"0", t.Volume.VolumeMount.ReadOnly)
		ginkgo.By("deploying the pod with cloned volume")
		podWithClonedVolume.Create()
		defer podWithClonedVolume.Cleanup()
		tpvc.WaitForBound()
	}
	tpvc.ValidateProvisionedPersistentVolume()
}
