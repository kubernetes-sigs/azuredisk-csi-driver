/*
Copyright 2022 The Kubernetes Authors.

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

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/skipper"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
)

// DynamicallyProvisionedScaleReplicasOnDetach will provision required PV(s), PVC(s) and Pod(s), enough to fill the available
// spots for attachments on all the nodes. Another PVC will be added that will have to wait for a opening on a node to connect
type DynamicallyProvisionedScaleReplicasOnDetach struct {
	CSIDriver              driver.DynamicPVTestDriver
	StatefulSetPod         resources.PodDetails
	StorageClassParameters map[string]string
	AzDiskClient           *azdisk.Clientset
	NewPod                 resources.PodDetails
}

func (t *DynamicallyProvisionedScaleReplicasOnDetach) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	nodes := nodeutil.ListAgentNodeNames(client, t.StatefulSetPod.IsWindows)
	skipper.SkipUnlessAtLeast(len(nodes), 3, "Need at least 3 agent nodes to verify the test case.")

	testPod := resources.TestPod{
		Client: client,
	}
	csiNode, err := client.StorageV1().CSINodes().Get(context.Background(), nodes[0], metav1.GetOptions{})
	framework.ExpectNoError(err, "Expected to successfully get the CSI node using the API.")

	numReplicas, ok := getNumReplicasFromCSINode(csiNode)
	framework.ExpectEqual(ok, true, "Expected to find %s.", consts.DefaultDriverName)
	framework.Logf("numReplicas is %d", numReplicas)

	// Cordoning all but two nodes
	for _, node := range nodes[2:] {
		node := node
		framework.Logf("Cordoning %s.", node)
		testPod.SetNodeUnschedulable(node, true)
		defer func() {
			framework.Logf("Uncordoning %s", node)
			testPod.SetNodeUnschedulable(node, false)
		}()
	}

	tStorageClass, storageCleanup := t.StatefulSetPod.CreateStorageClass(client, namespace, t.CSIDriver, t.StorageClassParameters)
	defer storageCleanup()

	labels := map[string]string{"group": "delete-for-replica-scaleup"}
	tStatefulSet, cleanupStatefulSet := t.StatefulSetPod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName, numReplicas, &tStorageClass, labels)
	isStatefulSetCleanupDone := false

	// Wrap the stateful set cleanup; we want to call this at a specific time but also call it in case of failure.
	wrappedCleanup := func() {
		if !isStatefulSetCleanupDone {
			cleanupStatefulSet(testconsts.PollTimeout)
			isStatefulSetCleanupDone = true
		}
	}
	defer wrappedCleanup()

	tStatefulSet.Create()
	// Check that AzVolumeAttachment resources were created correctly
	ginkgo.By("Verifying that the stateful set attachments are successful.")
	verifyStatefulSetAttachments := func() (bool, []azdiskv1beta2.AzVolumeAttachment, error) {
		return t.getStatefulSetAttachmentStatus(client, namespace, tStatefulSet)
	}
	unattachedAttachments, err := t.pollForFailedAttachments(verifyStatefulSetAttachments)
	framework.ExpectEmpty(unattachedAttachments, "Expected there to be no failed attachments.")
	framework.ExpectNoError(err, "Expected the stateful set to have successful attachments.")
	framework.Logf("Stateful set replica attachments verified successfully.")

	ginkgo.By("Creating a pod with volume.")
	// Uncordon a node for the primary attachment
	testPod.SetNodeUnschedulable(nodes[2], false)
	framework.Logf("Uncordoning %s.", nodes[2])
	tpod, cleanupFuncs := t.NewPod.SetupWithDynamicVolumes(client, namespace, t.CSIDriver, t.StorageClassParameters, schedulerName)
	for _, cleanupFunc := range cleanupFuncs {
		defer cleanupFunc()
	}

	ginkgo.By("Deploying the pod.")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("Checking that the pod is running.")
	tpod.WaitForRunning()
	t.NewPod.Name = tpod.Pod.Name

	ginkgo.By("Verifying that new pod was not able to create any replica attachments.")
	err = t.pollForReplicaFailedEvent(client, namespace)
	framework.ExpectNoError(err, "Expected to find failed replica event for %s.", t.NewPod.Name)

	ginkgo.By("Cleaning up the stateful set to free up attachments.")
	wrappedCleanup()

	ginkgo.By("Verifying that the new pod has successful replica attachment.")
	verifyPodAttachment := func() (bool, []azdiskv1beta2.AzVolumeAttachment, error) {
		isVerified, _, unattachedAttachments, err := resources.VerifySuccessfulAzVolumeAttachments(t.NewPod, t.AzDiskClient, t.StorageClassParameters, client, namespace)
		return isVerified, unattachedAttachments, err
	}
	unattachedAttachments, err = t.pollForFailedAttachments(verifyPodAttachment)
	framework.ExpectEmpty(unattachedAttachments, "Expected there to be no failed attachments.")
	framework.ExpectNoError(err, "Expected the new pod to have successful attachments.")
}

// Get the maximum number of unique volumes managed by the CSI driver that can be used on a node
func getNumReplicasFromCSINode(csiNode *storagev1.CSINode) (int, bool) {
	for _, driver := range csiNode.Spec.Drivers {
		if driver.Name == consts.DefaultDriverName {
			return int(*driver.Allocatable.Count), true
		}
	}
	return 0, false
}

func (t *DynamicallyProvisionedScaleReplicasOnDetach) pollForReplicaFailedEvent(client clientset.Interface, namespace *v1.Namespace) error {
	nameSelector := fields.OneTermEqualSelector("involvedObject.name", t.NewPod.Name).String()
	listOptions := metav1.ListOptions{FieldSelector: nameSelector, TypeMeta: metav1.TypeMeta{Kind: "Pod"}}
	err := wait.PollImmediate(testconsts.Poll, testconsts.PollTimeout, func() (bool, error) {
		events, err := client.CoreV1().Events(namespace.Name).List(context.TODO(), listOptions)
		if err != nil {
			return false, err
		}
		for _, event := range events.Items {
			if event.Reason == consts.ReplicaAttachmentFailedEvent {
				return true, nil
			}
		}
		return false, nil
	})
	return err
}

// Verifies that the stateful set has successful attachments for all pods
func (t *DynamicallyProvisionedScaleReplicasOnDetach) pollForFailedAttachments(verifyFunc func() (bool, []azdiskv1beta2.AzVolumeAttachment, error)) ([]azdiskv1beta2.AzVolumeAttachment, error) {
	var finalFailedAttachments []azdiskv1beta2.AzVolumeAttachment
	err := wait.PollImmediate(testconsts.Poll, testconsts.PollTimeout, func() (bool, error) {
		allAttached, failedAttachments, err := verifyFunc()
		finalFailedAttachments = failedAttachments
		return allAttached, err
	})
	return finalFailedAttachments, err
}

// Iterates over all pods in the stateful set to verify attachments for each pod
func (t *DynamicallyProvisionedScaleReplicasOnDetach) getStatefulSetAttachmentStatus(client clientset.Interface, namespace *v1.Namespace, tStatefulSet *resources.TestStatefulset) (bool, []azdiskv1beta2.AzVolumeAttachment, error) {
	allAttached := true
	var unattachedAttachments []azdiskv1beta2.AzVolumeAttachment
	for _, pod := range tStatefulSet.AllPods {
		isAttached, _, podUnattachedAttachments, err := resources.VerifySuccessfulAzVolumeAttachments(pod, t.AzDiskClient, t.StorageClassParameters, client, namespace)
		unattachedAttachments = append(unattachedAttachments, podUnattachedAttachments...)
		allAttached = allAttached && isAttached
		if err != nil {
			return allAttached, unattachedAttachments, err
		}
	}
	return allAttached, unattachedAttachments, nil
}
