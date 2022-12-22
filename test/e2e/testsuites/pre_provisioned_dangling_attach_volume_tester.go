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
	"strings"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/onsi/ginkgo/v2"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
)

// PreProvisionedDanglingAttachVolumeTest will provision required PV(s), PVC(s) and Pod(s)
// Testing that a volume could be reattached to a different node on DanglingAttachError
type PreProvisionedDanglingAttachVolumeTest struct {
	CSIDriver       driver.PreProvisionedVolumeTestDriver
	AzureDiskDriver azuredisk.CSIDriver
	Pod             PodDetails
	VolumeContext   map[string]string
}

func (t *PreProvisionedDanglingAttachVolumeTest) Run(client clientset.Interface, namespace *v1.Namespace) {
	// Setup required PV(s) and PVC(s) corresponding to PodDetails that will be shared among two pods
	tpod, cleanup := t.Pod.SetupWithPreProvisionedVolumes(client, namespace, t.CSIDriver, t.VolumeContext)
	// Defer must be called here for PV(s) and PVC(s) to be removed after the test completion and not earlier
	for i := range cleanup {
		defer cleanup[i]()
	}

	// Get the list of available nodes for scheduling the pod
	nodes := tpod.ListNodes()
	if len(nodes) < 2 {
		ginkgo.Skip("need at least 2 nodes to verify the test case. Current node count is %d", len(nodes))
	}
	var node string
	for _, n := range nodes {
		if !strings.Contains(n, "control-plane") {
			node = n
			break
		}
	}
	if node == "" {
		ginkgo.Skip("cannot get a suitable agent node to run test case")
	}

	ginkgo.By("attaching disk to node#0")
	req := &csi.ControllerPublishVolumeRequest{
		VolumeId: t.Pod.Volumes[0].VolumeID,
		NodeId:   node,
		VolumeCapability: &csi.VolumeCapability{
			AccessType: &csi.VolumeCapability_Mount{
				Mount: &csi.VolumeCapability_MountVolume{},
			},
			AccessMode: &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			},
		},
	}
	_, err := t.AzureDiskDriver.ControllerPublishVolume(context.Background(), req)
	framework.ExpectNoError(err)

	// Make node#0 unschedulable to ensure that pods are scheduled on a different node
	tpod.SetNodeUnschedulable(node, true)        // kubeclt cordon node
	defer tpod.SetNodeUnschedulable(node, false) // defer kubeclt uncordon node

	// Create a pod with the pvc mount for the disk currently attached to node#0 and wait for success
	ginkgo.By("deploying the pod")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod's command exits with no error")
	// DanglingAttachError would have caused a pod failure. Success means detach/attach happened as expected
	tpod.WaitForSuccess()
}
