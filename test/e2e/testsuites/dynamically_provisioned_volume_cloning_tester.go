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
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
)

// DynamicallyProvisionedVolumeCloningTest will provision required StorageClass(es), PVC(s) and Pod(s)
// ClonedVolumeSize optional for when testing for cloned volume with different size to the original volume
type DynamicallyProvisionedVolumeCloningTest struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    resources.PodDetails
	PodWithClonedVolume    resources.PodDetails
	ClonedVolumeSize       string
	StorageClassParameters map[string]string
}

func (t *DynamicallyProvisionedVolumeCloningTest) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	// create the storageClass
	tsc, tscCleanup := t.Pod.Volumes[0].CreateStorageClass(client, namespace, t.CSIDriver, t.StorageClassParameters)
	defer tscCleanup()

	// create the pod
	t.Pod.Volumes[0].StorageClass = tsc.StorageClass
	tpod, cleanups := t.Pod.SetupWithDynamicVolumes(client, namespace, t.CSIDriver, t.StorageClassParameters, schedulerName)
	for i := range cleanups {
		defer cleanups[i]()
	}

	ginkgo.By("deploying the pod")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod's command exits with no error")
	tpod.WaitForSuccess()

	ginkgo.By("cloning existing volume")
	clonedVolume := t.Pod.Volumes[0]
	clonedVolume.DataSource = &resources.DataSource{
		Name: tpod.Pod.Spec.Volumes[0].VolumeSource.PersistentVolumeClaim.ClaimName,
		Kind: testconsts.VolumePVCKind,
	}
	clonedVolume.StorageClass = tsc.StorageClass

	if t.ClonedVolumeSize != "" {
		clonedVolume.ClaimSize = t.ClonedVolumeSize
	}

	zone := tpod.GetZoneForVolume(0)

	t.PodWithClonedVolume.Volumes = []resources.VolumeDetails{clonedVolume}
	tpod, cleanups = t.PodWithClonedVolume.SetupWithDynamicVolumes(client, namespace, t.CSIDriver, t.StorageClassParameters, schedulerName)
	for i := range cleanups {
		defer cleanups[i]()
	}

	// Since an LRS disk cannot be cloned to a different zone, add a selector to the pod so
	// that it is created in the same zone as the source disk.
	if len(zone) != 0 {
		tpod.SetNodeSelector(map[string]string{testconsts.TopologyKey: zone})
	}

	ginkgo.By("deploying a second pod with cloned volume")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod's command exits with no error")
	tpod.WaitForSuccess()
}
