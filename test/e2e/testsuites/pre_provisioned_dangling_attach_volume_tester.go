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

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2021-07-01/compute"
	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

// PreProvisionedDanglingAttachVolumeTest will provision required PV(s), PVC(s) and Pod(s)
// Testing that a volume could be reattached to a different node on DanglingAttachError
type PreProvisionedDanglingAttachVolumeTest struct {
	CSIDriver     driver.PreProvisionedVolumeTestDriver
	AzureCloud    *provider.Cloud
	Pod           resources.PodDetails
	VolumeContext map[string]string
}

func (t *PreProvisionedDanglingAttachVolumeTest) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	// Setup required PV(s) and PVC(s) corresponding to PodDetails that will be shared among two pods
	tpod, cleanup := t.Pod.SetupWithPreProvisionedVolumes(client, namespace, t.CSIDriver, t.VolumeContext, schedulerName)
	// Defer must be called here for PV(s) and PVC(s) to be removed after the test completion and not earlier
	for i := range cleanup {
		defer cleanup[i]()
	}

	// Get the list of available nodes for scheduling the pod
	nodes := nodeutil.ListNodeNames(client)
	if len(nodes) < 2 {
		ginkgo.Skip("need at least 2 nodes to verify the test case. Current node count is %d", len(nodes))
	}

	ginkgo.By("attaching disk to node#0")
	diskURI := t.Pod.Volumes[0].VolumeID
	diskName, err := azureutils.GetDiskName(diskURI)
	framework.ExpectNoError(err)
	nodeName := types.NodeName(nodes[0])
	cachingMode, ok := t.VolumeContext[consts.CachingModeField]
	if !ok {
		cachingMode = "None"
	}
	resourceGroup, err := azureutils.GetResourceGroupFromURI(diskURI)
	framework.ExpectNoError(err)
	disk, rerr := t.AzureCloud.DisksClient.Get(context.Background(), resourceGroup, diskName)
	framework.ExpectNoError(rerr.Error())
	_, err = t.AzureCloud.AttachDisk(context.Background(), true, diskName, diskURI, nodeName, compute.CachingTypes(cachingMode), &disk)
	framework.ExpectNoError(err)

	// Make node#0 unschedulable to ensure that pods are scheduled on a different node
	tpod.SetNodeUnschedulable(nodes[0], true)        // kubeclt cordon node
	defer tpod.SetNodeUnschedulable(nodes[0], false) // defer kubeclt uncordon node

	// Create a pod with the pvc mount for the disk currently attached to node#0 and wait for success
	ginkgo.By("deploying the pod")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod's command exits with no error")
	// DanglingAttachError would have caused a pod failure. Success means detach/attach happened as expected
	tpod.WaitForSuccess()
}
