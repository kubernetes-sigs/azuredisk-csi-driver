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
	"time"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	v1alpha1ClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/typed/azuredisk/v1alpha1"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
)

// AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV will provision required PV(s), PVC(s) and Pod(s)
// Pod should successfully be re-scheduled on failover/scaling in a cluster with AzDriverNode and AzVolumeAttachment resources
type AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV struct {
	CSIDriver              driver.DynamicPVTestDriver
	AzDiskClientSet        v1alpha1ClientSet.DiskV1alpha1Interface
	AzNamespace            string
	Pod                    PodDetails
	Volume                 VolumeDetails
	StorageClassParameters map[string]string
}

func (t *AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	replicaCount := 3
	tStatefulSet, cleanup := t.Pod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName, replicaCount)
	for i := range cleanup {
		i := i
		defer cleanup[i]()
	}

	// Get the list of available nodes for scheduling the pod
	nodes := ListNodeNames(client)
	if len(nodes) < 1 {
		ginkgo.Skip("need at least 1 nodes to verify the test case. Current node count is %d", len(nodes))
	}

	ginkgo.By("deploying the statefulset")
	tStatefulSet.Create()

	ginkgo.By("checking that the pod for statefulset is running")
	tStatefulSet.WaitForPodReady()

	tStatefulSet.DeletePodAndWait()

	ginkgo.By("sleep 120s waiting for statefulset to recreate the replicas")
	time.Sleep(120 * time.Second)

	ginkgo.By("checking that the pod for statefulset is running")
	tStatefulSet.WaitForPodReady()
}
