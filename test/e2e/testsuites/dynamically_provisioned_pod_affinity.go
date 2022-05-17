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
	"fmt"
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
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	diskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
)

//  will provision required PV(s), PVC(s) and Pod(s)
// Primary AzVolumeAttachment and Replica AzVolumeAttachments should be created on set of nodes with matching label
type PodAffinity struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pods                   []resources.PodDetails
	IsMultiZone            bool
	Volume                 resources.VolumeDetails
	AzDiskClient           *azDiskClientSet.Clientset
	StorageClassParameters map[string]string
	IsAntiAffinityTest     bool
}

func (t *PodAffinity) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	numPods := len(t.Pods)
	if numPods < 2 {
		ginkgo.Skip("need at least 2 pods to verify the test case.")
	}

	_, maxMountReplicaCount := azureutils.GetMaxSharesAndMaxMountReplicaCount(t.StorageClassParameters, false)

	// Need independent nodes for the primary for each pod plus at least one for each replica.
	numNodesRequired := numPods + maxMountReplicaCount

	// Get the list of available nodes for scheduling the pod
	nodes := nodeutil.ListAgentNodeNames(client, t.Pods[0].IsWindows)
	if len(nodes) < numNodesRequired {
		ginkgo.Skip("need at least %d agent nodes to verify the test case. Current agent node count is %d", numNodesRequired+1, len(nodes))
	}

	ctx := context.Background()

	scheduledNodes := map[string]struct{}{}
	for podIndex := range t.Pods {
		tpod, cleanup := t.Pods[podIndex].SetupWithDynamicVolumes(client, namespace, t.CSIDriver, t.StorageClassParameters, schedulerName)
		// defer must be called here for resources not get removed before using them
		for j := range cleanup {
			defer cleanup[j]()
		}

		pod := tpod.Pod.DeepCopy()

		// set inter-pod affinity with topologyKey == "kubernetes.io/hostname" so that second pod can be placed on the same node as first pod
		// or anti-affinity so that second pod and its replicaMounts can be placed on different node as the first pod
		if podIndex == 0 {
			tpod.SetLabel(testconsts.TestLabel)
		} else {
			if t.IsAntiAffinityTest {
				tpod.SetAffinity(&testconsts.TestPodAntiAffinity)
			} else {
				tpod.SetAffinity(&testconsts.TestPodAffinity)
			}
		}
		ginkgo.By(fmt.Sprintf("deploying pod %d", podIndex))
		tpod.Create()
		defer tpod.Cleanup()
		ginkgo.By("checking that the pod is running")
		tpod.WaitForRunning()

		klog.Infof("volumes: %+v", pod.Spec.Volumes)
		diskNames := make([]string, len(pod.Spec.Volumes))
		for volumeIndex, volume := range pod.Spec.Volumes {
			if volume.PersistentVolumeClaim == nil {
				framework.Failf("volume (%s) does not hold PersistentVolumeClaim field", volume.Name)
			}
			pvc, err := client.CoreV1().PersistentVolumeClaims(tpod.Namespace.Name).Get(ctx, volume.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
			framework.ExpectNoError(err)

			pv, err := client.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
			framework.ExpectNoError(err)
			framework.ExpectNotEqual(pv.Spec.CSI, nil)
			framework.ExpectEqual(pv.Spec.CSI.Driver, consts.DefaultDriverName)

			diskName, err := azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
			framework.ExpectNoError(err)
			diskNames[volumeIndex] = strings.ToLower(diskName)
		}

		err := wait.PollImmediate(testconsts.Poll, testconsts.PollTimeout,
			func() (bool, error) {
				for _, diskName := range diskNames {
					labelSelector := labels.NewSelector()
					volReq, err := azureutils.CreateLabelRequirements(consts.VolumeNameLabel, selection.Equals, diskName)
					framework.ExpectNoError(err)
					labelSelector = labelSelector.Add(*volReq)

					azVolumeAttachments, err := t.AzDiskClient.DiskV1beta1().AzVolumeAttachments(consts.DefaultAzureDiskCrdNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
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

					// if first pod, check which nodes pod's volumes were attached to
					for _, azVolumeAttachment := range azVolumeAttachments.Items {
						if podIndex == 0 {
							// for anti-affinity, we don't expect the node filter to exclude replica nodes
							if t.IsAntiAffinityTest && azVolumeAttachment.Spec.RequestedRole == diskv1beta1.ReplicaRole {
								continue
							}
							scheduledNodes[azVolumeAttachment.Spec.NodeName] = struct{}{}
						} else {
							if _, ok := scheduledNodes[azVolumeAttachment.Spec.NodeName]; t.IsAntiAffinityTest == ok {
								return false, status.Errorf(codes.Internal, "AzVolumeAttachment (%s) for volume (%s) created on a wrong node (%s)", azVolumeAttachment.Name, diskName, azVolumeAttachment.Spec.NodeName)
							}
						}
					}
				}
				return true, nil
			})
		framework.ExpectNoError(err)
	}
}
