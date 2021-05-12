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

package provisioner

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
)

type CrdProvisioner struct {
	azDiskClient azDiskClientSet.Interface
	namespace    string
}

const (
	// TODO: Figure out good interval and timeout values, and make them configurable.
	interval = time.Duration(1) * time.Second
	timeout  = time.Duration(120) * time.Second
)

func NewCrdProvisioner(kubeConfig *rest.Config, objNamespace string) (*CrdProvisioner, error) {
	diskClient, err := azureutils.GetAzDiskClient(kubeConfig)
	if err != nil {
		return nil, err
	}

	return &CrdProvisioner{
		azDiskClient: diskClient,
		namespace:    objNamespace,
	}, nil
}

func (c *CrdProvisioner) RegisterDriverNode(
	ctx context.Context,
	node *v1.Node,
	nodePartition string,
	nodeID string) error {
	azN := c.azDiskClient.DiskV1alpha1().AzDriverNodes(c.namespace)
	azDriverNodeFromCache, err := azN.Get(ctx, nodeID, metav1.GetOptions{})
	var azDriverNodeUpdate *v1alpha1.AzDriverNode

	if err == nil && azDriverNodeFromCache != nil {
		// We found that the object already exists.
		klog.V(2).Infof("AzDriverNode exists, will update status. azDriverNodeFromCache=(%v)", azDriverNodeFromCache)
		azDriverNodeUpdate = azDriverNodeFromCache.DeepCopy()
	} else if errors.IsNotFound(err) {
		// If AzDriverNode object is not there create it
		klog.Errorf("AzDriverNode is not registered yet, will create. error: %v", err)
		azDriverNodeNew := &v1alpha1.AzDriverNode{
			ObjectMeta: metav1.ObjectMeta{
				Name: nodeID,
			},
			Spec: v1alpha1.AzDriverNodeSpec{
				NodeName: nodeID,
			},
		}
		if azDriverNodeNew.Labels == nil {
			azDriverNodeNew.Labels = make(map[string]string)
		}
		azDriverNodeNew.Labels[azureutils.PartitionLabel] = nodePartition
		klog.V(2).Infof("Creating AzDriverNode with details (%v)", azDriverNodeNew)
		azDriverNodeCreated, err := azN.Create(ctx, azDriverNodeNew, metav1.CreateOptions{})
		if err != nil || azDriverNodeCreated == nil {
			klog.Errorf("Failed to create/update azdrivernode resource for node (%s), error: %v", nodeID, err)
			return err
		}
		azDriverNodeUpdate = azDriverNodeCreated.DeepCopy()
	} else {
		klog.Errorf("Failed to get AzDriverNode for node (%s), error: %v", nodeID, err)
		return errors.NewBadRequest("Failed to get AzDriverNode or node not found, can not register the plugin.")
	}

	// Do an initial update to AzDriverNode status
	if azDriverNodeUpdate.Status == nil {
		azDriverNodeUpdate.Status = &v1alpha1.AzDriverNodeStatus{}
	}
	readyForAllocation := false
	timestamp := time.Now().UnixNano()
	statusMessage := "Driver node initializing."
	azDriverNodeUpdate.Status.ReadyForVolumeAllocation = &readyForAllocation
	azDriverNodeUpdate.Status.LastHeartbeatTime = &timestamp
	azDriverNodeUpdate.Status.StatusMessage = &statusMessage
	klog.V(2).Infof("Updating status for AzDriverNode Status=(%v)", azDriverNodeUpdate)
	_, err = azN.UpdateStatus(ctx, azDriverNodeUpdate, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("Failed to update status of azdrivernode resource for node (%s), error: %v", nodeID, err)
		return err
	}

	return nil
}

func (c *CrdProvisioner) CreateVolume(
	ctx context.Context,
	volumeName string,
	capacityRange *v1alpha1.CapacityRange,
	volumeCapabilities []v1alpha1.VolumeCapability,
	parameters map[string]string,
	secrets map[string]string,
	volumeContentSource *v1alpha1.ContentVolumeSource,
	accessibilityReq *v1alpha1.TopologyRequirement) (*v1alpha1.AzVolumeStatusParams, error) {
	azV := c.azDiskClient.DiskV1alpha1().AzVolumes(c.namespace)
	azVolume := &v1alpha1.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name: volumeName,
		},
		Spec: v1alpha1.AzVolumeSpec{
			UnderlyingVolume:          volumeName,
			VolumeCapability:          volumeCapabilities,
			CapacityRange:             capacityRange,
			Parameters:                parameters,
			Secrets:                   secrets,
			ContentVolumeSource:       volumeContentSource,
			AccessibilityRequirements: accessibilityReq,
		},
	}

	azVolumeCreated, err := azV.Create(ctx, azVolume, metav1.CreateOptions{})
	if err != nil {
		klog.Errorf("Failed to create azvolume resource for volume name (%s), error: %v", volumeName, err)
		return nil, err
	}

	if azVolumeCreated == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("Failed to create azvolume resource volume name (%s)", volumeName))
	}

	conditionFunc := func() (bool, error) {
		azVolumeCreated, err = azV.Get(ctx, volumeName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if azVolumeCreated.Status != nil {
			if azVolume.Status.AzVolumeError != nil {
				azVolumeError := status.Error(util.GetErrorCodeFromString(azVolume.Status.AzVolumeError.ErrorCode), azVolume.Status.AzVolumeError.ErrorMessage)
				return true, azVolumeError
			}
			return true, nil
		}
		return false, nil
	}

	err = wait.PollImmediate(interval, timeout, conditionFunc)
	if err != nil {
		return nil, err
	}
	if azVolumeCreated == nil || azVolumeCreated.Status == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("Unable to fetch status of volume created for volume name (%s)", volumeName))
	}

	return azVolumeCreated.Status.ResponseObject, nil
}

func (c *CrdProvisioner) DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error {
	azV := c.azDiskClient.DiskV1alpha1().AzVolumes(c.namespace)

	// TODO: Since the CRD provisioner needs to the AzVolume name and not the ARM disk URI, it should really
	// return the AzVolume name to the caller as the volume ID. To make this work, we would need to implement
	// snapshot APIs through the CRD provisioner.
	// Replace them in all instances in this file.
	volumeName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		return err
	}

	err = azV.Delete(ctx, volumeName, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		klog.Infof("Could not find the volume name (%s). Deletion succeeded", volumeName)
		return nil
	}

	if err != nil {
		klog.Errorf("Failed to delete azvolume resource for volume id (%s), error: %v", volumeName, err)
		return err
	}

	conditionFunc := func() (bool, error) {
		// Verify if the azVolume is deleted
		_, err := azV.Get(ctx, volumeName, metav1.GetOptions{})
		if err == nil {
			return false, nil
		}
		if errors.IsNotFound(err) {
			return true, nil
		}

		return false, status.Error(codes.Internal, fmt.Sprintf("Failed to delete azvolume resource for volume name (%s)", volumeName))
	}

	return wait.PollImmediate(interval, timeout, conditionFunc)
}

func (c *CrdProvisioner) PublishVolume(
	ctx context.Context,
	volumeID string,
	nodeID string,
	volumeCapability *v1alpha1.VolumeCapability,
	readOnly bool,
	secrets map[string]string,
	volumeContext map[string]string) (map[string]string, error) {
	azVA := c.azDiskClient.DiskV1alpha1().AzVolumeAttachments(c.namespace)
	volumeName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		return nil, err
	}

	attachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeID)

	azVolumeAttachment, err := azVA.Get(ctx, attachmentName, metav1.GetOptions{})
	if err == nil {

		// If there exists an attachment, we are trying to update
		// the AzVolumeAttachment role from Replica to Primary
		azVolumeAttachment.Spec.RequestedRole = v1alpha1.PrimaryRole
		_, err := azVA.Update(ctx, azVolumeAttachment, metav1.UpdateOptions{})
		if err != nil {
			return nil, err
		}

		return azVolumeAttachment.Status.PublishContext, nil
	} else if !errors.IsNotFound(err) {
		return nil, err
	}

	azVolumeAttachment = &v1alpha1.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name: attachmentName,
			Labels: map[string]string{
				"node-name":   nodeID,
				"volume-name": volumeName,
			},
		},
		Spec: v1alpha1.AzVolumeAttachmentSpec{
			UnderlyingVolume: volumeName,
			VolumeID:         volumeID,
			NodeName:         nodeID,
			VolumeContext:    volumeContext,
			RequestedRole:    v1alpha1.PrimaryRole,
		},
	}

	attachmentCreated, err := azVA.Create(ctx, azVolumeAttachment, metav1.CreateOptions{})
	if err != nil {
		klog.Errorf("Error creating azvolume attachment for volume id (%s) to node id (%s) error : %v", volumeID, nodeID, err)
		return nil, err
	}

	conditionFunc := func() (bool, error) {
		attachmentCreated, err = azVA.Get(ctx, attachmentName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if attachmentCreated.Status != nil {
			return true, nil
		}
		return false, nil
	}

	err = wait.PollImmediate(interval, timeout, conditionFunc)
	if err != nil {
		return nil, err
	}
	if attachmentCreated.Status == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("Failed to attach azvolume attachment resource for volume id (%s) to node (%s)", volumeID, nodeID))
	}

	return attachmentCreated.Status.PublishContext, nil
}

func (c *CrdProvisioner) UnpublishVolume(
	ctx context.Context,
	volumeID string,
	nodeID string,
	secrets map[string]string) error {
	azVA := c.azDiskClient.DiskV1alpha1().AzVolumeAttachments(c.namespace)

	volumeName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		return err
	}

	attachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeID)

	err = azVA.Delete(ctx, attachmentName, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		klog.Infof("Could not find the volume attachment (%s). Deletion succedded", volumeID)
		return nil
	}

	if err != nil {
		klog.Errorf("Failed to delete azvolume attachment resource for volume id (%s) to node (%s), error: %v", volumeID, nodeID, err)
		return err
	}

	conditionFunc := func() (bool, error) {
		// Verify if the azVolume is deleted
		_, err = azVA.Get(ctx, attachmentName, metav1.GetOptions{})
		if err == nil {
			return false, nil
		}
		if errors.IsNotFound(err) {
			return true, nil
		}

		return false, status.Error(codes.Internal, fmt.Sprintf("Failed to delete azvolume resource attachment for volume name (%s) to node (%s)", volumeID, nodeID))
	}

	return wait.PollImmediate(interval, timeout, conditionFunc)
}

func (c *CrdProvisioner) ExpandVolume(
	ctx context.Context,
	volumeID string,
	capacityRange *v1alpha1.CapacityRange,
	secrets map[string]string) (*v1alpha1.AzVolumeStatusParams, error) {
	azV := c.azDiskClient.DiskV1alpha1().AzVolumes(c.namespace)

	volumeName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		return nil, err
	}

	azVolume, err := azV.Get(ctx, volumeName, metav1.GetOptions{})
	if err != nil || azVolume == nil {
		klog.Errorf("Failed to retrieve existing volume id (%s)", volumeID)
		return nil, status.Error(codes.Internal, fmt.Sprintf("Failed to retrieve volume id (%s), error: %v", volumeID, err))
	}

	azVolume.Spec.CapacityRange = capacityRange
	azVolumeUpdated, err := azV.Update(ctx, azVolume, metav1.UpdateOptions{})
	if err != nil || azVolumeUpdated == nil {
		klog.Errorf("Failed to update azvolume resource for volume name (%s), error: %v", volumeID, err)
		return nil, err
	}

	conditionFunc := func() (bool, error) {
		azVolumeUpdated, err = azV.Get(ctx, volumeID, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		// Checking that the status is updated with the required capacityRange
		if azVolumeUpdated.Status != nil && azVolumeUpdated.Status.ResponseObject.CapacityBytes == capacityRange.RequiredBytes {
			return true, nil
		}

		return false, nil
	}

	err = wait.PollImmediate(interval, timeout, conditionFunc)
	if err != nil {
		return nil, err
	}
	if azVolumeUpdated.Status.ResponseObject.CapacityBytes != capacityRange.RequiredBytes {
		return nil, status.Error(codes.Internal, fmt.Sprintf("AzVolume status not updated with the new capacity for volume name (%s)", volumeID))
	}

	return azVolumeUpdated.Status.ResponseObject, nil
}

func (c *CrdProvisioner) GetDiskClientSet() azDiskClientSet.Interface {
	return c.azDiskClient
}

func (c *CrdProvisioner) GetDiskClientSetAddr() *azDiskClientSet.Interface {
	return &c.azDiskClient
}
