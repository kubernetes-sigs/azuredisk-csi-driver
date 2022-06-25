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

	"github.com/go-logr/logr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/kubernetes/pkg/features"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	util "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/watcher"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
		capacityRange *azdiskv1beta2.CapacityRange,
		volumeCapabilities []azdiskv1beta2.VolumeCapability,
		parameters map[string]string,
		secrets map[string]string,
		volumeContentSource *azdiskv1beta2.ContentVolumeSource,
		accessibilityTopology *azdiskv1beta2.TopologyRequirement) (*azdiskv1beta2.AzVolumeStatusDetail, error)
	DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error
	ExpandVolume(ctx context.Context, volumeID string, capacityRange *azdiskv1beta2.CapacityRange, secrets map[string]string) (*azdiskv1beta2.AzVolumeStatusDetail, error)
}

//Struct for the reconciler
type ReconcileAzVolume struct {
	logger                logr.Logger
	volumeProvisioner     VolumeProvisioner
	controllerSharedState *SharedState
	// stateLock prevents concurrent cloud operation for same volume to be executed due to state update race
	stateLock *sync.Map
	retryInfo *retryInfo
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &ReconcileAzVolume{}

var allowedTargetVolumeStates = map[string][]string{
	"": {string(azdiskv1beta2.VolumeOperationPending), string(azdiskv1beta2.VolumeCreating), string(azdiskv1beta2.VolumeDeleting)},
	string(azdiskv1beta2.VolumeOperationPending): {string(azdiskv1beta2.VolumeCreating), string(azdiskv1beta2.VolumeDeleting)},
	string(azdiskv1beta2.VolumeCreating):         {string(azdiskv1beta2.VolumeCreated), string(azdiskv1beta2.VolumeCreationFailed)},
	string(azdiskv1beta2.VolumeDeleting):         {string(azdiskv1beta2.VolumeDeleted), string(azdiskv1beta2.VolumeDeletionFailed)},
	string(azdiskv1beta2.VolumeUpdating):         {string(azdiskv1beta2.VolumeUpdated), string(azdiskv1beta2.VolumeUpdateFailed)},
	string(azdiskv1beta2.VolumeCreated):          {string(azdiskv1beta2.VolumeUpdating), string(azdiskv1beta2.VolumeDeleting)},
	string(azdiskv1beta2.VolumeDeleted):          {},
	string(azdiskv1beta2.VolumeUpdated):          {string(azdiskv1beta2.VolumeUpdating), string(azdiskv1beta2.VolumeDeleting)},
	string(azdiskv1beta2.VolumeCreationFailed):   {},
	string(azdiskv1beta2.VolumeDeletionFailed):   {},
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
		return reconcileReturnOnError(ctx, azVolume, "get", err, r.retryInfo)
	}

	ctx, _ = workflow.GetWorkflowFromObj(ctx, azVolume)

	// if underlying cloud operation already in process, skip until operation is completed
	if isOperationInProcess(azVolume) {
		return reconcileReturnOnSuccess(azVolume.Name, r.retryInfo)
	}

	// azVolume deletion
	if objectDeletionRequested(azVolume) {
		if err := r.triggerDelete(ctx, azVolume); err != nil {
			//If delete failed, requeue request
			return reconcileReturnOnError(ctx, azVolume, "delete", err, r.retryInfo)
		}
		//azVolume creation
	} else if azVolume.Status.Detail == nil {
		if err := r.triggerCreate(ctx, azVolume); err != nil {
			return reconcileReturnOnError(ctx, azVolume, "create", err, r.retryInfo)
		}
		// azVolume update
	} else if azVolume.Spec.CapacityRange != nil && azVolume.Spec.CapacityRange.RequiredBytes != azVolume.Status.Detail.CapacityBytes {
		if err := r.triggerUpdate(ctx, azVolume); err != nil {
			return reconcileReturnOnError(ctx, azVolume, "update", err, r.retryInfo)
		}
	}

	return reconcileReturnOnSuccess(azVolume.Name, r.retryInfo)
}

func (r *ReconcileAzVolume) triggerCreate(ctx context.Context, azVolume *azdiskv1beta2.AzVolume) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	// requeue if AzVolume's state is being updated by a different worker
	defer r.stateLock.Delete(azVolume.Name)
	if _, ok := r.stateLock.LoadOrStore(azVolume.Name, nil); ok {
		err = getOperationRequeueError("create", azVolume)
		return err
	}

	// update state
	updateFunc := func(obj client.Object) error {
		azv := obj.(*azdiskv1beta2.AzVolume)
		_, err := r.updateState(azv, azdiskv1beta2.VolumeCreating, normalUpdate)
		return err
	}
	if err = azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}

	w.Logger().Info("Creating Volume...")
	waitCh := make(chan goSignal)

	// create volume
	//nolint:contextcheck // call is asynchronous; context is not inherited by design
	go func() {
		var createErr error
		_, goWorkflow := workflow.New(ctx)
		defer func() { goWorkflow.Finish(createErr) }()
		waitCh <- goSignal{}

		goCtx := goWorkflow.SaveToContext(context.Background())
		cloudCtx, cloudCancel := context.WithTimeout(goCtx, cloudTimeout)
		defer cloudCancel()

		var updateFunc azureutils.UpdateCRIFunc
		var response *azdiskv1beta2.AzVolumeStatusDetail
		response, createErr = r.createVolume(cloudCtx, azVolume)
		updateMode := azureutils.UpdateCRIStatus
		if createErr != nil {
			updateFunc = func(obj client.Object) error {
				azv := obj.(*azdiskv1beta2.AzVolume)
				azv = r.updateError(azv, createErr)
				azv = r.deleteFinalizer(azv, map[string]bool{consts.AzVolumeFinalizer: true})
				_, derr := r.updateState(azv, azdiskv1beta2.VolumeCreationFailed, forceUpdate)
				return derr
			}
			updateMode = azureutils.UpdateAll
		} else {
			// create operation queue for the volume
			r.controllerSharedState.createOperationQueue(azVolume.Name)
			updateFunc = func(obj client.Object) error {
				azv := obj.(*azdiskv1beta2.AzVolume)
				if response == nil {
					return status.Errorf(codes.Internal, "non-nil AzVolumeStatusDetail expected but nil given")
				}
				azv = r.updateStatusDetail(azv, response, response.CapacityBytes, response.NodeExpansionRequired)
				_, derr := r.updateState(azv, azdiskv1beta2.VolumeCreated, forceUpdate)
				return derr
			}
		}

		// UpdateCRIWithRetry should be called on a context w/o timeout when called in a separate goroutine as it is not going to be retriggered and leave the CRI in unrecoverable transient state instead.
		_ = azureutils.UpdateCRIWithRetry(goCtx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.ForcedUpdateMaxNetRetry, updateMode)
	}()

	// wait for the workflow in goroutine to be created
	<-waitCh
	return nil
}

func (r *ReconcileAzVolume) triggerDelete(ctx context.Context, azVolume *azdiskv1beta2.AzVolume) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

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

	// only try deleting underlying volume 1) if volume creation was successful and 2) volumeDeleteRequestAnnotation is present
	// if the annotation is not present, only delete the CRI and not the underlying volume
	if isCreated(azVolume) && mode == detachAndDeleteCRI {
		// requeue if AzVolume's state is being updated by a different worker
		defer r.stateLock.Delete(azVolume.Name)
		if _, ok := r.stateLock.LoadOrStore(azVolume.Name, nil); ok {
			return getOperationRequeueError("delete", azVolume)
		}
		updateFunc := func(obj client.Object) error {
			azv := obj.(*azdiskv1beta2.AzVolume)
			_, derr := r.updateState(azv, azdiskv1beta2.VolumeDeleting, normalUpdate)
			return derr
		}

		if err = azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
			return err
		}

		if volumeDeleteRequested {
			w.Logger().Info("Deleting Volume...")
		}

		waitCh := make(chan goSignal)
		//nolint:contextcheck // call is asynchronous; context is not inherited by design
		go func() {
			_, goWorkflow := workflow.New(ctx)
			var deleteErr error
			defer func() { goWorkflow.Finish(deleteErr) }()
			waitCh <- goSignal{}

			goCtx := goWorkflow.SaveToContext(context.Background())

			deleteCtx, deleteCancel := context.WithTimeout(goCtx, cloudTimeout)
			defer deleteCancel()

			reportError := func(obj interface{}, err error) error {
				azv := obj.(*azdiskv1beta2.AzVolume)
				_ = r.updateError(azv, err)
				_, derr := r.updateState(azv, azdiskv1beta2.VolumeDeletionFailed, forceUpdate)
				return derr
			}

			var updateFunc azureutils.UpdateCRIFunc
			var err error
			updateMode := azureutils.UpdateCRIStatus

			// Delete all AzVolumeAttachment objects bound to the deleted AzVolume
			var attachments []azdiskv1beta2.AzVolumeAttachment
			attachments, err = r.controllerSharedState.cleanUpAzVolumeAttachmentByVolume(deleteCtx, azVolume.Name, azvolume, all, mode)
			if err != nil {
				updateFunc = func(obj client.Object) error {
					return reportError(obj, err)
				}
			} else {
				var wg sync.WaitGroup
				errors := make([]error, len(attachments))
				numErrors := uint32(0)

				// start waiting for replica AzVolumeAttachment CRIs to be deleted
				for i, attachment := range attachments {
					waiter, err := r.controllerSharedState.conditionWatcher.NewConditionWaiter(deleteCtx, watcher.AzVolumeAttachmentType, attachment.Name, verifyObjectDeleted)
					if err != nil {
						updateFunc = func(obj client.Object) error {
							return reportError(obj, err)
						}
						break
					}

					// wait async and report error to go channel
					wg.Add(1)
					go func(ctx context.Context, waiter *watcher.ConditionWaiter, i int) {
						defer waiter.Close()
						defer wg.Done()
						_, err := waiter.Wait(ctx)
						if err != nil {
							errors[i] = err
							atomic.AddUint32(&numErrors, 1)
						}
					}(deleteCtx, waiter, i)
				}

				wg.Wait()

				// if errors have been found with the wait calls, format the error msg and report via CRI
				if numErrors > 0 {
					var errMsgs []string
					for i, derr := range errors {
						if derr != nil {
							errMsgs = append(errMsgs, fmt.Sprintf("%s: %v", attachments[i].Name, derr))
						}
					}
					err = status.Errorf(codes.Internal, strings.Join(errMsgs, ", "))
					updateFunc = func(obj client.Object) error {
						return reportError(obj, err)
					}
				}
			}

			if err == nil {
				if volumeDeleteRequested {
					cloudCtx, cloudCancel := context.WithTimeout(goCtx, cloudTimeout)
					defer cloudCancel()

					deleteErr = r.deleteVolume(cloudCtx, azVolume)
				}
				if deleteErr != nil {
					updateFunc = func(obj client.Object) error {
						azv := obj.(*azdiskv1beta2.AzVolume)
						azv = r.updateError(azv, deleteErr)
						_, derr := r.updateState(azv, azdiskv1beta2.VolumeDeletionFailed, forceUpdate)
						return derr
					}
				} else {
					updateMode = azureutils.UpdateAll
					updateFunc = func(obj client.Object) error {
						azv := obj.(*azdiskv1beta2.AzVolume)
						_ = r.deleteFinalizer(azv, map[string]bool{consts.AzVolumeFinalizer: true})
						return nil
					}
				}
			}

			// UpdateCRIWithRetry should be called on a context w/o timeout when called in a separate goroutine as it is not going to be retriggered and leave the CRI in unrecoverable transient state instead.
			_ = azureutils.UpdateCRIWithRetry(goCtx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.ForcedUpdateMaxNetRetry, updateMode)
		}()
		<-waitCh
	} else {
		updateFunc := func(obj client.Object) error {
			azv := obj.(*azdiskv1beta2.AzVolume)
			_ = r.deleteFinalizer(azv, map[string]bool{consts.AzVolumeFinalizer: true})
			return nil
		}
		if err = azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRI); err != nil {
			return err
		}
	}
	return nil
}

func (r *ReconcileAzVolume) triggerUpdate(ctx context.Context, azVolume *azdiskv1beta2.AzVolume) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	// requeue if AzVolume's state is being updated by a different worker
	defer r.stateLock.Delete(azVolume.Name)
	if _, ok := r.stateLock.LoadOrStore(azVolume.Name, nil); ok {
		err = getOperationRequeueError("update", azVolume)
		return err
	}
	updateFunc := func(obj client.Object) error {
		azv := obj.(*azdiskv1beta2.AzVolume)
		_, derr := r.updateState(azv, azdiskv1beta2.VolumeUpdating, normalUpdate)
		return derr
	}
	if err = azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}

	w.Logger().Infof("Updating Volume")
	waitCh := make(chan goSignal)
	//nolint:contextcheck // call is asynchronous; context is not inherited by design
	go func() {
		var updateErr error
		_, goWorkflow := workflow.New(ctx)
		defer goWorkflow.Finish(updateErr)
		waitCh <- goSignal{}

		goCtx := goWorkflow.SaveToContext(ctx)
		cloudCtx, cloudCancel := context.WithTimeout(goCtx, cloudTimeout)
		defer cloudCancel()

		var updateFunc azureutils.UpdateCRIFunc
		var response *azdiskv1beta2.AzVolumeStatusDetail
		response, updateErr = r.expandVolume(cloudCtx, azVolume)
		if updateErr != nil {
			updateFunc = func(obj client.Object) error {
				azv := obj.(*azdiskv1beta2.AzVolume)
				azv = r.updateError(azv, updateErr)
				_, derr := r.updateState(azv, azdiskv1beta2.VolumeUpdateFailed, forceUpdate)
				return derr
			}
		} else {
			updateFunc = func(obj client.Object) error {
				azv := obj.(*azdiskv1beta2.AzVolume)
				if response == nil {
					return status.Errorf(codes.Internal, "non-nil AzVolumeStatusDetail expected but nil given")
				}
				azv = r.updateStatusDetail(azv, response, response.CapacityBytes, response.NodeExpansionRequired)
				_, derr := r.updateState(azv, azdiskv1beta2.VolumeUpdated, forceUpdate)
				return derr
			}
		}

		// UpdateCRIWithRetry should be called on a context w/o timeout when called in a separate goroutine as it is not going to be retriggered and leave the CRI in unrecoverable transient state instead.
		_ = azureutils.UpdateCRIWithRetry(goCtx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolume, updateFunc, consts.ForcedUpdateMaxNetRetry, azureutils.UpdateCRIStatus)
	}()
	<-waitCh

	return nil
}

func (r *ReconcileAzVolume) deleteFinalizer(azVolume *azdiskv1beta2.AzVolume, finalizersToDelete map[string]bool) *azdiskv1beta2.AzVolume {
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

func (r *ReconcileAzVolume) updateState(azVolume *azdiskv1beta2.AzVolume, state azdiskv1beta2.AzVolumeState, mode updateMode) (*azdiskv1beta2.AzVolume, error) {
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

func (r *ReconcileAzVolume) updateStatusDetail(azVolume *azdiskv1beta2.AzVolume, status *azdiskv1beta2.AzVolumeStatusDetail, capacityBytes int64, nodeExpansionRequired bool) *azdiskv1beta2.AzVolume {
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

func (r *ReconcileAzVolume) updateError(azVolume *azdiskv1beta2.AzVolume, err error) *azdiskv1beta2.AzVolume {
	if azVolume == nil {
		return nil
	}

	azVolume.Status.Error = util.NewAzError(err)

	return azVolume
}

func (r *ReconcileAzVolume) expandVolume(ctx context.Context, azVolume *azdiskv1beta2.AzVolume) (*azdiskv1beta2.AzVolumeStatusDetail, error) {
	if azVolume.Status.Detail == nil {
		err := status.Errorf(codes.Internal, "Disk for expansion does not exist for AzVolume (%s).", azVolume.Name)
		return nil, err
	}
	// use deep-copied version of the azVolume CRI to prevent any unwanted update to the object
	copied := azVolume.DeepCopy()
	return r.volumeProvisioner.ExpandVolume(ctx, copied.Status.Detail.VolumeID, copied.Spec.CapacityRange, copied.Spec.Secrets)
}

func (r *ReconcileAzVolume) createVolume(ctx context.Context, azVolume *azdiskv1beta2.AzVolume) (*azdiskv1beta2.AzVolumeStatusDetail, error) {
	if azVolume.Status.Detail != nil {
		return azVolume.Status.Detail, nil
	}
	// use deep-copied version of the azVolume CRI to prevent any unwanted update to the object
	copied := azVolume.DeepCopy()
	return r.volumeProvisioner.CreateVolume(ctx, copied.Spec.VolumeName, copied.Spec.CapacityRange, copied.Spec.VolumeCapability, copied.Spec.Parameters, copied.Spec.Secrets, copied.Spec.ContentVolumeSource, copied.Spec.AccessibilityRequirements)
}

func (r *ReconcileAzVolume) deleteVolume(ctx context.Context, azVolume *azdiskv1beta2.AzVolume) error {
	if azVolume.Status.Detail == nil {
		return nil
	}
	// use deep-copied version of the azVolume CRI to prevent any unwanted update to the object
	copied := azVolume.DeepCopy()
	return r.volumeProvisioner.DeleteVolume(ctx, copied.Status.Detail.VolumeID, copied.Spec.Secrets)
}

func (r *ReconcileAzVolume) recreateAzVolumes(ctx context.Context) error {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	// Get PV list and create AzVolume for PV with azuredisk CSI spec
	pvs, err := r.controllerSharedState.kubeClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		w.Logger().Error(err, "failed to get PV list")
	}

	for _, pv := range pvs.Items {
		if err := r.controllerSharedState.createAzVolumeFromPv(ctx, pv, make(map[string]string)); err != nil {
			w.Logger().Errorf(err, "failed to recover AzVolume for PV (%s)", pv.Name)
		}
	}
	return nil
}

func (r *ReconcileAzVolume) recoverAzVolume(ctx context.Context, recoveredAzVolumes *sync.Map) error {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	// list all AzVolumes
	azVolumes, err := r.controllerSharedState.azClient.DiskV1beta2().AzVolumes(r.controllerSharedState.objectNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		w.Logger().Error(err, "failed to get list of existing AzVolume CRI in controller recovery stage")
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
		go func(azv azdiskv1beta2.AzVolume, azvMap *sync.Map) {
			defer wg.Done()
			var targetState azdiskv1beta2.AzVolumeState
			updateFunc := func(obj client.Object) error {
				var err error
				azv := obj.(*azdiskv1beta2.AzVolume)
				// add a recover annotation to the CRI so that reconciliation can be triggered for the CRI even if CRI's current state == target state
				azv.Status.Annotations = azureutils.AddToMap(azv.Status.Annotations, consts.RecoverAnnotation, "azVolume")
				if azv.Status.State != targetState {
					_, err = r.updateState(azv, targetState, forceUpdate)
				}
				return err
			}
			switch azv.Status.State {
			case azdiskv1beta2.VolumeCreating:
				// reset state to Pending so Create operation can be redone
				targetState = azdiskv1beta2.VolumeOperationPending
			case azdiskv1beta2.VolumeDeleting:
				// reset state to Created so Delete operation can be redone
				targetState = azdiskv1beta2.VolumeCreated
			case azdiskv1beta2.VolumeUpdating:
				// reset state to Created so Update operation can be redone
				targetState = azdiskv1beta2.VolumeCreated
			default:
				targetState = azv.Status.State
			}

			if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, &azv, updateFunc, consts.ForcedUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
				w.Logger().Errorf(err, "failed to update AzVolume (%s) for recovery", azv.Name)
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
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	// recover CRI if possible
	w.Logger().Info("Recovering AzVolume CRIs...")
	if err = r.recreateAzVolumes(ctx); err != nil {
		w.Logger().Error(err, "failed to recreate missing AzVolume CRI")
	}

	recovered := &sync.Map{}
	for i := 0; i < maxRetry; i++ {
		if err = r.recoverAzVolume(ctx, recovered); err == nil {
			break
		}
		w.Logger().Error(err, "failed to recover AzVolume state")
	}
	return err
}

func NewAzVolumeController(mgr manager.Manager, volumeProvisioner VolumeProvisioner, controllerSharedState *SharedState) (*ReconcileAzVolume, error) {
	logger := mgr.GetLogger().WithValues("controller", "azvolume")

	reconciler := ReconcileAzVolume{
		volumeProvisioner:     volumeProvisioner,
		stateLock:             &sync.Map{},
		retryInfo:             newRetryInfo(),
		controllerSharedState: controllerSharedState,
		logger:                logger,
	}

	c, err := controller.New("azvolume-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		LogConstructor:          func(req *reconcile.Request) logr.Logger { return logger },
	})

	if err != nil {
		logger.Error(err, "failed to create controller")
		return nil, err
	}

	logger.V(2).Info("Starting to watch AzVolume.")

	// Watch for CRUD events on azVolume objects
	err = c.Watch(&source.Kind{Type: &azdiskv1beta2.AzVolume{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		logger.Error(err, "failed to initialize watch for AzVolume CRI")
		return nil, err
	}

	logger.V(2).Info("Controller set-up successful.")

	return &reconciler, nil
}

func (c *SharedState) createAzVolumeFromPv(ctx context.Context, pv v1.PersistentVolume, annotations map[string]string) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	var azVolume *azdiskv1beta2.AzVolume
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
		if azVolume.Labels == nil {
			azVolume.Labels = map[string]string{}
		}
		azVolume.Labels[consts.PvNameLabel] = pv.Name
		if pv.Spec.ClaimRef != nil {
			azVolume.Labels[consts.PvcNameLabel] = pv.Spec.ClaimRef.Name
			azVolume.Labels[consts.PvcNamespaceLabel] = pv.Spec.ClaimRef.Namespace
		}
		azVolume.Spec.CapacityRange = &azdiskv1beta2.CapacityRange{RequiredBytes: requiredBytes}
		azVolume.Spec.VolumeCapability = volumeCapability
		azVolume.Spec.PersistentVolume = pv.Name
		azVolume.Status.Annotations = annotations

		w.AddDetailToLogger(consts.PvNameKey, pv.Name, consts.VolumeNameLabel, azVolume.Name)

		w.Logger().Info("Creating AzVolume CRI")
		if err = c.createAzVolume(ctx, azVolume); err != nil {
			err = status.Errorf(codes.Internal, "failed to create AzVolume (%s) for PV (%s): %v", azVolume.Name, pv.Name, err)
			return err
		}
	}
	return nil
}

func (c *SharedState) createAzVolumeFromInline(ctx context.Context, inline *v1.AzureDiskVolumeSource) (err error) {
	azVolume := c.createAzVolumeFromAzureDiskVolumeSource(inline)

	if err = c.createAzVolume(ctx, azVolume); err != nil {
		err = status.Errorf(codes.Internal, "failed to create AzVolume (%s) for inline (%s): %v", azVolume.Name, inline.DiskName, err)
	}
	return
}

func (c *SharedState) createAzVolumeFromCSISource(source *v1.CSIPersistentVolumeSource) (*azdiskv1beta2.AzVolume, error) {
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

	azVolume := azdiskv1beta2.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:       azVolumeName,
			Finalizers: []string{consts.AzVolumeFinalizer},
		},
		Spec: azdiskv1beta2.AzVolumeSpec{
			MaxMountReplicaCount: maxMountReplicaCount,
			Parameters:           volumeParams,
			VolumeName:           diskName,
		},
		Status: azdiskv1beta2.AzVolumeStatus{
			Detail: &azdiskv1beta2.AzVolumeStatusDetail{
				VolumeID: source.VolumeHandle,
			},
			State: azdiskv1beta2.VolumeCreated,
		},
	}

	return &azVolume, nil
}

func (c *SharedState) createAzVolumeFromAzureDiskVolumeSource(source *v1.AzureDiskVolumeSource) *azdiskv1beta2.AzVolume {
	azVolume := azdiskv1beta2.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:       source.DiskName,
			Finalizers: []string{consts.AzVolumeFinalizer},
		},
		Spec: azdiskv1beta2.AzVolumeSpec{
			VolumeName:       source.DiskName,
			VolumeCapability: []azdiskv1beta2.VolumeCapability{},
		},
		Status: azdiskv1beta2.AzVolumeStatus{
			Detail: &azdiskv1beta2.AzVolumeStatusDetail{
				VolumeID: source.DataDiskURI,
			},
			State:       azdiskv1beta2.VolumeCreated,
			Annotations: map[string]string{consts.InlineVolumeAnnotation: source.DataDiskURI},
		},
	}

	return &azVolume
}

func (c *SharedState) createAzVolume(ctx context.Context, azVolume *azdiskv1beta2.AzVolume) error {
	var updated *azdiskv1beta2.AzVolume
	var err error

	if updated, err = c.azClient.DiskV1beta2().AzVolumes(c.objectNamespace).Create(ctx, azVolume, metav1.CreateOptions{}); err != nil {
		return err
	}
	updated = updated.DeepCopy()
	updated.Status = azVolume.Status
	if _, err := c.azClient.DiskV1beta2().AzVolumes(c.objectNamespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return err
	}
	// if AzVolume CRI successfully recreated, also recreate the operation queue for the volume
	c.createOperationQueue(azVolume.Name)
	return nil
}

func getVolumeCapabilityFromPv(pv *v1.PersistentVolume) []azdiskv1beta2.VolumeCapability {
	volCaps := []azdiskv1beta2.VolumeCapability{}

	for _, accessMode := range pv.Spec.AccessModes {
		volCap := azdiskv1beta2.VolumeCapability{}
		// default to Mount
		if pv.Spec.VolumeMode != nil && *pv.Spec.VolumeMode == v1.PersistentVolumeBlock {
			volCap.AccessType = azdiskv1beta2.VolumeCapabilityAccessBlock
		}
		switch accessMode {
		case v1.ReadWriteOnce:
			volCap.AccessMode = azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeSingleWriter
		case v1.ReadWriteMany:
			volCap.AccessMode = azdiskv1beta2.VolumeCapabilityAccessModeMultiNodeMultiWriter
		case v1.ReadOnlyMany:
			volCap.AccessMode = azdiskv1beta2.VolumeCapabilityAccessModeMultiNodeReaderOnly
		default:
			volCap.AccessMode = azdiskv1beta2.VolumeCapabilityAccessModeUnknown
		}
		volCaps = append(volCaps, volCap)
	}
	return volCaps
}
