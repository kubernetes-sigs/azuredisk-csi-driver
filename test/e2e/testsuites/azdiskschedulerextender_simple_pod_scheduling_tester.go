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
	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	testtypes "sigs.k8s.io/azuredisk-csi-driver/test/types"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
)

// AzDiskSchedulerExtenderSimplePodSchedulingTest will provision required PV(s), PVC(s) and Pod(s)
// Pod without PVs should successfully be scheduled in a cluster with AzDriverNode and AzVolumeAttachment resources
type AzDiskSchedulerExtenderSimplePodSchedulingTest struct {
	CSIDriver driver.DynamicPVTestDriver
	Pod       testtypes.PodDetails
}

func (t *AzDiskSchedulerExtenderSimplePodSchedulingTest) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	tpod := testtypes.NewTestPod(client, namespace, t.Pod.Cmd, schedulerName, t.Pod.IsWindows)

	// Get the list of available nodes for scheduling the pod
	nodeNames := nodeutil.ListNodeNames(client)
	if len(nodeNames) < 1 {
		ginkgo.Skip("need at least 1 nodes to verify the test case. Current node count is %d", len(nodeNames))
	}

	ginkgo.By("deploying the pod")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod's command exits with no error")
	tpod.WaitForSuccess()
}
