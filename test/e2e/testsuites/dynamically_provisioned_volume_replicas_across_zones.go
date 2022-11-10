/*
Copyright 2022 The Kubernetes Authors.

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

	"github.com/onsi/ginkgo/v2"
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
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
)

type DynamicallyProvisionedVolumeReplicasAcrossZones struct {
	Pod                    resources.PodDetails
	CSIDriver              driver.DynamicPVTestDriver
	AzDiskClient           *azdisk.Clientset
	StorageClassParameters map[string]string
}

func (t *DynamicallyProvisionedVolumeReplicasAcrossZones) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	_, maxMountReplicaCount := azureutils.GetMaxSharesAndMaxMountReplicaCount(t.StorageClassParameters, false, true)
	tpod, cleanups := t.Pod.SetupWithDynamicVolumes(client, namespace, t.CSIDriver, t.StorageClassParameters, schedulerName)
	for i := range cleanups {
		defer cleanups[i]()
	}

	ginkgo.By("deploying the pod")
	tpod.Create()
	defer tpod.Cleanup()
	ginkgo.By("checking that the pod is running")
	tpod.WaitForRunning()

	pod := tpod.Pod.DeepCopy()
	volume := pod.Spec.Volumes[0]

	ctx := context.Background()

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

	// Confirm that the primary and the replica AzVolumeAttachments are created on different zones
	err = wait.PollImmediate(testconsts.Poll, testconsts.PollTimeout,
		func() (bool, error) {

			labelSelector := labels.NewSelector()
			volReq, err := azureutils.CreateLabelRequirements(consts.VolumeNameLabel, selection.Equals, diskName)
			framework.ExpectNoError(err)
			labelSelector = labelSelector.Add(*volReq)

			azVolumeAttachments, err := t.AzDiskClient.DiskV1beta2().AzVolumeAttachments(consts.DefaultAzureDiskCrdNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
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

			// Get the number of zones of the cluster
			zoneToAttachmentCountMap := map[string]int{}
			nodeToZoneMap := map[string]string{}
			nodeList, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
			framework.ExpectNoError(err)
			for _, node := range nodeList.Items {
				zone, ok := node.GetLabels()[consts.WellKnownTopologyKey]
				if !ok {
					return false, status.Errorf(codes.Internal, "Unable to retrieve topology label for node (%s)", node.Name)
				}
				zoneToAttachmentCountMap[zone] = 0
				nodeToZoneMap[node.Name] = zone
			}

			desiredMaxAttachmentsPerZone := len(azVolumeAttachments.Items)/len(zoneToAttachmentCountMap) + 1

			for _, azVolumeAttachment := range azVolumeAttachments.Items {
				nodeName := azVolumeAttachment.Spec.NodeName
				zone, ok := nodeToZoneMap[nodeName]
				if !ok {
					return false, status.Errorf(codes.Internal, "Unable to retrieve zone from map for node (%s)", nodeName)
				}
				if _, ok := zoneToAttachmentCountMap[zone]; !ok {
					return false, status.Errorf(codes.Internal, "Invalid zone (%s)", zone)
				}
				zoneToAttachmentCountMap[zone]++
				if zoneToAttachmentCountMap[zone] > desiredMaxAttachmentsPerZone {
					return false, status.Errorf(codes.Internal, "The number(%d) of AzVolumeAttachments created in the zone(%s) is greater than the desired number: %d", zoneToAttachmentCountMap[zone], zone, desiredMaxAttachmentsPerZone)
				}
			}
			return true, nil
		})

	framework.ExpectNoError(err)
}
