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
	"strconv"
	"time"

	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

// PreProvisionedCheckForReplicasTest will provision required PV(s), PVC(s) and Pod(s)
// Test will create a pods with shared disks and will verify that correct number of replica attachments are created
type PreProvisionedCheckForReplicasTest struct {
	CSIDriver     driver.PreProvisionedVolumeTestDriver
	Pods          []PodDetails
	VolumeName    string
	AzDiskClient  *azDiskClientSet.Clientset
	VolumeContext map[string]string
}

func (t *PreProvisionedCheckForReplicasTest) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	// Get the list of available nodes for scheduling the pod
	nodes := ListAzDriverNodeNames(t.AzDiskClient.DiskV1alpha1().AzDriverNodes(consts.AzureDiskCrdNamespace))
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
		maxSharesValue, ok := t.VolumeContext[consts.MaxSharesField]
		if !ok {
			ginkgo.Fail("failed to get the volume maxshares")
		}
		maxShares, err := strconv.ParseInt(maxSharesValue, 10, 0)
		if err != nil {
			ginkgo.Fail("failed to parse the volume maxshares")
		}

		// we can only create as many replicas as there are nodes
		var expectedNumberOfReplicas int
		nodesAvailableForReplicas := len(nodes) - 1 // excluding the node hosting primary attachment
		if nodesAvailableForReplicas >= int(maxShares-1) {
			expectedNumberOfReplicas = int(maxShares - 1)
		} else {
			expectedNumberOfReplicas = nodesAvailableForReplicas
		}

		time.Sleep(3 * time.Minute)
		labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{"disk.csi.azure.com/volume-name": t.VolumeName, "disk.csi.azure.com/requested-role": "Replica"}}
		azVolumeAttachments, err := t.AzDiskClient.DiskV1alpha1().AzVolumeAttachments(consts.AzureDiskCrdNamespace).List(context.Background(), metav1.ListOptions{LabelSelector: labels.Set(labelSelector.MatchLabels).String()})
		if err != nil {
			ginkgo.Fail(fmt.Sprintf("failed to get replica attachments. Error: %v", err))
		}
		if len(azVolumeAttachments.Items) != int(expectedNumberOfReplicas) {
			ginkgo.Fail(fmt.Sprintf("expected number of replicas is not maintained. Expected %d replcias. Found %d.", expectedNumberOfReplicas, len(azVolumeAttachments.Items)))
		}
	}
}
