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
	"time"

	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	diskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
)

// DynamicallyProvisionedPodDelete will provision required StorageClass(es), PVC(s) and Pod(s)
// Tests whether AzVolumeAttachment CRI gets either deleted or demoted based on volume's maxMountReplicaCount
type DynamicallyProvisionedPodDelete struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    resources.PodDetails
	StorageClassParameters map[string]string
	AzDiskClient           *azDiskClientSet.Clientset
}

func (t *DynamicallyProvisionedPodDelete) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	_, maxMountReplicaCount := azureutils.GetMaxSharesAndMaxMountReplicaCount(t.StorageClassParameters, false)

	// Get the list of available nodes for scheduling the pod
	nodes := nodeutil.ListAgentNodeNames(client, t.Pod.IsWindows)
	necessaryNodeCount := maxMountReplicaCount + 2
	if len(nodes) < necessaryNodeCount {
		ginkgo.Skip("need at least %d agent nodes to verify the test case. Current agent node count is %d", necessaryNodeCount, len(nodes))
	}

	ctx := context.Background()
	tpod, cleanup := t.Pod.SetupWithDynamicVolumes(client, namespace, t.CSIDriver, t.StorageClassParameters, schedulerName)
	// defer must be called here for resources not get removed before using them
	for i := range cleanup {
		defer cleanup[i]()
	}
	pod := tpod.Pod.DeepCopy()

	ginkgo.By("deploying the pod")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod is running")
	tpod.WaitForRunning()

	time.Sleep(3 * time.Second)
	klog.Infof("volumes: %+v", pod.Spec.Volumes)
	diskNames := make([]string, len(pod.Spec.Volumes))
	for i, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim == nil {
			framework.Failf("volume (%s) does not hold PersistentVolumeClaim field", volume.Name)
		}
		pvc, err := client.CoreV1().PersistentVolumeClaims(tpod.Namespace.Name).Get(ctx, volume.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
		framework.ExpectNoError(err)

		pv, err := client.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
		framework.ExpectNoError(err)
		if pv.Spec.CSI == nil {
			klog.Infof("PV (%s) is not a CSI volume.", pv.Name)
			continue
		}
		klog.Infof("PV (%s) CSI spec:\n %+v", pv.Name, *pv.Spec.CSI)
		framework.ExpectEqual(pv.Spec.CSI.Driver, consts.DefaultDriverName)

		diskName, err := azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
		framework.ExpectNoError(err)
		diskNames[i] = strings.ToLower(diskName)
	}

	klog.Infof("volumes: %+v", pod.Spec.Volumes)

	ginkgo.By("begin to delete the pod")
	tpod.Cleanup()

	// make sure the AzVolumeAttachment CRIs are appropriately adjusted (demoted / deleted)
	err := wait.PollImmediate(testconsts.Poll, testconsts.PollTimeout,
		func() (bool, error) {
			for _, diskName := range diskNames {
				labelSelector := labels.NewSelector()
				volReq, err := azureutils.CreateLabelRequirements(consts.VolumeNameLabel, selection.Equals, diskName)
				framework.ExpectNoError(err)
				labelSelector = labelSelector.Add(*volReq)

				azVolumeAttachments, err := t.AzDiskClient.DiskV1beta1().AzVolumeAttachments(consts.DefaultAzureDiskCrdNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
				framework.ExpectNoError(err)

				if maxMountReplicaCount == 0 {
					return len(azVolumeAttachments.Items) == 0, nil
				}
				for _, azVolumeAttachment := range azVolumeAttachments.Items {
					if azVolumeAttachment.Status.Detail == nil || azVolumeAttachment.Status.Detail.Role != diskv1beta1.ReplicaRole {
						return false, nil
					}
				}
			}
			return true, nil
		})
	framework.ExpectNoError(err)
}
