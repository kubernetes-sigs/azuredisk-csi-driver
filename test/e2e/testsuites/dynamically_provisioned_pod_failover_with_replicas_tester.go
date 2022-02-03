/*
Copyright 2021 The Kubernetes Authors.

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
	"time"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	v1alpha1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	testtypes "sigs.k8s.io/azuredisk-csi-driver/test/types"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
)

//  will provision required PV(s), PVC(s) and Pod(s)
// Pod should successfully be re-scheduled on failover in a cluster with AzDriverNode and AzVolumeAttachment resources
type PodFailoverWithReplicas struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    testtypes.PodDetails
	Volume                 testtypes.VolumeDetails
	PodCheck               *PodExecCheck
	StorageClassParameters map[string]string
	AzDiskClient           *azDiskClientSet.Clientset
	IsMultiZone            bool
}

func (t *PodFailoverWithReplicas) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	tDeployment, cleanup := t.Pod.SetupDeployment(client, namespace, t.CSIDriver, schedulerName, t.StorageClassParameters)

	// defer must be called here so resources don't get removed before using them
	for i := range cleanup {
		defer cleanup[i]()
	}

	// Get the list of available nodes for scheduling the pod
	nodes := nodeutil.ListNodeNames(client)
	numRequiredNodes := 2
	if t.IsMultiZone {
		numRequiredNodes = 4
	}
	if len(nodes) < numRequiredNodes {
		ginkgo.Skip("need at least %d nodes to verify the test case. Current node count is %d", numRequiredNodes, len(nodes))
	}

	ginkgo.By("deploying the deployment")
	tDeployment.Create()

	ginkgo.By("checking that the pod is running")
	tDeployment.WaitForPodReady()

	if t.PodCheck != nil {
		ginkgo.By("sleep 3s and then check pod exec")
		time.Sleep(3 * time.Second)
		tDeployment.Exec(t.PodCheck.Cmd, t.PodCheck.ExpectedString)
	}

	//Check that AzVolumeAttachment resources were created correctly
	allReplicasAttached := true
	var failedReplicaAttachments *v1alpha1.AzVolumeAttachmentList

	err := wait.Poll(15*time.Second, 10*time.Minute, func() (bool, error) {
		failedReplicaAttachments = nil
		allReplicasAttached = true
		var err error
		var attached bool
		var podFailedReplicaAttachments *v1alpha1.AzVolumeAttachmentList
		for _, pod := range tDeployment.Pods {
			attached, podFailedReplicaAttachments, err = testtypes.VerifySuccessfulReplicaAzVolumeAttachments(pod, t.AzDiskClient, t.StorageClassParameters, client, namespace)
			allReplicasAttached = allReplicasAttached && attached
			if podFailedReplicaAttachments != nil {
				failedReplicaAttachments.Items = append(failedReplicaAttachments.Items, podFailedReplicaAttachments.Items...)
			}
		}

		return allReplicasAttached, err
	})

	if failedReplicaAttachments != nil {
		e2elog.Logf("found %d azvolumeattachments failed:", len(failedReplicaAttachments.Items))
		for _, attachments := range failedReplicaAttachments.Items {
			e2elog.Logf("azvolumeattachment: %s, err: %s", attachments.Name, attachments.Status.Error.ErrorMessage)
		}
		ginkgo.Fail("failed due to replicas failing to attach")
	} else if !allReplicasAttached {
		ginkgo.Fail("could not find correct number of replicas")
	} else if err != nil {
		ginkgo.Fail(fmt.Sprintf("failed to verify replica attachments, err: %s", err))

	}
	ginkgo.By("replica attachments verified successfully")

	ginkgo.By("cordoning node 0")

	testPod := testtypes.TestPod{
		Client: client,
	}

	// Make node#0 unschedulable to ensure that pods are scheduled on a different node
	testPod.SetNodeUnschedulable(nodes[0], true)        // kubeclt cordon node
	defer testPod.SetNodeUnschedulable(nodes[0], false) // defer kubeclt uncordon node

	ginkgo.By("deleting the pod for deployment")
	tDeployment.DeletePodAndWait()

	ginkgo.By("checking again that the pod is running")
	tDeployment.WaitForPodReady()

	if t.PodCheck != nil {
		ginkgo.By("sleep 3s and then check pod exec")
		time.Sleep(3 * time.Second)
		// pod will be restarted so expect to see 2 instances of string
		tDeployment.Exec(t.PodCheck.Cmd, t.PodCheck.ExpectedString+t.PodCheck.ExpectedString)
	}
}
