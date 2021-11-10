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
	"context"
	"strings"

	"github.com/onsi/ginkgo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog"
	"k8s.io/kubernetes/test/e2e/framework"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
)

//  will provision required PV(s), PVC(s) and Pod(s)
// Primary AzVolumeAttachment and Replica AzVolumeAttachments should be created on set of nodes with matching label
type PodNodeAffinity struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    PodDetails
	IsMultiZone            bool
	Volume                 VolumeDetails
	AzDiskClient           *azDiskClientSet.Clientset
	StorageClassParameters map[string]string
}

func (t *PodNodeAffinity) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	_, maxMountReplicaCount := azureutils.GetMaxSharesAndMaxMountReplicaCount(t.StorageClassParameters)

	// Get the list of available nodes for scheduling the pod
	nodes := ListNodeNames(client)
	necessaryNodeCount := maxMountReplicaCount + 2
	if len(nodes) < necessaryNodeCount {
		ginkgo.Skip("need at least %d nodes to verify the test case. Current node count is %d", necessaryNodeCount, len(nodes))
	}

	ctx := context.Background()

	// set node label
	numNodesWithLabel := maxMountReplicaCount + 1
	nodesWithLabel := map[string]struct{}{}
	count := 0
	for i := range nodes {
		nodeObj, err := client.CoreV1().Nodes().Get(ctx, nodes[i], metav1.GetOptions{})
		framework.ExpectNoError(err)

		if _, ok := nodeObj.Labels[masterNodeLabel]; ok {
			continue
		}

		if i < numNodesWithLabel {
			var labelCleanup func()
			_, labelCleanup, err = SetNodeLabels(client, nodeObj, testLabel)
			framework.ExpectNoError(err)
			defer labelCleanup()
			nodesWithLabel[nodes[i]] = struct{}{}
			count++
		}
	}

	tpod, cleanup := t.Pod.SetupWithDynamicVolumes(client, namespace, t.CSIDriver, t.StorageClassParameters, schedulerName)
	// defer must be called here for resources not get removed before using them
	for i := range cleanup {
		defer cleanup[i]()
	}
	pod := tpod.pod.DeepCopy()

	tpod.SetAffinity(&testAffinity)

	ginkgo.By("deploying the pod")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod is running")
	tpod.WaitForRunning()
	framework.ExpectNotEqual(t.Pod.Volumes[0].PersistentVolume, nil)

	klog.Infof("volumes: %+v", pod.Spec.Volumes)
	diskNames := make([]string, len(pod.Spec.Volumes))
	for i, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim == nil {
			framework.Failf("volume (%s) does not hold PersistentVolumeClaim field", volume.Name)
		}
		pvc, err := client.CoreV1().PersistentVolumeClaims(tpod.namespace.Name).Get(ctx, volume.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
		framework.ExpectNoError(err)

		pv, err := client.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		framework.ExpectNoError(err)
		framework.ExpectNotEqual(pv.Spec.CSI, nil)
		framework.ExpectEqual(pv.Spec.CSI.Driver, consts.DefaultDriverName)

		diskName, err := azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
		framework.ExpectNoError(err)
		diskNames[i] = strings.ToLower(diskName)
	}

	// confirm that the primary and replica AzVolumeAttachments for this volume are created on a node with the label
	err := wait.PollImmediate(poll, pollTimeout,
		func() (bool, error) {
			for _, diskName := range diskNames {
				labelSelector := labels.NewSelector()
				volReq, err := controller.CreateLabelRequirements(consts.VolumeNameLabel, selection.Equals, diskName)
				framework.ExpectNoError(err)
				labelSelector = labelSelector.Add(*volReq)

				azVolumeAttachments, err := t.AzDiskClient.DiskV1alpha1().AzVolumeAttachments(consts.AzureDiskCrdNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
				if err != nil {
					return false, err
				}

				if azVolumeAttachments == nil {
					return false, nil
				}

				klog.Infof("found %d AzVolumeAttachments for volume (%s)", len(azVolumeAttachments.Items), diskName)

				if len(azVolumeAttachments.Items) != maxMountReplicaCount+1 {
					return false, nil
				}

				for _, azVolumeAttachment := range azVolumeAttachments.Items {
					if _, ok := nodesWithLabel[azVolumeAttachment.Spec.NodeName]; !ok {
						return false, status.Errorf(codes.Internal, "AzVolumeAttachment (%s) for volume (%s) created on a wrong node (%s)", azVolumeAttachment.Name, diskName, azVolumeAttachment.Spec.NodeName)
					}
				}
			}
			return true, nil
		})

	framework.ExpectNoError(err)
}
