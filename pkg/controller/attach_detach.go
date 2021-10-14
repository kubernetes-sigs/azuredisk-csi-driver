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
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeClientSet "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azVolumeClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// defaultMaxReplicaUpdateCount refers to the maximum number of creation or deletion of AzVolumeAttachment objects in a single ManageReplica call
	defaultMaxReplicaUpdateCount = 1
)

type AttachmentProvisioner interface {
	PublishVolume(ctx context.Context, volumeID string, nodeID string, volumeContext map[string]string) (map[string]string, error)
	UnpublishVolume(ctx context.Context, volumeID string, nodeID string) error
}

/*
Attach Detach controller is responsible for
	1. attaching volume to a specified node upon creation of AzVolumeAttachment CRI
	2. promoting AzVolumeAttachment to primary upon spec update
	3. detaching volume upon deletions marked with certain annotations
*/
type ReconcileAttachDetach struct {
	client                client.Client
	azVolumeClient        azVolumeClientSet.Interface
	kubeClient            kubeClientSet.Interface
	namespace             string
	attachmentProvisioner AttachmentProvisioner
	stateLock             *sync.Map
	retryInfo             *retryInfo
}

var _ reconcile.Reconciler = &ReconcileAttachDetach{}

var allowedTargetAttachmentStates = map[string][]string{
	string(v1alpha1.AttachmentPending):  {string(v1alpha1.Attaching), string(v1alpha1.Detaching)},
	string(v1alpha1.Attaching):          {string(v1alpha1.Attached), string(v1alpha1.AttachmentFailed)},
	string(v1alpha1.Detaching):          {string(v1alpha1.Detached), string(v1alpha1.DetachmentFailed)},
	string(v1alpha1.Attached):           {string(v1alpha1.Detaching)},
	string(v1alpha1.AttachmentFailed):   {string(v1alpha1.Detaching)},
	string(v1alpha1.DetachmentFailed):   {string(v1alpha1.ForceDetachPending)},
	string(v1alpha1.ForceDetachPending): {string(v1alpha1.Detaching)},
}

func (r *ReconcileAttachDetach) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	azVolumeAttachment, err := azureutils.GetAzVolumeAttachment(ctx, r.client, r.azVolumeClient, request.Name, request.Namespace, true)
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
	if criDeletionRequested(&azVolumeAttachment.ObjectMeta) {
		if azVolumeAttachment.Status.State == v1alpha1.AttachmentPending || azVolumeAttachment.Status.State == v1alpha1.Attached || azVolumeAttachment.Status.State == v1alpha1.AttachmentFailed || azVolumeAttachment.Status.State == v1alpha1.DetachmentFailed {
			if err := r.triggerDetach(ctx, azVolumeAttachment); err != nil {
				return reconcileReturnOnError(azVolumeAttachment, "detach", err, r.retryInfo)
			}
		}
		// attachment request
	} else if azVolumeAttachment.Status.Detail == nil {
		if azVolumeAttachment.Status.State == v1alpha1.AttachmentPending || azVolumeAttachment.Status.State == v1alpha1.AttachmentFailed {
			if err := r.triggerAttach(ctx, azVolumeAttachment); err != nil {
				return reconcileReturnOnError(azVolumeAttachment, "attach", err, r.retryInfo)
			}
		}
		// promotion request
	} else if azVolumeAttachment.Spec.RequestedRole != azVolumeAttachment.Status.Detail.Role {
		if err := r.promote(ctx, azVolumeAttachment); err != nil {
			return reconcileReturnOnError(azVolumeAttachment, "promote", err, r.retryInfo)
		}
	}

	return reconcileReturnOnSuccess(azVolumeAttachment.Name, r.retryInfo)
}

func (r *ReconcileAttachDetach) triggerAttach(ctx context.Context, azVolumeAttachment *v1alpha1.AzVolumeAttachment) error {
	// requeue if AzVolumeAttachment's state is being updated by a different worker
	defer r.stateLock.Delete(azVolumeAttachment.Name)
	if _, ok := r.stateLock.LoadOrStore(azVolumeAttachment.Name, nil); ok {
		return getOperationRequeueError("attach", azVolumeAttachment)
	}

	// initialize metadata and update status block
	updateFunc := func(obj interface{}) error {
		azv := obj.(*v1alpha1.AzVolumeAttachment)
		// Update state to attaching, Initialize finalizer and add label to the object
		azv = r.initializeMeta(azv)
		_, derr := updateState(azv, v1alpha1.Attaching, normalUpdate)
		return derr
	}
	if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, azVolumeAttachment, updateFunc, consts.CRIDefaultMaxNetRetry); err != nil {
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
	go func() {
		cloudCtx, cloudCancel := context.WithTimeout(context.Background(), cloudTimeout)
		defer cloudCancel()
		updateCtx := context.Background()
		response, attachErr := r.attachVolume(cloudCtx, azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Spec.VolumeContext)
		var updateFunc func(interface{}) error
		if attachErr != nil {
			klog.Errorf("failed to attach volume %s to node %s: %v", azVolumeAttachment.Spec.UnderlyingVolume, azVolumeAttachment.Spec.NodeName, attachErr)

			updateFunc = func(obj interface{}) error {
				azv := obj.(*v1alpha1.AzVolumeAttachment)
				azv = updateError(azv, attachErr)
				_, uerr := updateState(azv, v1alpha1.AttachmentFailed, forceUpdate)
				return uerr
			}
		} else {
			klog.Infof("successfully attached volume (%s) to node (%s) and update status of AzVolumeAttachment (%s)", azVolumeAttachment.Spec.UnderlyingVolume, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Name)

			updateFunc = func(obj interface{}) error {
				azv := obj.(*v1alpha1.AzVolumeAttachment)
				azv = updateStatusDetail(azv, response)
				_, uerr := updateState(azv, v1alpha1.Attached, forceUpdate)
				return uerr
			}
		}
		if derr := azureutils.UpdateCRIWithRetry(updateCtx, nil, r.client, r.azVolumeClient, azVolumeAttachment, updateFunc, consts.CRITransientMaxNetRetry); derr != nil {
			klog.Errorf("failed to update AzVolumeAttachment (%s) with attachVolume result (response: %v, error: %v): %v", azVolumeAttachment.Name, response, attachErr, derr)
		} else {
			klog.Infof("Successfully updated AzVolumeAttachment (%s) with attachVolume result (response: %v, error: %v)", azVolumeAttachment.Name, response, attachErr)
		}
	}()

	return nil
}

func (r *ReconcileAttachDetach) triggerDetach(ctx context.Context, azVolumeAttachment *v1alpha1.AzVolumeAttachment) error {
	// only detach if detachment request was made for underlying volume attachment object
	detachmentRequested := volumeDetachRequested(azVolumeAttachment)

	if detachmentRequested {
		defer r.stateLock.Delete(azVolumeAttachment.Name)
		if _, ok := r.stateLock.LoadOrStore(azVolumeAttachment.Name, nil); ok {
			return getOperationRequeueError("detach", azVolumeAttachment)
		}

		updateFunc := func(obj interface{}) error {
			azv := obj.(*v1alpha1.AzVolumeAttachment)
			// Update state to detaching
			_, derr := updateState(azv, v1alpha1.Detaching, normalUpdate)
			return derr
		}
		if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, azVolumeAttachment, updateFunc, consts.CRIDefaultMaxNetRetry); err != nil {
			return err
		}

		klog.Infof("Detaching volume (%s) from node (%s)", azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName)

		go func() {
			cloudCtx, cloudCancel := context.WithTimeout(context.Background(), cloudTimeout)
			defer cloudCancel()
			updateCtx := context.Background()

			var updateFunc func(obj interface{}) error
			err := r.detachVolume(cloudCtx, azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName)
			if err != nil {
				updateFunc = func(obj interface{}) error {
					azv := obj.(*v1alpha1.AzVolumeAttachment)
					azv = updateError(azv, err)
					_, derr := updateState(azv, v1alpha1.DetachmentFailed, forceUpdate)
					return derr
				}
			} else {
				updateFunc = func(obj interface{}) error {
					azv := obj.(*v1alpha1.AzVolumeAttachment)
					azv = r.deleteFinalizer(azv)
					_, derr := updateState(azv, v1alpha1.Detached, forceUpdate)
					return derr
				}
			}
			if derr := azureutils.UpdateCRIWithRetry(updateCtx, nil, r.client, r.azVolumeClient, azVolumeAttachment, updateFunc, consts.CRITransientMaxNetRetry); derr != nil {
				klog.Errorf("failed to update AzVolumeAttachment (%s) with detachVolume result (error: %v): %v", azVolumeAttachment.Name, err, derr)
			} else {
				klog.Infof("Successfully updated AzVolumeAttachment (%s) with detachVolume result (error: %v)", azVolumeAttachment.Name, err)
			}
		}()
	} else {
		updateFunc := func(obj interface{}) error {
			azv := obj.(*v1alpha1.AzVolumeAttachment)
			// delete finalizer
			_ = r.deleteFinalizer(azv)
			return nil
		}
		if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, azVolumeAttachment, updateFunc, consts.CRIDefaultMaxNetRetry); err != nil {
			return err
		}
	}
	return nil
}

func (r *ReconcileAttachDetach) promote(ctx context.Context, azVolumeAttachment *v1alpha1.AzVolumeAttachment) error {
	// initialize metadata and update status block
	updateFunc := func(obj interface{}) error {
		azv := obj.(*v1alpha1.AzVolumeAttachment)
		_ = updateRole(azv, v1alpha1.PrimaryRole)
		return nil
	}
	return azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, azVolumeAttachment, updateFunc, consts.CRIDefaultMaxNetRetry)
}

func (r *ReconcileAttachDetach) initializeMeta(azVolumeAttachment *v1alpha1.AzVolumeAttachment) *v1alpha1.AzVolumeAttachment {
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
	azVolumeAttachment.Labels[consts.VolumeNameLabel] = azVolumeAttachment.Spec.UnderlyingVolume
	azVolumeAttachment.Labels[consts.RoleLabel] = string(azVolumeAttachment.Spec.RequestedRole)

	return azVolumeAttachment
}

func (r *ReconcileAttachDetach) deleteFinalizer(azVolumeAttachment *v1alpha1.AzVolumeAttachment) *v1alpha1.AzVolumeAttachment {
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

// TODO: reassess if adding additional finalizer to AzVolume CRI is necessary
// func (r *ReconcileAttachDetach) addFinalizerToAzVolume(ctx context.Context, volumeName string) error {
// 	var azVolume *v1alpha1.AzVolume
// 	azVolume, err := azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volumeName, r.namespace, true)
// 	if err != nil {
// 		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
// 		return err
// 	}

// 	updated := azVolume.DeepCopy()

// 	if updated.Finalizers == nil {
// 		updated.Finalizers = []string{}
// 	}

// 	if finalizerExists(updated.Finalizers, azureutils.AzVolumeAttachmentFinalizer) {
// 		return nil
// 	}

// 	updated.Finalizers = append(updated.Finalizers, azureutils.AzVolumeAttachmentFinalizer)
// 	if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
// 		klog.Errorf("failed to add finalizer (%s) to AzVolume(%s): %v", azureutils.AzVolumeAttachmentFinalizer, updated.Name, err)
// 		return err
// 	}
// 	klog.Infof("successfully added finalizer (%s) to AzVolume (%s)", azureutils.AzVolumeAttachmentFinalizer, updated.Name)
// 	return nil
// }
//
// func (r *ReconcileAttachDetach) deleteFinalizerFromAzVolume(ctx context.Context, volumeName string) error {
// 	var azVolume *v1alpha1.AzVolume
// 	azVolume, err := azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volumeName, r.namespace, true)
// 	if err != nil {
// 		if errors.IsNotFound(err) {
// 			return nil
// 		}
// 		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
// 		return err
// 	}

// 	updated := azVolume.DeepCopy()
// 	updatedFinalizers := []string{}

// 	for _, finalizer := range updated.Finalizers {
// 		if finalizer == azureutils.AzVolumeAttachmentFinalizer {
// 			continue
// 		}
// 		updatedFinalizers = append(updatedFinalizers, finalizer)
// 	}
// 	updated.Finalizers = updatedFinalizers

// 	if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
// 		klog.Errorf("failed to delete finalizer (%s) from AzVolume(%s): %v", azureutils.AzVolumeAttachmentFinalizer, updated.Name, err)
// 		return err
// 	}
// 	klog.Infof("successfully deleted finalizer (%s) from AzVolume (%s)", azureutils.AzVolumeAttachmentFinalizer, updated.Name)
// 	return nil
// }

func (r *ReconcileAttachDetach) attachVolume(ctx context.Context, volumeID, node string, volumeContext map[string]string) (map[string]string, error) {
	return r.attachmentProvisioner.PublishVolume(ctx, volumeID, node, volumeContext)
}

func (r *ReconcileAttachDetach) detachVolume(ctx context.Context, volumeID, node string) error {
	return r.attachmentProvisioner.UnpublishVolume(ctx, volumeID, node)
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
	recovered := sync.Map{}
	for i := 0; i < maxRetry; i++ {
		if err = r.recoverAzVolumeAttachment(ctx, recovered); err == nil {
			break
		}
		klog.Warningf("failed to recover AzVolumeAttachment state: %v", err)
	}

	return err
}

func updateRole(azVolumeAttachment *v1alpha1.AzVolumeAttachment, role v1alpha1.Role) *v1alpha1.AzVolumeAttachment {
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
	azVolumeAttachment.Status.Detail.Role = role

	return azVolumeAttachment
}

func updateStatusDetail(azVolumeAttachment *v1alpha1.AzVolumeAttachment, status map[string]string) *v1alpha1.AzVolumeAttachment {
	if azVolumeAttachment == nil {
		return nil
	}

	if azVolumeAttachment.Status.Detail == nil {
		azVolumeAttachment.Status.Detail = &v1alpha1.AzVolumeAttachmentStatusDetail{}
	}
	azVolumeAttachment.Status.Detail.Role = azVolumeAttachment.Spec.RequestedRole
	azVolumeAttachment.Status.Detail.PublishContext = status

	return azVolumeAttachment
}

func updateError(azVolumeAttachment *v1alpha1.AzVolumeAttachment, err error) *v1alpha1.AzVolumeAttachment {
	if azVolumeAttachment == nil {
		return nil
	}

	if err != nil {
		azVolumeAttachment.Status.Error = util.NewAzError(err)
	}

	return azVolumeAttachment
}

func updateState(azVolumeAttachment *v1alpha1.AzVolumeAttachment, state v1alpha1.AzVolumeAttachmentAttachmentState, mode updateMode) (*v1alpha1.AzVolumeAttachment, error) {
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
	volumeAttachments, err := r.kubeClient.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
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
		if volumeAttachment.Spec.Attacher == consts.DefaultDriverName {
			volumeName := volumeAttachment.Spec.Source.PersistentVolumeName
			if volumeName == nil {
				continue
			}
			// get PV and retrieve diskName
			pv, err := r.kubeClient.CoreV1().PersistentVolumes().Get(ctx, *volumeName, metav1.GetOptions{})
			if err != nil {
				klog.Errorf("failed to get PV (%s): %v", *volumeName, err)
				return syncedVolumeAttachments, volumesToSync, err
			}

			if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != consts.DefaultDriverName {
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
			_, err = azureutils.GetAzVolumeAttachment(ctx, r.client, r.azVolumeClient, azVolumeAttachmentName, r.namespace, false)
			if errors.IsNotFound(err) {
				klog.Infof("Recreating AzVolumeAttachment(%s)", azVolumeAttachmentName)
				azVolumeAttachment := v1alpha1.AzVolumeAttachment{
					ObjectMeta: metav1.ObjectMeta{
						Name: azVolumeAttachmentName,
						Labels: map[string]string{
							consts.NodeNameLabel:   nodeName,
							consts.VolumeNameLabel: *volumeName,
						},
						Finalizers: []string{consts.AzVolumeAttachmentFinalizer},
					},
					Spec: v1alpha1.AzVolumeAttachmentSpec{
						UnderlyingVolume: *volumeName,
						VolumeID:         pv.Spec.CSI.VolumeHandle,
						NodeName:         nodeName,
						RequestedRole:    v1alpha1.PrimaryRole,
						VolumeContext:    map[string]string{},
					},
					Status: v1alpha1.AzVolumeAttachmentStatus{
						State: azureutils.GetAzVolumeAttachmentState(volumeAttachment.Status),
					},
				}
				if azVolumeAttachment.Status.State == v1alpha1.Attached {
					azVolumeAttachment.Status.Detail = &v1alpha1.AzVolumeAttachmentStatusDetail{
						Role: v1alpha1.PrimaryRole,
					}
				}
				_, err := r.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(r.namespace).Create(ctx, &azVolumeAttachment, metav1.CreateOptions{})
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

func (r *ReconcileAttachDetach) recoverAzVolumeAttachment(ctx context.Context, recoverdAzVolumeAttachments sync.Map) error {
	// list all AzVolumeAttachment
	azVolumeAttachments, err := r.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(r.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("failed to get list of exising AzVolumeAttachment CRI in controller recovery stage")
		return err
	}

	var wg sync.WaitGroup
	numRecovered := int32(0)

	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		// skip if AzVolumeAttachment already recovered
		if _, ok := recoverdAzVolumeAttachments.Load(azVolumeAttachment.Name); ok {
			numRecovered++
			continue
		}

		wg.Add(1)
		go func(azv v1alpha1.AzVolumeAttachment, azvMap sync.Map) {
			defer wg.Done()
			var targetState v1alpha1.AzVolumeAttachmentAttachmentState
			updateFunc := func(obj interface{}) error {
				azv := obj.(*v1alpha1.AzVolumeAttachment)
				if azv.Status.State != targetState {
					updateState(azv, targetState, forceUpdate)
				}
				// add a recover annotation to the CRI so that reconciliation can be triggered for the CRI even if CRI's current state == target state
				if azv.ObjectMeta.Annotations == nil {
					azv.ObjectMeta.Annotations = map[string]string{}
				}
				azv.ObjectMeta.Annotations[consts.RecoverAnnotation] = "azVolumeAttachment"
				return nil
			}
			switch azv.Status.State {
			case v1alpha1.Attaching:
				// reset state to Pending so Attach operation can be redone
				targetState = v1alpha1.AttachmentPending
			case v1alpha1.Detaching:
				// reset state to Attached so Detach operation can be redone
				targetState = v1alpha1.Attached
			}

			if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, &azv, updateFunc, consts.CRITransientMaxNetRetry); err != nil {
				klog.Warningf("failed to udpate AzVolumeAttachment (%s) for recovery: %v", azv.Name)
			} else {
				// if update succeeded, add the CRI to the recoveryComplete list
				azvMap.Store(azv.Name, struct{}{})
				atomic.AddInt32(&numRecovered, 1)
			}
		}(azVolumeAttachment, recoverdAzVolumeAttachments)
	}
	wg.Wait()

	// if recovery has not been completed for all CRIs, return error
	if numRecovered < int32(len(azVolumeAttachments.Items)) {
		return status.Errorf(codes.Internal, "failed to recover some AzVolumeAttachment states")
	}
	return nil
}

func NewAttachDetachController(mgr manager.Manager, azVolumeClient azVolumeClientSet.Interface, kubeClient kubeClientSet.Interface, namespace string, attachmentProvisioner AttachmentProvisioner) (*ReconcileAttachDetach, error) {
	reconciler := ReconcileAttachDetach{
		client:                mgr.GetClient(),
		azVolumeClient:        azVolumeClient,
		kubeClient:            kubeClient,
		namespace:             namespace,
		attachmentProvisioner: attachmentProvisioner,
		stateLock:             &sync.Map{},
		retryInfo:             newRetryInfo(),
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
	err = c.Watch(&source.Kind{Type: &v1alpha1.AzVolumeAttachment{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		klog.Errorf("failed to initialize watch for azvolumeattachment object: %v", err)
		return nil, err
	}

	klog.V(2).Info("AzVolumeAttachment Controller successfully initialized.")
	return &reconciler, nil
}
