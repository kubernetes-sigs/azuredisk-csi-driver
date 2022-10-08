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

	"github.com/onsi/ginkgo/v2"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
	podutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/pod"
)

// AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV will provision required PV(s), PVC(s) and Pod(s)
// Pod should successfully be re-scheduled on failover/scaling in a cluster with AzDriverNode and AzVolumeAttachment resources
type AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    resources.PodDetails
	Replicas               int
	StorageClassParameters map[string]string
	AzDiskClient           *azdisk.Clientset
}

func (t *AzDiskSchedulerExtenderPodSchedulingOnFailoverMultiplePV) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	var tStatefulSets []*resources.TestStatefulset
	var wg sync.WaitGroup
	var statefulSetCount = 2
	var tokens = make(chan struct{}, 20) // avoid too many concurrent requests
	var errorsChan = make(chan error, statefulSetCount)

	tStorageClass, storageCleanup := t.Pod.CreateStorageClass(client, namespace, t.CSIDriver, t.StorageClassParameters)
	defer storageCleanup()
	for i := 0; i < statefulSetCount; i++ {
		tStatefulSet, cleanupStatefulSet := t.Pod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName, t.Replicas, &tStorageClass, map[string]string{"group": "delete-for-failover"})
		tStatefulSets = append(tStatefulSets, tStatefulSet)
		defer cleanupStatefulSet(15 * time.Minute)

		wg.Add(1)
		go func(ss *resources.TestStatefulset) {
			defer wg.Done()
			defer ginkgo.GinkgoRecover()
			tokens <- struct{}{} // acquire a token
			ss.Create()
			tStatefulSet.AllPods = ss.AllPods
			<-tokens // release the token
		}(tStatefulSet)
	}
	wg.Wait()

	// Get the list of available nodes for scheduling the pod
	nodes := nodeutil.ListAgentNodeNames(client, t.Pod.IsWindows)
	if len(nodes) < 2 {
		ginkgo.Skip("need at least 2 agent nodes to verify the test case. Current agent node count is %d", len(nodes))
	}

	time.Sleep(10 * time.Second)
	for i := 0; i < 3; i++ {
		podutil.DeleteAllPodsWithMatchingLabel(client, namespace, map[string]string{"group": "delete-for-failover"})
		for _, tStatefulSet := range tStatefulSets {
			go func(ss *resources.TestStatefulset) {
				defer ginkgo.GinkgoRecover()
				err := ss.WaitForPodReadyOrFail()
				errorsChan <- err
			}(tStatefulSet)
		}

		for range tStatefulSets {
			err := <-errorsChan
			if err != nil {
				framework.ExpectNoError(err, "Failed waiting for StatefulSet pod failover.")
			}
		}
	}

	//Check that AzVolumeAttachment resources were created correctly
	allAttached := true
	unattachedAttachments := []azdiskv1beta2.AzVolumeAttachment{}
	err := wait.Poll(15*time.Second, 10*time.Minute, func() (bool, error) {
		allAttached = true

		unattachedAttachments = []azdiskv1beta2.AzVolumeAttachment{}
		for _, ss := range tStatefulSets {
			for _, pod := range ss.AllPods {
				attached, _, podUnattachedAttachments, err := resources.VerifySuccessfulAzVolumeAttachments(pod, t.AzDiskClient, t.StorageClassParameters, client, namespace)
				allAttached = allAttached && attached
				if podUnattachedAttachments != nil {
					unattachedAttachments = append(unattachedAttachments, podUnattachedAttachments...)
				}
				if err != nil {
					return allAttached, err
				}
			}
		}
		return allAttached, nil
	})
	if len(unattachedAttachments) > 0 {
		e2elog.Logf("found %d azvolumeattachments not attached:", len(unattachedAttachments))
		for _, podAttachment := range unattachedAttachments {
			switch podAttachment.Status.State {
			case azdiskv1beta2.AttachmentFailed:
				e2elog.Logf("azvolumeattachment: %s, err: %s", podAttachment.Name, podAttachment.Status.Error.Message)
			default:
				e2elog.Logf("expected AzVolumeAttachment (%s) to be in Attached state but instead got %s", podAttachment.Name, podAttachment.Status.State)
			}
		}
		ginkgo.Fail("failed due to replicas failing to attach in time.")
	} else if !allAttached {
		ginkgo.Fail("could not find correct number of replicas")
	} else if err != nil {
		ginkgo.Fail(fmt.Sprintf("failed to verify replica attachments, err: %s", err))
	}
	ginkgo.By("replica attachments verified successfully")
}
