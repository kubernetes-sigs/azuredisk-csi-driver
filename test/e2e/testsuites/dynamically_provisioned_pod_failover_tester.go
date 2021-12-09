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
	"time"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	testtypes "sigs.k8s.io/azuredisk-csi-driver/test/types"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
)

//  will provision required PV(s), PVC(s) and Pod(s)
// Pod should successfully be re-scheduled on failover in a cluster with AzDriverNode and AzVolumeAttachment resources
type PodFailover struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    testtypes.PodDetails
	Volume                 testtypes.VolumeDetails
	PodCheck               *PodExecCheck
	StorageClassParameters map[string]string
}

func (t *PodFailover) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	tDeployment, cleanup := t.Pod.SetupDeployment(client, namespace, t.CSIDriver, schedulerName, 1, t.StorageClassParameters)

	// defer must be called here so resources don't get removed before using them
	for i := range cleanup {
		defer cleanup[i]()
	}

	// Get the list of available nodes for scheduling the pod
	nodes := nodeutil.ListNodeNames(client)
	if len(nodes) < 2 {
		ginkgo.Skip("need at least 2 nodes to verify the test case. Current node count is %d", len(nodes))
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
