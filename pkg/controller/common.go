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

package controller

import (
	"context"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maxRetry = 10
)

type cleanUpMode int

const (
	primaryOnly cleanUpMode = iota
	replicaOnly
	all
)

// TODO Make CloudProvisioner independent of csi types.
type CloudProvisioner interface {
	CreateVolume(
		ctx context.Context,
		volumeName string,
		capacityRange *v1alpha1.CapacityRange,
		volumeCapabilities []v1alpha1.VolumeCapability,
		parameters map[string]string,
		secrets map[string]string,
		volumeContentSource *v1alpha1.ContentVolumeSource,
		accessibilityTopology *v1alpha1.TopologyRequirement) (*v1alpha1.AzVolumeStatusParams, error)
	DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error
	PublishVolume(ctx context.Context, volumeID string, nodeID string, volumeContext map[string]string) (map[string]string, error)
	UnpublishVolume(ctx context.Context, volumeID string, nodeID string) error
	ExpandVolume(ctx context.Context, volumeID string, capacityRange *v1alpha1.CapacityRange, secrets map[string]string) (*v1alpha1.AzVolumeStatusParams, error)
	ListVolumes(ctx context.Context, maxEntries int32, startingToken string) (*v1alpha1.ListVolumesResult, error)
	CreateSnapshot(ctx context.Context, sourceVolumeID string, snapshotName string, secrets map[string]string, parameters map[string]string) (*v1alpha1.Snapshot, error)
	ListSnapshots(ctx context.Context, maxEntries int32, startingToken string, sourceVolumeID string, snapshotID string, secrets map[string]string) (*v1alpha1.ListSnapshotsResult, error)
	DeleteSnapshot(ctx context.Context, snapshotID string, secrets map[string]string) error
	CheckDiskExists(ctx context.Context, diskURI string) error
	GetCloud() *provider.Cloud
	GetMetricPrefix() string
}

func cleanUpAzVolumeAttachmentByVolume(ctx context.Context, client client.Client, azClient azClientSet.Interface, namespace, azVolumeName string, mode cleanUpMode) error {
	volRequirement, _ := labels.NewRequirement(VolumeNameLabel, selection.Equals, []string{azVolumeName})
	labelSelector := labels.NewSelector().Add(*volRequirement)

	attachments, err := azClient.DiskV1alpha1().AzVolumeAttachments(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		klog.Errorf("failed to get AzVolumeAttachments: %v", err)
		return err
	}

	cleanUps := []v1alpha1.AzVolumeAttachment{}
	for _, attachment := range attachments.Items {
		if shouldCleanUp(attachment, mode) {
			cleanUps = append(cleanUps, attachment)
		}
	}

	if err := cleanUpAzVolumeAttachments(ctx, client, azClient, namespace, cleanUps); err != nil {
		return err
	}
	klog.Infof("successfully deleted requested AzVolumeAttachments for AzVolume (%s)", azVolumeName)
	return nil
}

func cleanUpAzVolumeAttachmentByNode(ctx context.Context, client client.Client, azClient azClientSet.Interface, namespace, azDriverNodeName string, mode cleanUpMode) error {
	nodeRequirement, _ := labels.NewRequirement(NodeNameLabel, selection.Equals, []string{azDriverNodeName})
	labelSelector := labels.NewSelector().Add(*nodeRequirement)

	attachments, err := azClient.DiskV1alpha1().AzVolumeAttachments(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		klog.Errorf("failed to get AzVolumeAttachments: %v", err)
		return err
	}

	cleanUps := []v1alpha1.AzVolumeAttachment{}
	for _, attachment := range attachments.Items {
		if shouldCleanUp(attachment, mode) {
			cleanUps = append(cleanUps, attachment)
		}
	}

	if err := cleanUpAzVolumeAttachments(ctx, client, azClient, namespace, cleanUps); err != nil {
		return err
	}
	klog.Infof("successfully deleted AzVolumeAttachments for AzDriverNode (%s)", azDriverNodeName)
	return nil
}

func cleanUpAzVolumeAttachments(ctx context.Context, client client.Client, azClient azClientSet.Interface, namespace string, attachments []v1alpha1.AzVolumeAttachment) error {
	for _, attachment := range attachments {
		if err := azClient.DiskV1alpha1().AzVolumeAttachments(namespace).Delete(ctx, attachment.Name, metav1.DeleteOptions{}); err != nil {
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", attachment.Name, err)
			return err
		}
		klog.V(5).Infof("Set deletion timestamp for AzVolumeAttachment (%s)", attachment.Name)
	}
	return nil
}

func shouldCleanUp(attachment v1alpha1.AzVolumeAttachment, mode cleanUpMode) bool {
	return mode == all || (attachment.Spec.RequestedRole == v1alpha1.PrimaryRole && mode == primaryOnly) || (attachment.Spec.RequestedRole == v1alpha1.ReplicaRole && mode == replicaOnly)
}
