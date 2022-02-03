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
	diskv1alpha2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha2"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	testtypes "sigs.k8s.io/azuredisk-csi-driver/test/types"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
)

//  will provision required PV(s), PVC(s) and Pod(s)
// Primary AzVolumeAttachment and Replica AzVolumeAttachments should be created on set of nodes with matching label
type PodNodeScaleUp struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    testtypes.PodDetails
	Volume                 testtypes.VolumeDetails
	IsMultiZone            bool
	AzDiskClient           *azDiskClientSet.Clientset
	StorageClassParameters map[string]string
	PodCheck               *PodExecCheck
}

func (t *PodNodeScaleUp) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {

	// Get the list of available nodes for scheduling the pod
	nodes := nodeutil.ListNodeNames(client)
	if len(nodes) < 2 {
		ginkgo.Skip("need at least 2 nodes to verify the test case. Current node count is %d", len(nodes))
	}

	//Cordon a node
	testPod := testtypes.TestPod{
		Client: client,
	}
	testPod.SetNodeUnschedulable(nodes[0], true)
	defer testPod.SetNodeUnschedulable(nodes[0], false)

	tpod, cleanup := t.Pod.SetupWithDynamicVolumes(client, namespace, t.CSIDriver, t.StorageClassParameters, schedulerName)
	// defer must be called here for resources not get removed before using them
	for i := range cleanup {
		defer cleanup[i]()
	}

	ginkgo.By("deploying the pod")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod is running")
	tpod.WaitForRunning()
	t.Pod.Name = tpod.Pod.Name

	//Check that AzVolumeAttachment resources were created correctly
	allReplicasAttached := true
	var failedReplicaAttachments *diskv1alpha2.AzVolumeAttachmentList
	err := wait.Poll(15*time.Second, 10*time.Minute, func() (bool, error) {
		var err error
		allReplicasAttached, failedReplicaAttachments, err = testtypes.VerifySuccessfulReplicaAzVolumeAttachments(t.Pod, t.AzDiskClient, t.StorageClassParameters, client, namespace)
		return allReplicasAttached, err
	})

	if failedReplicaAttachments != nil {
		e2elog.Logf("found %d azvolumeattachments failed:", len(failedReplicaAttachments.Items))
		for _, attachments := range failedReplicaAttachments.Items {
			e2elog.Logf("azvolumeattachment: %s, err: %s", attachments.Name, attachments.Status.Error.Message)
		}
		ginkgo.Fail("failed due to replicas failing to attach")
	} else if !allReplicasAttached {
		ginkgo.Fail("could not find correct number of replicas")
	} else if err != nil {
		ginkgo.Fail(fmt.Sprintf("failed to verify replica attachments, err: %s", err))

	}
	ginkgo.By("replica attachments verified successfully")

	ginkgo.By("uncordoning node 0")
	testPod.SetNodeUnschedulable(nodes[0], false)

	//Check that AzVolumeAttachment resources were created correctly
	allReplicasAttached = true
	failedReplicaAttachments = nil
	err = wait.Poll(15*time.Second, 10*time.Minute, func() (bool, error) {
		var err error
		allReplicasAttached, failedReplicaAttachments, err = testtypes.VerifySuccessfulReplicaAzVolumeAttachments(t.Pod, t.AzDiskClient, t.StorageClassParameters, client, namespace)
		return allReplicasAttached, err
	})

	if failedReplicaAttachments != nil {
		e2elog.Logf("found %d azvolumeattachments failed:", len(failedReplicaAttachments.Items))
		for _, attachments := range failedReplicaAttachments.Items {
			e2elog.Logf("azvolumeattachment: %s, err: %s", attachments.Name, attachments.Status.Error.Message)
		}
		ginkgo.Fail("failed due to replicas failing to attach")
	} else if !allReplicasAttached {
		ginkgo.Fail("could not find correct number of replicas")
	} else if err != nil {
		ginkgo.Fail(fmt.Sprintf("failed to verify replica attachments, err: %s", err))

	}
	ginkgo.By("scaled up replica attachments verified successfully")

}
