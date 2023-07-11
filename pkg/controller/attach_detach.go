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
	"errors"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/go-logr/logr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/features"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/provisioner"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"

	volerr "k8s.io/cloud-provider/volume/errors"
	"sigs.k8s.io/cloud-provider-azure/pkg/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type CloudDiskAttachDetacher interface {
	PublishVolume(ctx context.Context, volumeID string, nodeID string, volumeContext map[string]string) provisioner.CloudAttachResult
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
	*SharedState
	logger            logr.Logger
	crdDetacher       CrdDetacher
	cloudDiskAttacher CloudDiskAttachDetacher
	stateLock         *sync.Map
	retryInfo         *retryInfo
}

var _ reconcile.Reconciler = &ReconcileAttachDetach{}

var allowedTargetAttachmentStates = map[string][]string{
	"":                                       {string(azdiskv1beta2.AttachmentPending), string(azdiskv1beta2.Attaching), string(azdiskv1beta2.Detaching)},
	string(azdiskv1beta2.AttachmentPending):  {string(azdiskv1beta2.Attaching), string(azdiskv1beta2.Detaching)},
	string(azdiskv1beta2.Attaching):          {string(azdiskv1beta2.Attached), string(azdiskv1beta2.AttachmentFailed)},
	string(azdiskv1beta2.Detaching):          {string(azdiskv1beta2.Detached), string(azdiskv1beta2.DetachmentFailed)},
	string(azdiskv1beta2.Attached):           {string(azdiskv1beta2.Detaching)},
	string(azdiskv1beta2.Detached):           {},
	string(azdiskv1beta2.AttachmentFailed):   {string(azdiskv1beta2.Detaching)},
	string(azdiskv1beta2.DetachmentFailed):   {string(azdiskv1beta2.ForceDetachPending)},
	string(azdiskv1beta2.ForceDetachPending): {string(azdiskv1beta2.Detaching)},
}

func (r *ReconcileAttachDetach) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if !r.isRecoveryComplete() {
		return reconcile.Result{Requeue: true}, nil
	}

	azVolumeAttachment, err := azureutils.GetAzVolumeAttachment(ctx, r.cachedClient, r.azClient, request.Name, request.Namespace, true)
	// if object is not found, it means the object has been deleted. Log the deletion and do not requeue
	if apiErrors.IsNotFound(err) {
		r.azVolumeAttachmentToVaMap.Delete(request.Name)
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

	// deletion request
	if deleteRequested, deleteAfter := objectDeletionRequested(azVolumeAttachment); deleteRequested {
		if deleteAfter > 0 {
			return reconcileAfter(deleteAfter, request.Name, r.retryInfo)
		}
		if err := r.removeFinalizer(ctx, azVolumeAttachment); err != nil {
			return reconcileReturnOnError(ctx, azVolumeAttachment, "delete", err, r.retryInfo)
		}
		// detachment request
	} else if volumeDetachRequested(azVolumeAttachment) {
		if err := r.triggerDetach(ctx, azVolumeAttachment); err != nil {
			return reconcileReturnOnError(ctx, azVolumeAttachment, "detach", err, r.retryInfo)
		}
		// attachment request
	} else if azVolumeAttachment.Status.Detail == nil {
		if err := r.triggerAttach(ctx, azVolumeAttachment); err != nil {
			return reconcileReturnOnError(ctx, azVolumeAttachment, "attach", err, r.retryInfo)
		}
		// promotion/demotion request
	} else if azVolumeAttachment.Spec.RequestedRole != azVolumeAttachment.Status.Detail.Role {
		switch azVolumeAttachment.Spec.RequestedRole {
		case azdiskv1beta2.PrimaryRole:
			if err := r.promote(ctx, azVolumeAttachment); err != nil {
				return reconcileReturnOnError(ctx, azVolumeAttachment, "promote", err, r.retryInfo)
			}
		case azdiskv1beta2.ReplicaRole:
			if err := r.demote(ctx, azVolumeAttachment); err != nil {
				return reconcileReturnOnError(ctx, azVolumeAttachment, "demote", err, r.retryInfo)
			}
		}
	}

	return reconcileReturnOnSuccess(azVolumeAttachment.Name, r.retryInfo)
}

func (r *ReconcileAttachDetach) triggerAttach(ctx context.Context, azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	// requeue if AzVolumeAttachment's state is being updated by a different worker
	if _, ok := r.stateLock.LoadOrStore(azVolumeAttachment.Name, nil); ok {
		err = getOperationRequeueError("attach", azVolumeAttachment)
		return err
	}
	defer r.stateLock.Delete(azVolumeAttachment.Name)

	if !volumeAttachRequested(azVolumeAttachment) {
		err = status.Errorf(codes.FailedPrecondition, "attach operation has not yet been requested")
		return err
	}

	var azVolume *azdiskv1beta2.AzVolume
	if azVolume, err = azureutils.GetAzVolume(ctx, r.cachedClient, r.azClient, strings.ToLower(azVolumeAttachment.Spec.VolumeName), r.config.ObjectNamespace, true); err != nil {
		if apiErrors.IsNotFound(err) {
			w.Logger().V(5).Infof("Aborting attach operation for AzVolumeAttachment (%s): AzVolume (%s) not found", azVolumeAttachment.Name, azVolumeAttachment.Spec.VolumeName)
			err = nil
			return nil
		}
		return err
	} else if deleteRequested, _ := objectDeletionRequested(azVolume); deleteRequested {
		w.Logger().V(5).Infof("Aborting attach operation for AzVolumeAttachment (%s): AzVolume (%s) scheduled for deletion", azVolumeAttachment.Name, azVolumeAttachment.Spec.VolumeName)
		return nil
	}

	// update status block
	updateFunc := func(obj client.Object) error {
		azv := obj.(*azdiskv1beta2.AzVolumeAttachment)
		// Update state to attaching, Initialize finalizer and add label to the object
		_, derr := updateState(azv, azdiskv1beta2.Attaching, normalUpdate)
		return derr
	}

	var updatedObj client.Object
	if updatedObj, err = azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}
	azVolumeAttachment = updatedObj.(*azdiskv1beta2.AzVolumeAttachment)

	w.Logger().V(5).Info("Attaching volume")
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
		var publishCtx map[string]string
		var pods []v1.Pod
		if azVolumeAttachment.Spec.RequestedRole == azdiskv1beta2.ReplicaRole {
			var err error
			pods, err = r.getPodsFromVolume(goCtx, r.cachedClient, azVolumeAttachment.Spec.VolumeName)
			if err != nil {
				goWorkflow.Logger().Error(err, "failed to list pods for volume")
			}
		}

		var handleSuccess func(bool)
		var handleError func()

		attachAndUpdate := func() {
			attachResult := r.attachVolume(cloudCtx, azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Spec.VolumeContext)
			if publishCtx = attachResult.PublishContext(); publishCtx != nil {
				klog.Infof("attach_detach line 224: handleSuccess(false)")
				handleSuccess(false)
				klog.Infof("attach_detach line 226")
			}
			var ok bool
			if attachErr, ok = <-attachResult.ResultChannel(); !ok || attachErr != nil {
				klog.Infof("attach_detach line 228 ok: %+v attachError: %v", attachErr, ok)
				handleError()
			} else {
				klog.Infof("attach_detach line 231: success")
				handleSuccess(true)
			}

		}

		handleError = func() {
			if len(pods) > 0 {
				for _, pod := range pods {
					r.eventRecorder.Eventf(pod.DeepCopyObject(), v1.EventTypeWarning, consts.ReplicaAttachmentFailedEvent, "Replica mount for volume %s failed to be attached to node %s with error: %v", azVolumeAttachment.Spec.VolumeName, azVolumeAttachment.Spec.NodeName, attachErr)
				}
			}

			// if the disk is attached to a different node
			if danglingAttachErr, ok := attachErr.(*volerr.DanglingAttachError); ok {
				// get disk, current node and attachment name
				currentNodeName := string(danglingAttachErr.CurrentNode)
				currentAttachmentName := azureutils.GetAzVolumeAttachmentName(azVolumeAttachment.Spec.VolumeName, currentNodeName)
				goWorkflow.Logger().Infof("Dangling attach detected for %s", currentNodeName)

				// check if AzVolumeAttachment exists for the existing attachment
				_, err := r.azClient.DiskV1beta2().AzVolumeAttachments(r.config.ObjectNamespace).Get(cloudCtx, currentAttachmentName, metav1.GetOptions{})
				var detachErr error
				if apiErrors.IsNotFound(err) {
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
					attachAndUpdate()
					return
				}
			}

			updateFunc := func(obj client.Object) error {
				azva := obj.(*azdiskv1beta2.AzVolumeAttachment)
				// add retriable annotation if the replica attach error is PartialUpdateError or timeout
				if azva.Spec.RequestedRole == azdiskv1beta2.ReplicaRole {
					if _, ok := attachErr.(*retry.PartialUpdateError); ok || errors.Is(err, context.DeadlineExceeded) {
						azva.Status.Annotations = azureutils.AddToMap(azva.Status.Annotations, consts.ReplicaVolumeAttachRetryAnnotation, "true")
					}
				}
				_, uerr := reportError(azva, azdiskv1beta2.AttachmentFailed, attachErr)
				return uerr
			}
			//nolint:contextcheck // final status update of the CRI must occur even when the current context's deadline passes.
			_, _ = azureutils.UpdateCRIWithRetry(goCtx, nil, r.cachedClient, r.azClient, azVolumeAttachment, updateFunc, consts.ForcedUpdateMaxNetRetry, azureutils.UpdateCRIStatus)
		}

		handleSuccess = func(asyncComplete bool) {
			// Publish event to indicate attachment success
			if asyncComplete {
				if len(pods) > 0 {
					for _, pod := range pods {
						r.eventRecorder.Eventf(pod.DeepCopyObject(), v1.EventTypeNormal, consts.ReplicaAttachmentSuccessEvent, "Replica mount for volume %s successfully attached to node %s", azVolumeAttachment.Spec.VolumeName, azVolumeAttachment.Spec.NodeName)
					}
				}
				// the node's remaining capacity of disk attachment should be decreased by 1, since the disk attachment is succeeded.
				r.decrementNodeCapacity(ctx, azVolumeAttachment.Spec.NodeName)
			}

			updateFunc := func(obj client.Object) error {
				klog.Infof("updateFunc line 307 + asyncComplete: %+v", asyncComplete)
				azv := obj.(*azdiskv1beta2.AzVolumeAttachment)
				azv = updateStatusDetail(azv, publishCtx)
				var uerr error
				if asyncComplete {
					var v *azdiskv1beta2.AzVolumeAttachment
					v, uerr = updateState(azv, azdiskv1beta2.Attached, forceUpdate)
					klog.Infof("updateFunc state: %+v", v.Status)
				}
				return uerr
			}

			if asyncComplete && azVolumeAttachment.Spec.RequestedRole == azdiskv1beta2.PrimaryRole {
				_ = r.updateVolumeAttachmentWithResult(goCtx, azVolumeAttachment)
			}
			updatedObj, _ = azureutils.UpdateCRIWithRetry(goCtx, nil, r.cachedClient, r.azClient, azVolumeAttachment, updateFunc, consts.ForcedUpdateMaxNetRetry, azureutils.UpdateCRIStatus)
			azVolumeAttachment = updatedObj.(*azdiskv1beta2.AzVolumeAttachment)
			klog.Infof("attach_detach line 316 azvolumeattachment: %+v", azVolumeAttachment)
			klog.Infof("returning from handle success")
		}

		attachAndUpdate()
	}()
	<-waitCh

	return nil
}

func (r *ReconcileAttachDetach) triggerDetach(ctx context.Context, azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	if _, ok := r.stateLock.LoadOrStore(azVolumeAttachment.Name, nil); ok {
		return getOperationRequeueError("detach", azVolumeAttachment)
	}
	defer r.stateLock.Delete(azVolumeAttachment.Name)

	updateFunc := func(obj client.Object) error {
		azv := obj.(*azdiskv1beta2.AzVolumeAttachment)
		// Update state to detaching
		_, derr := updateState(azv, azdiskv1beta2.Detaching, normalUpdate)
		return derr
	}

	var updatedObj client.Object
	if updatedObj, err = azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}
	azVolumeAttachment = updatedObj.(*azdiskv1beta2.AzVolumeAttachment)

	w.Logger().V(5).Info("Detaching volume")
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

		var updateFunc func(obj client.Object) error
		detachErr = r.detachVolume(cloudCtx, azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName)
		if detachErr != nil {
			updateFunc = func(obj client.Object) error {
				azv := obj.(*azdiskv1beta2.AzVolumeAttachment)
				_, derr := reportError(azv, azdiskv1beta2.DetachmentFailed, detachErr)
				return derr
			}
			//nolint:contextcheck // final status update of the CRI must occur even when the current context's deadline passes.
			if _, uerr := azureutils.UpdateCRIWithRetry(goCtx, nil, r.cachedClient, r.azClient, azVolumeAttachment, updateFunc, consts.ForcedUpdateMaxNetRetry, azureutils.UpdateCRIStatus); uerr != nil {
				w.Logger().Errorf(uerr, "failed to update final status of AzVolumeAttachement")
			}
		} else {
			// detach of azVolumeAttachment is succeeded, the node's remaining capacity of disk attachment should be increased by 1
			r.incrementNodeCapacity(ctx, azVolumeAttachment.Spec.NodeName)

			//nolint:contextcheck // delete of the CRI must occur even when the current context's deadline passes.
			if derr := r.cachedClient.Delete(goCtx, azVolumeAttachment); derr != nil {
				w.Logger().Error(derr, "failed to delete AzVolumeAttachment")
			}
		}
	}()
	<-waitCh
	return nil
}

func (r *ReconcileAttachDetach) removeFinalizer(ctx context.Context, azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	updateFunc := func(obj client.Object) error {
		azv := obj.(*azdiskv1beta2.AzVolumeAttachment)
		// delete finalizer
		_ = r.deleteFinalizer(azv)
		return nil
	}

	_, err = azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRI)
	return err
}

func (r *ReconcileAttachDetach) promote(ctx context.Context, azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	w.Logger().Infof("Promoting AzVolumeAttachment")
	if err = r.updateVolumeAttachmentWithResult(ctx, azVolumeAttachment); err != nil {
		return err
	}
	// initialize metadata and update status block
	updateFunc := func(obj client.Object) error {
		azv := obj.(*azdiskv1beta2.AzVolumeAttachment)
		_ = updateRole(azv, azdiskv1beta2.PrimaryRole)
		return nil
	}
	if _, err = azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}
	return nil
}

func (r *ReconcileAttachDetach) demote(ctx context.Context, azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	w.Logger().V(5).Info("Demoting AzVolumeAttachment")
	// initialize metadata and update status block
	updateFunc := func(obj client.Object) error {
		azv := obj.(*azdiskv1beta2.AzVolumeAttachment)
		delete(azv.Status.Annotations, consts.VolumeAttachmentKey)
		_ = updateRole(azv, azdiskv1beta2.ReplicaRole)
		return nil
	}

	if _, err = azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}
	return nil
}

func (r *ReconcileAttachDetach) updateVolumeAttachmentWithResult(ctx context.Context, azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment) error {
	ctx, w := workflow.New(ctx)
	var vaName string
	vaName, err := r.waitForVolumeAttachmentName(ctx, azVolumeAttachment)
	if err != nil {
		return err
	}

	vaUpdateFunc := func(obj client.Object) error {
		va := obj.(*storagev1.VolumeAttachment)
		if azVolumeAttachment.Status.Detail != nil {
			for key, value := range azVolumeAttachment.Status.Detail.PublishContext {
				va.Status.AttachmentMetadata = azureutils.AddToMap(va.Status.AttachmentMetadata, key, value)
			}
		}
		return nil
	}

	originalVA := &storagev1.VolumeAttachment{}
	if err = r.cachedClient.Get(ctx, types.NamespacedName{Namespace: azVolumeAttachment.Namespace, Name: vaName}, originalVA); err != nil {
		w.Logger().Errorf(err, "failed to get original VolumeAttachment (%s)", vaName)
		return err
	}
	_, err = azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, originalVA, vaUpdateFunc, consts.ForcedUpdateMaxNetRetry, azureutils.UpdateCRIStatus)
	return err
}

func (r *ReconcileAttachDetach) deleteFinalizer(azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment) *azdiskv1beta2.AzVolumeAttachment {
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

func (r *ReconcileAttachDetach) attachVolume(ctx context.Context, volumeID, node string, volumeContext map[string]string) provisioner.CloudAttachResult {
	return r.cloudDiskAttacher.PublishVolume(ctx, volumeID, node, volumeContext)
}

func (r *ReconcileAttachDetach) detachVolume(ctx context.Context, volumeID, node string) error {
	return r.cloudDiskAttacher.UnpublishVolume(ctx, volumeID, node)
}

func (r *ReconcileAttachDetach) Recover(ctx context.Context, recoveryID string) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	w.Logger().V(5).Info("Recovering AzVolumeAttachment CRIs...")
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
		if err = r.recoverAzVolumeAttachment(ctx, recovered, recoveryID); err == nil {
			break
		}
		w.Logger().Error(err, "failed to recover AzVolumeAttachment state")
	}

	return err
}

func updateRole(azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment, role azdiskv1beta2.Role) *azdiskv1beta2.AzVolumeAttachment {
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

func updateStatusDetail(azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment, status map[string]string) *azdiskv1beta2.AzVolumeAttachment {
	if azVolumeAttachment == nil {
		return nil
	}

	if azVolumeAttachment.Status.Detail == nil {
		azVolumeAttachment.Status.Detail = &azdiskv1beta2.AzVolumeAttachmentStatusDetail{}
	}

	azVolumeAttachment.Status.Detail.PreviousRole = azVolumeAttachment.Status.Detail.Role
	azVolumeAttachment.Status.Detail.Role = azVolumeAttachment.Spec.RequestedRole
	azVolumeAttachment.Status.Detail.PublishContext = status

	return azVolumeAttachment
}

func updateError(azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment, err error) *azdiskv1beta2.AzVolumeAttachment {
	if azVolumeAttachment == nil {
		return nil
	}

	if err != nil {
		azVolumeAttachment.Status.Error = util.NewAzError(err)
	}

	return azVolumeAttachment
}

func updateState(azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment, state azdiskv1beta2.AzVolumeAttachmentAttachmentState, mode updateMode) (*azdiskv1beta2.AzVolumeAttachment, error) {
	var err error
	if azVolumeAttachment == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "function `updateState` requires non-nil AzVolumeAttachment object.")
	}
	if mode == normalUpdate {
		if expectedStates, exists := allowedTargetAttachmentStates[string(azVolumeAttachment.Status.State)]; !exists || !containsString(string(state), expectedStates) {
			err = status.Error(codes.FailedPrecondition, formatUpdateStateError("azVolumeAttachment", string(azVolumeAttachment.Status.State), string(state), expectedStates...))
		}
	}
	if err == nil {
		azVolumeAttachment.Status.State = state
	}
	return azVolumeAttachment, err
}

func reportError(azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment, state azdiskv1beta2.AzVolumeAttachmentAttachmentState, err error) (*azdiskv1beta2.AzVolumeAttachment, error) {
	if azVolumeAttachment == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "function `reportError` requires non-nil AzVolumeAttachment object.")
	}
	azVolumeAttachment = updateError(azVolumeAttachment, err)
	return updateState(azVolumeAttachment, state, forceUpdate)
}

func (r *ReconcileAttachDetach) recreateAzVolumeAttachment(ctx context.Context, syncedVolumeAttachments map[string]bool, volumesToSync map[string]bool) (map[string]bool, map[string]bool, error) {
	w, _ := workflow.GetWorkflowFromContext(ctx)
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
		if volumeAttachment.Spec.Attacher == r.config.DriverName {
			pvName := volumeAttachment.Spec.Source.PersistentVolumeName
			if pvName == nil {
				continue
			}
			// get PV and retrieve diskName
			pv, err := r.kubeClient.CoreV1().PersistentVolumes().Get(ctx, *pvName, metav1.GetOptions{})
			if err != nil {
				w.Logger().Errorf(err, "failed to get PV (%s)", *pvName)
				return syncedVolumeAttachments, volumesToSync, err
			}

			// if pv is migrated intree pv, convert it to csi pv for processing
			// translate intree pv to csi pv to convert them into AzVolume resource
			if utilfeature.DefaultFeatureGate.Enabled(features.CSIMigration) &&
				utilfeature.DefaultFeatureGate.Enabled(features.CSIMigrationAzureDisk) &&
				pv.Spec.AzureDisk != nil {
				// translate intree pv to csi pv to convert them into AzVolume resource
				if utilfeature.DefaultFeatureGate.Enabled(features.CSIMigration) &&
					utilfeature.DefaultFeatureGate.Enabled(features.CSIMigrationAzureDisk) &&
					pv.Spec.AzureDisk != nil {
					if pv, err = r.translateInTreePVToCSI(pv); err != nil {
						w.Logger().V(5).Errorf(err, "skipping azVolumeAttachment creation for volumeAttachment (%s)", volumeAttachment.Name)
					}
				}
			}

			if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != r.config.DriverName {
				continue
			}
			volumesToSync[pv.Spec.CSI.VolumeHandle] = true

			diskName, err := azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
			if err != nil {
				w.Logger().Errorf(err, "failed to extract disk name from volumehandle (%s)", pv.Spec.CSI.VolumeHandle)
				delete(volumesToSync, pv.Spec.CSI.VolumeHandle)
				continue
			}
			volumeName := strings.ToLower(diskName)
			nodeName := volumeAttachment.Spec.NodeName
			azVolumeAttachmentName := azureutils.GetAzVolumeAttachmentName(diskName, nodeName)
			r.azVolumeAttachmentToVaMap.Store(azVolumeAttachmentName, volumeAttachment.Name)

			desiredAzVolumeAttachment := &azdiskv1beta2.AzVolumeAttachment{
				ObjectMeta: metav1.ObjectMeta{
					Name: azVolumeAttachmentName,
					Labels: map[string]string{
						consts.NodeNameLabel:   nodeName,
						consts.VolumeNameLabel: volumeName,
					},
					// if the volumeAttachment shows not yet attached, and VolumeAttachRequestAnnotation needs to be set from the controllerserver
					// if the volumeAttachment shows attached but the actual volume isn't due to controller restart, VolumeAttachRequestAnnotation needs to be set by the noderserver during NodeStageVolume
					Finalizers: []string{consts.AzVolumeAttachmentFinalizer},
				},
				Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
					VolumeName:    volumeName,
					VolumeID:      pv.Spec.CSI.VolumeHandle,
					NodeName:      nodeName,
					RequestedRole: azdiskv1beta2.PrimaryRole,
					VolumeContext: pv.Spec.CSI.VolumeAttributes,
				},
			}
			azureutils.AnnotateAPIVersion(desiredAzVolumeAttachment)

			statusUpdateRequired := true
			// check if the CRI exists already
			azVolumeAttachment, err := azureutils.GetAzVolumeAttachment(ctx, r.cachedClient, r.azClient, azVolumeAttachmentName, r.config.ObjectNamespace, false)
			if err != nil {
				if apiErrors.IsNotFound(err) {
					w.Logger().Infof("Recreating AzVolumeAttachment(%s)", azVolumeAttachmentName)

					azVolumeAttachment, err = r.azClient.DiskV1beta2().AzVolumeAttachments(r.config.ObjectNamespace).Create(ctx, desiredAzVolumeAttachment, metav1.CreateOptions{})
					if err != nil {
						w.Logger().Errorf(err, "failed to create AzVolumeAttachment (%s) for volume (%s) and node (%s): %v", azVolumeAttachmentName, *pvName, nodeName, err)
						return syncedVolumeAttachments, volumesToSync, err
					}
				} else {
					w.Logger().Errorf(err, "failed to get AzVolumeAttachment (%s): %v", azVolumeAttachmentName, err)
					return syncedVolumeAttachments, volumesToSync, err
				}
			} else if apiVersion, ok := azureutils.GetFromMap(azVolumeAttachment.Annotations, consts.APIVersion); !ok || apiVersion != azdiskv1beta2.APIVersion {
				w.Logger().Infof("Found AzVolumeAttachment (%s) with older api version. Converting to apiVersion(%s)", azVolumeAttachmentName, azdiskv1beta2.APIVersion)

				for k, v := range desiredAzVolumeAttachment.Labels {
					azVolumeAttachment.Labels = azureutils.AddToMap(azVolumeAttachment.Labels, k, v)
				}

				for k, v := range azVolumeAttachment.Annotations {
					azVolumeAttachment.Status.Annotations = azureutils.AddToMap(azVolumeAttachment.Status.Annotations, k, v)
				}

				// for now, we don't empty the meta annotatinos after migrating them to status annotation for safety.
				// note that this will leave some remnant garbage entries in meta annotations

				for k, v := range desiredAzVolumeAttachment.Annotations {
					azVolumeAttachment.Annotations = azureutils.AddToMap(azVolumeAttachment.Status.Annotations, k, v)
				}

				azVolumeAttachment, err = r.azClient.DiskV1beta2().AzVolumeAttachments(r.config.ObjectNamespace).Update(ctx, azVolumeAttachment, metav1.UpdateOptions{})
				if err != nil {
					w.Logger().Errorf(err, "failed to update AzVolumeAttachment (%s) for volume (%s) and node (%s): %v", azVolumeAttachmentName, *pvName, nodeName, err)
					return syncedVolumeAttachments, volumesToSync, err
				}
			} else {
				statusUpdateRequired = false
			}

			if statusUpdateRequired {
				azVolumeAttachment.Status.Annotations = azureutils.AddToMap(azVolumeAttachment.Status.Annotations, consts.VolumeAttachmentKey, volumeAttachment.Name)

				// update status
				_, err = r.azClient.DiskV1beta2().AzVolumeAttachments(r.config.ObjectNamespace).UpdateStatus(ctx, azVolumeAttachment, metav1.UpdateOptions{})
				if err != nil {
					w.Logger().Errorf(err, "failed to update status of AzVolumeAttachment (%s) for volume (%s) and node (%s): %v", azVolumeAttachmentName, *pvName, nodeName, err)
					return syncedVolumeAttachments, volumesToSync, err
				}
			}

			syncedVolumeAttachments[volumeAttachment.Name] = true
		}
	}
	return syncedVolumeAttachments, volumesToSync, nil
}

func (r *ReconcileAttachDetach) recoverAzVolumeAttachment(ctx context.Context, recoveredAzVolumeAttachments *sync.Map, recoveryID string) error {
	w, _ := workflow.GetWorkflowFromContext(ctx)

	// list all AzVolumeAttachment
	azVolumeAttachments, err := r.azClient.DiskV1beta2().AzVolumeAttachments(r.config.ObjectNamespace).List(ctx, metav1.ListOptions{})
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
		go func(azv azdiskv1beta2.AzVolumeAttachment, azvMap *sync.Map) {
			defer wg.Done()
			var targetState azdiskv1beta2.AzVolumeAttachmentAttachmentState
			updateMode := azureutils.UpdateCRIStatus
			if azv.Spec.RequestedRole == azdiskv1beta2.ReplicaRole {
				updateMode = azureutils.UpdateAll
			}
			updateFunc := func(obj client.Object) error {
				var err error
				azv := obj.(*azdiskv1beta2.AzVolumeAttachment)
				if azv.Spec.RequestedRole == azdiskv1beta2.ReplicaRole {
					// conversion logic from v1beta1 to v1beta2 for replicas come here
					azv.Status.Annotations = azv.ObjectMeta.Annotations
					azv.ObjectMeta.Annotations = map[string]string{consts.VolumeAttachRequestAnnotation: "CRI recovery"}
				}
				// add a recover annotation to the CRI so that reconciliation can be triggered for the CRI even if CRI's current state == target state
				azv.Status.Annotations = azureutils.AddToMap(azv.Status.Annotations, consts.RecoverAnnotation, recoveryID)
				if azv.Status.State != targetState {
					_, err = updateState(azv, targetState, forceUpdate)
				}
				return err
			}
			switch azv.Status.State {
			case azdiskv1beta2.Attaching:
				// reset state to Pending so Attach operation can be redone
				targetState = azdiskv1beta2.AttachmentPending
				updateFunc = azureutils.AppendToUpdateCRIFunc(updateFunc, func(obj client.Object) error {
					azv := obj.(*azdiskv1beta2.AzVolumeAttachment)
					azv.Status.Detail = nil
					azv.Status.Error = nil
					return nil
				})
			case azdiskv1beta2.Detaching:
				// reset state to Attached so Detach operation can be redone
				targetState = azdiskv1beta2.Attached
			default:
				targetState = azv.Status.State
			}

			if _, err := azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, &azv, updateFunc, consts.ForcedUpdateMaxNetRetry, updateMode); err != nil {
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
		crdDetacher:       crdDetacher,
		cloudDiskAttacher: cloudDiskAttacher,
		stateLock:         &sync.Map{},
		retryInfo:         newRetryInfo(),
		SharedState:       controllerSharedState,
		logger:            logger,
	}

	c, err := controller.New("azvolumeattachment-controller", mgr, controller.Options{
		MaxConcurrentReconciles: controllerSharedState.config.ControllerConfig.WorkerThreads,
		Reconciler:              &reconciler,
		LogConstructor:          func(req *reconcile.Request) logr.Logger { return logger },
	})

	if err != nil {
		c.GetLogger().Error(err, "failed to create controller")
		return nil, err
	}

	c.GetLogger().Info("Starting to watch AzVolumeAttachments.")

	// Watch for CRUD events on azVolumeAttachment objects
	err = c.Watch(&source.Kind{Type: &azdiskv1beta2.AzVolumeAttachment{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		c.GetLogger().Error(err, "failed to initialize watch for AzVolumeAttachment CRI")
		return nil, err
	}

	c.GetLogger().Info("Controller set-up successful.")
	return &reconciler, nil
}
