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

	"github.com/container-storage-interface/spec/lib/go/csi"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	ListVolumes(ctx context.Context, maxEntries int32, startingToken string) (*csi.ListVolumesResponse, error)
	CreateSnapshot(ctx context.Context, sourceVolumeID string, snapshotName string, secrets map[string]string, parameters map[string]string) (*csi.CreateSnapshotResponse, error)
	ListSnapshots(ctx context.Context, maxEntries int32, startingToken string, sourceVolumeID string, snapshotID string, secrets map[string]string) (*csi.ListSnapshotsResponse, error)
	DeleteSnapshot(ctx context.Context, snapshotID string, secrets map[string]string) (*csi.DeleteSnapshotResponse, error)
	CheckDiskExists(ctx context.Context, diskURI string) error
	GetCloud() *provider.Cloud
	GetMetricPrefix() string
}

const (
	DriverName = "disk.csi.azure.com"
)

func CleanUpAzVolumeAttachment(ctx context.Context, client client.Client, azClient azClientSet.Interface, namespace, azVolumeName string) error {
	var azVolume v1alpha1.AzVolume
	err := client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: azVolumeName}, &azVolume)
	if err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", azVolumeName, err)
		return err
	}

	volRequirement, _ := labels.NewRequirement(VolumeNameLabel, selection.Equals, []string{azVolume.Spec.UnderlyingVolume})
	labelSelector := labels.NewSelector().Add(*volRequirement)

	attachments, err := azClient.DiskV1alpha1().AzVolumeAttachments(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil && !errors.IsNotFound(err) {
		klog.Errorf("failed to get AzVolumeAttachments: %v", err)
		return err
	}

	for _, attachment := range attachments.Items {
		if err = azClient.DiskV1alpha1().AzVolumeAttachments(namespace).Delete(ctx, attachment.Name, metav1.DeleteOptions{}); err != nil {
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", attachment.Name, err)
			return err
		}
		klog.V(5).Infof("Set deletion timestamp for AzVolumeAttachment (%s)", attachment.Name)
	}

	klog.Infof("successfully deleted AzVolumeAttachments for AzVolume (%s)", azVolume.Name)
	return nil
}
