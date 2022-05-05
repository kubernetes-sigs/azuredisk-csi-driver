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
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	diskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
	podutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/pod"
)

//  will provision required PV(s), PVC(s) and Pod(s)
// Pod should successfully be re-scheduled on failover in a cluster with AzDriverNode and AzVolumeAttachment resources
type PodFailoverWithReplicas struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    resources.PodDetails
	Volume                 resources.VolumeDetails
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

	//Cordon nodes except for one worker node
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
	allAttached := true
	var failedAttachments []diskv1beta1.AzVolumeAttachment
	var allAttachments []diskv1beta1.AzVolumeAttachment

	err := wait.Poll(15*time.Second, 10*time.Minute, func() (bool, error) {
		failedAttachments = []diskv1beta1.AzVolumeAttachment{}
		allAttachments = []diskv1beta1.AzVolumeAttachment{}
		allAttached = true
		var err error

		for _, pod := range tDeployment.Pods {
			attached, podAllAttachments, podFailedAttachments, derr := resources.VerifySuccessfulAzVolumeAttachments(pod, t.AzDiskClient, t.StorageClassParameters, client, namespace)
			allAttached = allAttached && attached
			if podFailedAttachments != nil {
				failedAttachments = append(failedAttachments, podFailedAttachments...)
			}
			if podAllAttachments != nil {
				allAttachments = append(allAttachments, podAllAttachments...)
			}
			err = derr
		}

		return allAttached, err
	})

	if len(failedAttachments) > 0 {
		e2elog.Logf("found %d azvolumeattachments failed:", len(failedAttachments))
		for _, attachments := range failedAttachments {
			e2elog.Logf("azvolumeattachment: %s, err: %s", attachments.Name, attachments.Status.Error.Message)
		}
		ginkgo.Fail("failed due to replicas failing to attach")
	} else if !allAttached {
		ginkgo.Fail("could not find correct number of replicas")
	} else if err != nil {
		ginkgo.Fail(fmt.Sprintf("failed to verify replica attachments, err: %s", err))

	}
	ginkgo.By("attachments verified successfully")

	var primaryNode string
	replicaNodeSet := map[string]struct{}{}
	for _, attachment := range allAttachments {
		if attachment.Spec.RequestedRole == diskv1beta1.PrimaryRole {
			primaryNode = attachment.Spec.NodeName
		} else {
			replicaNodeSet[attachment.Spec.NodeName] = struct{}{}
		}
	}

	ginkgo.By(fmt.Sprintf("cordoning node (%s) with primary attachment", primaryNode))

	testPod := resources.TestPod{
		Client: client,
	}

	// Make primary node unschedulable to ensure that pods are scheduled on a different node
	testPod.SetNodeUnschedulable(primaryNode, true)        // kubeclt cordon node
	defer testPod.SetNodeUnschedulable(primaryNode, false) // defer kubeclt uncordon node

	ginkgo.By("deleting the pod for deployment")
	time.Sleep(10 * time.Second)
	tDeployment.DeletePodAndWait()

	ginkgo.By("checking again that the pod is running")
	tDeployment.WaitForPodReady()

	if t.PodCheck != nil {
		ginkgo.By("sleep 3s and then check pod exec")
		time.Sleep(3 * time.Second)
		// pod will be restarted so expect to see 2 instances of string
		tDeployment.Exec(t.PodCheck.Cmd, t.PodCheck.ExpectedString+t.PodCheck.ExpectedString)
	}

	ginkgo.By("Verifying that pod got created on the correct node.")
	// verfiy that the pod failed over the replica node
	podObjs, err := podutil.GetPodsForDeployment(tDeployment.Client, tDeployment.Deployment)
	framework.ExpectNoError(err)
	for _, podObj := range podObjs.Items {
		_, isOnCorrectNode := replicaNodeSet[podObj.Spec.NodeName]
		framework.ExpectEqual(isOnCorrectNode, true)
	}
}
