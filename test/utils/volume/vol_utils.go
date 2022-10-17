/*
Copyright 2019 The Kubernetes Authors.

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

package volutil

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

func WaitForAllVolumeAttachmentsToDetach(testTimeout int, tickerDuration time.Duration, totalNumberOfVolumeAttachments int, cs clientset.Interface) (numberOfAttachedVolumeAttachments int) {
	numberOfAttachedVolumeAttachments = totalNumberOfVolumeAttachments
	ticker := time.NewTicker(tickerDuration)
	tickerCount := 0
	timeout := time.After(time.Duration(testTimeout) * time.Minute)

	for {
		select {
		case <-timeout:
			return
		case <-ticker.C:
			tickerCount++
			numberOfAttachedVolumeAttachments = CountAllVolumeAttachments(cs)
			e2elog.Logf("%.1f min: %d volumes are attached", float32(tickerCount*30)/60, numberOfAttachedVolumeAttachments)
			if numberOfAttachedVolumeAttachments <= 0 {
				return
			}
		}
	}
}

func CountAllVolumeAttachments(cs clientset.Interface) int {
	e2elog.Logf("Getting all VolumeAttachments")
	vatts, err := cs.StorageV1().VolumeAttachments().List(context.TODO(), metav1.ListOptions{})
	if !errors.IsNotFound(err) {
		framework.ExpectNoError(err)
	}
	return len(vatts.Items)
}

// waitForPersistentVolumeClaimDeleted waits for a PersistentVolumeClaim to be removed from the system until timeout occurs, whichever comes first.
func WaitForPersistentVolumeClaimDeleted(c clientset.Interface, ns string, pvcName string, poll, timeout time.Duration) error {
	framework.Logf("Waiting up to %v for PersistentVolumeClaim %s to be removed", timeout, pvcName)
	err := wait.PollImmediate(poll, timeout, func() (bool, error) {
		_, err := c.CoreV1().PersistentVolumeClaims(ns).Get(context.TODO(), pvcName, metav1.GetOptions{})
		if err != nil {
			// PVC Deleted, return success
			if errors.IsNotFound(err) {
				framework.Logf("Claim %q in namespace %q doesn't exist in the system", pvcName, ns)
				return true, nil
			}
			// Log error, keep retrying
			framework.Logf("Failed to get claim %q in namespace %q, retrying in %v. Error: %v", pvcName, ns, poll, err)
		}

		// Continue polling
		return false, nil
	})

	if err != nil {
		return fmt.Errorf("PersistentVolumeClaim %s is not removed from the system within %v. Error: %v ", pvcName, timeout, err)
	}

	return nil
}

// WaitForVolumeDetach waits for volumeattachment for a PV to be removed from the cluster
func WaitForVolumeDetach(c clientset.Interface, pvName string, poll, pollTimeout time.Duration) error {
	ginkgo.By("waiting for disk to detach from node")
	ctx, cancelFunc := context.WithTimeout(context.Background(), pollTimeout)
	defer cancelFunc()
	return wait.PollImmediateUntil(poll, func() (bool, error) {
		volumeAttachments, err := c.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		notFound := true
		for _, volumeAttachment := range volumeAttachments.Items {
			if volumeAttachment.Spec.Source.PersistentVolumeName != nil && *volumeAttachment.Spec.Source.PersistentVolumeName == pvName {
				notFound = false
			}
		}
		return notFound, nil
	}, ctx.Done())
}

func WaitForAllReplicaAttachmentsToAttach(testTimeout int, tickerDuration time.Duration, desiredNumberOfReplicaAtts int, cs *azdisk.Clientset) (numberOfAttachedReplicaAtts int) {
	ticker := time.NewTicker(tickerDuration)
	tickerCount := 0
	timeout := time.After(time.Duration(testTimeout) * time.Minute)

	for {
		select {
		case <-timeout:
			return
		case <-ticker.C:
			tickerCount++
			numberOfAttachedReplicaAtts = CountAllAttachedReplicaAttachments(cs)
			e2elog.Logf("%d min: %d replica attachments are attached", tickerCount, numberOfAttachedReplicaAtts)
			if numberOfAttachedReplicaAtts >= desiredNumberOfReplicaAtts {
				return
			}
		}
	}
}

func WaitForAllReplicaAttachmentsToDetach(testTimeout int, tickerDuration time.Duration, totalNumberOfReplicaAtts int, cs *azdisk.Clientset) (numberOfAttachedReplicaAtts int) {
	numberOfAttachedReplicaAtts = totalNumberOfReplicaAtts
	ticker := time.NewTicker(tickerDuration)
	tickerCount := 0
	timeout := time.After(time.Duration(testTimeout) * time.Minute)

	for {
		select {
		case <-timeout:
			return
		case <-ticker.C:
			tickerCount++
			numberOfAttachedReplicaAtts = CountAllAttachedReplicaAttachments(cs)
			e2elog.Logf("%d min: %d replica attachments are attached", tickerCount, numberOfAttachedReplicaAtts)
			if numberOfAttachedReplicaAtts <= 0 {
				return
			}
		}
	}
}

func CountAllAttachedReplicaAttachments(cs *azdisk.Clientset) int {
	e2elog.Logf("Getting all replica attachments")
	roleReq, err := azureutils.CreateLabelRequirements(consts.RoleLabel, selection.Equals, string(azdiskv1beta2.ReplicaRole))
	framework.ExpectNoError(err)
	labelSelector := labels.NewSelector().Add(*roleReq)

	replicaAtts, err := cs.DiskV1beta2().AzVolumeAttachments(consts.DefaultAzureDiskCrdNamespace).List(context.TODO(), metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		e2elog.Failf("Failed to get replica attachments: %v.", err)
	}

	numberOfAttachedReplicaAtts := 0
	for _, replicaAtt := range replicaAtts.Items {
		if replicaAtt.Status.State == azdiskv1beta2.Attached {
			numberOfAttachedReplicaAtts++
		}
	}
	return numberOfAttachedReplicaAtts
}
