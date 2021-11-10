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
	"fmt"
	"sync"
	"time"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	v1alpha1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
)

// AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV will provision required PV(s), PVC(s) and Pod(s)
// Pod should successfully be re-scheduled on failover/scaling in a cluster with AzDriverNode and AzVolumeAttachment resources
type AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    PodDetails
	Replicas               int
	StorageClassParameters map[string]string
	AzDiskClient           *azDiskClientSet.Clientset
}

func (t *AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	var tStatefulSets []*TestStatefulset
	var wg sync.WaitGroup
	var tokens = make(chan struct{}, 20) // avoid too many concurrent requests
	tStorageClass, storageCleanup := t.Pod.CreateStorageClass(client, namespace, t.CSIDriver, t.StorageClassParameters)
	defer storageCleanup()
	for i := 0; i < 2; i++ {
		tStatefulSet, cleanupStatefulSet := t.Pod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName, t.Replicas, t.StorageClassParameters, &tStorageClass)
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
			tStatefulSet.allPods = ss.allPods
			<-tokens // release the token
		}(tStatefulSet)
	}
	wg.Wait()

	// Get the list of available nodes for scheduling the pod
	nodes := ListNodeNames(client)
	if len(nodes) < 2 {
		ginkgo.Skip("need at least 2 nodes to verify the test case. Current node count is %d", len(nodes))
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

	//Check that AzVolumeAttachment resources were created correctly
	allReplicasAttached := true
	var failedReplicaAttachments []*v1alpha1.AzVolumeAttachmentList
	err := wait.Poll(15*time.Second, 10*time.Minute, func() (bool, error) {
		failedReplicaAttachments = nil
		var err error
		var attached bool
		var podFailedReplicaAttachments *v1alpha1.AzVolumeAttachmentList
		for _, ss := range tStatefulSets {
			for _, pod := range ss.allPods {
				attached, podFailedReplicaAttachments, err = VerifySuccessfulReplicaAzVolumeAttachments(pod, t.AzDiskClient, t.StorageClassParameters, client, namespace)
				allReplicasAttached = allReplicasAttached && attached
				if podFailedReplicaAttachments != nil {
					failedReplicaAttachments = append(failedReplicaAttachments, podFailedReplicaAttachments)
				}
			}
		}
		return allReplicasAttached, err
	})
	if len(failedReplicaAttachments) > 0 {
		e2elog.Logf("found %d azvolumeattachments failed:", len(failedReplicaAttachments))
		for _, podAttachments := range failedReplicaAttachments {
			for _, attachments := range podAttachments.Items {
				e2elog.Logf("azvolumeattachment: %s, err: %s", attachments.Name, attachments.Status.Error.ErrorMessage)
			}
		}
		ginkgo.Fail("failed due to replicas failing to attach")
	} else if !allReplicasAttached {
		ginkgo.Fail("could not find correct number of replicas")
	} else if err != nil {
		ginkgo.Fail(fmt.Sprintf("failed to verify replica attachments, err: %s", err))

	}
	ginkgo.By("replica attachments verified successfully")

}
