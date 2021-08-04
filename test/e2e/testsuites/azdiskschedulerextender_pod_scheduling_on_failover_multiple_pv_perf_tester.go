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
	"sync"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
)

// AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV will provision required PV(s), PVC(s) and Pod(s)
// Pod should successfully be re-scheduled on failover/scaling in a cluster with AzDriverNode and AzVolumeAttachment resources
type AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    PodDetails
	Replicas               int
	StorageClassParameters map[string]string
}

func (t *AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	var tStatefulSets []*TestStatefulset
	var wg sync.WaitGroup
	var tokens = make(chan struct{}, 20) // avoid too many concurrent requests
	for i := 0; i < 2; i++ {
		tStatefulSet, cleanupStatefulSet := t.Pod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName, t.Replicas, t.StorageClassParameters)
		tStatefulSets = append(tStatefulSets, tStatefulSet)
		for i := range cleanupStatefulSet {
			i := i
			defer cleanupStatefulSet[i]()
		}
		wg.Add(1)
		go func(ss *TestStatefulset) {
			defer wg.Done()
			defer ginkgo.GinkgoRecover()
			tokens <- struct{}{} // acquire a token
			ss.Create()
			<-tokens // release the token
		}(tStatefulSet)
	}
	wg.Wait()

	// Get the list of available nodes for scheduling the pod
	nodes := ListNodeNames(client)
	if len(nodes) < 1 {
		ginkgo.Skip("need at least 1 nodes to verify the test case. Current node count is %d", len(nodes))
	}

	for i := 0; i < 3; i++ {
		DeleteAllPodsWithMatchingLabel(client, namespace, map[string]string{"app": "azuredisk-volume-tester"})
		for _, tStatefulSet := range tStatefulSets {
			wg.Add(1)
			go func(ss *TestStatefulset) {
				defer wg.Done()
				defer ginkgo.GinkgoRecover()
				ss.WaitForPodReady()
			}(tStatefulSet)
		}
		wg.Wait()
	}
}
