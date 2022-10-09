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
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
)

// Will provision required PV(s), PVC(s) and Pod(s)
//
// Primary AzVolumeAttachment and Replica AzVolumeAttachments should be created on set of nodes with matching label
type PodNodeScaleUp struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    resources.PodDetails
	Volume                 resources.VolumeDetails
	IsMultiZone            bool
	AzDiskClient           *azdisk.Clientset
	StorageClassParameters map[string]string
	PodCheck               *PodExecCheck
}

func (t *PodNodeScaleUp) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	ctx := context.Background()
	// Get the list of available nodes for scheduling the pod
	nodes := nodeutil.ListAgentNodeNames(client, t.Pod.IsWindows)
	if len(nodes) < 2 {
		ginkgo.Skip("need at least 2 agent nodes to verify the test case. Current agent node count is %d", len(nodes))
	}

	cordoned := []string{}
	var selectedNode string
	for _, node := range nodes {
		nodeObj, err := client.CoreV1().Nodes().Get(ctx, node, metav1.GetOptions{})
		framework.ExpectNoError(err)
		// skip tainted nodes
		if len(nodeObj.Spec.Taints) > 0 {
			continue
		}
		if !nodeObj.Spec.Unschedulable {
			if selectedNode == "" {
				selectedNode = node
				continue
			}
			nodeutil.SetNodeUnschedulable(client, node, true)
			defer nodeutil.SetNodeUnschedulable(client, node, false)
			cordoned = append(cordoned, node)
		}

	}

	tpod, cleanup := t.Pod.SetupWithDynamicVolumes(client, namespace, t.CSIDriver, t.StorageClassParameters, schedulerName)
	// defer must be called here for resources not get removed before using them
	for i := range cleanup {
		defer cleanup[i]()
	}
	tpod.Pod.Spec.NodeName = selectedNode
	ginkgo.By("deploying the pod")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod is running")
	tpod.WaitForRunning()
	t.Pod.Name = tpod.Pod.Name

	time.Sleep(10 * time.Second)
	var successfulAttachments []azdiskv1beta2.AzVolumeAttachment
	//Check that only 1 AzVolumeAttachment is created

	_, allAttachments, _, err := resources.VerifySuccessfulAzVolumeAttachments(t.Pod, t.AzDiskClient, t.StorageClassParameters, client, namespace)
	framework.ExpectNoError(err)
	for _, attachment := range allAttachments {
		if attachment.Status.Detail != nil && attachment.Status.State == azdiskv1beta2.Attached {
			successfulAttachments = append(successfulAttachments, attachment)
		}
	}

	framework.ExpectEqual(len(successfulAttachments), 1)
	ginkgo.By("Verified that no replica AzVolumeAttachment was created.")

	ginkgo.By(fmt.Sprintf("uncordoning nodes: %+v", cordoned))
	for _, node := range cordoned {
		nodeutil.SetNodeUnschedulable(client, node, false)
	}

	//Check that AzVolumeAttachment resources were created correctly
	isAttached := true
	var unattachedAttachments []azdiskv1beta2.AzVolumeAttachment
	err = wait.Poll(15*time.Second, 10*time.Minute, func() (bool, error) {
		var err error
		isAttached, _, unattachedAttachments, err = resources.VerifySuccessfulAzVolumeAttachments(t.Pod, t.AzDiskClient, t.StorageClassParameters, client, namespace)
		return isAttached, err
	})

	if unattachedAttachments != nil {
		e2elog.Logf("found %d azvolumeattachments failing to attach in time:", len(unattachedAttachments))
		for _, attachment := range unattachedAttachments {
			switch attachment.Status.State {
			case azdiskv1beta2.AttachmentFailed:
				e2elog.Logf("azvolumeattachment: %s, err: %s", attachment.Name, attachment.Status.Error.Message)
			default:
				e2elog.Logf("expected AzVolumeAttachment (%s) to be in Attached state but instead got %s", attachment.Name, attachment.Status.State)
			}
		}
		ginkgo.Fail("failed due to replicas failing to attach")
	} else if !isAttached {
		ginkgo.Fail("could not find correct number of replicas")
	} else if err != nil {
		ginkgo.Fail(fmt.Sprintf("failed to verify replica attachments, err: %s", err))

	}
	ginkgo.By("scaled up replica attachments verified successfully")

}
