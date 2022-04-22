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
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
)

// PreProvisionedMultiplePodsTest will provision required PV(s), PVC(s) and Pod(s)
// Testing that a volume could be mounted by multiple pods
type PreProvisionedMultiplePodsTest struct {
	CSIDriver     driver.PreProvisionedVolumeTestDriver
	Pods          []resources.PodDetails
	VolumeContext map[string]string
}

func (t *PreProvisionedMultiplePodsTest) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	for _, pod := range t.Pods {
		tpod, cleanup := pod.SetupWithPreProvisionedVolumes(client, namespace, t.CSIDriver, t.VolumeContext, schedulerName)
		// defer must be called here for resources not get removed before using them
		for i := range cleanup {
			defer cleanup[i]()
		}

		ginkgo.By("deploying the pod")
		tpod.Create()
		defer tpod.Cleanup()
		ginkgo.By("checking that the pod's command exits with no error")
		tpod.WaitForSuccess()
	}
}
