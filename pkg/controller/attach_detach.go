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
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-logr/logr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	azdiskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"

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
	UnpublishVolume(ctx context.Context, volumeID string, nodeID string, secrets map[string]string, mode consts.UnpublishMode) error
	WaitForDetach(ctx context.Context, volumeID, nodeID string) error
}

/*
Attach Detach controller is responsible for
	1. attaching volume to a specified node upon creation of AzVolumeAttachment CRI
	2. promoting AzVolumeAttachment to primary upon spec update
	3. detaching volume upon deletions marked with certain annotations
*/
type ReconcileAttachDetach struct {
	logger                logr.Logger
	crdDetacher           CrdDetacher
	cloudDiskAttacher     CloudDiskAttachDetacher
	controllerSharedState *SharedState
	stateLock             *sync.Map
	retryInfo             *retryInfo
}

var _ reconcile.Reconciler = &ReconcileAttachDetach{}

var allowedTargetAttachmentStates = map[string][]string{
	"":                                       {string(azdiskv1beta1.AttachmentPending), string(azdiskv1beta1.Attaching), string(azdiskv1beta1.Detaching)},
	string(azdiskv1beta1.AttachmentPending):  {string(azdiskv1beta1.Attaching), string(azdiskv1beta1.Detaching)},
	string(azdiskv1beta1.Attaching):          {string(azdiskv1beta1.Attached), string(azdiskv1beta1.AttachmentFailed)},
	string(azdiskv1beta1.Detaching):          {string(azdiskv1beta1.Detached), string(azdiskv1beta1.DetachmentFailed)},
	string(azdiskv1beta1.Attached):           {string(azdiskv1beta1.Detaching)},
	string(azdiskv1beta1.Detached):           {},
	string(azdiskv1beta1.AttachmentFailed):   {string(azdiskv1beta1.Detaching)},
	string(azdiskv1beta1.DetachmentFailed):   {string(azdiskv1beta1.ForceDetachPending)},
	string(azdiskv1beta1.ForceDetachPending): {string(azdiskv1beta1.Detaching)},
}

func (r *ReconcileAttachDetach) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if !r.controllerSharedState.isRecoveryComplete() {
		return reconcile.Result{Requeue: true}, nil
	}

	azVolumeAttachment, err := azureutils.GetAzVolumeAttachment(ctx, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, request.Name, request.Namespace, true)
	// if object is not found, it means the object has been deleted. Log the deletion and do not requeue
	if errors.IsNotFound(err) {
		return reconcileReturnOnSuccess(request.Name, r.retryInfo)
	} else if err != nil {
		azVolumeAttachment.Name = request.Name
		return reconcileReturnOnError(ctx, azVolumeAttachment, "get", err, r.retryInfo)
	}

	ctx, _ = workflow.GetWorkflowFromObj(ctx, azVolumeAttachment)

	// if underlying cloud operation already in process, skip until operation is completed
	if isOperationInProcess(azVolumeAttachment) {
		return reconcileReturnOnSuccess(azVolumeAttachment.Name, r.retryInfo)
	}

	// detachment request
	if objectDeletionRequested(azVolumeAttachment) {
		if err := r.triggerDetach(ctx, azVolumeAttachment); err != nil {
			return reconcileReturnOnError(ctx, azVolumeAttachment, "detach", err, r.retryInfo)
		}
		// attachment request
	} else if azVolumeAttachment.Status.Detail == nil {
		if err := r.triggerAttach(ctx, azVolumeAttachment); err != nil {
			return reconcileReturnOnError(ctx, azVolumeAttachment, "attach", err, r.retryInfo)
		}
		// promotion request
	} else if azVolumeAttachment.Spec.RequestedRole != azVolumeAttachment.Status.Detail.Role {
		switch azVolumeAttachment.Spec.RequestedRole {
		case azdiskv1beta1.PrimaryRole:
			if err := r.promote(ctx, azVolumeAttachment); err != nil {
				return reconcileReturnOnError(ctx, azVolumeAttachment, "promote", err, r.retryInfo)
			}
		case azdiskv1beta1.ReplicaRole:
			if err := r.demote(ctx, azVolumeAttachment); err != nil {
				return reconcileReturnOnError(ctx, azVolumeAttachment, "demote", err, r.retryInfo)
			}
		}
	}

	return reconcileReturnOnSuccess(azVolumeAttachment.Name, r.retryInfo)
}

func (r *ReconcileAttachDetach) triggerAttach(ctx context.Context, azVolumeAttachment *azdiskv1beta1.AzVolumeAttachment) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	// requeue if AzVolumeAttachment's state is being updated by a different worker
	defer r.stateLock.Delete(azVolumeAttachment.Name)
	if _, ok := r.stateLock.LoadOrStore(azVolumeAttachment.Name, nil); ok {
		err = getOperationRequeueError("attach", azVolumeAttachment)
		return err
	}

	var azVolume *azdiskv1beta1.AzVolume
	if azVolume, err = azureutils.GetAzVolume(ctx, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, strings.ToLower(azVolumeAttachment.Spec.VolumeName), r.controllerSharedState.objectNamespace, true); err != nil {
		if errors.IsNotFound(err) {
			w.Logger().V(5).Infof("Aborting attach operation for AzVolumeAttachment (%s): AzVolume (%s) not found", azVolumeAttachment.Name, azVolumeAttachment.Spec.VolumeName)
			err = nil
			return nil
		}
		return err
	} else if objectDeletionRequested(azVolume) {
		w.Logger().V(5).Infof("Aborting attach operation for AzVolumeAttachment (%s): AzVolume (%s) scheduled for deletion", azVolumeAttachment.Name, azVolumeAttachment.Spec.VolumeName)
		return nil
	}

	// update status block
	updateFunc := func(obj interface{}) error {
		azv := obj.(*azdiskv1beta1.AzVolumeAttachment)
		// Update state to attaching, Initialize finalizer and add label to the object
		_, derr := updateState(azv, azdiskv1beta1.Attaching, normalUpdate)
		return derr
	}
	if err = azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}

	w.Logger().Info("Attaching volume")
	waitCh := make(chan goSignal)
	//nolint:contextcheck // call is asynchronous; context is not inherited by design
	go func() {
		var attachErr error
		_, goWorkflow := workflow.New(ctx)
		defer func() { goWorkflow.Finish(attachErr) }()
		waitCh <- goSignal{}

		goCtx := goWorkflow.SaveToContext(context.Background())
		cloudCtx, cloudCancel := context.WithTimeout(goCtx, cloudTimeout)
		defer cloudCancel()

		// attempt to attach the disk to a node
		var response map[string]string
		response, attachErr = r.attachVolume(cloudCtx, azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Spec.VolumeContext)
		// if the disk is attached to a different node
		if danglingAttachErr, ok := attachErr.(*volerr.DanglingAttachError); ok {
			// get disk, current node and attachment name
			currentNodeName := string(danglingAttachErr.CurrentNode)
			currentAttachmentName := azureutils.GetAzVolumeAttachmentName(azVolumeAttachment.Spec.VolumeName, currentNodeName)
			goWorkflow.Logger().Infof("Dangling attach detected for %s", currentNodeName)

			// check if AzVolumeAttachment exists for the existing attachment
			_, err := r.controllerSharedState.azClient.DiskV1beta1().AzVolumeAttachments(r.controllerSharedState.objectNamespace).Get(cloudCtx, currentAttachmentName, metav1.GetOptions{})
			var detachErr error
			if errors.IsNotFound(err) {
				// AzVolumeAttachment doesn't exist so we only need to detach disk from cloud
				detachErr = r.cloudDiskAttacher.UnpublishVolume(goCtx, azVolumeAttachment.Spec.VolumeID, currentNodeName)
				if detachErr != nil {
					goWorkflow.Logger().Errorf(detachErr, "failed to detach dangling volume (%s) from node (%s). Error: %v", azVolumeAttachment.Spec.VolumeID, currentNodeName, err)
				}
			} else {
				// AzVolumeAttachment exist so we need to detach disk through crdProvisioner
				detachErr = r.crdDetacher.UnpublishVolume(goCtx, azVolumeAttachment.Spec.VolumeID, currentNodeName, make(map[string]string), consts.Detach)
				if detachErr != nil {
					goWorkflow.Logger().Errorf(detachErr, "failed to make a unpublish request dangling AzVolumeAttachment for volume (%s) and node (%s)", azVolumeAttachment.Spec.VolumeID, currentNodeName)
				}
				detachErr = r.crdDetacher.WaitForDetach(goCtx, azVolumeAttachment.Spec.VolumeID, currentNodeName)
				if detachErr != nil {
					goWorkflow.Logger().Errorf(detachErr, "failed to unpublish dangling AzVolumeAttachment for volume (%s) and node (%s)", azVolumeAttachment.Spec.VolumeID, currentNodeName)
				}
			}
			// attempt to attach the disk to a node after detach
			if detachErr == nil {
				response, attachErr = r.attachVolume(cloudCtx, azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Spec.VolumeContext)
			}
		}
		var pods []v1.Pod
		if azVolumeAttachment.Spec.RequestedRole == azdiskv1beta1.ReplicaRole {
			var err error
			pods, err = r.controllerSharedState.getPodsFromVolume(goCtx, r.controllerSharedState.cachedClient, azVolumeAttachment.Spec.VolumeName)
			if err != nil {
				goWorkflow.Logger().Error(err, "failed to list pods for volume")
			}
		}

		// update AzVolumeAttachment CRI with the result of the attach operation
		var updateFunc func(interface{}) error
		if attachErr != nil {
			if len(pods) > 0 {
				for _, pod := range pods {
					r.controllerSharedState.eventRecorder.Eventf(pod.DeepCopyObject(), v1.EventTypeWarning, consts.ReplicaAttachmentFailedEvent, "Replica mount for volume %s failed to be attached to node %s with error: %v", azVolumeAttachment.Spec.VolumeName, azVolumeAttachment.Spec.NodeName, attachErr)
				}
			}

			updateFunc = func(obj interface{}) error {
				azv := obj.(*azdiskv1beta1.AzVolumeAttachment)
				azv = updateError(azv, attachErr)
				_, uerr := updateState(azv, azdiskv1beta1.AttachmentFailed, forceUpdate)
				return uerr
			}
		} else {
			// Publish event to indicate attachment success
			if len(pods) > 0 {
				for _, pod := range pods {
					r.controllerSharedState.eventRecorder.Eventf(pod.DeepCopyObject(), v1.EventTypeNormal, consts.ReplicaAttachmentSuccessEvent, "Replica mount for volume %s successfully attached to node %s", azVolumeAttachment.Spec.VolumeName, azVolumeAttachment.Spec.NodeName)
				}
			}

			updateFunc = func(obj interface{}) error {
				azv := obj.(*azdiskv1beta1.AzVolumeAttachment)
				azv = updateStatusDetail(azv, response)
				_, uerr := updateState(azv, azdiskv1beta1.Attached, forceUpdate)
				return uerr
			}
		}

		// UpdateCRIWithRetry should be called on a context w/o timeout when called in a separate goroutine as it is not going to be retriggered and leave the CRI in unrecoverable transient state instead.
		_ = azureutils.UpdateCRIWithRetry(goCtx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.ForcedUpdateMaxNetRetry, azureutils.UpdateCRIStatus)
	}()
	<-waitCh

	return nil
}

func (r *ReconcileAttachDetach) triggerDetach(ctx context.Context, azVolumeAttachment *azdiskv1beta1.AzVolumeAttachment) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()
	// only detach if detachment request was made for underlying volume attachment object
	detachmentRequested := volumeDetachRequested(azVolumeAttachment)

	if detachmentRequested {
		defer r.stateLock.Delete(azVolumeAttachment.Name)
		if _, ok := r.stateLock.LoadOrStore(azVolumeAttachment.Name, nil); ok {
			return getOperationRequeueError("detach", azVolumeAttachment)
		}

		updateFunc := func(obj interface{}) error {
			azv := obj.(*azdiskv1beta1.AzVolumeAttachment)
			// Update state to detaching
			_, derr := updateState(azv, azdiskv1beta1.Detaching, normalUpdate)
			return derr
		}
		if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
			return err
		}

		w.Logger().Info("Detaching volume")
		waitCh := make(chan goSignal)
		//nolint:contextcheck // call is asynchronous; context is not inherited by design
		go func() {
			var detachErr error
			_, goWorkflow := workflow.New(ctx)
			defer func() { goWorkflow.Finish(detachErr) }()
			waitCh <- goSignal{}

			goCtx := goWorkflow.SaveToContext(context.Background())
			cloudCtx, cloudCancel := context.WithTimeout(goCtx, cloudTimeout)
			defer cloudCancel()

			var updateFunc func(obj interface{}) error
			updateMode := azureutils.UpdateCRIStatus
			detachErr = r.detachVolume(cloudCtx, azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName)
			if detachErr != nil {
				updateFunc = func(obj interface{}) error {
					azv := obj.(*azdiskv1beta1.AzVolumeAttachment)
					azv = updateError(azv, detachErr)
					_, derr := updateState(azv, azdiskv1beta1.DetachmentFailed, forceUpdate)
					return derr
				}
			} else {
				updateFunc = func(obj interface{}) error {
					azv := obj.(*azdiskv1beta1.AzVolumeAttachment)
					azv = r.deleteFinalizer(azv)
					_, derr := updateState(azv, azdiskv1beta1.Detached, forceUpdate)
					return derr
				}
				updateMode = azureutils.UpdateAll
			}
			// UpdateCRIWithRetry should be called on a context w/o timeout when called in a separate goroutine as it is not going to be retriggered and leave the CRI in unrecoverable transient state instead.
			_ = azureutils.UpdateCRIWithRetry(goCtx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.ForcedUpdateMaxNetRetry, updateMode)
		}()
		<-waitCh
	} else {
		updateFunc := func(obj interface{}) error {
			azv := obj.(*azdiskv1beta1.AzVolumeAttachment)
			// delete finalizer
			_ = r.deleteFinalizer(azv)
			return nil
		}
		if err = azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRI); err != nil {
			return err
		}
	}
	return nil
}

func (r *ReconcileAttachDetach) promote(ctx context.Context, azVolumeAttachment *azdiskv1beta1.AzVolumeAttachment) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	w.Logger().Infof("Promoting AzVolumeAttachment")
	// initialize metadata and update status block
	updateFunc := func(obj interface{}) error {
		azv := obj.(*azdiskv1beta1.AzVolumeAttachment)
		_ = updateRole(azv, azdiskv1beta1.PrimaryRole)
		return nil
	}
	if err = azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}
	return nil
}

func (r *ReconcileAttachDetach) demote(ctx context.Context, azVolumeAttachment *azdiskv1beta1.AzVolumeAttachment) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	w.Logger().Info("Demoting AzVolumeAttachment")
	// initialize metadata and update status block
	updateFunc := func(obj interface{}) error {
		azv := obj.(*azdiskv1beta1.AzVolumeAttachment)
		_ = updateRole(azv, azdiskv1beta1.ReplicaRole)
		return nil
	}

	if err = azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}
	return nil
}

func (r *ReconcileAttachDetach) deleteFinalizer(azVolumeAttachment *azdiskv1beta1.AzVolumeAttachment) *azdiskv1beta1.AzVolumeAttachment {
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
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	w.Logger().Info("Recovering AzVolumeAttachment CRIs...")
	// try to recreate missing AzVolumeAttachment CRI
	var syncedVolumeAttachments, volumesToSync map[string]bool

	for i := 0; i < maxRetry; i++ {
		if syncedVolumeAttachments, volumesToSync, err = r.recreateAzVolumeAttachment(ctx, syncedVolumeAttachments, volumesToSync); err == nil {
			break
		}
		w.Logger().Error(err, "failed to recreate missing AzVolumeAttachment CRI")
	}
	// retrigger any aborted operation from possible previous controller crash
	recovered := &sync.Map{}
	for i := 0; i < maxRetry; i++ {
		if err = r.recoverAzVolumeAttachment(ctx, recovered); err == nil {
			break
		}
		w.Logger().Error(err, "failed to recover AzVolumeAttachment state")
	}

	return err
}

func updateRole(azVolumeAttachment *azdiskv1beta1.AzVolumeAttachment, role azdiskv1beta1.Role) *azdiskv1beta1.AzVolumeAttachment {
	if azVolumeAttachment == nil {
		return nil
	}

	if azVolumeAttachment.Status.Detail == nil {
		return azVolumeAttachment
	}

	azVolumeAttachment.Status.Detail.PreviousRole = azVolumeAttachment.Status.Detail.Role
	azVolumeAttachment.Status.Detail.Role = role

	return azVolumeAttachment
}

func updateStatusDetail(azVolumeAttachment *azdiskv1beta1.AzVolumeAttachment, status map[string]string) *azdiskv1beta1.AzVolumeAttachment {
	if azVolumeAttachment == nil {
		return nil
	}

	if azVolumeAttachment.Status.Detail == nil {
		azVolumeAttachment.Status.Detail = &azdiskv1beta1.AzVolumeAttachmentStatusDetail{}
	}

	azVolumeAttachment.Status.Detail.PreviousRole = azVolumeAttachment.Status.Detail.Role
	azVolumeAttachment.Status.Detail.Role = azVolumeAttachment.Spec.RequestedRole
	azVolumeAttachment.Status.Detail.PublishContext = status

	return azVolumeAttachment
}

func updateError(azVolumeAttachment *azdiskv1beta1.AzVolumeAttachment, err error) *azdiskv1beta1.AzVolumeAttachment {
	if azVolumeAttachment == nil {
		return nil
	}

	if err != nil {
		azVolumeAttachment.Status.Error = util.NewAzError(err)
	}

	return azVolumeAttachment
}

func updateState(azVolumeAttachment *azdiskv1beta1.AzVolumeAttachment, state azdiskv1beta1.AzVolumeAttachmentAttachmentState, mode updateMode) (*azdiskv1beta1.AzVolumeAttachment, error) {
	var err error
	if azVolumeAttachment == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "function `updateState` requires non-nil AzVolumeAttachment object.")
	}
	if mode == normalUpdate {
		if expectedStates, exists := allowedTargetAttachmentStates[string(azVolumeAttachment.Status.State)]; !exists || !containsString(string(state), expectedStates) {
			err = status.Error(codes.FailedPrecondition, formatUpdateStateError("azVolume", string(azVolumeAttachment.Status.State), string(state), expectedStates...))
		}
	}
	if err == nil {
		azVolumeAttachment.Status.State = state
	}
	return azVolumeAttachment, err
}

func (r *ReconcileAttachDetach) recreateAzVolumeAttachment(ctx context.Context, syncedVolumeAttachments map[string]bool, volumesToSync map[string]bool) (map[string]bool, map[string]bool, error) {
	w, _ := workflow.GetWorkflowFromContext(ctx)
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
				w.Logger().Errorf(err, "failed to get PV (%s)", *volumeName)
				return syncedVolumeAttachments, volumesToSync, err
			}

			if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != r.controllerSharedState.driverName {
				continue
			}
			volumesToSync[pv.Spec.CSI.VolumeHandle] = true

			diskName, err := azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
			if err != nil {
				w.Logger().Errorf(err, "failed to extract disk name from volumehandle (%s)", pv.Spec.CSI.VolumeHandle)
				delete(volumesToSync, pv.Spec.CSI.VolumeHandle)
				continue
			}
			nodeName := volumeAttachment.Spec.NodeName
			azVolumeAttachmentName := azureutils.GetAzVolumeAttachmentName(diskName, nodeName)

			// check if the CRI exists already
			azVolumeAttachment, err := azureutils.GetAzVolumeAttachment(ctx, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachmentName, r.controllerSharedState.objectNamespace, false)
			if err != nil {
				if errors.IsNotFound(err) {
					w.Logger().Infof("Recreating AzVolumeAttachment(%s)", azVolumeAttachmentName)
					azVolumeAttachment = &azdiskv1beta1.AzVolumeAttachment{
						ObjectMeta: metav1.ObjectMeta{
							Name: azVolumeAttachmentName,
							Labels: map[string]string{
								consts.NodeNameLabel:   nodeName,
								consts.VolumeNameLabel: *volumeName,
							},
							Finalizers: []string{consts.AzVolumeAttachmentFinalizer},
						},
						Spec: azdiskv1beta1.AzVolumeAttachmentSpec{

							VolumeName:    *volumeName,
							VolumeID:      pv.Spec.CSI.VolumeHandle,
							NodeName:      nodeName,
							RequestedRole: azdiskv1beta1.PrimaryRole,
							VolumeContext: map[string]string{},
						},
					}
					azVolumeAttachment, err = r.controllerSharedState.azClient.DiskV1beta1().AzVolumeAttachments(r.controllerSharedState.objectNamespace).Create(ctx, azVolumeAttachment, metav1.CreateOptions{})
					if err != nil {
						w.Logger().Errorf(err, "failed to create AzVolumeAttachment (%s) for volume (%s) and node (%s): %v", azVolumeAttachmentName, *volumeName, nodeName, err)
						return syncedVolumeAttachments, volumesToSync, err
					}
				} else {
					w.Logger().Errorf(err, "failed to get AzVolumeAttachment (%s): %v", azVolumeAttachmentName, err)
					return syncedVolumeAttachments, volumesToSync, err
				}
			}

			azVolumeAttachment.Status = azdiskv1beta1.AzVolumeAttachmentStatus{
				State: azureutils.GetAzVolumeAttachmentState(volumeAttachment.Status),
			}
			if azVolumeAttachment.Status.State == azdiskv1beta1.Attached {
				azVolumeAttachment.Status.Detail = &azdiskv1beta1.AzVolumeAttachmentStatusDetail{
					Role: azdiskv1beta1.PrimaryRole,
				}
			}
			// update status
			_, err = r.controllerSharedState.azClient.DiskV1beta1().AzVolumeAttachments(r.controllerSharedState.objectNamespace).UpdateStatus(ctx, azVolumeAttachment, metav1.UpdateOptions{})
			if err != nil {
				w.Logger().Errorf(err, "failed to update status of AzVolumeAttachment (%s) for volume (%s) and node (%s): %v", azVolumeAttachmentName, *volumeName, nodeName, err)
				return syncedVolumeAttachments, volumesToSync, err
			}

			syncedVolumeAttachments[volumeAttachment.Name] = true
		}
	}
	return syncedVolumeAttachments, volumesToSync, nil
}

func (r *ReconcileAttachDetach) recoverAzVolumeAttachment(ctx context.Context, recoveredAzVolumeAttachments *sync.Map) error {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	// list all AzVolumeAttachment
	azVolumeAttachments, err := r.controllerSharedState.azClient.DiskV1beta1().AzVolumeAttachments(r.controllerSharedState.objectNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		w.Logger().Error(err, "failed to get list of existing AzVolumeAttachment CRI in controller recovery stage")
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
		go func(azv azdiskv1beta1.AzVolumeAttachment, azvMap *sync.Map) {
			defer wg.Done()
			var targetState azdiskv1beta1.AzVolumeAttachmentAttachmentState
			updateFunc := func(obj interface{}) error {
				var err error
				azv := obj.(*azdiskv1beta1.AzVolumeAttachment)
				// add a recover annotation to the CRI so that reconciliation can be triggered for the CRI even if CRI's current state == target state
				azv.Status.Annotations = azureutils.AddToMap(azv.Status.Annotations, consts.RecoverAnnotation, "azVolumeAttachment")
				if azv.Status.State != targetState {
					_, err = updateState(azv, targetState, forceUpdate)
				}
				return err
			}
			switch azv.Status.State {
			case azdiskv1beta1.Attaching:
				// reset state to Pending so Attach operation can be redone
				targetState = azdiskv1beta1.AttachmentPending
			case azdiskv1beta1.Detaching:
				// reset state to Attached so Detach operation can be redone
				targetState = azdiskv1beta1.Attached
			default:
				targetState = azv.Status.State
			}

			if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, &azv, updateFunc, consts.ForcedUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
				w.Logger().Errorf(err, "failed to update AzVolumeAttachment (%s) for recovery", azv.Name)
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
	logger := mgr.GetLogger().WithValues("controller", "azvolumeattachment")
	reconciler := ReconcileAttachDetach{
		crdDetacher:           crdDetacher,
		cloudDiskAttacher:     cloudDiskAttacher,
		stateLock:             &sync.Map{},
		retryInfo:             newRetryInfo(),
		controllerSharedState: controllerSharedState,
		logger:                logger,
	}

	c, err := controller.New("azvolumeattachment-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		Log:                     logger,
	})

	if err != nil {
		c.GetLogger().Error(err, "failed to create controller")
		return nil, err
	}

	c.GetLogger().Info("Starting to watch AzVolumeAttachments.")

	// Watch for CRUD events on azVolumeAttachment objects
	err = c.Watch(&source.Kind{Type: &azdiskv1beta1.AzVolumeAttachment{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		c.GetLogger().Error(err, "failed to initialize watch for AzVolumeAttachment CRI")
		return nil, err
	}

	c.GetLogger().Info("Controller set-up successful.")
	return &reconciler, nil
}
