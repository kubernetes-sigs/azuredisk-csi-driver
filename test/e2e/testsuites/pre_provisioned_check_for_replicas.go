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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog"
	"k8s.io/kubernetes/test/e2e/framework"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	testtypes "sigs.k8s.io/azuredisk-csi-driver/test/types"
)

// PreProvisionedCheckForReplicasTest will provision required PV(s), PVC(s) and Pod(s)
// Test will create a pods with shared disks and will verify that correct number of replica attachments are created
type PreProvisionedCheckForReplicasTest struct {
	CSIDriver     driver.PreProvisionedVolumeTestDriver
	Pods          []testtypes.PodDetails
	VolumeName    string
	AzDiskClient  azDiskClientSet.Interface
	VolumeContext map[string]string
}

func (t *PreProvisionedCheckForReplicasTest) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	// Get the list of available nodes for scheduling the pod
	nodes := nodeutil.ListAzDriverNodeNames(t.AzDiskClient.DiskV1alpha1().AzDriverNodes(consts.AzureDiskCrdNamespace))
	if len(nodes) < 2 {
		ginkgo.Skip(fmt.Sprintf("need at least 2 nodes to verify the test case. Current node count is %d", len(nodes)))
	}

	for _, pod := range t.Pods {
		tpod, cleanup := pod.SetupWithPreProvisionedVolumes(client, namespace, t.CSIDriver, t.VolumeContext, schedulerName)
		for i := range cleanup {
			defer cleanup[i]()
		}

		ginkgo.By("deploying the pod")
		tpod.Create()
		defer tpod.Cleanup()
		ginkgo.By("waiting for pod running")
		tpod.WaitForRunning()

		// get the expected number of replicas
		_, maxMountReplicaCount := azureutils.GetMaxSharesAndMaxMountReplicaCount(t.VolumeContext)

		// we can only create as many replicas as there are nodes
		var expectedNumberOfReplicas int
		nodesAvailableForReplicas := len(nodes) - 1 // excluding the node hosting primary attachment
		if nodesAvailableForReplicas >= maxMountReplicaCount {
			expectedNumberOfReplicas = maxMountReplicaCount
		} else {
			expectedNumberOfReplicas = nodesAvailableForReplicas
		}

		labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{consts.VolumeNameLabel: t.VolumeName, consts.RoleLabel: "Replica"}}
		err := wait.PollImmediate(testconsts.Poll, testconsts.PollTimeout,
			func() (bool, error) {
				azVolumeAttachments, err := t.AzDiskClient.DiskV1alpha1().AzVolumeAttachments(consts.AzureDiskCrdNamespace).List(context.Background(), metav1.ListOptions{LabelSelector: labels.Set(labelSelector.MatchLabels).String()})
				if err != nil {
					return false, status.Errorf(codes.Internal, "failed to get replica attachments. Error: %v", err)
				}
				if len(azVolumeAttachments.Items) != int(expectedNumberOfReplicas) {
					klog.Errorf("Expected %d replcias. Found %d.", expectedNumberOfReplicas, len(azVolumeAttachments.Items))
					return false, nil
				}
				return true, nil
			})
		framework.ExpectNoError(err)
	}
}
