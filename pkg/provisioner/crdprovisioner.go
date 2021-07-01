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
	"reflect"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	volerr "k8s.io/cloud-provider/volume/errors"

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
	azDriverNodeFromCache, err := azN.Get(ctx, strings.ToLower(nodeID), metav1.GetOptions{})
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
	maxShares := 1
	maxMountReplicaCount := 0

	var err error
	for parameter, value := range parameters {
		if strings.EqualFold(azureutils.MaxSharesField, parameter) {
			maxShares, err = strconv.Atoi(value)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s failed with error: %v", value, err))
			}
			if maxShares < 1 {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s returned with invalid value: %d", value, maxShares))
			}
		} else if strings.EqualFold(azureutils.MaxMountReplicaCountField, parameter) {
			maxMountReplicaCount, err = strconv.Atoi(value)
			if err != nil {
				klog.Warningf("setting default value derived from MaxShares: unable to parse MaxMountReplicaCount (%s): %v", value, err)
			}
		}
	}

	// If maxMountReplicaCount > maxShares - 1, its value will be adjusted to maxShares - 1 by defeault
	if maxMountReplicaCount < maxShares-1 {
		maxMountReplicaCount = maxShares - 1
	}

	// Getting the validVolumeName here since after volume
	// creation the diskURI will consist of the validVolumeName
	validVolumeName := azureutils.GetValidDiskName(volumeName)

	azV := c.azDiskClient.DiskV1alpha1().AzVolumes(c.namespace)

	azVolumeInstance, err := azV.Get(ctx, strings.ToLower(validVolumeName), metav1.GetOptions{})

	if err == nil {
		if azVolumeInstance.Status.Detail != nil && azVolumeInstance.Status.Detail.ResponseObject != nil {
			// If current request has different specifications than the existing volume, return error.
			if !isAzVolumeSpecSameAsRequestParams(azVolumeInstance, maxMountReplicaCount, capacityRange, parameters, secrets, volumeContentSource, accessibilityReq) {
				return nil, status.Errorf(codes.AlreadyExists, "Volume with name (%s) already exists with different specifications", volumeName)
			}
			// The volume creation was successful previously,
			// Returning the response object from the status
			return azVolumeInstance.Status.Detail.ResponseObject, nil
		} else if azVolumeInstance.Status.Error != nil {
			updatedInstance := azVolumeInstance.DeepCopy()
			updatedInstance.Status.Error = nil
			updatedInstance.Status.State = v1alpha1.VolumeOperationPending

			if !isAzVolumeSpecSameAsRequestParams(updatedInstance, maxMountReplicaCount, capacityRange, parameters, secrets, volumeContentSource, accessibilityReq) {
				// Updating the spec fields to keep it up to date with the request
				updatedInstance.Spec.MaxMountReplicaCount = maxMountReplicaCount
				updatedInstance.Spec.CapacityRange = capacityRange
				updatedInstance.Spec.Parameters = parameters
				updatedInstance.Spec.Secrets = secrets
				updatedInstance.Spec.ContentVolumeSource = volumeContentSource
				updatedInstance.Spec.AccessibilityRequirements = accessibilityReq
			}

			_, err = azV.Update(ctx, updatedInstance, metav1.UpdateOptions{})
			if err != nil {
				return nil, err
			}
		}
		// if the error was caused by errors other than IsNotFound, return failure
	} else if !errors.IsNotFound(err) {
		klog.Error("failed to get AzVolume (%s): %v", strings.ToLower(validVolumeName), err)
		return nil, err
	} else {
		klog.Infof("Creating a new AzVolume CRI (%s)...", strings.ToLower(validVolumeName))
		azVolume := &v1alpha1.AzVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: strings.ToLower(validVolumeName),
			},
			Spec: v1alpha1.AzVolumeSpec{
				MaxMountReplicaCount:      maxMountReplicaCount,
				UnderlyingVolume:          validVolumeName,
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

		azVolumeInstance, err := azV.Create(ctx, azVolume, metav1.CreateOptions{})
		if err != nil {
			klog.Errorf("Failed to create azvolume resource for volume name (%s), error: %v", volumeName, err)
			return nil, err
		}

		if azVolumeInstance == nil {
			return nil, status.Error(codes.Internal, fmt.Sprintf("Failed to create azvolume resource volume name (%s)", volumeName))
		}

		klog.Infof("Successfully created AzVolume CRI (%s)...", strings.ToLower(validVolumeName))
	}

	conditionFunc := func() (bool, error) {
		azVolumeInstance, err = azV.Get(ctx, strings.ToLower(validVolumeName), metav1.GetOptions{})

		if err != nil {
			return true, err
		}
		if azVolumeInstance.Status.Detail != nil {
			return true, nil
		} else if azVolumeInstance.Status.Error != nil {
			azVolumeError := status.Error(util.GetErrorCodeFromString(azVolumeInstance.Status.Error.ErrorCode), azVolumeInstance.Status.Error.ErrorMessage)
			return true, azVolumeError
		}
		return false, nil
	}

	err = wait.PollImmediate(interval, timeout, conditionFunc)
	if err != nil {
		return nil, err
	}
	if azVolumeInstance == nil || azVolumeInstance.Status.Detail == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("Unable to fetch status of volume created for volume name (%s)", volumeName))
	}

	return azVolumeInstance.Status.Detail.ResponseObject, nil
}

func (c *CrdProvisioner) DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error {
	azV := c.azDiskClient.DiskV1alpha1().AzVolumes(c.namespace)

	// TODO: Since the CRD provisioner needs to the AzVolume name and not the ARM disk URI, it should really
	// return the AzVolume name to the caller as the volume ID. To make this work, we would need to implement
	// snapshot APIs through the CRD provisioner.
	// Replace them in all instances in this file.
	volumeName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		klog.Errorf("Invalid diskURI (%s) for DeleteVolume operation. Error : (%v)", volumeID, err)
		return nil
	}

	azVolumeName := strings.ToLower(volumeName)

	azVolume, err := azV.Get(ctx, azVolumeName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("Could not find the volume name (%s). Deletion succeeded", volumeName)
			return nil
		}
		klog.Infof("failed to get AzVolume (%s): %v", volumeName)
		return err
	}

	// update azVolume with delete annotation
	updated := azVolume.DeepCopy()

	if updated.Annotations == nil {
		updated.Annotations = map[string]string{}
	}
	updated.Annotations[azureutils.VolumeDeleteRequestAnnotation] = "cloud-delete-volume"

	_, err = azV.Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		klog.Infof("failed to update AzVolume (%s) with annotation (%s): %v", volumeName, azureutils.VolumeDeleteRequestAnnotation)
		return err
	}

	err = azV.Delete(ctx, azVolumeName, metav1.DeleteOptions{})
	if err != nil {
		klog.Errorf("Failed to delete azvolume resource for volume id (%s), error: %v", volumeName, err)
		return err
	}

	conditionFunc := func() (bool, error) {
		// Verify if the azVolume is deleted
		azVolume, err := azV.Get(ctx, azVolumeName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return true, nil
			}
			return true, status.Error(codes.Internal, fmt.Sprintf("Failed to delete azvolume resource for volume name (%s)", volumeName))
		} else if azVolume.Status.Error != nil {
			azVolumeError := status.Error(util.GetErrorCodeFromString(azVolume.Status.Error.ErrorCode), azVolume.Status.Error.ErrorMessage)
			return true, azVolumeError
		}
		return false, nil
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
		return nil, status.Errorf(codes.NotFound, fmt.Sprintf("Error finding volume : %v", err))
	}

	attachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeID)

	if volumeContext == nil {
		volumeContext = map[string]string{}
	}
	azVolumeAttachmentInstance, err := azVA.Get(ctx, attachmentName, metav1.GetOptions{})
	if err == nil {
		updated := azVolumeAttachmentInstance.DeepCopy()

		// reset error and state field of the AzVolumeAttachment so that the reconciler can retry attachment
		if updated.Status.Error != nil {
			updated.Status.Error = nil
			updated.Status.State = v1alpha1.AttachmentPending
		}
		// If there already exists a primary attachment with populated responseObject return
		if updated.Status.Detail != nil && updated.Status.Detail.PublishContext != nil && updated.Status.Detail.Role == v1alpha1.PrimaryRole {
			return updated.Status.Detail.PublishContext, nil
		}

		// Otherwise, we are trying to update
		// the AzVolumeAttachment role from Replica to Primary
		updated.Spec.RequestedRole = v1alpha1.PrimaryRole
		// Keeping the spec fields up to date with the request parameters
		updated.Spec.VolumeContext = volumeContext
		_, err := azVA.Update(ctx, updated, metav1.UpdateOptions{})
		if err != nil {
			return nil, err
		}
	} else if !errors.IsNotFound(err) {
		return nil, err
	} else {
		azVolumeAttachment := &v1alpha1.AzVolumeAttachment{
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
			Status: v1alpha1.AzVolumeAttachmentStatus{
				State: v1alpha1.AttachmentPending,
			},
		}

		azVolumeAttachmentInstance, err = azVA.Create(ctx, azVolumeAttachment, metav1.CreateOptions{})
		if err != nil {
			klog.Errorf("Error creating azvolume attachment for volume id (%s) to node id (%s) error : %v", volumeID, nodeID, err)
			return nil, err
		}
	}

	conditionFunc := func() (bool, error) {
		azVolumeAttachmentInstance, err = azVA.Get(ctx, attachmentName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if azVolumeAttachmentInstance.Status.Detail != nil {
			return true, nil
		}
		if azVolumeAttachmentInstance.Status.Error != nil {
			// if dangling attach error
			if azVolumeAttachmentInstance.Status.Error.ErrorCode == util.DanglingAttachErrorCode {
				klog.Errorf("dangling error found from AzVolumeAttachment (%s): %v", attachmentName, azVolumeAttachmentInstance.Status.Error)
				return true, volerr.NewDanglingError(azVolumeAttachmentInstance.Status.Error.ErrorMessage, azVolumeAttachmentInstance.Status.Error.CurrentNode, azVolumeAttachmentInstance.Status.Error.DevicePath)
			}
			azVolumeAttachmentError := status.Error(util.GetErrorCodeFromString(azVolumeAttachmentInstance.Status.Error.ErrorCode), azVolumeAttachmentInstance.Status.Error.ErrorMessage)
			return true, azVolumeAttachmentError
		}
		return false, nil
	}

	err = wait.PollImmediate(interval, timeout, conditionFunc)
	if err != nil {
		klog.Errorf("attachment failed: %v", err)
		return nil, err
	}

	if azVolumeAttachmentInstance.Status.Detail == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("Failed to attach azvolume attachment resource for volume id (%s) to node (%s)", volumeID, nodeID))
	}

	return azVolumeAttachmentInstance.Status.Detail.PublishContext, nil
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

	azVolumeAttachment, err := azVA.Get(ctx, attachmentName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			klog.Infof("AzVolumeAttachment (%s) has already been deleted.", attachmentName)
			return nil
		}
		klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, err)
		return err
	}

	// if AzVolumeAttachment instance indicates that previous attachment request was successful, annotate the CRI with detach request so that the underlying volume attachment can be properly detached.
	if azVolumeAttachment.Status.Detail != nil {
		updated := azVolumeAttachment.DeepCopy()
		if updated.Annotations == nil {
			updated.Annotations = map[string]string{}
		}
		updated.Annotations[azureutils.VolumeDetachRequestAnnotation] = "cloud-detach-volume"
		if _, err = azVA.Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			klog.Errorf("failed to update AzVolumeAttachment (%s) with Annotation (%s): %v", attachmentName, azureutils.VolumeDetachRequestAnnotation, err)
			return err
		}
	}

	err = azVA.Delete(ctx, attachmentName, metav1.DeleteOptions{})
	if errors.IsNotFound(err) {
		klog.Infof("Could not find the volume attachment (%s). Deletion succeeded", attachmentName)
		return nil
	}

	if err != nil {
		klog.Errorf("Failed to delete azvolume attachment resource for volume id (%s) to node (%s), error: %v", volumeID, nodeID, err)
		return err
	}

	conditionFunc := func() (bool, error) {
		// Verify if the azVolume is deleted
		azVolumeAttachment, err := azVA.Get(ctx, attachmentName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				return true, nil
			}
			return true, status.Error(codes.Internal, fmt.Sprintf("Failed to delete azvolume resource attachment for volume name (%s) to node (%s)", volumeID, nodeID))
		} else if azVolumeAttachment.Status.Error != nil {
			azVolumeAttachmentError := status.Error(util.GetErrorCodeFromString(azVolumeAttachment.Status.Error.ErrorCode), azVolumeAttachment.Status.Error.ErrorMessage)
			return true, azVolumeAttachmentError
		}
		return false, nil
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

	azVolume, err := azV.Get(ctx, strings.ToLower(volumeName), metav1.GetOptions{})
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
		azVolumeUpdated, err = azV.Get(ctx, strings.ToLower(volumeName), metav1.GetOptions{})
		if err != nil {
			return true, err
		}
		// Checking that the status is updated with the required capacityRange
		if azVolumeUpdated.Status.Detail != nil {
			if azVolumeUpdated.Status.Detail.ResponseObject != nil && azVolumeUpdated.Status.Detail.ResponseObject.CapacityBytes == capacityRange.RequiredBytes {
				return true, nil
			}
		}
		if azVolumeUpdated.Status.Error != nil {
			azVolumeError := status.Error(util.GetErrorCodeFromString(azVolumeUpdated.Status.Error.ErrorCode), azVolumeUpdated.Status.Error.ErrorMessage)
			return true, azVolumeError
		}
		return false, nil
	}

	err = wait.PollImmediate(interval, timeout, conditionFunc)
	if err != nil {
		return nil, err
	}
	if azVolumeUpdated.Status.Detail.ResponseObject.CapacityBytes != capacityRange.RequiredBytes {
		return nil, status.Error(codes.Internal, fmt.Sprintf("AzVolume status not updated with the new capacity for volume name (%s)", volumeID))
	}

	return azVolumeUpdated.Status.Detail.ResponseObject, nil
}

func (c *CrdProvisioner) GetDiskClientSet() azDiskClientSet.Interface {
	return c.azDiskClient
}

func (c *CrdProvisioner) GetDiskClientSetAddr() *azDiskClientSet.Interface {
	return &c.azDiskClient
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
	if defaultAccReq.Preferred == nil {
		defaultAccReq.Preferred = []v1alpha1.Topology{}
	}
	if defaultAccReq.Requisite == nil {
		defaultAccReq.Requisite = []v1alpha1.Topology{}
	}

	return (defaultAzVolume.Spec.MaxMountReplicaCount == maxMountReplicaCount &&
		reflect.DeepEqual(defaultAzVolume.Spec.CapacityRange, capacityRange) &&
		reflect.DeepEqual(defaultParams, parameters) &&
		reflect.DeepEqual(defaultSecret, secrets) &&
		reflect.DeepEqual(defaultAzVolume.Spec.ContentVolumeSource, volumeContentSource) &&
		reflect.DeepEqual(defaultAccReq, accessibilityReq))
}
