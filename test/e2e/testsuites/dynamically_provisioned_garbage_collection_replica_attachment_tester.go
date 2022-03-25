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
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
)

// DynamicallyProvisionedScaleReplicasOnDetach will provision required PV(s), PVC(s) and Pod(s), enough to fill the available
// spots for attachments on all the nodes. Another PVC will be added that will have to wait for a opening on a node to connect

type DynamicallyProvisionedScaleReplicasOnDetach struct {
	CSIDriver              driver.DynamicPVTestDriver
	StatefulSetPod         resources.PodDetails
	StorageClassParameters map[string]string
	AzDiskClient           *azDiskClientSet.Clientset
	NewPod                 resources.PodDetails
}

func (t *DynamicallyProvisionedScaleReplicasOnDetach) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {

	nodes, err := client.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{FieldSelector: fields.Set{
		"spec.unschedulable": "false",
	}.AsSelector().String()})
	framework.ExpectNoError(err)

	if len(nodes.Items) < 3 {
		ginkgo.Skip("need at least 3 schedulable nodes to verify the test case. Current node count is %d", len(nodes.Items))
	}

	testPod := resources.TestPod{
		Client: client,
	}
	csiNode, err := client.StorageV1().CSINodes().Get(context.Background(), nodes.Items[0].Name, metav1.GetOptions{})
	if err != nil {
		ginkgo.Fail("failed cannot get CSINode")
	}
	var numReplicas int
	for _, driver := range csiNode.Spec.Drivers {
		if driver.Name == consts.DefaultDriverName {
			numReplicas = int(*driver.Allocatable.Count)
		}
	}

	for i, node := range nodes.Items {
		if i > 1 {
			testPod.SetNodeUnschedulable(node.Name, true)
		}
	}

	tStorageClass, storageCleanup := t.StatefulSetPod.CreateStorageClass(client, namespace, t.CSIDriver, t.StorageClassParameters)
	defer storageCleanup()
	tStatefulSet, cleanupStatefulSet := t.StatefulSetPod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName, numReplicas, &tStorageClass, map[string]string{"group": "delete-for-replica-scaleup"})
	tStatefulSet.Create()

	//Check that AzVolumeAttachment resources were created correctly
	allReplicasAttached := true
	failedReplicaAttachments := &v1beta1.AzVolumeAttachmentList{}
	err = wait.Poll(15*time.Second, 10*time.Minute, func() (bool, error) {
		allReplicasAttached = true
		var err error
		var attached bool
		var podFailedReplicaAttachments *v1beta1.AzVolumeAttachmentList
		for _, pod := range tStatefulSet.AllPods {
			attached, podFailedReplicaAttachments, err = resources.VerifySuccessfulReplicaAzVolumeAttachments(pod, t.AzDiskClient, t.StorageClassParameters, client, namespace)
			allReplicasAttached = allReplicasAttached && attached
			if podFailedReplicaAttachments != nil {
				failedReplicaAttachments.Items = append(failedReplicaAttachments.Items, podFailedReplicaAttachments.Items...)
			}
		}
		return allReplicasAttached, err
	})
	if len(failedReplicaAttachments.Items) > 0 {
		e2elog.Logf("found %d azvolumeattachments failed:", len(failedReplicaAttachments.Items))
		for _, podAttachments := range failedReplicaAttachments.Items {
			e2elog.Logf("azvolumeattachment: %s, err: %s", podAttachments.Name, podAttachments.Status.Error.Message)
		}
		ginkgo.Fail("failed due to replicas failing to attach")
	} else if !allReplicasAttached {
		ginkgo.Fail("could not find correct number of replicas")
	} else if err != nil {
		ginkgo.Fail(fmt.Sprintf("failed to verify replica attachments, err: %s", err))

	}
	ginkgo.By("stateful set replica attachments verified successfully")
	ginkgo.By("create pod with volume")

	//uncordon a node for the primary attachment
	testPod.SetNodeUnschedulable(nodes.Items[2].Name, false)

	tpod, cleanup := t.NewPod.SetupWithDynamicVolumes(client, namespace, t.CSIDriver, t.StorageClassParameters, schedulerName)
	for i := range cleanup {
		defer cleanup[i]()
	}

	ginkgo.By("deploying the pod")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod is running")
	tpod.WaitForRunning()
	t.NewPod.Name = tpod.Pod.Name

	ginkgo.By("verifying that new pod was not able to create any replica attachments")

	allReplicasAttached = true
	err = wait.Poll(15*time.Second, time.Minute, func() (bool, error) {
		var err error
		allReplicasAttached, failedReplicaAttachments, err = resources.VerifySuccessfulReplicaAzVolumeAttachments(t.NewPod, t.AzDiskClient, t.StorageClassParameters, client, namespace)
		return allReplicasAttached, err
	})
	if err == nil {
		ginkgo.Fail("Expected failed replicas, but all replicas were created")
	}

	cleanupStatefulSet(15 * time.Minute)

	allReplicasAttached = true
	err = wait.Poll(15*time.Second, 10*time.Minute, func() (bool, error) {
		var err error
		allReplicasAttached, failedReplicaAttachments, err = resources.VerifySuccessfulReplicaAzVolumeAttachments(t.NewPod, t.AzDiskClient, t.StorageClassParameters, client, namespace)
		return allReplicasAttached, err
	})
	if err != nil {
		ginkgo.Fail(fmt.Sprintf("Expected successful replicas for new pod, failed replicas:%v ", failedReplicaAttachments))
	}
}
