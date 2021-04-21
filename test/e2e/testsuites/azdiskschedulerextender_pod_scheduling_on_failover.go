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
	"time"

	"github.com/onsi/ginkgo"
	scale "k8s.io/api/autoscaling/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	v1alpha1ClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/typed/azuredisk/v1alpha1"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
)

// AzDiskSchedulerExtenderPodSchedulingOnFailover will provision required PV(s), PVC(s) and Pod(s)
// Pod should successfully be re-scheduled on failover/scaling in a cluster with AzDriverNode and AzVolumeAttachment resources
type AzDiskSchedulerExtenderPodSchedulingOnFailover struct {
	CSIDriver              driver.DynamicPVTestDriver
	AzDiskClientSet        v1alpha1ClientSet.DiskV1alpha1Interface
	AzNamespace            string
	Pod                    PodDetails
	Volume                 VolumeDetails
	StorageClassParameters map[string]string
}

func (t *AzDiskSchedulerExtenderPodSchedulingOnFailover) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	tStatefulSet, cleanup := t.Pod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName)
	for i := range cleanup {
		i := i
		defer cleanup[i]()
	}

	// Get the list of available nodes for scheduling the pod
	nodes := ListNodeNames(client)
	if len(nodes) < 1 {
		ginkgo.Skip("need at least 1 nodes to verify the test case. Current node count is %d", len(nodes))
	}

	volumeName := t.Volume.VolumeMount.NameGenerate + "1"
	testAzAtt := SetupTestAzVolumeAttachment(t.AzDiskClientSet, t.AzNamespace, volumeName, nodes[0], 0)
	defer testAzAtt.Cleanup()
	_ = testAzAtt.Create()

	err := testAzAtt.WaitForAttach(time.Duration(5) * time.Minute)
	framework.ExpectNoError(err)

	ginkgo.By("deploying the statefulset")
	tStatefulSet.Create()

	ginkgo.By("checking that the pod for statefulset is running")
	tStatefulSet.WaitForPodReady()

	// Define a new scale for statefulset
	newScale := &scale.Scale{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tStatefulSet.statefulset.Name,
			Namespace: tStatefulSet.namespace.Name,
		},
		Spec: scale.ScaleSpec{
			Replicas: int32(0)}}

	// Scale statefulset to 0
	_, err = client.AppsV1().StatefulSets(tStatefulSet.namespace.Name).UpdateScale(context.TODO(), tStatefulSet.statefulset.Name, newScale, metav1.UpdateOptions{})
	framework.ExpectNoError(err)

	ginkgo.By("sleep 240s waiting for statefulset update to complete and disk to detach")
	time.Sleep(240 * time.Second)

	// Scale the stateful set back to 1 pod
	newScale.Spec.Replicas = int32(1)

	_, err = client.AppsV1().StatefulSets(tStatefulSet.namespace.Name).UpdateScale(context.TODO(), tStatefulSet.statefulset.Name, newScale, metav1.UpdateOptions{})
	framework.ExpectNoError(err)

	ginkgo.By("sleep 30s waiting for statefulset update to complete")
	time.Sleep(30 * time.Second)

	ginkgo.By("checking that the pod for statefulset is running")
	tStatefulSet.WaitForPodReady()
}
