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

package resources

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

type TestAzVolumeAttachment struct {
	Azclient             azdisk.Interface
	Namespace            string
	UnderlyingVolume     string
	PrimaryNodeName      string
	MaxMountReplicaCount int
}

func NewTestAzVolumeAttachment(azclient azdisk.Interface, volumeAttachmentName, nodeName, volumeName, ns string) *azdiskv1beta2.AzVolumeAttachment {
	// Delete leftover azVolumeAttachments from previous runs
	azVolumeAttachments := azclient.DiskV1beta2().AzVolumeAttachments(ns)
	if _, err := azVolumeAttachments.Get(context.Background(), volumeAttachmentName, metav1.GetOptions{}); err == nil {
		err := azVolumeAttachments.Delete(context.Background(), volumeAttachmentName, metav1.DeleteOptions{})
		framework.ExpectNoError(err)
	}

	newAzVolumeAttachment, err := azVolumeAttachments.Create(context.Background(), &azdiskv1beta2.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name: volumeAttachmentName,
		},
		Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
			VolumeName:    volumeName,
			NodeName:      nodeName,
			RequestedRole: azdiskv1beta2.PrimaryRole,
			VolumeContext: map[string]string{
				"controller": "azdrivernode",
				"name":       volumeAttachmentName,
				"namespace":  ns,
				"partition":  "default",
			},
		},
		Status: azdiskv1beta2.AzVolumeAttachmentStatus{
			Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
				Role:           azdiskv1beta2.PrimaryRole,
				PublishContext: map[string]string{},
			},
			State: azdiskv1beta2.Attached,
		},
	}, metav1.CreateOptions{})
	framework.ExpectNoError(err)

	return newAzVolumeAttachment
}

func DeleteTestAzVolumeAttachment(azclient azdisk.Interface, namespace, volumeAttachmentName string) {
	_ = azclient.DiskV1beta2().AzVolumeAttachments(namespace).Delete(context.Background(), volumeAttachmentName, metav1.DeleteOptions{})
}

func SetupTestAzVolumeAttachment(azclient azdisk.Interface, namespace, underlyingVolume, primaryNodeName string, maxMountReplicaCount int) *TestAzVolumeAttachment {
	return &TestAzVolumeAttachment{
		Azclient:             azclient,
		Namespace:            namespace,
		UnderlyingVolume:     underlyingVolume,
		PrimaryNodeName:      primaryNodeName,
		MaxMountReplicaCount: maxMountReplicaCount,
	}
}

func (t *TestAzVolumeAttachment) Create() *azdiskv1beta2.AzVolumeAttachment {
	// create test az volume
	_ = NewTestAzVolume(t.Azclient, t.Namespace, t.UnderlyingVolume, t.MaxMountReplicaCount)

	// create test az volume attachment
	attName := azureutils.GetAzVolumeAttachmentName(t.UnderlyingVolume, t.PrimaryNodeName)
	att := NewTestAzVolumeAttachment(t.Azclient, attName, t.PrimaryNodeName, t.UnderlyingVolume, t.Namespace)

	return att
}

func (t *TestAzVolumeAttachment) Cleanup() {
	klog.Info("cleaning up")
	err := t.Azclient.DiskV1beta2().AzVolumes(t.Namespace).Delete(context.Background(), t.UnderlyingVolume, metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		framework.ExpectNoError(err)
	}

	// Delete All AzVolumeAttachments for t.UnderlyingVolume
	err = t.Azclient.DiskV1beta2().AzVolumeAttachments(t.Namespace).Delete(context.Background(), azureutils.GetAzVolumeAttachmentName(t.UnderlyingVolume, t.PrimaryNodeName), metav1.DeleteOptions{})
	if !errors.IsNotFound(err) {
		framework.ExpectNoError(err)
	}

	nodes, err := t.Azclient.DiskV1beta2().AzDriverNodes(t.Namespace).List(context.Background(), metav1.ListOptions{})
	if !errors.IsNotFound(err) {
		framework.ExpectNoError(err)
	}
	for _, node := range nodes.Items {
		err = t.Azclient.DiskV1beta2().AzVolumeAttachments(t.Namespace).Delete(context.Background(), azureutils.GetAzVolumeAttachmentName(t.UnderlyingVolume, node.Name), metav1.DeleteOptions{})
		if !errors.IsNotFound(err) {
			framework.ExpectNoError(err)
		}
	}
}

func (t *TestAzVolumeAttachment) WaitForAttach(timeout time.Duration) error {
	conditionFunc := func() (bool, error) {
		att, err := t.Azclient.DiskV1beta2().AzVolumeAttachments(t.Namespace).Get(context.TODO(), azureutils.GetAzVolumeAttachmentName(t.UnderlyingVolume, t.PrimaryNodeName), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if att.Status.Detail != nil {
			klog.Infof("volume (%s) attached to node (%s)", att.Spec.VolumeName, att.Spec.NodeName)
			return true, nil
		}
		return false, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}

func (t *TestAzVolumeAttachment) WaitForFinalizer(timeout time.Duration) error {
	conditionFunc := func() (bool, error) {
		att, err := t.Azclient.DiskV1beta2().AzVolumeAttachments(t.Namespace).Get(context.TODO(), azureutils.GetAzVolumeAttachmentName(t.UnderlyingVolume, t.PrimaryNodeName), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if att.ObjectMeta.Finalizers == nil {
			return false, nil
		}
		for _, finalizer := range att.ObjectMeta.Finalizers {
			if finalizer == consts.AzVolumeAttachmentFinalizer {
				klog.Infof("finalizer (%s) found on AzVolumeAttachment object (%s)", consts.AzVolumeAttachmentFinalizer, att.Name)
				return true, nil
			}
		}
		return false, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}

func (t *TestAzVolumeAttachment) WaitForLabels(timeout time.Duration) error {
	conditionFunc := func() (bool, error) {
		att, err := t.Azclient.DiskV1beta2().AzVolumeAttachments(t.Namespace).Get(context.TODO(), azureutils.GetAzVolumeAttachmentName(t.UnderlyingVolume, t.PrimaryNodeName), metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if att.Labels == nil {
			return false, nil
		}
		if _, ok := att.Labels[consts.NodeNameLabel]; !ok {
			return false, nil
		}
		if _, ok := att.Labels[consts.VolumeNameLabel]; !ok {
			return false, nil
		}
		return true, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}

func (t *TestAzVolumeAttachment) WaitForDelete(nodeName string, timeout time.Duration) error {
	attName := azureutils.GetAzVolumeAttachmentName(t.UnderlyingVolume, nodeName)
	conditionFunc := func() (bool, error) {
		_, err := t.Azclient.DiskV1beta2().AzVolumeAttachments(t.Namespace).Get(context.TODO(), attName, metav1.GetOptions{})
		if errors.IsNotFound(err) {
			klog.Infof("azVolumeAttachment %s not found.", attName)
			return true, nil
		} else if err != nil {
			return false, err
		}
		return false, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}

func (t *TestAzVolumeAttachment) WaitForPrimary(timeout time.Duration) error {
	conditionFunc := func() (bool, error) {
		attachments, err := t.Azclient.DiskV1beta2().AzVolumeAttachments(t.Namespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		for _, attachment := range attachments.Items {
			if attachment.Status.Detail == nil {
				continue
			}
			if attachment.Spec.VolumeName == t.UnderlyingVolume && attachment.Spec.RequestedRole == azdiskv1beta2.PrimaryRole && attachment.Status.Detail.Role == azdiskv1beta2.PrimaryRole {
				return true, nil
			}
		}
		return false, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}

func (t *TestAzVolumeAttachment) WaitForReplicas(numReplica int, timeout time.Duration) error {
	conditionFunc := func() (bool, error) {
		attachments, err := t.Azclient.DiskV1beta2().AzVolumeAttachments(t.Namespace).List(context.TODO(), metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		counter := 0
		for _, attachment := range attachments.Items {
			if attachment.Status.Detail == nil {
				continue
			}
			if attachment.Spec.VolumeName == t.UnderlyingVolume && attachment.Status.Detail.Role == azdiskv1beta2.ReplicaRole {
				counter++
			}
		}
		klog.Infof("%d replica found for volume %s", counter, t.UnderlyingVolume)
		return counter == numReplica, nil
	}
	return wait.PollImmediate(time.Duration(15)*time.Second, timeout, conditionFunc)
}
