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
	"reflect"
	"strings"
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
	azurediskInformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
)

type CrdProvisioner struct {
	azDiskClient     azDiskClientSet.Interface
	namespace        string
	conditionWatcher *conditionWatcher
}

const (
	// TODO: Figure out good interval and timeout values, and make them configurable.
	interval       = time.Duration(1) * time.Second
	informerResync = time.Duration(30) * time.Second
)

func NewCrdProvisioner(kubeConfig *rest.Config, objNamespace string) (*CrdProvisioner, error) {
	diskClient, err := azureutils.GetAzDiskClient(kubeConfig)
	if err != nil {
		return nil, err
	}

	informerFactory := azurediskInformers.NewSharedInformerFactory(diskClient, informerResync)

	return &CrdProvisioner{
		azDiskClient:     diskClient,
		namespace:        objNamespace,
		conditionWatcher: newConditionWatcher(context.Background(), diskClient, informerFactory, objNamespace),
	}, nil
}

func (c *CrdProvisioner) RegisterDriverNode(
	ctx context.Context,
	node *v1.Node,
	nodePartition string,
	nodeID string) error {
	azN := c.azDiskClient.DiskV1alpha1().AzDriverNodes(c.namespace)
	azDriverNodeFromCache, err := azN.Get(ctx, strings.ToLower(nodeID), metav1.GetOptions{})
	var azDriverNodeUpdate *v1alpha1.AzDriverNode

	if err == nil && azDriverNodeFromCache != nil {
		// We found that the object already exists.
		klog.V(2).Infof("AzDriverNode (%s) exists, will update status. azDriverNodeFromCache=(%v)", nodeID, azDriverNodeFromCache)
		azDriverNodeUpdate = azDriverNodeFromCache.DeepCopy()
	} else if errors.IsNotFound(err) {
		// If AzDriverNode object is not there create it
		klog.V(2).Infof("AzDriverNode (%s) is not registered yet, will create.", nodeID)
		azDriverNodeNew := &v1alpha1.AzDriverNode{
			ObjectMeta: metav1.ObjectMeta{
				Name: strings.ToLower(nodeID),
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

/*
CreateVolume creates AzVolume CRI to correspond with the given CSI request.
*/
func (c *CrdProvisioner) CreateVolume(
	ctx context.Context,
	volumeName string,
	capacityRange *v1alpha1.CapacityRange,
	volumeCapabilities []v1alpha1.VolumeCapability,
	parameters map[string]string,
	secrets map[string]string,
	volumeContentSource *v1alpha1.ContentVolumeSource,
	accessibilityReq *v1alpha1.TopologyRequirement) (*v1alpha1.AzVolumeStatusParams, error) {

	lister := c.conditionWatcher.informerFactory.Disk().V1alpha1().AzVolumes().Lister().AzVolumes(c.namespace)
	azVolumeClient := c.azDiskClient.DiskV1alpha1().AzVolumes(c.namespace)

	_, maxMountReplicaCount := azureutils.GetMaxSharesAndMaxMountReplicaCount(parameters)

	// Getting the validVolumeName here since after volume
	// creation the diskURI will consist of the validVolumeName
	volumeName = azureutils.GetValidDiskName(volumeName)
	azVolumeName := strings.ToLower(volumeName)

	waiter, err := c.conditionWatcher.newConditionWaiter(ctx, azVolumeType, azVolumeName, func(obj interface{}, _ bool) (bool, error) {
		if obj == nil {
			return false, nil
		}
		azVolumeInstance := obj.(*v1alpha1.AzVolume)
		if azVolumeInstance.Status.Detail != nil {
			return true, nil
		} else if azVolumeInstance.Status.Error != nil {
			return false, util.ErrorFromAzError(azVolumeInstance.Status.Error)
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}
	defer waiter.Close()

	azVolumeInstance, err := lister.Get(azVolumeName)

	if err == nil {
		if azVolumeInstance.Status.Detail != nil && azVolumeInstance.Status.Detail.ResponseObject != nil {
			// If current request has different specifications than the existing volume, return error.
			if !isAzVolumeSpecSameAsRequestParams(azVolumeInstance, maxMountReplicaCount, capacityRange, parameters, secrets, volumeContentSource, accessibilityReq) {
				return nil, status.Errorf(codes.AlreadyExists, "Volume with name (%s) already exists with different specifications", volumeName)
			}
			// The volume creation was successful previously,
			// Returning the response object from the status
			return azVolumeInstance.Status.Detail.ResponseObject, nil
		}
		// return if volume creation is already in process to prevent duplicate request
		if azVolumeInstance.Status.State == v1alpha1.VolumeCreating {
			return nil, status.Errorf(codes.Aborted, "creation still in process for volume (%s)", volumeName)
		}

		// otherwise requeue operation
		updateFunc := func(obj interface{}) error {
			updateInstance := obj.(*v1alpha1.AzVolume)
			updateInstance.Status.Error = nil
			updateInstance.Status.State = v1alpha1.VolumeOperationPending

			if !isAzVolumeSpecSameAsRequestParams(updateInstance, maxMountReplicaCount, capacityRange, parameters, secrets, volumeContentSource, accessibilityReq) {
				// Updating the spec fields to keep it up to date with the request
				updateInstance.Spec.MaxMountReplicaCount = maxMountReplicaCount
				updateInstance.Spec.CapacityRange = capacityRange
				updateInstance.Spec.Parameters = parameters
				updateInstance.Spec.Secrets = secrets
				updateInstance.Spec.ContentVolumeSource = volumeContentSource
				updateInstance.Spec.AccessibilityRequirements = accessibilityReq
			}

			return nil
		}

		if err = azureutils.UpdateCRIWithRetry(ctx, c.conditionWatcher.informerFactory, nil, c.azDiskClient, azVolumeInstance, updateFunc); err != nil {
			klog.Errorf("failed to update AzVolume (%s) with updated spec: %v", azVolumeName, err)
			return nil, err
		}
		// if the error was caused by errors other than IsNotFound, return failure
	} else if !errors.IsNotFound(err) {
		klog.Error("failed to get AzVolume (%s): %v", azVolumeName, err)
		return nil, err
	} else {
		klog.Infof("Creating a new AzVolume CRI (%s)...", azVolumeName)
		azVolume := &v1alpha1.AzVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: azVolumeName,
			},
			Spec: v1alpha1.AzVolumeSpec{
				MaxMountReplicaCount:      maxMountReplicaCount,
				UnderlyingVolume:          volumeName,
				VolumeCapability:          volumeCapabilities,
				CapacityRange:             capacityRange,
				Parameters:                parameters,
				Secrets:                   secrets,
				ContentVolumeSource:       volumeContentSource,
				AccessibilityRequirements: accessibilityReq,
			},
			Status: v1alpha1.AzVolumeStatus{
				State: v1alpha1.VolumeOperationPending,
			},
		}

		azVolumeInstance, err := azVolumeClient.Create(ctx, azVolume, metav1.CreateOptions{})
		if err != nil {
			klog.Errorf("Failed to create azvolume resource for volume name (%s), error: %v", volumeName, err)
			return nil, err
		}

		if azVolumeInstance == nil {
			return nil, status.Errorf(codes.Internal, "Failed to create azvolume resource volume name (%s)", volumeName)
		}

		klog.Infof("Successfully created AzVolume CRI (%s)...", azVolumeName)
	}

	obj, err := waiter.Wait(ctx)
	if obj == nil || err != nil {
		// if volume creation was unsuccessful, delete the AzVolume CRI and return error
		go func() {
			conditionFunc := func() (bool, error) {
				if err := azVolumeClient.Delete(context.Background(), azVolumeName, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
					klog.Errorf("failed to delete AzVolume (%s): %v", azVolumeName, err)
					return false, nil
				}
				return true, nil
			}
			if err := wait.PollImmediateInfinite(interval, conditionFunc); err != nil {
				klog.Errorf("failed to complete AzVolume (%s) deletion: %v", azVolumeName, err)
			}
		}()
		return nil, err
	}
	azVolumeInstance = obj.(*v1alpha1.AzVolume)

	if azVolumeInstance.Status.Detail == nil {
		// this line should not be reached
		return nil, status.Errorf(codes.Internal, "Failed to create azvolume resource for volume (%s)", volumeName)
	}

	return azVolumeInstance.Status.Detail.ResponseObject, nil
}

func (c *CrdProvisioner) DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error {
	// TODO: Since the CRD provisioner needs to the AzVolume name and not the ARM disk URI, it should really
	// return the AzVolume name to the caller as the volume ID. To make this work, we would need to implement
	// snapshot APIs through the CRD provisioner.
	// Replace them in all instances in this file.
	lister := c.conditionWatcher.informerFactory.Disk().V1alpha1().AzVolumes().Lister().AzVolumes(c.namespace)
	azVolumeClient := c.azDiskClient.DiskV1alpha1().AzVolumes(c.namespace)

	volumeName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		klog.Errorf("Error finding volume : %v", err)
		return nil
	}
	azVolumeName := strings.ToLower(volumeName)

	waiter, err := c.conditionWatcher.newConditionWaiter(ctx, azVolumeType, azVolumeName, func(obj interface{}, objectDeleted bool) (bool, error) {
		// if no object is found, object is deleted
		if obj == nil || objectDeleted {
			return true, nil
		}

		// otherwise, the volume deletion has either failed with error or pending
		azVolumeInstance := obj.(*v1alpha1.AzVolume)
		if azVolumeInstance.Status.Error != nil {
			return false, util.ErrorFromAzError(azVolumeInstance.Status.Error)
		}
		return false, nil
	})

	if err != nil {
		return err
	}
	defer waiter.Close()

	azVolumeInstance, err := lister.Get(azVolumeName)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Could not find the volume name (%s). Deletion succeeded", volumeName)
			return nil
		}
		klog.Infof("failed to get AzVolume (%s): %v", azVolumeName)
		return err
	}

	// if volume deletion already in process, return to prevent duplicate request
	if azVolumeInstance.Status.State == v1alpha1.VolumeDeleting {
		return status.Errorf(codes.Aborted, "deletion still in process for volume (%s)", volumeName)
	}

	// otherwise requeue deletion
	updateFunc := func(obj interface{}) error {
		updateInstance := obj.(*v1alpha1.AzVolume)
		if updateInstance.Annotations == nil {
			updateInstance.Annotations = map[string]string{}
		}
		updateInstance.Annotations[azureutils.VolumeDeleteRequestAnnotation] = "cloud-delete-volume"

		// remove deletion failure error from AzVolume CRI to retrigger deletion
		updateInstance.Status.Error = nil
		// revert volume deletion state to avoid confusion
		updateInstance.Status.State = v1alpha1.VolumeCreated

		return nil
	}

	// update AzVolume CRI with annotation and resetted state with retry upon conflict
	if err := azureutils.UpdateCRIWithRetry(ctx, c.conditionWatcher.informerFactory, nil, c.azDiskClient, azVolumeInstance, updateFunc); err != nil {
		return err
	}

	err = azVolumeClient.Delete(ctx, azVolumeName, metav1.DeleteOptions{})
	if err != nil {
		klog.Errorf("Failed to delete azvolume resource for volume id (%s), error: %v", volumeName, err)
		return err
	}

	_, err = waiter.Wait(ctx)
	return err
}

func (c *CrdProvisioner) PublishVolume(
	ctx context.Context,
	volumeID string,
	nodeID string,
	volumeCapability *v1alpha1.VolumeCapability,
	readOnly bool,
	secrets map[string]string,
	volumeContext map[string]string) (map[string]string, error) {

	lister := c.conditionWatcher.informerFactory.Disk().V1alpha1().AzVolumeAttachments().Lister().AzVolumeAttachments(c.namespace)
	azVAClient := c.azDiskClient.DiskV1alpha1().AzVolumeAttachments(c.namespace)

	volumeName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "Error finding volume : %v", err)
	}

	attachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeID)

	waiter, err := c.conditionWatcher.newConditionWaiter(ctx, azVolumeAttachmentType, attachmentName, func(obj interface{}, _ bool) (bool, error) {
		if obj == nil {
			return false, nil
		}
		azVolumeAttachmentInstance := obj.(*v1alpha1.AzVolumeAttachment)
		if azVolumeAttachmentInstance.Status.Detail != nil {
			return true, nil
		}
		if azVolumeAttachmentInstance.Status.Error != nil {
			return false, util.ErrorFromAzError(azVolumeAttachmentInstance.Status.Error)
		}
		return false, nil
	})

	if err != nil {
		return nil, err
	}
	defer waiter.Close()

	if volumeContext == nil {
		volumeContext = map[string]string{}
	}
	azVolumeAttachmentInstance, err := lister.Get(attachmentName)
	if err == nil {
		// if CRI is scheduled for deletion, retry until operations are complete
		if azVolumeAttachmentInstance.DeletionTimestamp != nil {
			return nil, status.Errorf(codes.Aborted, "need to wait until attachment is fully deleted for volume (%s) and node (%s) before attaching", volumeName, nodeID)
		}
		// If there already exists a primary attachment with populated responseObject return
		if azVolumeAttachmentInstance.Status.Detail != nil && azVolumeAttachmentInstance.Status.Detail.PublishContext != nil && azVolumeAttachmentInstance.Status.Detail.Role == v1alpha1.PrimaryRole {
			return azVolumeAttachmentInstance.Status.Detail.PublishContext, nil
			// if attachment is pending return error to prevent redundant attachment request
		} else if azVolumeAttachmentInstance.Status.State == v1alpha1.Attaching {
			return nil, status.Errorf(codes.Aborted, "attachment still in process for volume (%s) and node (%s)", volumeName, nodeID)
		}

		updateFunc := func(obj interface{}) error {
			updateInstance := obj.(*v1alpha1.AzVolumeAttachment)
			if updateInstance.Status.Error != nil {
				// reset error and state field of the AzVolumeAttachment so that the reconciler can retry attachment
				updateInstance.Status.Error = nil
				updateInstance.Status.State = v1alpha1.AttachmentPending
			}

			// Otherwise, we are trying to update
			// the updateInstanceolumeAttachment role from Replica to Primary
			updateInstance.Spec.RequestedRole = v1alpha1.PrimaryRole
			// Keeping the spec fields up to date with the request parameters
			updateInstance.Spec.VolumeContext = volumeContext
			// Update the label of the AzVolumeAttachment
			if updateInstance.Labels == nil {
				updateInstance.Labels = map[string]string{}
			}
			updateInstance.Labels[azureutils.RoleLabel] = string(v1alpha1.PrimaryRole)

			return nil
		}

		if err := azureutils.UpdateCRIWithRetry(ctx, c.conditionWatcher.informerFactory, nil, c.azDiskClient, azVolumeAttachmentInstance, updateFunc); err != nil {
			return nil, err
		}
	} else if !errors.IsNotFound(err) {
		return nil, err
	} else {
		azVolumeAttachment := &v1alpha1.AzVolumeAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name: attachmentName,
				Labels: map[string]string{
					azureutils.NodeNameLabel:   nodeID,
					azureutils.VolumeNameLabel: volumeName,
					azureutils.RoleLabel:       string(v1alpha1.PrimaryRole),
				},
			},
			Spec: v1alpha1.AzVolumeAttachmentSpec{
				UnderlyingVolume: volumeName,
				VolumeID:         volumeID,
				NodeName:         nodeID,
				VolumeContext:    volumeContext,
				RequestedRole:    v1alpha1.PrimaryRole,
			},
			Status: v1alpha1.AzVolumeAttachmentStatus{
				State: v1alpha1.AttachmentPending,
			},
		}

		_, err = azVAClient.Create(ctx, azVolumeAttachment, metav1.CreateOptions{})
		if err != nil {
			klog.Errorf("Error creating azvolume attachment for volume id (%s) to node id (%s) error : %v", volumeID, nodeID, err)
			return nil, err
		}
	}

	obj, err := waiter.Wait(ctx)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, status.Errorf(codes.Aborted, "failed to wait for attachment for volume (%s) and node (%s) to complete: unknown error", volumeName, nodeID)
	}
	azVolumeAttachmentInstance = obj.(*v1alpha1.AzVolumeAttachment)
	if azVolumeAttachmentInstance.Status.Detail == nil {
		return nil, status.Errorf(codes.Internal, "Failed to attach azvolume attachment resource for volume id (%s) to node (%s)", volumeID, nodeID)
	}

	return azVolumeAttachmentInstance.Status.Detail.PublishContext, nil
}

func (c *CrdProvisioner) UnpublishVolume(
	ctx context.Context,
	volumeID string,
	nodeID string,
	secrets map[string]string) error {

	azVAClient := c.azDiskClient.DiskV1alpha1().AzVolumeAttachments(c.namespace)
	lister := c.conditionWatcher.informerFactory.Disk().V1alpha1().AzVolumeAttachments().Lister().AzVolumeAttachments(c.namespace)

	volumeName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		return err
	}
	attachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeID)

	waiter, err := c.conditionWatcher.newConditionWaiter(ctx, azVolumeAttachmentType, attachmentName, func(obj interface{}, objectDeleted bool) (bool, error) {
		// if no object is found, return
		if obj == nil || objectDeleted {
			return true, nil
		}

		// otherwise, the volume detachment has either failed with error or pending
		azVolumeAttachmentInstance := obj.(*v1alpha1.AzVolumeAttachment)
		if azVolumeAttachmentInstance.Status.Error != nil {
			return false, util.ErrorFromAzError(azVolumeAttachmentInstance.Status.Error)
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	defer waiter.Close()

	azVolumeAttachmentInstance, err := lister.Get(attachmentName)
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("AzVolumeAttachment (%s) has already been deleted.", attachmentName)
			return nil
		}
		klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, err)
		return err
	}

	// if AzVolumeAttachment instance indicates that previous attachment request was successful, annotate the CRI with detach request so that the underlying volume attachment can be properly detached.
	if azVolumeAttachmentInstance.Status.Detail != nil {
		// if detachment is pending, return to prevent duplicate request
		if azVolumeAttachmentInstance.Status.State == v1alpha1.Detaching {
			return status.Errorf(codes.Aborted, "detachment still in process for volume (%s) and node (%s)", volumeName, nodeID)
		}
		updateFunc := func(obj interface{}) error {
			updateInstance := obj.(*v1alpha1.AzVolumeAttachment)
			if updateInstance.Annotations == nil {
				updateInstance.Annotations = map[string]string{}
			}
			updateInstance.Annotations[azureutils.VolumeDetachRequestAnnotation] = "crdProvisioner"

			// remove detachment failure error from AzVolumeAttachment CRI to retrigger detachment
			updateInstance.Status.Error = nil
			// revert attachment state to avoid confusion
			updateInstance.Status.State = v1alpha1.Attached

			return nil
		}
		if err = azureutils.UpdateCRIWithRetry(ctx, c.conditionWatcher.informerFactory, nil, c.azDiskClient, azVolumeAttachmentInstance, updateFunc); err != nil {
			klog.Errorf("failed to update AzVolumeAttachment (%s) with Annotation (%s): %v", attachmentName, azureutils.VolumeDetachRequestAnnotation, err)
			return err
		}
	}

	err = azVAClient.Delete(ctx, attachmentName, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		klog.Infof("Could not find the volume attachment (%s). Deletion succeeded", attachmentName)
		return nil
	} else if err != nil {
		klog.Errorf("Failed to delete azvolume attachment resource for volume id (%s) to node (%s), error: %v", volumeID, nodeID, err)
		return err
	}

	_, err = waiter.Wait(ctx)
	return err
}

func (c *CrdProvisioner) ExpandVolume(
	ctx context.Context,
	volumeID string,
	capacityRange *v1alpha1.CapacityRange,
	secrets map[string]string) (*v1alpha1.AzVolumeStatusParams, error) {

	lister := c.conditionWatcher.informerFactory.Disk().V1alpha1().AzVolumes().Lister().AzVolumes(c.namespace)

	volumeName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		return nil, err
	}
	azVolumeName := strings.ToLower(volumeName)

	waiter, err := c.conditionWatcher.newConditionWaiter(ctx, azVolumeType, azVolumeName, func(obj interface{}, _ bool) (bool, error) {
		if obj == nil {
			return false, nil
		}
		azVolumeInstance := obj.(*v1alpha1.AzVolume)
		// Checking that the status is updated with the required capacityRange
		if azVolumeInstance.Status.Detail != nil {
			if azVolumeInstance.Status.Detail.ResponseObject != nil && azVolumeInstance.Status.Detail.ResponseObject.CapacityBytes == capacityRange.RequiredBytes {
				return true, nil
			}
		}
		if azVolumeInstance.Status.Error != nil {
			return false, util.ErrorFromAzError(azVolumeInstance.Status.Error)
		}
		return false, nil
	})
	if err != nil {
		return nil, err
	}

	defer waiter.Close()

	azVolume, err := lister.Get(volumeName)
	if err != nil || azVolume == nil {
		klog.Errorf("Failed to retrieve existing volume id (%s)", volumeID)
		return nil, status.Errorf(codes.Internal, "Failed to retrieve volume id (%s), error: %v", volumeID, err)
	}

	updateFunc := func(obj interface{}) error {
		udpateInstance := obj.(*v1alpha1.AzVolume)
		udpateInstance.Spec.CapacityRange = capacityRange
		return nil
	}

	if err := azureutils.UpdateCRIWithRetry(ctx, c.conditionWatcher.informerFactory, nil, c.azDiskClient, azVolume, updateFunc); err != nil {
		klog.Errorf("Failed to update AzVolume capacity range for volume (%s), error: %v", volumeID, err)
		return nil, err
	}

	obj, err := waiter.Wait(ctx)
	if err != nil {
		klog.Errorf("Failed to update azvolume resource (%s), error: %v", azVolumeName, err)
		return nil, err
	}
	if obj == nil {
		return nil, status.Errorf(codes.Aborted, "failed to volume expansion for volume (%s) to complete: unknown error", volumeName)
	}

	azVolumeInstance := obj.(*v1alpha1.AzVolume)
	if azVolumeInstance.Status.Detail.ResponseObject.CapacityBytes != capacityRange.RequiredBytes {
		return nil, status.Errorf(codes.Internal, "AzVolume status not updated with the new capacity for volume name (%s)", volumeID)
	}

	return azVolumeInstance.Status.Detail.ResponseObject, nil
}

func (c *CrdProvisioner) GetAzVolumeAttachmentState(ctx context.Context, volumeID string, nodeID string) (*v1alpha1.AzVolumeAttachmentAttachmentState, error) {
	diskName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		return nil, err
	}
	azVolumeAttachmentName := azureutils.GetAzVolumeAttachmentName(diskName, nodeID)
	var azVolumeAttachment *v1alpha1.AzVolumeAttachment

	if c.conditionWatcher == nil || c.conditionWatcher.informerFactory == nil {
		if azVolumeAttachment, err = c.azDiskClient.DiskV1alpha1().AzVolumeAttachments(c.namespace).Get(ctx, azVolumeAttachmentName, metav1.GetOptions{}); err != nil {
			return nil, err
		}
	} else {
		if azVolumeAttachment, err = c.conditionWatcher.informerFactory.Disk().V1alpha1().AzVolumeAttachments().Lister().AzVolumeAttachments(c.namespace).Get(azVolumeAttachmentName); err != nil {
			return nil, err
		}
	}

	return &azVolumeAttachment.Status.State, nil
}

func (c *CrdProvisioner) GetDiskClientSet() azDiskClientSet.Interface {
	return c.azDiskClient
}

// Compares the fields in the AzVolumeSpec with the other parameters.
// Returns true if they are equal, false otherwise.
func isAzVolumeSpecSameAsRequestParams(defaultAzVolume *v1alpha1.AzVolume,
	maxMountReplicaCount int,
	capacityRange *v1alpha1.CapacityRange,
	parameters map[string]string,
	secrets map[string]string,
	volumeContentSource *v1alpha1.ContentVolumeSource,
	accessibilityReq *v1alpha1.TopologyRequirement) bool {
	// Since, reflect.DeepEqual doesnt treat nil and empty map/array as equal.
	// For comparison purpose, we want nil and empty map/array as equal.
	// Thus, modifyng the nil values to empty map/array for desired result.
	defaultParams := defaultAzVolume.Spec.Parameters
	defaultSecret := defaultAzVolume.Spec.Secrets
	defaultAccReq := defaultAzVolume.Spec.AccessibilityRequirements
	defaultVolContentSource := defaultAzVolume.Spec.ContentVolumeSource
	defaultCapRange := defaultAzVolume.Spec.CapacityRange
	if defaultParams == nil {
		defaultParams = make(map[string]string)
	}
	if parameters == nil {
		parameters = make(map[string]string)
	}
	if defaultSecret == nil {
		defaultSecret = make(map[string]string)
	}
	if secrets == nil {
		secrets = make(map[string]string)
	}
	if defaultAccReq == nil {
		defaultAccReq = &v1alpha1.TopologyRequirement{}
	}
	if defaultAccReq.Preferred == nil {
		defaultAccReq.Preferred = []v1alpha1.Topology{}
	}
	if defaultAccReq.Requisite == nil {
		defaultAccReq.Requisite = []v1alpha1.Topology{}
	}
	if accessibilityReq == nil {
		accessibilityReq = &v1alpha1.TopologyRequirement{}
	}
	if accessibilityReq.Preferred == nil {
		accessibilityReq.Preferred = []v1alpha1.Topology{}
	}
	if accessibilityReq.Requisite == nil {
		accessibilityReq.Requisite = []v1alpha1.Topology{}
	}
	if defaultVolContentSource == nil {
		defaultVolContentSource = &v1alpha1.ContentVolumeSource{}
	}
	if volumeContentSource == nil {
		volumeContentSource = &v1alpha1.ContentVolumeSource{}
	}
	if defaultCapRange == nil {
		defaultCapRange = &v1alpha1.CapacityRange{}
	}
	if capacityRange == nil {
		capacityRange = &v1alpha1.CapacityRange{}
	}

	return (defaultAzVolume.Spec.MaxMountReplicaCount == maxMountReplicaCount &&
		reflect.DeepEqual(defaultCapRange, capacityRange) &&
		reflect.DeepEqual(defaultParams, parameters) &&
		reflect.DeepEqual(defaultSecret, secrets) &&
		reflect.DeepEqual(defaultVolContentSource, volumeContentSource) &&
		reflect.DeepEqual(defaultAccReq, accessibilityReq))
}
