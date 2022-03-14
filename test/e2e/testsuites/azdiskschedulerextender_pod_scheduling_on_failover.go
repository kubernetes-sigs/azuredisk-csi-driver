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
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
)

// AzDiskSchedulerExtenderPodSchedulingOnFailover will provision required PV(s), PVC(s) and Pod(s)
// Pod should successfully be re-scheduled on failover/scaling in a cluster with AzDriverNode and AzVolumeAttachment resources
type AzDiskSchedulerExtenderPodSchedulingOnFailover struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    resources.PodDetails
	StorageClassParameters map[string]string
}

func (t *AzDiskSchedulerExtenderPodSchedulingOnFailover) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	tStorageClass, storageCleanup := t.Pod.CreateStorageClass(client, namespace, t.CSIDriver, t.StorageClassParameters)
	defer storageCleanup()
	tStatefulSet, cleanup := t.Pod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName, 1, &tStorageClass, nil)
	defer cleanup(15 * time.Minute)

	// Get the list of available nodes for scheduling the pod
	nodes := nodeutil.ListNodeNames(client)
	if len(nodes) < 1 {
		ginkgo.Skip("need at least 1 nodes to verify the test case. Current node count is %d", len(nodes))
	}

	ginkgo.By("deploying the statefulset")
	tStatefulSet.Create()

	ginkgo.By("checking that the pod for statefulset is running")
	err := tStatefulSet.WaitForPodReadyOrFail()
	framework.ExpectNoError(err)

	// Define a new scale for statefulset
	newScale := &scale.Scale{
		ObjectMeta: metav1.ObjectMeta{
			Name:      tStatefulSet.Statefulset.Name,
			Namespace: tStatefulSet.Namespace.Name,
		},
		Spec: scale.ScaleSpec{
			Replicas: int32(0)}}

	// Scale statefulset to 0
	_, err = client.AppsV1().StatefulSets(tStatefulSet.Namespace.Name).UpdateScale(context.TODO(), tStatefulSet.Statefulset.Name, newScale, metav1.UpdateOptions{})
	framework.ExpectNoError(err)

	ginkgo.By("sleep 240s waiting for statefulset update to complete and disk to detach")
	time.Sleep(240 * time.Second)

	// Scale the stateful set back to 1 pod
	newScale.Spec.Replicas = int32(1)

	_, err = client.AppsV1().StatefulSets(tStatefulSet.Namespace.Name).UpdateScale(context.TODO(), tStatefulSet.Statefulset.Name, newScale, metav1.UpdateOptions{})
	framework.ExpectNoError(err)

	ginkgo.By("sleep 30s waiting for statefulset update to complete")
	time.Sleep(30 * time.Second)

	ginkgo.By("checking that the pod for statefulset is running")
	err = tStatefulSet.WaitForPodReadyOrFail()
	framework.ExpectNoError(err)
}
