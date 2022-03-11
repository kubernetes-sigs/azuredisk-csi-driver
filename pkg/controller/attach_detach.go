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
	"sync"
	"sync/atomic"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	diskv1alpha2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"

	volerr "k8s.io/cloud-provider/volume/errors"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type CloudDiskAttachDetacher interface {
	PublishVolume(ctx context.Context, volumeID string, nodeID string, volumeContext map[string]string) (map[string]string, error)
	UnpublishVolume(ctx context.Context, volumeID string, nodeID string) error
}

type CrdDetacher interface {
	UnpublishVolume(ctx context.Context, volumeID string, nodeID string, secrets map[string]string) error
	WaitForDetach(ctx context.Context, volumeID, nodeID string) error
}

/*
Attach Detach controller is responsible for
	1. attaching volume to a specified node upon creation of AzVolumeAttachment CRI
	2. promoting AzVolumeAttachment to primary upon spec update
	3. detaching volume upon deletions marked with certain annotations
*/
type ReconcileAttachDetach struct {
	crdDetacher           CrdDetacher
	cloudDiskAttacher     CloudDiskAttachDetacher
	controllerSharedState *SharedState
	stateLock             *sync.Map
	retryInfo             *retryInfo
}

var _ reconcile.Reconciler = &ReconcileAttachDetach{}

var allowedTargetAttachmentStates = map[string][]string{
	string(diskv1alpha2.AttachmentPending):  {string(diskv1alpha2.Attaching), string(diskv1alpha2.Detaching)},
	string(diskv1alpha2.Attaching):          {string(diskv1alpha2.Attached), string(diskv1alpha2.AttachmentFailed)},
	string(diskv1alpha2.Detaching):          {string(diskv1alpha2.Detached), string(diskv1alpha2.DetachmentFailed)},
	string(diskv1alpha2.Attached):           {string(diskv1alpha2.Detaching)},
	string(diskv1alpha2.AttachmentFailed):   {string(diskv1alpha2.Detaching)},
	string(diskv1alpha2.DetachmentFailed):   {string(diskv1alpha2.ForceDetachPending)},
	string(diskv1alpha2.ForceDetachPending): {string(diskv1alpha2.Detaching)},
}

func (r *ReconcileAttachDetach) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	azVolumeAttachment, err := azureutils.GetAzVolumeAttachment(ctx, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, request.Name, request.Namespace, true)
	// if object is not found, it means the object has been deleted. Log the deletion and do not requeue
	if errors.IsNotFound(err) {
		return reconcileReturnOnSuccess(request.Name, r.retryInfo)
	} else if err != nil {
		azVolumeAttachment.Name = request.Name
		return reconcileReturnOnError(azVolumeAttachment, "get", err, r.retryInfo)
	}

	// if underlying cloud operation already in process, skip until operation is completed
	if isOperationInProcess(azVolumeAttachment) {
		klog.V(5).Infof("Another operation (%s) is already in process for the AzVolumeAttachment (%s). Will be requeued once complete.", azVolumeAttachment.Status.State, azVolumeAttachment.Name)
		return reconcileReturnOnSuccess(azVolumeAttachment.Name, r.retryInfo)
	}

	// detachment request
	if objectDeletionRequested(azVolumeAttachment) {
		if err := r.triggerDetach(ctx, azVolumeAttachment); err != nil {
			return reconcileReturnOnError(azVolumeAttachment, "detach", err, r.retryInfo)
		}
		// attachment request
	} else if azVolumeAttachment.Status.Detail == nil {
		if err := r.triggerAttach(ctx, azVolumeAttachment); err != nil {
			return reconcileReturnOnError(azVolumeAttachment, "attach", err, r.retryInfo)
		}
		// promotion request
	} else if azVolumeAttachment.Spec.RequestedRole != azVolumeAttachment.Status.Detail.Role {
		switch azVolumeAttachment.Spec.RequestedRole {
		case diskv1alpha2.PrimaryRole:
			if err := r.promote(ctx, azVolumeAttachment); err != nil {
				return reconcileReturnOnError(azVolumeAttachment, "promote", err, r.retryInfo)
			}
		case diskv1alpha2.ReplicaRole:
			if err := r.demote(ctx, azVolumeAttachment); err != nil {
				return reconcileReturnOnError(azVolumeAttachment, "demote", err, r.retryInfo)
			}
		}
	}

	return reconcileReturnOnSuccess(azVolumeAttachment.Name, r.retryInfo)
}

func (r *ReconcileAttachDetach) triggerAttach(ctx context.Context, azVolumeAttachment *diskv1alpha2.AzVolumeAttachment) error {
	// requeue if AzVolumeAttachment's state is being updated by a different worker
	defer r.stateLock.Delete(azVolumeAttachment.Name)
	if _, ok := r.stateLock.LoadOrStore(azVolumeAttachment.Name, nil); ok {
		return getOperationRequeueError("attach", azVolumeAttachment)
	}

	// initialize metadata and update status block
	updateFunc := func(obj interface{}) error {
		azv := obj.(*diskv1alpha2.AzVolumeAttachment)
		// Update state to attaching, Initialize finalizer and add label to the object
		azv = r.initializeMeta(azv)
		_, derr := updateState(azv, diskv1alpha2.Attaching, normalUpdate)
		return derr
	}
	if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry); err != nil {
		return err
	}

	klog.Infof("Attaching volume (%s) to node (%s)", azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName)

	// TODO reassess if adding additional finalizer to AzVolume CRI is necessary
	// // add finalizer to the bound AzVolume CRI
	// conditionFunc = func() (bool, error) {
	// 	if err := r.addFinalizerToAzVolume(ctx, azVolumeAttachment.Spec.UnderlyingVolume); err != nil {
	// 		return false, nil
	// 	}
	// 	return true, nil
	// }
	// if err := wait.PollImmediate(updateAttemptInterval, updateTimeout, conditionFunc); err != nil {
	// 	return err
	// }

	// initiate goroutine to attach volume
	//nolint:contextcheck // Attach is performed asynchronously by the reconciler; context is not inherited by design
	go func() {
		cloudCtx, cloudCancel := context.WithTimeout(context.Background(), cloudTimeout)
		defer cloudCancel()

		// attempt to attach the disk to a node
		response, attachErr := r.attachVolume(cloudCtx, azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Spec.VolumeContext)
		// if the disk is attached to a different node
		if danglingAttachErr, ok := attachErr.(*volerr.DanglingAttachError); ok {
			// get disk, current node and attachment name
			diskName, err := azureutils.GetDiskName(azVolumeAttachment.Spec.VolumeID)
			if err != nil {
				klog.Warningf("failed to extract disk name from volumeID (%s): %v", azVolumeAttachment.Spec.VolumeID, err)
			}
			currentNodeName := string(danglingAttachErr.CurrentNode)
			currentAttachmentName := azureutils.GetAzVolumeAttachmentName(diskName, currentNodeName)

			// check if AzVolumeAttachment exists for the existing attachment
			_, err = r.controllerSharedState.azClient.DiskV1alpha2().AzVolumeAttachments(r.controllerSharedState.objectNamespace).Get(cloudCtx, currentAttachmentName, metav1.GetOptions{})
			var detachErr error
			if errors.IsNotFound(err) {
				// AzVolumeAttachment doesn't exist so we only need to detach disk from cloud
				detachErr = r.cloudDiskAttacher.UnpublishVolume(ctx, azVolumeAttachment.Spec.VolumeID, currentNodeName)
				if detachErr != nil {
					klog.Errorf("failed to detach volume (%s) from node (%s). Error: %v", azVolumeAttachment.Spec.VolumeID, currentNodeName, err)
				}
			} else {
				// AzVolumeAttachment exist so we need to detach disk through crdProvisioner
				detachErr = r.crdDetacher.UnpublishVolume(ctx, azVolumeAttachment.Spec.VolumeID, currentNodeName, make(map[string]string))
				if detachErr != nil {
					klog.Errorf("failed to make a unpublish request AzVolumeAttachment for volume (%s) and node (%s). Error: %v", azVolumeAttachment.Spec.VolumeID, currentNodeName, err)
				}
				detachErr = r.crdDetacher.WaitForDetach(ctx, azVolumeAttachment.Spec.VolumeID, currentNodeName)
				if detachErr != nil {
					klog.Errorf("failed to unpublish AzVolumeAttachment for volume (%s) and node (%s). Error: %v", azVolumeAttachment.Spec.VolumeID, currentNodeName, err)
				}
			}
			// attempt to attach the disk to a node after detach
			if detachErr == nil {
				response, attachErr = r.attachVolume(cloudCtx, azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Spec.VolumeContext)
			}
		}
		var pods []v1.Pod
		if azVolumeAttachment.Spec.RequestedRole == diskv1alpha2.ReplicaRole {
			var err error
			pods, err = r.controllerSharedState.getPodsFromVolume(ctx, r.controllerSharedState.cachedClient, azVolumeAttachment.Spec.VolumeName)
			if err != nil {
				klog.Infof("failed to get pods for volume (%s). Error: %v", azVolumeAttachment.Spec.VolumeName, err)
			}
		}

		// update AzVolumeAttachment CRI with the result of the attach operation
		var updateFunc func(interface{}) error
		if attachErr != nil {
			klog.Errorf("failed to attach volume %s to node %s: %v", azVolumeAttachment.Spec.VolumeName, azVolumeAttachment.Spec.NodeName, attachErr)

			if len(pods) > 0 {
				for _, pod := range pods {
					r.controllerSharedState.eventRecorder.Eventf(pod.DeepCopyObject(), v1.EventTypeWarning, consts.ReplicaAttachmentFailedEvent, "Replica mount for volume %s failed to be attached to node %s with error: %v", azVolumeAttachment.Spec.VolumeName, azVolumeAttachment.Spec.NodeName, attachErr)
				}
			}

			updateFunc = func(obj interface{}) error {
				azv := obj.(*diskv1alpha2.AzVolumeAttachment)
				azv = updateError(azv, attachErr)
				_, uerr := updateState(azv, diskv1alpha2.AttachmentFailed, forceUpdate)
				return uerr
			}
		} else {
			klog.Infof("successfully attached volume (%s) to node (%s) and update status of AzVolumeAttachment (%s)", azVolumeAttachment.Spec.VolumeName, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Name)

			// Publish event to indicate attachment success
			if len(pods) > 0 {
				for _, pod := range pods {
					r.controllerSharedState.eventRecorder.Eventf(pod.DeepCopyObject(), v1.EventTypeNormal, consts.ReplicaAttachmentSuccessEvent, "Replica mount for volume %s successfully attached to node %s", azVolumeAttachment.Spec.VolumeName, azVolumeAttachment.Spec.NodeName)
				}
			}

			updateFunc = func(obj interface{}) error {
				azv := obj.(*diskv1alpha2.AzVolumeAttachment)
				azv = updateStatusDetail(azv, response)
				_, uerr := updateState(azv, diskv1alpha2.Attached, forceUpdate)
				return uerr
			}
		}

		// UpdateCRIWithRetry should be called on a context w/o timeout when called in a separate goroutine as it is not going to be retriggered and leave the CRI in unrecoverable transient state instead.
		updateCtx := context.Background()
		_ = azureutils.UpdateCRIWithRetry(updateCtx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.ForcedUpdateMaxNetRetry)
	}()

	return nil
}

func (r *ReconcileAttachDetach) triggerDetach(ctx context.Context, azVolumeAttachment *diskv1alpha2.AzVolumeAttachment) error {
	// only detach if detachment request was made for underlying volume attachment object
	detachmentRequested := volumeDetachRequested(azVolumeAttachment)

	if detachmentRequested {
		defer r.stateLock.Delete(azVolumeAttachment.Name)
		if _, ok := r.stateLock.LoadOrStore(azVolumeAttachment.Name, nil); ok {
			return getOperationRequeueError("detach", azVolumeAttachment)
		}

		updateFunc := func(obj interface{}) error {
			azv := obj.(*diskv1alpha2.AzVolumeAttachment)
			// Update state to detaching
			_, derr := updateState(azv, diskv1alpha2.Detaching, normalUpdate)
			return derr
		}
		if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry); err != nil {
			return err
		}

		klog.Infof("Detaching volume (%s) from node (%s)", azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName)

		go func() {
			cloudCtx, cloudCancel := context.WithTimeout(context.Background(), cloudTimeout)
			defer cloudCancel()

			var updateFunc func(obj interface{}) error
			err := r.detachVolume(cloudCtx, azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName)
			if err != nil {
				updateFunc = func(obj interface{}) error {
					azv := obj.(*diskv1alpha2.AzVolumeAttachment)
					azv = updateError(azv, err)
					_, derr := updateState(azv, diskv1alpha2.DetachmentFailed, forceUpdate)
					return derr
				}
			} else {
				updateFunc = func(obj interface{}) error {
					azv := obj.(*diskv1alpha2.AzVolumeAttachment)
					azv = r.deleteFinalizer(azv)
					_, derr := updateState(azv, diskv1alpha2.Detached, forceUpdate)
					return derr
				}
			}

			// UpdateCRIWithRetry should be called on a context w/o timeout when called in a separate goroutine as it is not going to be retriggered and leave the CRI in unrecoverable transient state instead.
			updateCtx := context.Background()
			_ = azureutils.UpdateCRIWithRetry(updateCtx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.ForcedUpdateMaxNetRetry)
		}()
	} else {
		updateFunc := func(obj interface{}) error {
			azv := obj.(*diskv1alpha2.AzVolumeAttachment)
			// delete finalizer
			_ = r.deleteFinalizer(azv)
			return nil
		}
		if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry); err != nil {
			return err
		}
	}
	return nil
}

func (r *ReconcileAttachDetach) promote(ctx context.Context, azVolumeAttachment *diskv1alpha2.AzVolumeAttachment) error {
	klog.Infof("Promoting volume attachment (%s) for volume (%s) on node (%s) from %s to Primary",
		azVolumeAttachment.Name, azVolumeAttachment.Spec.VolumeName, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Status.Detail.Role)

	// initialize metadata and update status block
	updateFunc := func(obj interface{}) error {
		azv := obj.(*diskv1alpha2.AzVolumeAttachment)
		_ = updateRole(azv, diskv1alpha2.PrimaryRole)
		return nil
	}
	return azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry)
}

func (r *ReconcileAttachDetach) demote(ctx context.Context, azVolumeAttachment *diskv1alpha2.AzVolumeAttachment) error {
	klog.Infof("Demoting volume attachment (%s) for volume (%s) on node (%s) from %s to Replica",
		azVolumeAttachment.Name, azVolumeAttachment.Spec.VolumeName, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Status.Detail.Role)

	// initialize metadata and update status block
	updateFunc := func(obj interface{}) error {
		azv := obj.(*diskv1alpha2.AzVolumeAttachment)
		_ = updateRole(azv, diskv1alpha2.ReplicaRole)
		return nil
	}
	return azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry)
}

func (r *ReconcileAttachDetach) initializeMeta(azVolumeAttachment *diskv1alpha2.AzVolumeAttachment) *diskv1alpha2.AzVolumeAttachment {
	if azVolumeAttachment == nil {
		return nil
	}

	// if the required metadata already exists return
	if finalizerExists(azVolumeAttachment.Finalizers, consts.AzVolumeAttachmentFinalizer) &&
		labelExists(azVolumeAttachment.Labels, consts.NodeNameLabel) &&
		labelExists(azVolumeAttachment.Labels, consts.VolumeNameLabel) &&
		labelExists(azVolumeAttachment.Labels, consts.RoleLabel) {
		return azVolumeAttachment
	}

	// add finalizer
	if azVolumeAttachment.Finalizers == nil {
		azVolumeAttachment.Finalizers = []string{}
	}

	if !finalizerExists(azVolumeAttachment.Finalizers, consts.AzVolumeAttachmentFinalizer) {
		azVolumeAttachment.Finalizers = append(azVolumeAttachment.Finalizers, consts.AzVolumeAttachmentFinalizer)
	}

	// add label
	if azVolumeAttachment.Labels == nil {
		azVolumeAttachment.Labels = make(map[string]string)
	}
	azVolumeAttachment.Labels[consts.NodeNameLabel] = azVolumeAttachment.Spec.NodeName
	azVolumeAttachment.Labels[consts.VolumeNameLabel] = azVolumeAttachment.Spec.VolumeName
	azVolumeAttachment.Labels[consts.RoleLabel] = string(azVolumeAttachment.Spec.RequestedRole)

	return azVolumeAttachment
}

func (r *ReconcileAttachDetach) deleteFinalizer(azVolumeAttachment *diskv1alpha2.AzVolumeAttachment) *diskv1alpha2.AzVolumeAttachment {
	if azVolumeAttachment == nil {
		return nil
	}

	if azVolumeAttachment.ObjectMeta.Finalizers == nil {
		return azVolumeAttachment
	}

	finalizers := []string{}
	for _, finalizer := range azVolumeAttachment.ObjectMeta.Finalizers {
		if finalizer == consts.AzVolumeAttachmentFinalizer {
			continue
		}
		finalizers = append(finalizers, finalizer)
	}
	azVolumeAttachment.ObjectMeta.Finalizers = finalizers
	return azVolumeAttachment
}

func (r *ReconcileAttachDetach) attachVolume(ctx context.Context, volumeID, node string, volumeContext map[string]string) (map[string]string, error) {
	return r.cloudDiskAttacher.PublishVolume(ctx, volumeID, node, volumeContext)
}

func (r *ReconcileAttachDetach) detachVolume(ctx context.Context, volumeID, node string) error {
	return r.cloudDiskAttacher.UnpublishVolume(ctx, volumeID, node)
}

func (r *ReconcileAttachDetach) Recover(ctx context.Context) error {
	klog.Info("Recovering AzVolumeAttachment CRIs...")
	// try to recreate missing AzVolumeAttachment CRI
	var syncedVolumeAttachments, volumesToSync map[string]bool
	var err error

	for i := 0; i < maxRetry; i++ {
		if syncedVolumeAttachments, volumesToSync, err = r.recreateAzVolumeAttachment(ctx, syncedVolumeAttachments, volumesToSync); err == nil {
			break
		}
		klog.Warningf("failed to recreate missing AzVolumeAttachment CRI: %v", err)
	}
	// retrigger any aborted operation from possible previous controller crash
	recovered := &sync.Map{}
	for i := 0; i < maxRetry; i++ {
		if err = r.recoverAzVolumeAttachment(ctx, recovered); err == nil {
			break
		}
		klog.Warningf("failed to recover AzVolumeAttachment state: %v", err)
	}

	return err
}

func updateRole(azVolumeAttachment *diskv1alpha2.AzVolumeAttachment, role diskv1alpha2.Role) *diskv1alpha2.AzVolumeAttachment {
	if azVolumeAttachment == nil {
		return nil
	}

	if azVolumeAttachment.Labels == nil {
		azVolumeAttachment.Labels = map[string]string{}
	}
	azVolumeAttachment.Labels[consts.RoleLabel] = string(role)

	if azVolumeAttachment.Status.Detail == nil {
		return azVolumeAttachment
	}

	azVolumeAttachment.Status.Detail.PreviousRole = azVolumeAttachment.Status.Detail.Role
	azVolumeAttachment.Status.Detail.Role = role

	if azVolumeAttachment.Status.Detail.PreviousRole == diskv1alpha2.PrimaryRole && azVolumeAttachment.Status.Detail.Role == diskv1alpha2.ReplicaRole {
		azVolumeAttachment.Labels[consts.RoleChangeLabel] = consts.Demoted
	} else if azVolumeAttachment.Status.Detail.PreviousRole == diskv1alpha2.ReplicaRole && azVolumeAttachment.Status.Detail.Role == diskv1alpha2.PrimaryRole {
		azVolumeAttachment.Labels[consts.RoleChangeLabel] = consts.Promoted
	}

	return azVolumeAttachment
}

func updateStatusDetail(azVolumeAttachment *diskv1alpha2.AzVolumeAttachment, status map[string]string) *diskv1alpha2.AzVolumeAttachment {
	if azVolumeAttachment == nil {
		return nil
	}

	if azVolumeAttachment.Status.Detail == nil {
		azVolumeAttachment.Status.Detail = &diskv1alpha2.AzVolumeAttachmentStatusDetail{}
	}

	azVolumeAttachment.Status.Detail.PreviousRole = azVolumeAttachment.Status.Detail.Role
	azVolumeAttachment.Status.Detail.Role = azVolumeAttachment.Spec.RequestedRole
	azVolumeAttachment.Status.Detail.PublishContext = status

	return azVolumeAttachment
}

func updateError(azVolumeAttachment *diskv1alpha2.AzVolumeAttachment, err error) *diskv1alpha2.AzVolumeAttachment {
	if azVolumeAttachment == nil {
		return nil
	}

	if err != nil {
		azVolumeAttachment.Status.Error = util.NewAzError(err)
	}

	return azVolumeAttachment
}

func updateState(azVolumeAttachment *diskv1alpha2.AzVolumeAttachment, state diskv1alpha2.AzVolumeAttachmentAttachmentState, mode updateMode) (*diskv1alpha2.AzVolumeAttachment, error) {
	var err error
	if azVolumeAttachment == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "function `updateState` requires non-nil AzVolumeAttachment object.")
	}
	if mode == normalUpdate {
		expectedStates := allowedTargetAttachmentStates[string(azVolumeAttachment.Status.State)]
		if !containsString(string(state), expectedStates) {
			err = status.Error(codes.FailedPrecondition, formatUpdateStateError("azVolume", string(azVolumeAttachment.Status.State), string(state), expectedStates...))
		}
	}
	if err == nil {
		azVolumeAttachment.Status.State = state
	}
	return azVolumeAttachment, err
}

func (r *ReconcileAttachDetach) recreateAzVolumeAttachment(ctx context.Context, syncedVolumeAttachments map[string]bool, volumesToSync map[string]bool) (map[string]bool, map[string]bool, error) {
	// Get all volumeAttachments
	volumeAttachments, err := r.controllerSharedState.kubeClient.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
	if err != nil {
		return syncedVolumeAttachments, volumesToSync, err
	}

	if syncedVolumeAttachments == nil {
		syncedVolumeAttachments = map[string]bool{}
	}
	if volumesToSync == nil {
		volumesToSync = map[string]bool{}
	}

	// Loop through volumeAttachments and create Primary AzVolumeAttachments in correspondence
	for _, volumeAttachment := range volumeAttachments.Items {
		// skip if sync has been completed volumeAttachment
		if syncedVolumeAttachments[volumeAttachment.Name] {
			continue
		}
		if volumeAttachment.Spec.Attacher == r.controllerSharedState.driverName {
			volumeName := volumeAttachment.Spec.Source.PersistentVolumeName
			if volumeName == nil {
				continue
			}
			// get PV and retrieve diskName
			pv, err := r.controllerSharedState.kubeClient.CoreV1().PersistentVolumes().Get(ctx, *volumeName, metav1.GetOptions{})
			if err != nil {
				klog.Errorf("failed to get PV (%s): %v", *volumeName, err)
				return syncedVolumeAttachments, volumesToSync, err
			}

			if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != r.controllerSharedState.driverName {
				continue
			}
			volumesToSync[pv.Spec.CSI.VolumeHandle] = true

			diskName, err := azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
			if err != nil {
				klog.Warningf("failed to extract disk name from volumehandle (%s): %v", pv.Spec.CSI.VolumeHandle, err)
				delete(volumesToSync, pv.Spec.CSI.VolumeHandle)
				continue
			}
			nodeName := volumeAttachment.Spec.NodeName
			azVolumeAttachmentName := azureutils.GetAzVolumeAttachmentName(diskName, nodeName)

			// check if the CRI exists already
			_, err = azureutils.GetAzVolumeAttachment(ctx, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachmentName, r.controllerSharedState.objectNamespace, false)
			if errors.IsNotFound(err) {
				klog.Infof("Recreating AzVolumeAttachment(%s)", azVolumeAttachmentName)
				azVolumeAttachment := diskv1alpha2.AzVolumeAttachment{
					ObjectMeta: metav1.ObjectMeta{
						Name: azVolumeAttachmentName,
						Labels: map[string]string{
							consts.NodeNameLabel:   nodeName,
							consts.VolumeNameLabel: *volumeName,
						},
						Finalizers: []string{consts.AzVolumeAttachmentFinalizer},
					},
					Spec: diskv1alpha2.AzVolumeAttachmentSpec{

						VolumeName:    *volumeName,
						VolumeID:      pv.Spec.CSI.VolumeHandle,
						NodeName:      nodeName,
						RequestedRole: diskv1alpha2.PrimaryRole,
						VolumeContext: map[string]string{},
					},
					Status: diskv1alpha2.AzVolumeAttachmentStatus{
						State: azureutils.GetAzVolumeAttachmentState(volumeAttachment.Status),
					},
				}
				if azVolumeAttachment.Status.State == diskv1alpha2.Attached {
					azVolumeAttachment.Status.Detail = &diskv1alpha2.AzVolumeAttachmentStatusDetail{
						Role: diskv1alpha2.PrimaryRole,
					}
				}
				_, err := r.controllerSharedState.azClient.DiskV1alpha2().AzVolumeAttachments(r.controllerSharedState.objectNamespace).Create(ctx, &azVolumeAttachment, metav1.CreateOptions{})
				if err != nil {
					klog.Errorf("failed to create AzVolumeAttachment (%s) for volume (%s) and node (%s): %v", azVolumeAttachmentName, *volumeName, nodeName, err)
					return syncedVolumeAttachments, volumesToSync, err
				}
			} else {
				klog.Errorf("failed to get AzVolumeAttachment (%s): %v", azVolumeAttachmentName, err)
				return syncedVolumeAttachments, volumesToSync, err
			}

			syncedVolumeAttachments[volumeAttachment.Name] = true
		}
	}
	return syncedVolumeAttachments, volumesToSync, nil
}

func (r *ReconcileAttachDetach) recoverAzVolumeAttachment(ctx context.Context, recoveredAzVolumeAttachments *sync.Map) error {
	// list all AzVolumeAttachment
	azVolumeAttachments, err := r.controllerSharedState.azClient.DiskV1alpha2().AzVolumeAttachments(r.controllerSharedState.objectNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("failed to get list of existing AzVolumeAttachment CRI in controller recovery stage")
		return err
	}

	var wg sync.WaitGroup
	numRecovered := int32(0)

	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		// skip if AzVolumeAttachment already recovered
		if _, ok := recoveredAzVolumeAttachments.Load(azVolumeAttachment.Name); ok {
			numRecovered++
			continue
		}

		wg.Add(1)
		go func(azv diskv1alpha2.AzVolumeAttachment, azvMap *sync.Map) {
			defer wg.Done()
			var targetState diskv1alpha2.AzVolumeAttachmentAttachmentState
			updateFunc := func(obj interface{}) error {
				var err error
				azv := obj.(*diskv1alpha2.AzVolumeAttachment)
				// add a recover annotation to the CRI so that reconciliation can be triggered for the CRI even if CRI's current state == target state
				if azv.ObjectMeta.Annotations == nil {
					azv.ObjectMeta.Annotations = map[string]string{}
				}
				azv.ObjectMeta.Annotations[consts.RecoverAnnotation] = "azVolumeAttachment"
				if azv.Status.State != targetState {
					_, err = updateState(azv, targetState, forceUpdate)
				}
				return err
			}
			switch azv.Status.State {
			case diskv1alpha2.Attaching:
				// reset state to Pending so Attach operation can be redone
				targetState = diskv1alpha2.AttachmentPending
			case diskv1alpha2.Detaching:
				// reset state to Attached so Detach operation can be redone
				targetState = diskv1alpha2.Attached
			default:
				targetState = azv.Status.State
			}

			if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, &azv, updateFunc, consts.ForcedUpdateMaxNetRetry); err != nil {
				klog.Warningf("failed to update AzVolumeAttachment (%s) for recovery: %v", azv.Name)
			} else {
				// if update succeeded, add the CRI to the recoveryComplete list
				azvMap.Store(azv.Name, struct{}{})
				atomic.AddInt32(&numRecovered, 1)
			}
		}(azVolumeAttachment, recoveredAzVolumeAttachments)
	}
	wg.Wait()

	// if recovery has not been completed for all CRIs, return error
	if numRecovered < int32(len(azVolumeAttachments.Items)) {
		return status.Errorf(codes.Internal, "failed to recover some AzVolumeAttachment states")
	}
	return nil
}

func NewAttachDetachController(mgr manager.Manager, cloudDiskAttacher CloudDiskAttachDetacher, crdDetacher CrdDetacher, controllerSharedState *SharedState) (*ReconcileAttachDetach, error) {
	reconciler := ReconcileAttachDetach{
		crdDetacher:           crdDetacher,
		cloudDiskAttacher:     cloudDiskAttacher,
		stateLock:             &sync.Map{},
		retryInfo:             newRetryInfo(),
		controllerSharedState: controllerSharedState,
	}

	c, err := controller.New("azvolumeattachment-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		Log:                     mgr.GetLogger().WithValues("controller", "azvolumeattachment"),
	})

	if err != nil {
		klog.Errorf("failed to create a new azvolumeattachment controller: %v", err)
		return nil, err
	}

	// Watch for CRUD events on azVolumeAttachment objects
	err = c.Watch(&source.Kind{Type: &diskv1alpha2.AzVolumeAttachment{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		klog.Errorf("failed to initialize watch for azvolumeattachment object: %v", err)
		return nil, err
	}

	klog.V(2).Info("AzVolumeAttachment Controller successfully initialized.")
	return &reconciler, nil
}
