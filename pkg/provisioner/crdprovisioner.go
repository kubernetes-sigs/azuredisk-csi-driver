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
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azdiskinformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/watcher"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"
)

type CrdProvisioner struct {
	azDiskClient     azdisk.Interface
	namespace        string
	conditionWatcher *watcher.ConditionWatcher
}

const (
	// TODO: Figure out good interval and timeout values, and make them configurable.
	interval = time.Duration(1) * time.Second
)

func NewCrdProvisioner(kubeConfig *rest.Config, objNamespace string) (*CrdProvisioner, error) {
	diskClient, err := azureutils.GetAzDiskClient(kubeConfig)
	if err != nil {
		return nil, err
	}

	informerFactory := azdiskinformers.NewSharedInformerFactory(diskClient, consts.DefaultInformerResync)

	return &CrdProvisioner{
		azDiskClient:     diskClient,
		namespace:        objNamespace,
		conditionWatcher: watcher.New(context.Background(), diskClient, informerFactory, objNamespace),
	}, nil
}

func (c *CrdProvisioner) RegisterDriverNode(
	ctx context.Context,
	node *v1.Node,
	nodePartition string,
	nodeID string) error {
	azN := c.azDiskClient.DiskV1beta2().AzDriverNodes(c.namespace)
	azDriverNodeFromCache, err := azN.Get(ctx, strings.ToLower(nodeID), metav1.GetOptions{})
	var azDriverNodeUpdate *azdiskv1beta2.AzDriverNode

	if err == nil && azDriverNodeFromCache != nil {
		// We found that the object already exists.
		klog.V(2).Infof("AzDriverNode (%s) exists, will update status. azDriverNodeFromCache=(%v)", nodeID, azDriverNodeFromCache)
		azDriverNodeUpdate = azDriverNodeFromCache.DeepCopy()
	} else if apiErrors.IsNotFound(err) {
		// If AzDriverNode object is not there create it
		klog.V(2).Infof("AzDriverNode (%s) is not registered yet, will create.", nodeID)
		azDriverNodeNew := &azdiskv1beta2.AzDriverNode{
			ObjectMeta: metav1.ObjectMeta{
				Name: strings.ToLower(nodeID),
			},
			Spec: azdiskv1beta2.AzDriverNodeSpec{
				NodeName: nodeID,
			},
		}
		if azDriverNodeNew.Labels == nil {
			azDriverNodeNew.Labels = make(map[string]string)
		}
		azDriverNodeNew.Labels[consts.PartitionLabel] = nodePartition
		klog.V(2).Infof("Creating AzDriverNode with details (%v)", azDriverNodeNew)
		azDriverNodeCreated, err := azN.Create(ctx, azDriverNodeNew, metav1.CreateOptions{})
		if err != nil || azDriverNodeCreated == nil {
			klog.Errorf("Failed to create/update azdrivernode resource for node (%s), error: %v", nodeID, err)
			return err
		}
		azDriverNodeUpdate = azDriverNodeCreated.DeepCopy()
	} else {
		klog.Errorf("Failed to get AzDriverNode for node (%s), error: %v", nodeID, err)
		return apiErrors.NewBadRequest("Failed to get AzDriverNode or node not found, can not register the plugin.")
	}

	// Do an initial update to AzDriverNode status
	if azDriverNodeUpdate.Status == nil {
		azDriverNodeUpdate.Status = &azdiskv1beta2.AzDriverNodeStatus{}
	}
	readyForAllocation := false
	timestamp := metav1.Now()
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
	capacityRange *azdiskv1beta2.CapacityRange,
	volumeCapabilities []azdiskv1beta2.VolumeCapability,
	parameters map[string]string,
	secrets map[string]string,
	volumeContentSource *azdiskv1beta2.ContentVolumeSource,
	accessibilityReq *azdiskv1beta2.TopologyRequirement) (*azdiskv1beta2.AzVolumeStatusDetail, error) {
	var err error
	azVLister := c.conditionWatcher.InformerFactory().Disk().V1beta2().AzVolumes().Lister().AzVolumes(c.namespace)
	azVolumeClient := c.azDiskClient.DiskV1beta2().AzVolumes(c.namespace)

	_, maxMountReplicaCount := azureutils.GetMaxSharesAndMaxMountReplicaCount(parameters, azureutils.HasMultiNodeAzVolumeCapabilityAccessMode(volumeCapabilities))

	// Getting the validVolumeName here since after volume
	// creation the diskURI will consist of the validVolumeName
	volumeName = azureutils.CreateValidDiskName(volumeName, true)
	azVolumeName := strings.ToLower(volumeName)

	ctx, w := workflow.New(ctx, workflow.WithDetails(consts.VolumeNameLabel, volumeName, consts.PvNameKey, parameters[consts.PvNameKey]))
	defer func() { w.Finish(err) }()

	var azVolumeInstance *azdiskv1beta2.AzVolume
	azVolumeInstance, err = azVLister.Get(azVolumeName)
	if err == nil {
		if azVolumeInstance.Status.Detail != nil {
			// If current request has different specifications than the existing volume, return error.
			if !isAzVolumeSpecSameAsRequestParams(azVolumeInstance, maxMountReplicaCount, capacityRange, parameters, secrets, volumeContentSource, accessibilityReq) {
				err = status.Errorf(codes.AlreadyExists, "Volume with name (%s) already exists with different specifications", volumeName)
				return nil, err
			}
			// The volume creation was successful previously,
			// Returning the response object from the status
			return azVolumeInstance.Status.Detail, nil
		}
		// return if volume creation is already in process to prevent duplicate request
		if azVolumeInstance.Status.State == azdiskv1beta2.VolumeCreating {
			err = status.Errorf(codes.Aborted, "creation still in process for volume (%s)", volumeName)
			return nil, err
		}

		w.Logger().V(5).Info("Requeuing CreateVolume request")
		// otherwise requeue operation
		updateFunc := func(obj interface{}) error {
			updateInstance := obj.(*azdiskv1beta2.AzVolume)
			updateInstance.Status.Error = nil
			updateInstance.Status.State = azdiskv1beta2.VolumeOperationPending

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

		if err = azureutils.UpdateCRIWithRetry(ctx, c.conditionWatcher.InformerFactory(), nil, c.azDiskClient, azVolumeInstance, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateAll); err != nil {
			return nil, err
		}
		// if the error was caused by errors other than IsNotFound, return failure
	} else if !apiErrors.IsNotFound(err) {
		err = status.Errorf(codes.Internal, "failed to get AzVolume CRI: %v", err)
		return nil, err
	} else {
		pv, exists := parameters[consts.PvNameKey]
		if !exists {
			w.Logger().Info("CreateVolume request does not contain persistent volume name. Please enable -extra-create-metadata flag in csi-provisioner")
		}
		azVolume := &azdiskv1beta2.AzVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name:        azVolumeName,
				Finalizers:  []string{consts.AzVolumeFinalizer},
				Annotations: map[string]string{consts.RequestIDKey: w.RequestID(), consts.RequestStartimeKey: w.StartTime().Format(consts.RequestTimeFormat)},
			},
			Spec: azdiskv1beta2.AzVolumeSpec{
				MaxMountReplicaCount:      maxMountReplicaCount,
				VolumeName:                volumeName,
				VolumeCapability:          volumeCapabilities,
				CapacityRange:             capacityRange,
				Parameters:                parameters,
				Secrets:                   secrets,
				ContentVolumeSource:       volumeContentSource,
				AccessibilityRequirements: accessibilityReq,
				PersistentVolume:          pv,
			},
		}

		w.Logger().V(5).Info("Creating AzVolume CRI")

		_, err = azVolumeClient.Create(ctx, azVolume, metav1.CreateOptions{})
		if err != nil {
			err = status.Errorf(codes.Internal, "failed to create AzVolume CRI: %v", err)
			return nil, err
		}

		w.Logger().V(5).Info("Successfully created AzVolume CRI")
	}

	var waiter *watcher.ConditionWaiter
	waiter, err = c.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeType, azVolumeName, func(obj interface{}, objectDeleted bool) (bool, error) {
		if obj == nil || objectDeleted {
			return false, nil
		}
		azVolumeInstance := obj.(*azdiskv1beta2.AzVolume)
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

	var obj runtime.Object
	obj, err = waiter.Wait(ctx)
	if obj == nil || err != nil {
		// if the error was due to context deadline exceeding, do not delete AzVolume CRI
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, err
		}
		// if volume creation was unsuccessful, delete the AzVolume CRI and return error
		go func() {
			w.Logger().Info("Deleting volume due to failed volume creation")
			conditionFunc := func() (bool, error) {
				if err := azVolumeClient.Delete(context.Background(), azVolumeName, metav1.DeleteOptions{}); err != nil && !apiErrors.IsNotFound(err) {
					w.Logger().Error(err, "failed to make a delete request for AzVolume CRI")
					return false, nil
				}
				return true, nil
			}
			if err := wait.PollImmediateInfinite(interval, conditionFunc); err != nil {
				w.Logger().Error(err, "failed to delete AzVolume CRI")
			}
		}()
		return nil, err
	}
	azVolumeInstance = obj.(*azdiskv1beta2.AzVolume)

	if azVolumeInstance.Status.Detail == nil {
		// this line should not be reached
		err = status.Errorf(codes.Internal, "failed to create volume (%s)", volumeName)
		return nil, err
	}

	return azVolumeInstance.Status.Detail, nil
}

func (c *CrdProvisioner) DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error {
	// TODO: Since the CRD provisioner needs to the AzVolume name and not the ARM disk URI, it should really
	// return the AzVolume name to the caller as the volume ID. To make this work, we would need to implement
	// snapshot APIs through the CRD provisioner.
	// Replace them in all instances in this file.
	var err error
	lister := c.conditionWatcher.InformerFactory().Disk().V1beta2().AzVolumes().Lister().AzVolumes(c.namespace)
	azVolumeClient := c.azDiskClient.DiskV1beta2().AzVolumes(c.namespace)

	var volumeName string
	volumeName, err = azureutils.GetDiskName(volumeID)
	if err != nil {
		return nil
	}

	azVolumeName := strings.ToLower(volumeName)

	var azVolumeInstance *azdiskv1beta2.AzVolume
	azVolumeInstance, err = lister.Get(azVolumeName)
	ctx, w := workflow.New(ctx, workflow.WithDetails(workflow.GetObjectDetails(azVolumeInstance)...))
	defer func() { w.Finish(err) }()

	if err != nil {
		if apiErrors.IsNotFound(err) {
			w.Logger().Logger.WithValues(consts.VolumeNameLabel, volumeName).Info("Deletion successful: could not find the AzVolume CRI")
			return nil
		}
		w.Logger().Logger.WithValues(consts.VolumeNameLabel, volumeName).Error(err, "failed to get AzVolume CRI")
		return err
	}

	var waiter *watcher.ConditionWaiter
	waiter, err = c.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeType, azVolumeName, func(obj interface{}, objectDeleted bool) (bool, error) {
		// if no object is found, object is deleted
		if obj == nil || objectDeleted {
			return true, nil
		}

		// otherwise, the volume deletion has either failed with error or pending
		azVolumeInstance := obj.(*azdiskv1beta2.AzVolume)
		if azVolumeInstance.Status.Error != nil {
			return false, util.ErrorFromAzError(azVolumeInstance.Status.Error)
		}
		return false, nil
	})

	if err != nil {
		return err
	}
	defer waiter.Close()

	// we don't want to delete pre-provisioned volumes
	if azureutils.MapContains(azVolumeInstance.Status.Annotations, consts.PreProvisionedVolumeAnnotation) {
		w.Logger().Info("AzVolume is pre-provisioned and won't be deleted.")
		return nil
	}

	// if volume deletion already in process, return to prevent duplicate request
	if azVolumeInstance.Status.State == azdiskv1beta2.VolumeDeleting {
		err = status.Errorf(codes.Aborted, "deletion still in process for volume (%s)", volumeName)
		return err
	}

	// otherwise requeue deletion
	updateFunc := func(obj interface{}) error {
		updateInstance := obj.(*azdiskv1beta2.AzVolume)
		w.AnnotateObject(updateInstance)
		updateInstance.Status.Annotations = azureutils.AddToMap(updateInstance.Status.Annotations, consts.VolumeDeleteRequestAnnotation, "cloud-delete-volume")
		// remove deletion failure error from AzVolume CRI to retrigger deletion
		updateInstance.Status.Error = nil
		// revert volume deletion state to avoid confusion
		updateInstance.Status.State = azdiskv1beta2.VolumeCreated

		return nil
	}

	// update AzVolume CRI with annotation and reset state with retry upon conflict
	if err = azureutils.UpdateCRIWithRetry(ctx, c.conditionWatcher.InformerFactory(), nil, c.azDiskClient, azVolumeInstance, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}

	// only make delete request if object's deletion timestamp is not set
	if azVolumeInstance.DeletionTimestamp.IsZero() {
		err = azVolumeClient.Delete(ctx, azVolumeName, metav1.DeleteOptions{})
		if err != nil {
			if apiErrors.IsNotFound(err) {
				return nil
			}

			err = status.Errorf(codes.Internal, "failed to delete AzVolume CRI: %v", err)
			return err
		}
	}

	_, err = waiter.Wait(ctx)
	return err
}

func (c *CrdProvisioner) PublishVolume(
	ctx context.Context,
	volumeID string,
	nodeID string,
	volumeCapability *azdiskv1beta2.VolumeCapability,
	readOnly bool,
	secrets map[string]string,
	volumeContext map[string]string,
) (map[string]string, error) {
	var err error
	azVALister := c.conditionWatcher.InformerFactory().Disk().V1beta2().AzVolumeAttachments().Lister().AzVolumeAttachments(c.namespace)
	nodeLister := c.conditionWatcher.InformerFactory().Disk().V1beta2().AzDriverNodes().Lister().AzDriverNodes(c.namespace)
	volumeLister := c.conditionWatcher.InformerFactory().Disk().V1beta2().AzVolumes().Lister().AzVolumes(c.namespace)
	azVAClient := c.azDiskClient.DiskV1beta2().AzVolumeAttachments(c.namespace)
	var volumeName string
	volumeName, err = azureutils.GetDiskName(volumeID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "Error finding volume : %v", err)
	}
	volumeName = strings.ToLower(volumeName)

	ctx, w := workflow.New(ctx, workflow.WithDetails(consts.VolumeNameLabel, volumeName, consts.NodeNameLabel, nodeID, consts.RoleLabel, azdiskv1beta2.PrimaryRole))
	defer func() { w.Finish(err) }()

	// return error if volume is not found
	var azVolume *azdiskv1beta2.AzVolume
	azVolume, err = volumeLister.Get(volumeName)
	if apiErrors.IsNotFound(err) {
		err = status.Errorf(codes.NotFound, "volume (%s) does not exist: %v", volumeName, err)
		return nil, err
	}

	// return error if node is not found
	if _, err = nodeLister.Get(nodeID); apiErrors.IsNotFound(err) {
		err = status.Errorf(codes.NotFound, "node (%s) does not exist: %v", nodeID, err)
		return nil, err
	}

	if volumeContext == nil {
		volumeContext = map[string]string{}
	}

	publishContext := map[string]string{}

	// preemptively detach any replica volume if necessary
	if azVolume.Spec.MaxMountReplicaCount > 0 {
		// get undemoted AzVolumeAttachments for volume == volumeName and node != nodeID
		volumeLabel, _ := azureutils.CreateLabelRequirements(consts.VolumeNameLabel, selection.Equals, volumeName)
		nodeLabel, _ := azureutils.CreateLabelRequirements(consts.NodeNameLabel, selection.NotEquals, nodeID)
		azVASelector := labels.NewSelector().Add(*volumeLabel, *nodeLabel)

		var attachments []*azdiskv1beta2.AzVolumeAttachment
		attachments, err = azVALister.List(azVASelector)
		if err != nil && !apiErrors.IsNotFound(err) {
			err = status.Errorf(codes.Internal, "failed to list undemoted AzVolumeAttachments with volume (%s) not attached to node (%s): %v", volumeName, nodeID, err)
			return nil, err
		}

		// TODO: come up with a logic to select replica for unpublishing)
		sort.Slice(attachments, func(i, _ int) bool {
			iRole, iExists := attachments[i].Labels[consts.RoleChangeLabel]
			// if i is demoted, prioritize it. Otherwise, prioritize j
			return iExists && iRole == consts.Demoted
		})

		var unpublishOrder []*azdiskv1beta2.AzVolumeAttachment
		// unpublish demoted ones first over undemoted ones
		for i := range attachments {
			// only append if deletionTimestamp not set
			if attachments[i].GetDeletionTimestamp().IsZero() {
				unpublishOrder = append(unpublishOrder, attachments[i])
			}
		}

		// if maxMountReplicaCount has been exceeded, unpublish demoted AzVolumeAttachment or if demoted AzVolumeAttachment does not exist, select one to unpublish
		requiredUnpublishCount := len(unpublishOrder) - azVolume.Spec.MaxMountReplicaCount
		for i := 0; requiredUnpublishCount > 0 && i < len(unpublishOrder); i++ {
			if err = c.detachVolume(ctx, unpublishOrder[i]); err != nil {
				err = status.Errorf(codes.Internal, "failed to make request to unpublish volume (%s) from node (%s): %v", unpublishOrder[i].Spec.VolumeName, unpublishOrder[i].Spec.NodeName, err)
				return nil, err
			}
			requiredUnpublishCount--
		}

	}

	attachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeID)

	var attachmentObj *azdiskv1beta2.AzVolumeAttachment
	attachmentObj, err = azVALister.Get(attachmentName)
	if err != nil {
		if !apiErrors.IsNotFound(err) {
			err = status.Errorf(codes.Internal, "failed to get AzVolumeAttachment CRI: %v", err)
			return nil, err
		}
		// if replica AzVolumeAttachment CRI does not exist for the volume-node pair, create one

		azVolumeAttachment := &azdiskv1beta2.AzVolumeAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name: attachmentName,
				Labels: map[string]string{
					consts.NodeNameLabel:   nodeID,
					consts.VolumeNameLabel: volumeName,
					consts.RoleLabel:       string(azdiskv1beta2.PrimaryRole),
				},
				Finalizers: []string{consts.AzVolumeAttachmentFinalizer},
				Annotations: map[string]string{
					consts.RequestIDKey:       w.RequestID(),
					consts.RequestStartimeKey: w.StartTime().Format(consts.RequestTimeFormat),
				},
			},
			Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
				VolumeName:    volumeName,
				VolumeID:      volumeID,
				NodeName:      nodeID,
				VolumeContext: volumeContext,
				RequestedRole: azdiskv1beta2.PrimaryRole,
			},
		}

		_, err = azVAClient.Create(ctx, azVolumeAttachment, metav1.CreateOptions{})
		if err != nil {
			err = status.Errorf(codes.Internal, "failed to create AzVolumeAttachment CRI: %v", err)
		}
		return publishContext, err
	}

	var updateFunc func(obj interface{}) error
	updateMode := azureutils.UpdateCRIStatus
	// if attachment is scheduled for deletion, new attachment should only be created after the deletion
	if attachmentObj.DeletionTimestamp != nil {
		err = status.Error(codes.Aborted, "need to wait until attachment is fully deleted before attaching")
		return nil, err
	}

	if attachmentObj.Spec.RequestedRole == azdiskv1beta2.PrimaryRole {
		if attachmentObj.Status.Error == nil {
			return publishContext, nil
		}
		// if primary attachment failed with an error, and for whatever reason, controllerPublishVolume request was made instead of NodeStageVolume request, reset the error here if ever reached
		updateFunc = func(obj interface{}) error {
			updateInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
			w.AnnotateObject(updateInstance)
			updateInstance.Status.State = azdiskv1beta2.AttachmentPending
			updateInstance.Status.Error = nil
			return nil
		}
	} else {
		if attachmentObj.Status.Error != nil {
			// or if replica attachment failed with an error, ideally we would want to reset the error to retrigger the attachment here but this exposes the logic to a race between replica controller deleting failed replica attachment and crd provisioner resetting error. So, return error here to wait for the failed replica attachment to be deleted
			err = status.Errorf(codes.Aborted, "replica attachment (%s) failed with an error (%v). Will retry once the attachment is deleted.", attachmentName, attachmentObj.Status.Error)
			return nil, err
		}
		// otherwise, there is a replica attachment for this volume-node pair, so promote it to primary and return success
		updateFunc = func(obj interface{}) error {
			updateInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
			w.AnnotateObject(updateInstance)
			updateInstance.Spec.RequestedRole = azdiskv1beta2.PrimaryRole
			// Keeping the spec fields up to date with the request parameters
			updateInstance.Spec.VolumeContext = volumeContext
			// Update the label of the AzVolumeAttachment
			if updateInstance.Labels == nil {
				updateInstance.Labels = map[string]string{}
			}
			updateInstance.Labels[consts.RoleLabel] = string(azdiskv1beta2.PrimaryRole)
			updateInstance.Labels[consts.RoleChangeLabel] = consts.Promoted

			return nil
		}
		updateMode = azureutils.UpdateAll
	}
	err = azureutils.UpdateCRIWithRetry(ctx, c.conditionWatcher.InformerFactory(), nil, c.azDiskClient, attachmentObj, updateFunc, consts.NormalUpdateMaxNetRetry, updateMode)
	return publishContext, err
}

func (c *CrdProvisioner) WaitForAttach(ctx context.Context, volumeID, nodeID string) (*azdiskv1beta2.AzVolumeAttachment, error) {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	lister := c.conditionWatcher.InformerFactory().Disk().V1beta2().AzVolumeAttachments().Lister().AzVolumeAttachments(c.namespace)

	var volumeName string
	volumeName, err = azureutils.GetDiskName(volumeID)
	if err != nil {
		return nil, err
	}
	volumeName = strings.ToLower(volumeName)
	attachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeID)

	azVolumeAttachmentInstance, err := lister.Get(attachmentName)
	if err != nil {
		err = status.Errorf(codes.Internal, "unexpected error while getting AzVolumeAttachment (%s): %v", attachmentName, err)
		return nil, err
	}

	var waiter *watcher.ConditionWaiter
	waiter, err = c.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeAttachmentType, attachmentName, func(obj interface{}, objectDeleted bool) (bool, error) {
		if obj == nil || objectDeleted {
			return false, nil
		}
		azVolumeAttachmentInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
		if azVolumeAttachmentInstance.Status.Detail != nil && azVolumeAttachmentInstance.Status.Detail.PublishContext != nil && azVolumeAttachmentInstance.Status.Detail.Role == azdiskv1beta2.PrimaryRole {
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

	// If the AzVolumeAttachment is attached, return without triggering informer wait
	if azVolumeAttachmentInstance.Status.Detail != nil && azVolumeAttachmentInstance.Status.Detail.PublishContext != nil && azVolumeAttachmentInstance.Status.Detail.Role == azdiskv1beta2.PrimaryRole {
		return azVolumeAttachmentInstance, nil
	}

	// If the attachment had previously failed with an error, reset the error and state, and retrigger attach
	if azVolumeAttachmentInstance.Status.Error != nil {
		updateFunc := func(obj interface{}) error {
			updateInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
			updateInstance.Status.State = azdiskv1beta2.AttachmentPending
			updateInstance.Status.Error = nil
			return nil
		}
		if err = azureutils.UpdateCRIWithRetry(ctx, c.conditionWatcher.InformerFactory(), nil, c.azDiskClient, azVolumeAttachmentInstance, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
			return nil, err
		}
	}

	obj, err := waiter.Wait(ctx)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		err = status.Errorf(codes.Aborted, "failed to wait for attachment for volume (%s) and node (%s) to complete: unknown error", volumeID, nodeID)
		return nil, err
	}
	azVolumeAttachmentInstance = obj.(*azdiskv1beta2.AzVolumeAttachment)
	if azVolumeAttachmentInstance.Status.Detail == nil {
		err = status.Errorf(codes.Internal, "failed to attach azvolume attachment resource for volume id (%s) to node (%s)", volumeID, nodeID)
		return nil, err
	}
	return azVolumeAttachmentInstance, nil
}

func (c *CrdProvisioner) UnpublishVolume(
	ctx context.Context,
	volumeID string,
	nodeID string,
	secrets map[string]string,
	mode consts.UnpublishMode) error {
	var err error
	azVALister := c.conditionWatcher.InformerFactory().Disk().V1beta2().AzVolumeAttachments().Lister().AzVolumeAttachments(c.namespace)

	var volumeName string
	volumeName, err = azureutils.GetDiskName(volumeID)
	if err != nil {
		return err
	}

	attachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeID)
	volumeName = strings.ToLower(volumeName)

	var azVolumeAttachmentInstance *azdiskv1beta2.AzVolumeAttachment
	azVolumeAttachmentInstance, err = azVALister.Get(attachmentName)
	ctx, w := workflow.New(ctx, workflow.WithDetails(workflow.GetObjectDetails(azVolumeAttachmentInstance)...))
	defer func() { w.Finish(err) }()

	if err != nil {
		if apiErrors.IsNotFound(err) {
			w.Logger().WithValues(consts.VolumeNameLabel, volumeName, consts.NodeNameLabel, nodeID).Info("Volume detach successful")
			err = nil
			return nil
		}
		w.Logger().WithValues(consts.VolumeNameLabel, volumeName, consts.NodeNameLabel, nodeID).Error(err, "failed to get AzVolumeAttachment")
		return err
	}

	if demote, derr := c.shouldDemote(volumeName, mode); derr != nil {
		err = derr
		return err
	} else if demote {
		return c.demoteVolume(ctx, azVolumeAttachmentInstance)
	}
	return c.detachVolume(ctx, azVolumeAttachmentInstance)
}

func (c *CrdProvisioner) shouldDemote(volumeName string, mode consts.UnpublishMode) (bool, error) {
	if mode == consts.Detach {
		return false, nil
	}
	azVolumeInstance, err := c.conditionWatcher.InformerFactory().Disk().V1beta2().AzVolumes().Lister().AzVolumes(c.namespace).Get(volumeName)
	if err != nil {
		return false, err
	}
	// if volume's maxMountReplicaCount == 0, detach and wait for detachment to complete
	// otherwise, demote volume to replica
	return azVolumeInstance.Spec.MaxMountReplicaCount > 0, nil
}

func (c *CrdProvisioner) demoteVolume(ctx context.Context, azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment) error {
	ctx, w := workflow.GetWorkflowFromObj(ctx, azVolumeAttachment)
	w.Logger().V(5).Infof("Requesting AzVolumeAttachment (%s) demotion", azVolumeAttachment.Name)

	updateFunc := func(obj interface{}) error {
		updateInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
		w.AnnotateObject(updateInstance)
		updateInstance.Spec.RequestedRole = azdiskv1beta2.ReplicaRole

		if updateInstance.Labels == nil {
			updateInstance.Labels = map[string]string{}
		}

		updateInstance.Labels[consts.RoleLabel] = string(azdiskv1beta2.ReplicaRole)
		updateInstance.Labels[consts.RoleChangeLabel] = consts.Demoted
		return nil
	}
	return azureutils.UpdateCRIWithRetry(ctx, c.conditionWatcher.InformerFactory(), nil, c.azDiskClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateAll)
}

func (c *CrdProvisioner) detachVolume(ctx context.Context, azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment) error {
	var err error
	attachmentName := azVolumeAttachment.Name
	nodeName := azVolumeAttachment.Spec.NodeName
	volumeID := azVolumeAttachment.Spec.VolumeID
	if err != nil {
		return err
	}

	ctx, w := workflow.GetWorkflowFromObj(ctx, azVolumeAttachment)

	updateFunc := func(obj interface{}) error {
		updateInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
		if updateInstance.Annotations == nil {
			updateInstance.Annotations = map[string]string{}
		}
		w.AnnotateObject(updateInstance)
		updateInstance.Status.Annotations = azureutils.AddToMap(updateInstance.Status.Annotations, consts.VolumeDetachRequestAnnotation, "crdProvisioner")
		return nil
	}
	switch azVolumeAttachment.Status.State {
	case azdiskv1beta2.Detaching:
		// if detachment is pending, return to prevent duplicate request
		return status.Error(codes.Aborted, "detachment still in process")
	case azdiskv1beta2.Attaching:
		// if attachment is still happening in the background async, wait until attachment is complete
		w.Logger().V(5).Info("Attachment is in process... Waiting for the attachment to complete.")
		if azVolumeAttachment, err = c.WaitForAttach(ctx, volumeID, nodeName); err != nil {
			return err
		}
	case azdiskv1beta2.DetachmentFailed:
		innerFunc := updateFunc
		// if detachment failed, reset error and state to retrigger operation
		updateFunc = func(obj interface{}) error {
			_ = innerFunc(obj)
			updateInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)

			// remove detachment failure error from AzVolumeAttachment CRI to retrigger detachment
			updateInstance.Status.Error = nil
			// revert attachment state to avoid confusion
			updateInstance.Status.State = azdiskv1beta2.Attached

			return nil
		}
	}

	w.Logger().V(5).Infof("Requesting AzVolumeAttachment (%s) detachment", azVolumeAttachment.Name)

	if err = azureutils.UpdateCRIWithRetry(ctx, c.conditionWatcher.InformerFactory(), nil, c.azDiskClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}

	// only make delete request if deletionTimestamp is not set
	if azVolumeAttachment.DeletionTimestamp.IsZero() {
		err = c.azDiskClient.DiskV1beta2().AzVolumeAttachments(c.namespace).Delete(ctx, attachmentName, metav1.DeleteOptions{})
		if apiErrors.IsNotFound(err) {
			w.Logger().Infof("Successfully deleted AzVolumeAttachment CRI (%s)", attachmentName)
			return nil
		} else if err != nil {
			err = status.Errorf(codes.Internal, "failed to delete AzVolumeAttachment CRI (%s): %v", attachmentName, err)
			return err
		}
	}
	return c.WaitForDetach(ctx, volumeID, nodeName)
}

func (c *CrdProvisioner) WaitForDetach(ctx context.Context, volumeID, nodeID string) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	var volumeName string
	volumeName, err = azureutils.GetDiskName(volumeID)
	if err != nil {
		return err
	}
	volumeName = strings.ToLower(volumeName)
	attachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeID)

	lister := c.conditionWatcher.InformerFactory().Disk().V1beta2().AzVolumeAttachments().Lister().AzVolumeAttachments(c.namespace)

	var waiter *watcher.ConditionWaiter
	waiter, err = c.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeAttachmentType, attachmentName, func(obj interface{}, objectDeleted bool) (bool, error) {
		// if no object is found, return
		if obj == nil || objectDeleted {
			return true, nil
		}

		// otherwise, the volume detachment has either failed with error or pending
		azVolumeAttachmentInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
		if azVolumeAttachmentInstance.Status.Error != nil {
			return false, util.ErrorFromAzError(azVolumeAttachmentInstance.Status.Error)
		}
		return false, nil
	})

	if err != nil {
		return err
	}
	defer waiter.Close()

	if _, err := lister.Get(attachmentName); apiErrors.IsNotFound(err) {
		return nil
	}

	_, err = waiter.Wait(ctx)
	return err
}

func (c *CrdProvisioner) ExpandVolume(
	ctx context.Context,
	volumeID string,
	capacityRange *azdiskv1beta2.CapacityRange,
	secrets map[string]string) (*azdiskv1beta2.AzVolumeStatusDetail, error) {
	var err error
	lister := c.conditionWatcher.InformerFactory().Disk().V1beta2().AzVolumes().Lister().AzVolumes(c.namespace)

	volumeName, err := azureutils.GetDiskName(volumeID)
	if err != nil {
		return nil, err
	}
	azVolumeName := strings.ToLower(volumeName)

	azVolume, err := lister.Get(volumeName)
	if err != nil || azVolume == nil {
		err = status.Errorf(codes.Internal, "failed to retrieve volume id (%s): %v", volumeID, err)
		return nil, err
	}

	ctx, w := workflow.New(ctx, workflow.WithDetails(workflow.GetObjectDetails(azVolume)...))
	defer func() { w.Finish(err) }()

	var waiter *watcher.ConditionWaiter
	waiter, err = c.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeType, azVolumeName, func(obj interface{}, objectDeleted bool) (bool, error) {
		if obj == nil || objectDeleted {
			return false, nil
		}
		azVolumeInstance := obj.(*azdiskv1beta2.AzVolume)
		// Checking that the status is updated with the required capacityRange
		if azVolumeInstance.Status.Detail != nil && azVolumeInstance.Status.Detail.CapacityBytes == capacityRange.RequiredBytes {
			return true, nil
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

	updateFunc := func(obj interface{}) error {
		updateInstance := obj.(*azdiskv1beta2.AzVolume)
		w.AnnotateObject(updateInstance)
		updateInstance.Spec.CapacityRange = capacityRange
		return nil
	}

	updateMode := azureutils.UpdateCRI

	switch azVolume.Status.State {
	case azdiskv1beta2.VolumeUpdating:
		err = status.Errorf(codes.Aborted, "expand operation still in process")
		return nil, err
	case azdiskv1beta2.VolumeUpdateFailed:
		innerFunc := updateFunc
		updateFunc = func(obj interface{}) error {
			_ = innerFunc(obj)
			updateInstance := obj.(*azdiskv1beta2.AzVolume)
			updateInstance.Status.Error = nil
			updateInstance.Status.State = azdiskv1beta2.VolumeCreated
			return nil
		}
		updateMode = azureutils.UpdateAll
	case azdiskv1beta2.VolumeUpdated:
		break
	case azdiskv1beta2.VolumeCreated:
		break
	default:
		err = status.Errorf(codes.Internal, "unexpected expand volume request: volume is currently in %s state", azVolume.Status.State)
		return nil, err
	}

	if err = azureutils.UpdateCRIWithRetry(ctx, c.conditionWatcher.InformerFactory(), nil, c.azDiskClient, azVolume, updateFunc, consts.NormalUpdateMaxNetRetry, updateMode); err != nil {
		return nil, err
	}

	var obj runtime.Object
	obj, err = waiter.Wait(ctx)
	if err != nil {
		err = status.Errorf(codes.Internal, "failed to update AzVolume CRI: %v", err)
		return nil, err
	}
	if obj == nil {
		err = status.Error(codes.Aborted, "failed to expand volume: unknown error")
		return nil, err
	}

	azVolumeInstance := obj.(*azdiskv1beta2.AzVolume)
	if azVolumeInstance.Status.Detail.CapacityBytes != capacityRange.RequiredBytes {
		err = status.Error(codes.Internal, "AzVolume status not updated with the new capacity")
		return nil, err
	}

	return azVolumeInstance.Status.Detail, nil
}

func (c *CrdProvisioner) GetAzVolumeAttachment(ctx context.Context, volumeID string, nodeID string) (*azdiskv1beta2.AzVolumeAttachment, error) {
	diskName, err := azureutils.GetDiskName(volumeID)
	if err != nil {
		return nil, err
	}
	azVolumeAttachmentName := azureutils.GetAzVolumeAttachmentName(diskName, nodeID)
	var azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment

	if azVolumeAttachment, err = c.conditionWatcher.InformerFactory().Disk().V1beta2().AzVolumeAttachments().Lister().AzVolumeAttachments(c.namespace).Get(azVolumeAttachmentName); err != nil {
		return nil, err
	}

	return azVolumeAttachment, nil
}

func (c *CrdProvisioner) GetDiskClientSet() azdisk.Interface {
	return c.azDiskClient
}

// Compares the fields in the AzVolumeSpec with the other parameters.
// Returns true if they are equal, false otherwise.
func isAzVolumeSpecSameAsRequestParams(defaultAzVolume *azdiskv1beta2.AzVolume,
	maxMountReplicaCount int,
	capacityRange *azdiskv1beta2.CapacityRange,
	parameters map[string]string,
	secrets map[string]string,
	volumeContentSource *azdiskv1beta2.ContentVolumeSource,
	accessibilityReq *azdiskv1beta2.TopologyRequirement) bool {
	// Since, reflect. DeepEqual doesn't treat nil and empty map/array as equal.
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
		defaultAccReq = &azdiskv1beta2.TopologyRequirement{}
	}
	if defaultAccReq.Preferred == nil {
		defaultAccReq.Preferred = []azdiskv1beta2.Topology{}
	}
	if defaultAccReq.Requisite == nil {
		defaultAccReq.Requisite = []azdiskv1beta2.Topology{}
	}
	if accessibilityReq == nil {
		accessibilityReq = &azdiskv1beta2.TopologyRequirement{}
	}
	if accessibilityReq.Preferred == nil {
		accessibilityReq.Preferred = []azdiskv1beta2.Topology{}
	}
	if accessibilityReq.Requisite == nil {
		accessibilityReq.Requisite = []azdiskv1beta2.Topology{}
	}
	if defaultVolContentSource == nil {
		defaultVolContentSource = &azdiskv1beta2.ContentVolumeSource{}
	}
	if volumeContentSource == nil {
		volumeContentSource = &azdiskv1beta2.ContentVolumeSource{}
	}
	if defaultCapRange == nil {
		defaultCapRange = &azdiskv1beta2.CapacityRange{}
	}
	if capacityRange == nil {
		capacityRange = &azdiskv1beta2.CapacityRange{}
	}

	return (defaultAzVolume.Spec.MaxMountReplicaCount == maxMountReplicaCount &&
		reflect.DeepEqual(defaultCapRange, capacityRange) &&
		reflect.DeepEqual(defaultParams, parameters) &&
		reflect.DeepEqual(defaultSecret, secrets) &&
		reflect.DeepEqual(defaultVolContentSource, volumeContentSource) &&
		reflect.DeepEqual(defaultAccReq, accessibilityReq))
}
