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
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	testtypes "sigs.k8s.io/azuredisk-csi-driver/test/types"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
)

//  will provision required PV(s), PVC(s) and Pod(s)
// Primary AzVolumeAttachment and Replica AzVolumeAttachments should be created on set of nodes with matching label
type PodAffinity struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pods                   []testtypes.PodDetails
	IsMultiZone            bool
	Volume                 testtypes.VolumeDetails
	AzDiskClient           *azDiskClientSet.Clientset
	StorageClassParameters map[string]string
}

func (t *PodAffinity) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	_, maxMountReplicaCount := azureutils.GetMaxSharesAndMaxMountReplicaCount(t.StorageClassParameters)

	// Get the list of available nodes for scheduling the pod
	nodes := nodeutil.ListNodeNames(client)
	if len(nodes) < maxMountReplicaCount+1 {
		ginkgo.Skip("need at least %d nodes to verify the test case. Current node count is %d", maxMountReplicaCount+1, len(nodes))
	}

	if len(t.Pods) < 2 {
		ginkgo.Skip("need at least 2 pods to verify the test case.")
	}

	ctx := context.Background()

	scheduledNodes := map[string]struct{}{}
	for i := range t.Pods {
		tpod, cleanup := t.Pods[i].SetupWithDynamicVolumes(client, namespace, t.CSIDriver, t.StorageClassParameters, schedulerName)
		// defer must be called here for resources not get removed before using them
		for i := range cleanup {
			defer cleanup[i]()
		}

		pod := tpod.Pod.DeepCopy()

		// add master node toleration to pod so that the test can utilize all available nodes
		tpod.SetAffinity(&testconsts.TestAffinity)
		tpod.SetLabel(testconsts.TestLabel)
		ginkgo.By(fmt.Sprintf("deploying pod %d", i))
		tpod.Create()
		defer tpod.Cleanup()
		ginkgo.By("checking that the pod is running")
		tpod.WaitForRunning()

		klog.Infof("volumes: %+v", pod.Spec.Volumes)
		diskNames := make([]string, len(pod.Spec.Volumes))
		for j, volume := range pod.Spec.Volumes {
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
			diskNames[j] = strings.ToLower(diskName)
		}

		err := wait.PollImmediate(testconsts.Poll, testconsts.PollTimeout,
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

					// if first pod, check which nodes pod's volumes were attached to
					for _, azVolumeAttachment := range azVolumeAttachments.Items {
						if i == 0 {
							scheduledNodes[azVolumeAttachment.Spec.NodeName] = struct{}{}
						} else {
							if _, ok := scheduledNodes[azVolumeAttachment.Spec.NodeName]; !ok {
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
