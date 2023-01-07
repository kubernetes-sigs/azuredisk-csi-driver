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
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-03-01/compute"
	"github.com/go-logr/logr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
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
	GetNodeDataDisks(nodeName types.NodeName) ([]compute.DataDisk, *string, error)
	GetDiskLun(diskName, diskURI string, nodeName types.NodeName) (int32, *string, error)
	CheckDiskExists(ctx context.Context, diskURI string) (*compute.Disk, error)
	GetNodeNameByProviderID(providerID string) (types.NodeName, error)
}

type CrdDetacher interface {
	UnpublishVolume(ctx context.Context, volumeID string, nodeID string, secrets map[string]string, mode consts.UnpublishMode) error
	WaitForDetach(ctx context.Context, volumeID, nodeID string) error
}

type volumeInfo struct {
	syncedAzVolumeAttachments map[string]bool
	handle                    string
	attributes                map[string]string
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
	volumeInfos       map[string]*volumeInfo
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
		// deletion of azVolumeAttachment is succeeded, the node's remaining capacity of disk attachment should be increased by 1
		r.incrementAttachmentCount(ctx, azVolumeAttachment.Spec.NodeName)
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
		// Update state to attaching
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
				handleSuccess(false)
			}
			var ok bool
			if attachErr, ok = <-attachResult.ResultChannel(); !ok || attachErr != nil {
				handleError()
			} else {
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
				// add retriable annotation if the attach error is PartialUpdateError or timeout
				if _, ok := attachErr.(*retry.PartialUpdateError); ok || errors.Is(err, context.DeadlineExceeded) {
					azva.Status.Annotations = azureutils.AddToMap(azva.Status.Annotations, consts.ReplicaVolumeAttachRetryAnnotation, "true")
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
				r.decrementAttachmentCount(ctx, azVolumeAttachment.Spec.NodeName)
			}

			updateFunc := func(obj client.Object) error {
				azv := obj.(*azdiskv1beta2.AzVolumeAttachment)
				azv = updateStatusDetail(azv, publishCtx)
				var uerr error
				if asyncComplete {
					_, uerr = updateState(azv, azdiskv1beta2.Attached, forceUpdate)
				}
				return uerr
			}

			if asyncComplete && azVolumeAttachment.Spec.RequestedRole == azdiskv1beta2.PrimaryRole {
				_ = r.updateVolumeAttachmentWithResult(goCtx, azVolumeAttachment)
			}
			updatedObj, _ = azureutils.UpdateCRIWithRetry(goCtx, nil, r.cachedClient, r.azClient, azVolumeAttachment, updateFunc, consts.ForcedUpdateMaxNetRetry, azureutils.UpdateCRIStatus)
			azVolumeAttachment = updatedObj.(*azdiskv1beta2.AzVolumeAttachment)
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

func (r *ReconcileAttachDetach) recreateAzVolumeAttachmentWithPrimaryRole(ctx context.Context) error {
	var err error
	w, _ := workflow.GetWorkflowFromContext(ctx)
	// Get all volumeAttachments
	volumeAttachments, err := r.kubeClient.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
	if err != nil {
		w.Logger().Error(err, "failed to get a list of VolumeAttachments")
		return err
	}

	// Loop through volumeAttachments and create Primary AzVolumeAttachments in correspondence
	for _, volumeAttachment := range volumeAttachments.Items {
		if volumeAttachment.Spec.Attacher == r.config.DriverName {
			pvName := volumeAttachment.Spec.Source.PersistentVolumeName
			if pvName == nil {
				continue
			}
			nodeName := volumeAttachment.Spec.NodeName
			volumeName := strings.ToLower(*pvName)
			azVolumeAttachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeName)
			if err = r.recreateAzVolumeAttachmentFromVolumeInfo(ctx, volumeAttachment.Name, azVolumeAttachmentName, volumeName, nodeName); err != nil {
				w.Logger().Errorf(err, "failed to recreate primary AzVolumeAttachment (%s) from volumeInfo.", azVolumeAttachmentName)
				// don't return err yet. continue to recreate others
			}
		}
	}
	return err
}

// recreateAzVolumeAttachmentWithReplicaRole fetches attachment information from VM to properly recreate replica AzVolumeAttachments
// In a normal case, all replica AzVolumeAttachments should already be detached and deleted on driver uninstall. So nothing needs to be recreated here.
// But in case of any failed detachment on previous driver uninstall, this function is needed.
func (r *ReconcileAttachDetach) recreateAzVolumeAttachmentWithReplicaRole(ctx context.Context) error {
	var err error
	w, _ := workflow.GetWorkflowFromContext(ctx)
	nodes, err := r.kubeClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		w.Logger().Errorf(err, "failed to get list of nodes")
		return err
	}
	for _, node := range nodes.Items {
		nodeName := node.Name
		// Get all attached disks from VM
		disks, _, err := r.cloudDiskAttacher.GetNodeDataDisks(types.NodeName(nodeName))
		if err != nil {
			w.Logger().Errorf(err, "failed to get data disks for node (%s).", nodeName)
			// don't return err yet. continue to other nodes
			continue
		}

		for _, dataDisk := range disks {
			volumeName := strings.ToLower(*dataDisk.Name)
			azVolumeAttachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeName)
			if err = r.recreateAzVolumeAttachmentFromVolumeInfo(ctx, "", azVolumeAttachmentName, volumeName, nodeName); err != nil {
				w.Logger().Errorf(err, "failed to recreate replica AzVolumeAttachment (%s) from volumeInfo.", azVolumeAttachmentName)
				// don't return err yet. continue to recreate others
			}
		}
	}

	return err
}

func (r *ReconcileAttachDetach) recreateAzVolumeAttachmentFromVolumeInfo(ctx context.Context, volumeAttachmentName, azVolumeAttachmentName, volumeName, nodeName string) error {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	volumeHandle, volumeContext, volumeInfo, hasSynced, err := r.loadOrStoreVolumeInfo(ctx, volumeName, azVolumeAttachmentName)
	if hasSynced {
		// this azVolumeAttachment has already been recreated
		return nil
	}
	if err != nil {
		return err
	}
	attached, err := r.isAttached(ctx, volumeHandle, nodeName)
	if !attached && err == nil {
		w.Logger().Infof("disk (%s) is not attached to node (%s). needn't create azVolumeAttachment for it.", volumeHandle, nodeName)
		return nil
	} else if err != nil {
		// If there is an error checking the attachment status, we can still recreate the AzVolumeAttachment based on the previous information we got.
		// This way we don't miss any AzVolumeAttachment
		w.Logger().Infof("failed to check if disk (%s) is attached to node (%s) with error (%v).", volumeHandle, nodeName, err)
	}
	role := azdiskv1beta2.ReplicaRole
	if volumeAttachmentName != "" {
		r.azVolumeAttachmentToVaMap.Store(azVolumeAttachmentName, volumeAttachmentName)
		// Set the role to Primary since it has a volumeAttachmentName
		role = azdiskv1beta2.PrimaryRole
	}

	desiredAzVolumeAttachment := r.generateDesiredAzVolumeAttachment(azVolumeAttachmentName, nodeName, volumeName, volumeHandle, role, volumeContext)
	if err = r.recreateAzVolumeAttachment(ctx, azVolumeAttachmentName, desiredAzVolumeAttachment); err != nil {
		w.Logger().Errorf(err, "failed to recreate AzVolumeAttachment (%s)", azVolumeAttachmentName)
		return err
	}
	volumeInfo.syncedAzVolumeAttachments[azVolumeAttachmentName] = true
	return nil
}

func (r *ReconcileAttachDetach) loadOrStoreVolumeInfo(ctx context.Context, volumeName, azVolumeAttachmentName string) (string, map[string]string, *volumeInfo, bool, error) {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	var err error
	var volumeHandle string
	var volumeContext map[string]string
	hasSynced := false
	vInfo, ok := r.volumeInfos[volumeName]
	if ok {
		volumeHandle = vInfo.handle
		volumeContext = vInfo.attributes
		if synced, hasAzVolumeAttachment := vInfo.syncedAzVolumeAttachments[azVolumeAttachmentName]; hasAzVolumeAttachment && synced {
			hasSynced = true
			return volumeHandle, volumeContext, vInfo, hasSynced, nil
		}
	} else {
		w.Logger().Infof("getting volumeHandle and volumeContext from PersistentVolume for volume (%s)", volumeName)
		volumeHandle, volumeContext, err = r.getVolumeInfoFromPV(ctx, volumeName)
		if err != nil {
			w.Logger().Errorf(err, "failed to find volume info from PersistentVolume for volume (%s)", volumeName)
			return "", nil, vInfo, hasSynced, err
		}
		vInfo = newVolumeInfo(volumeHandle, volumeContext)
		r.volumeInfos[volumeName] = vInfo
	}
	return volumeHandle, volumeContext, vInfo, hasSynced, nil
}

func newVolumeInfo(volumeHandle string, volumeContext map[string]string) *volumeInfo {
	var vInfo volumeInfo
	vInfo.handle = volumeHandle
	vInfo.attributes = volumeContext
	vInfo.syncedAzVolumeAttachments = make(map[string]bool)
	return &vInfo
}

func (r *ReconcileAttachDetach) recreateAzVolumeAttachments(ctx context.Context) error {
	var err error
	w, _ := workflow.GetWorkflowFromContext(ctx)

	// try to recreate missing AzVolumeAttachment CRI with Primary role from VolumeAttachment
	if err = r.recreateAzVolumeAttachmentWithPrimaryRole(ctx); err != nil {
		w.Logger().Error(err, "failed to recreate missing AzVolumeAttachment CRI with primary role from VolumeAttachments.")
	}

	// try to recreate replica AzVolumeAttachment that had failed to detach for any reason during driver uninstall
	if err = r.recreateAzVolumeAttachmentWithReplicaRole(ctx); err != nil {
		w.Logger().Error(err, "failed to recreate missing AzVolumeAttachment CRIs with replica role from VMs.")
	}

	return err
}

func (r *ReconcileAttachDetach) recoverAzVolumeAttachment(ctx context.Context, recoveredAzVolumeAttachments *sync.Map, recoveryID string) error {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	w.Logger().Info("starting to recover AzVolumeAttachments")
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
		go func(azva azdiskv1beta2.AzVolumeAttachment, azvaMap *sync.Map) {
			defer wg.Done()
			var publishCtx map[string]string
			if lun, _, err := r.cloudDiskAttacher.GetDiskLun(azva.Spec.VolumeName, azva.Spec.VolumeID, types.NodeName(azva.Spec.NodeName)); err == nil {
				publishCtx = map[string]string{azureconstants.LUN: strconv.Itoa(int(lun))}
			}
			var targetState azdiskv1beta2.AzVolumeAttachmentAttachmentState
			updateMode := azureutils.UpdateCRIStatus
			updateFunc := func(obj client.Object) error {
				var err error
				azva := obj.(*azdiskv1beta2.AzVolumeAttachment)
				if azva.Status.Detail == nil {
					azva.Status.Detail = &azdiskv1beta2.AzVolumeAttachmentStatusDetail{}
				}
				azva.Status.Detail.Role = azva.Spec.RequestedRole
				azva.Status.Detail.PublishContext = publishCtx

				// add a recover annotation to the CRI so that reconciliation can be triggered for the CRI even if CRI's current state == target state
				azva.Status.Annotations = azureutils.AddToMap(azva.Status.Annotations, consts.RecoverAnnotation, recoveryID)
				if azva.Status.State != targetState {
					_, err = updateState(azva, targetState, forceUpdate)
				}
				return err
			}

			requireDelete := false
			if attached, err := r.isAttached(ctx, azva.Spec.VolumeID, azva.Spec.NodeName); attached && err == nil {
				targetState = azdiskv1beta2.Attached
			} else if !attached && err == nil {
				// disk is not attached. Delete the azVolumeAttachment
				w.Logger().V(5).Infof("need to delete the AzVolumeAttachment (%s)", azva.Name)
				requireDelete = true
			} else {
				switch azva.Status.State {
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
					targetState = azva.Status.State
				}
			}

			if requireDelete {
				if err := r.cachedClient.Delete(ctx, &azva); err != nil {
					w.Logger().Errorf(err, "failed to delete the AzVolumeAttachment (%s)", azva.Name)
					return
				}
			} else {
				if _, err := azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, &azva, updateFunc, consts.ForcedUpdateMaxNetRetry, updateMode); err != nil {
					w.Logger().Errorf(err, "failed to update AzVolumeAttachment (%s) for recovery", azva.Name)
					return
				}
			}
			// if recover succeeded, add the CRI to the recoveryComplete list
			azvaMap.Store(azva.Name, struct{}{})
			atomic.AddInt32(&numRecovered, 1)
		}(azVolumeAttachment, recoveredAzVolumeAttachments)
	}
	wg.Wait()

	// if recovery has not been completed for all CRIs, return error
	if numRecovered < int32(len(azVolumeAttachments.Items)) {
		return status.Errorf(codes.Internal, "failed to recover some AzVolumeAttachment states")
	}
	return nil
}

func (r *ReconcileAttachDetach) isAttached(ctx context.Context, volumeHandle, nodeName string) (bool, error) {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	disk, err := r.cloudDiskAttacher.CheckDiskExists(ctx, volumeHandle)
	if disk == nil && err == nil {
		// there is possibility that disk is nil when it is throttled
		// don't check disk state when GetDisk is throttled
		// The rate limit of Get Disk call is 15000 times per 3 mins. We shouldn't get throttled often.
		return false, fmt.Errorf("getting disk is throttled. %s", azureconstants.RateLimited)
	}
	if err != nil {
		w.Logger().Errorf(err, "Volume (%s) not found", volumeHandle)
		return false, err
	}
	if disk != nil {
		// ManagedByExtended is the source of truth to check if the disk is attached to the node
		// Description of disk managedByExtended can be found here: https://learn.microsoft.com/en-us/rest/api/compute/disks/get?tabs=HTTP#disk
		for _, managedByNodeID := range *disk.ManagedByExtended {
			// Get the node name from the node provider ID.
			// The node name is not necessarily a substring of the node provider ID, so we need to use a helper function.
			name, err := r.cloudDiskAttacher.GetNodeNameByProviderID(managedByNodeID)
			if err != nil {
				w.Logger().Errorf(err, "Failed to get node name by node ID (%s)", managedByNodeID)
				continue
			}
			if strings.EqualFold(nodeName, string(name)) {
				w.Logger().V(5).Infof("disk(%s) is attached to node(%s)", volumeHandle, nodeName)
				return true, nil
			}
		}
	}

	return false, nil
}

func (r *ReconcileAttachDetach) Recover(ctx context.Context, recoveryID string) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	w.Logger().V(5).Info("Recovering AzVolumeAttachment CRIs...")

	if err = r.recreateAzVolumeAttachments(ctx); err != nil {
		w.Logger().Error(err, "failed to recreate missing AzVolumeAttachment CRI")
	}

	// retrigger any aborted operation from possible previous controller crash
	recovered := &sync.Map{}
	for i := 0; i < maxRetry; i++ {
		if err = r.recoverAzVolumeAttachment(ctx, recovered, recoveryID); err == nil {
			break
		}
		w.Logger().Errorf(err, "failed to recover AzVolumeAttachment with retry number (%d)", i)
	}

	return err
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
		volumeInfos:       make(map[string]*volumeInfo),
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
