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

	"github.com/onsi/ginkgo/v2"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
)

func CountAllVolumeAttachments(cs clientset.Interface) int {
	e2elog.Logf("Getting all volume attachments")
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
