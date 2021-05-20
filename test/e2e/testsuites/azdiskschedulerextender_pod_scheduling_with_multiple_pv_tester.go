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
	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	v1alpha1ClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/typed/azuredisk/v1alpha1"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
)

// AzDiskSchedulerExtenderPodSchedulingWithMultiplePVTest will provision required PV(s), PVC(s) and Pod(s)
// Pod with multiple PVs should successfully be scheduled in a cluster with AzDriverNode and AzVolumeAttachment resources
type AzDiskSchedulerExtenderPodSchedulingWithMultiplePVTest struct {
	CSIDriver       driver.DynamicPVTestDriver
	AzDiskClientSet v1alpha1ClientSet.DiskV1alpha1Interface
	AzNamespace     string
	Pod             PodDetails
}

func (t *AzDiskSchedulerExtenderPodSchedulingWithMultiplePVTest) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	tpod, cleanup := t.Pod.SetupWithDynamicMultipleVolumes(client, namespace, t.CSIDriver, schedulerName)
	// defer must be called here for resources not get removed before using them
	for i := range cleanup {
		i := i
		defer cleanup[i]()
	}

	// get the list of available nodes for scheduling the pod
	nodeNames := ListNodeNames(client)
	if len(nodeNames) < 1 {
		ginkgo.Skip("need at least 1 nodes to verify the test case. Current node count is %d", len(nodeNames))
	}

	ginkgo.By("deploying the pod")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod's command exits with no error")
	tpod.WaitForSuccess()
}
