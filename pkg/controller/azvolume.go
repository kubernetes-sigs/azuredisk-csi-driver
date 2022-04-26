/*
Copyright 2017 The Kubernetes Authors.

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
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/features"
	diskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	util "sigs.k8s.io/azuredisk-csi-driver/pkg/util"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// VolumeProvisioner defines the subset of Cloud Provisioner functions used by the AzVolume controller.
type VolumeProvisioner interface {
	CreateVolume(
		ctx context.Context,
		volumeName string,
		capacityRange *diskv1beta1.CapacityRange,
		volumeCapabilities []diskv1beta1.VolumeCapability,
		parameters map[string]string,
		secrets map[string]string,
		volumeContentSource *diskv1beta1.ContentVolumeSource,
		accessibilityTopology *diskv1beta1.TopologyRequirement) (*diskv1beta1.AzVolumeStatusDetail, error)
	DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error
	ExpandVolume(ctx context.Context, volumeID string, capacityRange *diskv1beta1.CapacityRange, secrets map[string]string) (*diskv1beta1.AzVolumeStatusDetail, error)
}

//Struct for the reconciler
type ReconcileAzVolume struct {
	volumeProvisioner     VolumeProvisioner
	controllerSharedState *SharedState
	// stateLock prevents concurrent cloud operation for same volume to be executed due to state update race
	stateLock *sync.Map
	retryInfo *retryInfo
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &ReconcileAzVolume{}

var allowedTargetVolumeStates = map[string][]string{
	"": {string(diskv1beta1.VolumeOperationPending), string(diskv1beta1.VolumeCreating), string(diskv1beta1.VolumeDeleting)},
	string(diskv1beta1.VolumeOperationPending): {string(diskv1beta1.VolumeCreating), string(diskv1beta1.VolumeDeleting)},
	string(diskv1beta1.VolumeCreating):         {string(diskv1beta1.VolumeCreated), string(diskv1beta1.VolumeCreationFailed)},
	string(diskv1beta1.VolumeDeleting):         {string(diskv1beta1.VolumeDeleted), string(diskv1beta1.VolumeDeletionFailed)},
	string(diskv1beta1.VolumeUpdating):         {string(diskv1beta1.VolumeUpdated), string(diskv1beta1.VolumeUpdateFailed)},
	string(diskv1beta1.VolumeCreated):          {string(diskv1beta1.VolumeUpdating), string(diskv1beta1.VolumeDeleting)},
	string(diskv1beta1.VolumeDeleted):          {},
	string(diskv1beta1.VolumeUpdated):          {string(diskv1beta1.VolumeUpdating), string(diskv1beta1.VolumeDeleting)},
	string(diskv1beta1.VolumeCreationFailed):   {},
	string(diskv1beta1.VolumeDeletionFailed):   {},
}

func (r *ReconcileAzVolume) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if !r.controllerSharedState.isRecoveryComplete() {
		return reconcile.Result{Requeue: true}, nil
	}

	azVolume, err := azureutils.GetAzVolume(ctx, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, request.Name, request.Namespace, true)
	if err != nil {
		// if AzVolume has been deleted, delete the operation queue for the volume and return success
		if errors.IsNotFound(err) {
			r.controllerSharedState.deleteOperationQueue(request.Name)
			return reconcileReturnOnSuccess(request.Name, r.retryInfo)
		}

		// if the GET failure is triggered by other errors, requeue the request
		azVolume.Name = request.Name
		return reconcileReturnOnError(azVolume, "get", err, r.retryInfo)
	}

	// if underlying cloud operation already in process, skip until operation is completed
	if isOperationInProcess(azVolume) {
		klog.V(5).Infof("Another operation (%s) is already in process for the AzVolume (%s). Will be requeued once complete.", azVolume.Name, azVolume.Status.State)
		return reconcileReturnOnSuccess(azVolume.Name, r.retryInfo)
	}

	// azVolume deletion
	if objectDeletionRequested(azVolume) {
		if err := r.triggerDelete(ctx, azVolume); err != nil {
			//If delete failed, requeue request
			return reconcileReturnOnError(azVolume, "delete", err, r.retryInfo)
		}
		//azVolume creation
	} else if azVolume.Status.Detail == nil {
		if err := r.triggerCreate(ctx, azVolume); err != nil {
			klog.Errorf("failed to create volume (%s): %v", azVolume.Spec.VolumeName, err)
			return reconcileReturnOnError(azVolume, "create", err, r.retryInfo)
		}
		// azVolume update
	} else if azVolume.Spec.CapacityRange != nil && azVolume.Spec.CapacityRange.RequiredBytes != azVolume.Status.Detail.CapacityBytes {
		if err := r.triggerUpdate(ctx, azVolume); err != nil {
			klog.Errorf("failed to update volume (%s): %v", azVolume.Spec.VolumeName, err)
			return reconcileReturnOnError(azVolume, "update", err, r.retryInfo)
		}
		// azVolume released, so clean up replica Attachments (primary Attachment should be deleted via UnpublishVolume request to avoid race condition)
	}

	return reconcileReturnOnSuccess(azVolume.Name, r.retryInfo)
}

func (r *ReconcileAzVolume) triggerCreate(ctx context.Context, azVolume *diskv1beta1.AzVolume) error {
	// requeue if AzVolume's state is being updated by a different worker
	defer r.stateLock.Delete(azVolume.Name)
	if _, ok := r.stateLock.LoadOrStore(azVolume.Name, nil); ok {
		return getOperationRequeueError("create", azVolume)
	}

	// update state
	updateFunc := func(obj interface{}) error {
		azv := obj.(*diskv1beta1.AzVolume)
		_, err := r.updateState(azv, diskv1beta1.VolumeCreating, normalUpdate)
		return err
	}
	if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}

	klog.Infof("Creating Volume (%s)...", azVolume.Spec.VolumeName)

	// create volume
	go func() {
		cloudCtx, cloudCancel := context.WithTimeout(context.Background(), cloudTimeout)
		defer cloudCancel()

		var updateFunc func(interface{}) error
		// UpdateCRIWithRetry should be called on a context w/o timeout when called in a separate goroutine as it is not going to be retriggered and leave the CRI in unrecoverable transient state instead.
		response, err := r.createVolume(cloudCtx, azVolume)
		updateMode := azureutils.UpdateCRIStatus
		if err != nil {
			klog.Errorf("failed to create volume %s: %v", azVolume.Spec.VolumeName, err)
			updateFunc = func(obj interface{}) error {
				azv := obj.(*diskv1beta1.AzVolume)
				azv = r.updateError(azv, err)
				azv = r.deleteFinalizer(azv, map[string]bool{consts.AzVolumeFinalizer: true})
				_, derr := r.updateState(azv, diskv1beta1.VolumeCreationFailed, forceUpdate)
				return derr
			}
			updateMode = azureutils.UpdateAll
		} else {
			klog.Infof("Successfully created volume %s: %v", azVolume.Spec.VolumeName, response)
			// create operation queue for the volume
			r.controllerSharedState.createOperationQueue(azVolume.Name)
			updateFunc = func(obj interface{}) error {
				azv := obj.(*diskv1beta1.AzVolume)
				if response == nil {
					return status.Errorf(codes.Internal, "non-nil AzVolumeStatusDetail expected but nil given")
				}
				azv = r.updateStatusDetail(azv, response, response.CapacityBytes, response.NodeExpansionRequired)
				_, derr := r.updateState(azv, diskv1beta1.VolumeCreated, forceUpdate)
				return derr
			}
		}
		updateCtx := context.Background()
		_ = azureutils.UpdateCRIWithRetry(updateCtx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.ForcedUpdateMaxNetRetry, updateMode)
	}()

	return nil
}

func (r *ReconcileAzVolume) triggerDelete(ctx context.Context, azVolume *diskv1beta1.AzVolume) error {
	// Determine if this is a controller server requested deletion or driver clean up
	volumeDeleteRequested := volumeDeleteRequested(azVolume)
	preProvisionCleanupRequested := isPreProvisionCleanupRequested(azVolume)

	mode := deleteCRIOnly
	if volumeDeleteRequested || preProvisionCleanupRequested {
		mode = detachAndDeleteCRI
	}

	// override volume operation queue to prevent any other replica operation from being executed
	release := r.controllerSharedState.closeOperationQueue(azVolume.Name)
	defer func() {
		if release != nil {
			release()
		}
	}()

	// Delete all AzVolumeAttachment objects bound to the deleted AzVolume
	attachments, err := r.controllerSharedState.cleanUpAzVolumeAttachmentByVolume(ctx, azVolume.Name, azvolume, all, mode)
	if err != nil {
		return err
	}

	if len(attachments) > 0 {
		return status.Errorf(codes.Aborted, "volume deletion requeued until attached azVolumeAttachments are entirely detached...")
	}

	// only try deleting underlying volume 1) if volume creation was successful and 2) volumeDeleteRequestAnnotation is present
	// if the annotation is not present, only delete the CRI and not the underlying volume
	if isCreated(azVolume) && volumeDeleteRequested {
		// requeue if AzVolume's state is being updated by a different worker
		defer r.stateLock.Delete(azVolume.Name)
		if _, ok := r.stateLock.LoadOrStore(azVolume.Name, nil); ok {
			return getOperationRequeueError("delete", azVolume)
		}
		updateFunc := func(obj interface{}) error {
			azv := obj.(*diskv1beta1.AzVolume)
			_, derr := r.updateState(azv, diskv1beta1.VolumeDeleting, normalUpdate)
			return derr
		}

		klog.Infof("Deleting Volume (%s)...", azVolume.Spec.VolumeName)

		if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
			return err
		}

		go func() {
			cloudCtx, cloudCancel := context.WithTimeout(context.Background(), cloudTimeout)
			defer cloudCancel()

			var updateFunc func(interface{}) error
			updateMode := azureutils.UpdateCRIStatus
			err := r.deleteVolume(cloudCtx, azVolume)
			if err != nil {
				klog.Errorf("failed to delete volume (%s): %v", azVolume.Spec.VolumeName, err)
				updateFunc = func(obj interface{}) error {
					azv := obj.(*diskv1beta1.AzVolume)
					azv = r.updateError(azv, err)
					_, derr := r.updateState(azv, diskv1beta1.VolumeDeletionFailed, forceUpdate)
					return derr
				}
			} else {
				klog.Infof("successfully deleted volume (%s)", azVolume.Spec.VolumeName)
				updateFunc = func(obj interface{}) error {
					azv := obj.(*diskv1beta1.AzVolume)
					azv = r.deleteFinalizer(azv, map[string]bool{consts.AzVolumeFinalizer: true})
					_, derr := r.updateState(azv, diskv1beta1.VolumeDeleted, forceUpdate)
					return derr
				}
				updateMode = azureutils.UpdateAll

			}
			// UpdateCRIWithRetry should be called on a context w/o timeout when called in a separate goroutine as it is not going to be retriggered and leave the CRI in unrecoverable transient state instead.
			updateCtx := context.Background()
			_ = azureutils.UpdateCRIWithRetry(updateCtx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.ForcedUpdateMaxNetRetry, updateMode)
		}()
	} else {
		updateFunc := func(obj interface{}) error {
			azv := obj.(*diskv1beta1.AzVolume)
			_ = r.deleteFinalizer(azv, map[string]bool{consts.AzVolumeFinalizer: true})
			return nil
		}
		if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRI); err != nil {
			return err
		}
	}
	return nil
}

func (r *ReconcileAzVolume) triggerUpdate(ctx context.Context, azVolume *diskv1beta1.AzVolume) error {
	// requeue if AzVolume's state is being updated by a different worker
	defer r.stateLock.Delete(azVolume.Name)
	if _, ok := r.stateLock.LoadOrStore(azVolume.Name, nil); ok {
		return getOperationRequeueError("update", azVolume)
	}
	updateFunc := func(obj interface{}) error {
		azv := obj.(*diskv1beta1.AzVolume)
		_, derr := r.updateState(azv, diskv1beta1.VolumeUpdating, normalUpdate)
		return derr
	}
	if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}

	klog.Infof("Updating Volume (%s)...", azVolume.Spec.VolumeName)

	go func() {
		cloudCtx, cloudCancel := context.WithTimeout(context.Background(), cloudTimeout)
		defer cloudCancel()

		var updateFunc func(interface{}) error
		response, err := r.expandVolume(cloudCtx, azVolume)
		if err != nil {
			klog.Errorf("failed to update volume %s: %v", azVolume.Spec.VolumeName, err)
			updateFunc = func(obj interface{}) error {
				azv := obj.(*diskv1beta1.AzVolume)
				azv = r.updateError(azv, err)
				_, derr := r.updateState(azv, diskv1beta1.VolumeUpdateFailed, forceUpdate)
				return derr
			}
		} else {
			klog.Infof("Successfully updated volume %s: %v", azVolume.Spec.VolumeName, response)
			updateFunc = func(obj interface{}) error {
				azv := obj.(*diskv1beta1.AzVolume)
				if response == nil {
					return status.Errorf(codes.Internal, "non-nil AzVolumeStatusDetail expected but nil given")
				}
				azv = r.updateStatusDetail(azv, response, response.CapacityBytes, response.NodeExpansionRequired)
				_, derr := r.updateState(azv, diskv1beta1.VolumeUpdated, forceUpdate)
				return derr
			}
		}

		// UpdateCRIWithRetry should be called on a context w/o timeout when called in a separate goroutine as it is not going to be retriggered and leave the CRI in unrecoverable transient state instead.
		updateCtx := context.Background()
		_ = azureutils.UpdateCRIWithRetry(updateCtx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.ForcedUpdateMaxNetRetry, azureutils.UpdateCRIStatus)
	}()

	return nil
}

func (r *ReconcileAzVolume) deleteFinalizer(azVolume *diskv1beta1.AzVolume, finalizersToDelete map[string]bool) *diskv1beta1.AzVolume {
	if azVolume == nil {
		return nil
	}

	if azVolume.ObjectMeta.Finalizers == nil {
		return azVolume
	}

	finalizers := []string{}
	for _, finalizer := range azVolume.ObjectMeta.Finalizers {
		if exists := finalizersToDelete[finalizer]; exists {
			continue
		}
		finalizers = append(finalizers, finalizer)
	}
	azVolume.ObjectMeta.Finalizers = finalizers
	return azVolume
}

func (r *ReconcileAzVolume) updateState(azVolume *diskv1beta1.AzVolume, state diskv1beta1.AzVolumeState, mode updateMode) (*diskv1beta1.AzVolume, error) {
	var err error
	if azVolume == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "function `updateState` requires non-nil AzVolume object.")
	}
	if mode == normalUpdate {
		if expectedStates, exists := allowedTargetVolumeStates[string(azVolume.Status.State)]; !exists || !containsString(string(state), expectedStates) {
			err = status.Error(codes.FailedPrecondition, formatUpdateStateError("azVolume", string(azVolume.Status.State), string(state), expectedStates...))
		}
	}
	if err == nil {
		azVolume.Status.State = state
	}
	return azVolume, err
}

func (r *ReconcileAzVolume) updateStatusDetail(azVolume *diskv1beta1.AzVolume, status *diskv1beta1.AzVolumeStatusDetail, capacityBytes int64, nodeExpansionRequired bool) *diskv1beta1.AzVolume {
	if azVolume == nil {
		return nil
	}
	if azVolume.Status.Detail == nil {
		azVolume.Status.Detail = status
	} else {
		azVolume.Status.Detail.CapacityBytes = capacityBytes
		azVolume.Status.Detail.NodeExpansionRequired = nodeExpansionRequired
	}
	return azVolume
}

func (r *ReconcileAzVolume) updateError(azVolume *diskv1beta1.AzVolume, err error) *diskv1beta1.AzVolume {
	if azVolume == nil {
		return nil
	}

	azVolume.Status.Error = util.NewAzError(err)

	return azVolume
}

func (r *ReconcileAzVolume) expandVolume(ctx context.Context, azVolume *diskv1beta1.AzVolume) (*diskv1beta1.AzVolumeStatusDetail, error) {
	if azVolume.Status.Detail == nil {
		err := status.Errorf(codes.Internal, "Disk for expansion does not exist for AzVolume (%s).", azVolume.Name)
		klog.Errorf("skipping expandVolume operation for AzVolume (%s): %v", azVolume.Name, err)
		return nil, err
	}
	// use deep-copied version of the azVolume CRI to prevent any unwanted update to the object
	copied := azVolume.DeepCopy()
	return r.volumeProvisioner.ExpandVolume(ctx, copied.Status.Detail.VolumeID, copied.Spec.CapacityRange, copied.Spec.Secrets)
}

func (r *ReconcileAzVolume) createVolume(ctx context.Context, azVolume *diskv1beta1.AzVolume) (*diskv1beta1.AzVolumeStatusDetail, error) {
	if azVolume.Status.Detail != nil {
		return azVolume.Status.Detail, nil
	}
	// use deep-copied version of the azVolume CRI to prevent any unwanted update to the object
	copied := azVolume.DeepCopy()
	return r.volumeProvisioner.CreateVolume(ctx, copied.Spec.VolumeName, copied.Spec.CapacityRange, copied.Spec.VolumeCapability, copied.Spec.Parameters, copied.Spec.Secrets, copied.Spec.ContentVolumeSource, copied.Spec.AccessibilityRequirements)
}

func (r *ReconcileAzVolume) deleteVolume(ctx context.Context, azVolume *diskv1beta1.AzVolume) error {
	if azVolume.Status.Detail == nil {
		klog.Infof("skipping deleteVolume operation for AzVolume (%s): No volume to delete for AzVolume (%s)", azVolume.Name, azVolume.Name)
		return nil
	}
	// use deep-copied version of the azVolume CRI to prevent any unwanted update to the object
	copied := azVolume.DeepCopy()
	return r.volumeProvisioner.DeleteVolume(ctx, copied.Status.Detail.VolumeID, copied.Spec.Secrets)
}

func (r *ReconcileAzVolume) recreateAzVolumes(ctx context.Context) error {
	// Get PV list and create AzVolume for PV with azuredisk CSI spec
	pvs, err := r.controllerSharedState.kubeClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("failed to get PV list: %v", err)
	}

	for _, pv := range pvs.Items {
		if err := r.controllerSharedState.createAzVolumeFromPv(ctx, pv, make(map[string]string)); err != nil {
			klog.Errorf("failed to recover AzVolume for PV (%s): %v", pv.Name, err)
		}
	}
	return nil
}

func (r *ReconcileAzVolume) recoverAzVolume(ctx context.Context, recoveredAzVolumes *sync.Map) error {
	// list all AzVolumes
	azVolumes, err := r.controllerSharedState.azClient.DiskV1beta1().AzVolumes(r.controllerSharedState.objectNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("failed to get list of existing AzVolume CRI in controller recovery stage")
		return err
	}

	var wg sync.WaitGroup
	numRecovered := int32(0)

	for _, azVolume := range azVolumes.Items {
		// skip if AzVolume already recovered
		if _, ok := recoveredAzVolumes.Load(azVolume.Name); ok {
			numRecovered++
			continue
		}

		wg.Add(1)
		go func(azv diskv1beta1.AzVolume, azvMap *sync.Map) {
			defer wg.Done()
			var targetState diskv1beta1.AzVolumeState
			updateFunc := func(obj interface{}) error {
				var err error
				azv := obj.(*diskv1beta1.AzVolume)
				// add a recover annotation to the CRI so that reconciliation can be triggered for the CRI even if CRI's current state == target state
				azv.Status.Annotations = azureutils.AddToMap(azv.Status.Annotations, consts.RecoverAnnotation, "azVolume")
				if azv.Status.State != targetState {
					_, err = r.updateState(azv, targetState, forceUpdate)
				}
				return err
			}
			switch azv.Status.State {
			case diskv1beta1.VolumeCreating:
				// reset state to Pending so Create operation can be redone
				targetState = diskv1beta1.VolumeOperationPending
			case diskv1beta1.VolumeDeleting:
				// reset state to Created so Delete operation can be redone
				targetState = diskv1beta1.VolumeCreated
			case diskv1beta1.VolumeUpdating:
				// reset state to Created so Update operation can be redone
				targetState = diskv1beta1.VolumeCreated
			default:
				targetState = azv.Status.State
			}

			if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, &azv, updateFunc, consts.ForcedUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
				klog.Warningf("failed to update AzVolume (%s) for recovery: %v", azv.Name)
			} else {
				// if update succeeded, add the CRI to the recoveryComplete list
				azvMap.Store(azv.Name, struct{}{})
				atomic.AddInt32(&numRecovered, 1)
			}
		}(azVolume, recoveredAzVolumes)
	}
	wg.Wait()

	// if recovery has not been completed for all CRIs, return error
	if numRecovered < int32(len(azVolumes.Items)) {
		return status.Errorf(codes.Internal, "failed to recover some AzVolume states")
	}
	return nil
}

func (r *ReconcileAzVolume) Recover(ctx context.Context) error {
	// recover CRI if possible
	klog.Info("Recovering AzVolume CRIs...")
	var err error
	if err = r.recreateAzVolumes(ctx); err != nil {
		klog.Warningf("failed to recreate missing AzVolume CRI: %v", err)
	}

	recovered := &sync.Map{}
	for i := 0; i < maxRetry; i++ {
		if err = r.recoverAzVolume(ctx, recovered); err == nil {
			break
		}
		klog.Warningf("failed to recover AzVolume state: %v", err)
	}
	return err
}

func NewAzVolumeController(mgr manager.Manager, volumeProvisioner VolumeProvisioner, controllerSharedState *SharedState) (*ReconcileAzVolume, error) {
	reconciler := ReconcileAzVolume{
		volumeProvisioner:     volumeProvisioner,
		stateLock:             &sync.Map{},
		retryInfo:             newRetryInfo(),
		controllerSharedState: controllerSharedState,
	}
	logger := mgr.GetLogger().WithValues("controller", "azvolume")

	c, err := controller.New("azvolume-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		Log:                     logger,
	})

	if err != nil {
		klog.Errorf("Failed to create azvolume controller. Error: (%v)", err)
		return nil, err
	}

	klog.V(2).Info("Starting to watch cluster AzVolumes.")

	// Watch for CRUD events on azVolume objects
	err = c.Watch(&source.Kind{Type: &diskv1beta1.AzVolume{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		klog.Errorf("Failed to watch AzVolume. Error: %v", err)
		return nil, err
	}

	klog.V(2).Info("Controller set-up successful.")

	return &reconciler, nil
}

func (c *SharedState) createAzVolumeFromPv(ctx context.Context, pv v1.PersistentVolume, annotations map[string]string) error {
	var azVolume *diskv1beta1.AzVolume
	var err error
	requiredBytes, _ := pv.Spec.Capacity.Storage().AsInt64()
	volumeCapability := getVolumeCapabilityFromPv(&pv)

	// create AzVolume CRI for CSI Volume Source
	if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == c.driverName {
		azVolume, err = c.createAzVolumeFromCSISource(pv.Spec.CSI)
		if err != nil {
			return err
		}
		if azureutils.IsMultiNodePersistentVolume(pv) {
			azVolume.Spec.MaxMountReplicaCount = 0
		}

		// create AzVolume CRI for AzureDisk Volume Source for migration case
	} else if utilfeature.DefaultFeatureGate.Enabled(features.CSIMigration) &&
		utilfeature.DefaultFeatureGate.Enabled(features.CSIMigrationAzureDisk) &&
		pv.Spec.AzureDisk != nil {
		azVolume = c.createAzVolumeFromAzureDiskVolumeSource(pv.Spec.AzureDisk)
	}

	if azVolume != nil {
		azVolume.Spec.CapacityRange = &diskv1beta1.CapacityRange{RequiredBytes: requiredBytes}
		azVolume.Spec.VolumeCapability = volumeCapability
		azVolume.Spec.PersistentVolume = pv.Name
		azVolume.Status.Annotations = annotations

		klog.Infof("Creating AzVolume (%s) for PV(%s)", azVolume.Name, pv.Name)
		if err := c.createAzVolume(ctx, azVolume); err != nil {
			klog.Errorf("failed to create AzVolume (%s) for PV (%s): %v", azVolume.Name, pv.Name, err)
			return err
		}
	}
	return nil
}

func (c *SharedState) createAzVolumeFromInline(ctx context.Context, inline *v1.AzureDiskVolumeSource) (err error) {
	azVolume := c.createAzVolumeFromAzureDiskVolumeSource(inline)

	if err = c.createAzVolume(ctx, azVolume); err != nil {
		klog.Errorf("failed to create AzVolume (%s) for inline (%s): %v", azVolume.Name, inline.DiskName, err)
	}
	return
}

func (c *SharedState) createAzVolumeFromCSISource(source *v1.CSIPersistentVolumeSource) (*diskv1beta1.AzVolume, error) {
	diskName, err := azureutils.GetDiskName(source.VolumeHandle)
	if err != nil {
		return nil, fmt.Errorf("failed to extract diskName from volume handle (%s): %v", source.VolumeHandle, err)
	}

	_, maxMountReplicaCount := azureutils.GetMaxSharesAndMaxMountReplicaCount(source.VolumeAttributes, false)

	var volumeParams map[string]string
	if source.VolumeAttributes == nil {
		volumeParams = make(map[string]string)
	} else {
		volumeParams = source.VolumeAttributes
	}

	azVolumeName := strings.ToLower(diskName)

	azVolume := diskv1beta1.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:       azVolumeName,
			Finalizers: []string{consts.AzVolumeFinalizer},
		},
		Spec: diskv1beta1.AzVolumeSpec{
			MaxMountReplicaCount: maxMountReplicaCount,
			Parameters:           volumeParams,
			VolumeName:           diskName,
		},
		Status: diskv1beta1.AzVolumeStatus{
			Detail: &diskv1beta1.AzVolumeStatusDetail{
				VolumeID: source.VolumeHandle,
			},
			State: diskv1beta1.VolumeCreated,
		},
	}

	return &azVolume, nil
}

func (c *SharedState) createAzVolumeFromAzureDiskVolumeSource(source *v1.AzureDiskVolumeSource) *diskv1beta1.AzVolume {
	azVolume := diskv1beta1.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:       source.DiskName,
			Finalizers: []string{consts.AzVolumeFinalizer},
		},
		Spec: diskv1beta1.AzVolumeSpec{
			VolumeName:       source.DiskName,
			VolumeCapability: []diskv1beta1.VolumeCapability{},
		},
		Status: diskv1beta1.AzVolumeStatus{
			Detail: &diskv1beta1.AzVolumeStatusDetail{
				VolumeID: source.DataDiskURI,
			},
			State:       diskv1beta1.VolumeCreated,
			Annotations: map[string]string{consts.InlineVolumeAnnotation: source.DataDiskURI},
		},
	}

	return &azVolume
}

func (c *SharedState) createAzVolume(ctx context.Context, azVolume *diskv1beta1.AzVolume) error {
	var updated *diskv1beta1.AzVolume
	var err error
	if updated, err = c.azClient.DiskV1beta1().AzVolumes(c.objectNamespace).Create(ctx, azVolume, metav1.CreateOptions{}); err != nil {
		klog.Errorf("failed to create AzVolume (%s): %v", azVolume.Name, err)
		return err
	}
	updated = updated.DeepCopy()
	updated.Status = azVolume.Status
	if _, err := c.azClient.DiskV1beta1().AzVolumes(c.objectNamespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
		klog.Errorf("failed to update AzVolume (%s) Status: %v", azVolume.Name, err)
		return err
	}
	// if AzVolume CRI successfully recreated, also recreate the operation queue for the volume
	c.createOperationQueue(azVolume.Name)
	return nil
}

func getVolumeCapabilityFromPv(pv *v1.PersistentVolume) []diskv1beta1.VolumeCapability {
	volCaps := []diskv1beta1.VolumeCapability{}

	for _, accessMode := range pv.Spec.AccessModes {
		volCap := diskv1beta1.VolumeCapability{}
		// default to Mount
		if pv.Spec.VolumeMode != nil && *pv.Spec.VolumeMode == v1.PersistentVolumeBlock {
			volCap.AccessType = diskv1beta1.VolumeCapabilityAccessBlock
		}
		switch accessMode {
		case v1.ReadWriteOnce:
			volCap.AccessMode = diskv1beta1.VolumeCapabilityAccessModeSingleNodeSingleWriter
		case v1.ReadWriteMany:
			volCap.AccessMode = diskv1beta1.VolumeCapabilityAccessModeMultiNodeMultiWriter
		case v1.ReadOnlyMany:
			volCap.AccessMode = diskv1beta1.VolumeCapabilityAccessModeMultiNodeReaderOnly
		default:
			volCap.AccessMode = diskv1beta1.VolumeCapabilityAccessModeUnknown
		}
		volCaps = append(volCaps, volCap)
	}
	return volCaps
}
