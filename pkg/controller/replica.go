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
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/watcher"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type ReconcileReplica struct {
	*SharedState
	logger                     logr.Logger
	timeUntilGarbageCollection time.Duration
}

var _ reconcile.Reconciler = &ReconcileReplica{}

func (r *ReconcileReplica) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if !r.isRecoveryComplete() {
		return reconcile.Result{Requeue: true}, nil
	}

	azVolumeAttachment, err := azureutils.GetAzVolumeAttachment(ctx, r.cachedClient, r.azClient, request.Name, request.Namespace, true)
	if errors.IsNotFound(err) {
		return reconcile.Result{}, nil
	} else if err != nil {
		r.logger.Error(err, fmt.Sprintf("failed to get AzVolumeAttachment (%s)", request.Name))
		return reconcile.Result{Requeue: true}, err
	}

	if azVolumeAttachment.Spec.RequestedRole == azdiskv1beta2.PrimaryRole {
		// Detach Event
		if volumeDetachRequested(azVolumeAttachment) && !r.driverLifecycle.IsDriverUninstall() {
			// If primary attachment is marked for deletion, queue garbage collection for replica attachments
			r.triggerGarbageCollection(ctx, azVolumeAttachment.Spec.VolumeName) //nolint:contextcheck // Garbage collection is asynchronous; context is not inherited by design
		} else {
			// If not, cancel scheduled garbage collection if there is one enqueued
			r.removeGarbageCollection(azVolumeAttachment.Spec.VolumeName)

			// If promotion event, create a replacement replica
			if isAttached(azVolumeAttachment) && azVolumeAttachment.Status.Detail.PreviousRole == azdiskv1beta2.ReplicaRole {
				r.triggerManageReplica(azVolumeAttachment.Spec.VolumeName)
			}
		}
	} else {
		// queue garbage collection if a primary attachment is being demoted
		if isDemotionRequested(azVolumeAttachment) {
			//nolint:contextcheck // Garbage collection is asynchronous; context is not inherited by design
			r.triggerGarbageCollection(ctx, azVolumeAttachment.Spec.VolumeName)
			return reconcile.Result{}, nil
		}

		if deleteRequested, _ := objectDeletionRequested(azVolumeAttachment); !deleteRequested {
			// create a replica attachment was requested for a detach, create a replacement replica if the attachment is not being cleaned up
			if volumeDetachRequested(azVolumeAttachment) {
				if azVolumeAttachment.Status.State == azdiskv1beta2.DetachmentFailed {
					// if detachment failed and the driver is uninstalling, delete azvolumeattachment CRI to let uninstallation proceed
					if r.driverLifecycle.IsDriverUninstall() {
						r.logger.Info("Deleting AzVolumeAttachment in DetachmentFailed state without detaching disk", workflow.GetObjectDetails(azVolumeAttachment)...)
						if err := r.cachedClient.Delete(ctx, azVolumeAttachment); err != nil {
							return reconcile.Result{Requeue: true}, err
						}
					} else {
						if _, err := azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, azVolumeAttachment, func(obj client.Object) error {
							azVolumeAttachment := obj.(*azdiskv1beta2.AzVolumeAttachment)
							_, err = updateState(azVolumeAttachment, azdiskv1beta2.ForceDetachPending, normalUpdate)
							return err
						}, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
							return reconcile.Result{Requeue: true}, err
						}
					}
				}

				if !isCleanupRequested(azVolumeAttachment) {
					go r.handleReplicaDelete(context.Background(), azVolumeAttachment)
				}
			} else if azVolumeAttachment.Status.State == azdiskv1beta2.AttachmentFailed {
				// if the failed attachment isn't retriable, delete the CRI so that replacing replica AzVolumeAttachment can be created
				// once the attachment has ever been retriable, it should be retried until it succeeds or gets detached
				if !azureutils.MapContains(azVolumeAttachment.Status.Annotations, consts.ReplicaVolumeAttachRetryAnnotation) && !azureutils.MapContains(azVolumeAttachment.Status.Annotations, consts.ReplicaVolumeAttachRetryCount) {
					if err := r.cachedClient.Delete(ctx, azVolumeAttachment); err != nil {
						return reconcile.Result{Requeue: true}, err
					}
					go r.handleReplicaDelete(context.Background(), azVolumeAttachment)
					// else, reset the state to AttachmentPending to retry the attachment
				} else {
					retryCount := 0
					if value, exists := azureutils.GetFromMap(azVolumeAttachment.Status.Annotations, consts.ReplicaVolumeAttachRetryCount); exists {
						if n, err := strconv.Atoi(value); err == nil {
							retryCount = n
						} else {
							r.logger.Error(err, "failed to convert retry count to int")
						}
					}

					var updateFunc azureutils.UpdateCRIFunc
					if retryCount < r.config.ControllerConfig.ReplicaVolumeAttachRetryLimit {
						updateFunc = func(obj client.Object) error {
							azva := obj.(*azdiskv1beta2.AzVolumeAttachment)
							azureutils.RemoveFromMap(azva.Status.Annotations, consts.ReplicaVolumeAttachRetryAnnotation)
							azureutils.AddToMap(azva.Status.Annotations, consts.ReplicaVolumeAttachRetryCount, strconv.Itoa(retryCount+1))
							_, derr := updateState(azva, azdiskv1beta2.AttachmentPending, forceUpdate)
							azva.Status.Detail = nil
							azva.Status.Error = nil
							return derr
						}
					} else {
						updateFunc = func(obj client.Object) error {
							azva := obj.(*azdiskv1beta2.AzVolumeAttachment)
							azureutils.AddToMap(azva.Status.Annotations, consts.VolumeDetachRequestAnnotation, "replica-controller")
							_, derr := updateState(azva, azdiskv1beta2.AttachmentPending, forceUpdate)
							return derr
						}
					}

					if _, err := azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
						return reconcile.Result{Requeue: true}, err
					}
				}
			}
		}
	}
	return reconcile.Result{}, nil
}

func (r *ReconcileReplica) handleReplicaDelete(ctx context.Context, azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment) {
	// wait for replica AzVolumeAttachment deletion
	waiter := r.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeAttachmentType, azVolumeAttachment.Name, verifyObjectDeleted)
	defer waiter.Close()
	_, _ = waiter.Wait(ctx)

	// add replica management operation to the queue
	r.triggerManageReplica(azVolumeAttachment.Spec.VolumeName)
}

//nolint:contextcheck // context is not inherited by design
func (r *ReconcileReplica) triggerManageReplica(volumeName string) {
	manageReplicaCtx, w := workflow.New(context.Background(), workflow.WithDetails(consts.VolumeNameLabel, volumeName))
	defer w.Finish(nil)
	r.addToOperationQueue(
		manageReplicaCtx,
		volumeName,
		replica,
		func(ctx context.Context) error {
			return r.manageReplicas(ctx, volumeName)
		},
		false,
	)
}

//nolint:contextcheck // Garbage collection is asynchronous; context is not inherited by design
func (r *ReconcileReplica) triggerGarbageCollection(ctx context.Context, volumeName string) {
	deletionCtx, cancelFunc := context.WithCancel(context.Background())
	if _, ok := r.cleanUpMap.LoadOrStore(volumeName, cancelFunc); ok {
		cancelFunc()
		return
	}

	workflowCtx, w := workflow.New(deletionCtx, workflow.WithDetails(consts.VolumeNameLabel, volumeName))
	w.Logger().V(5).Infof("Garbage collection of AzVolumeAttachments for AzVolume (%s) scheduled in %s.", volumeName, r.timeUntilGarbageCollection.String())

	go func() {
		defer w.Finish(nil)
		select {
		case <-deletionCtx.Done():
			return
		case <-time.After(r.timeUntilGarbageCollection):
			r.garbageCollectReplicas(workflowCtx, volumeName, replica)
			r.triggerCreateFailedReplicas(workflowCtx, volumeName)
		}
	}()

}

// After volumes are garbage collected and attachment capacity opens up on a node, this method
// attempts to create previously failed replicas
func (r *ReconcileReplica) triggerCreateFailedReplicas(ctx context.Context, volumeName string) {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	w.Logger().V(5).Info("Checking for replicas to be created after garbage collection.")
	volRequirement, err := azureutils.CreateLabelRequirements(consts.VolumeNameLabel, selection.Equals, volumeName)
	if err != nil {
		w.Logger().Errorf(err, "failed to create label requirements.")
		return
	}
	labelSelector := labels.NewSelector().Add(*volRequirement)
	listOptions := client.ListOptions{LabelSelector: labelSelector}
	azVolumeAttachmentList := &azdiskv1beta2.AzVolumeAttachmentList{}

	waitForDelete := true
	if err = r.cachedClient.List(ctx, azVolumeAttachmentList, &listOptions); err != nil {
		if errors.IsNotFound(err) {
			waitForDelete = false
		} else {
			w.Logger().Errorf(err, "failed to list AzVolumeAttachments for volume (%s)", volumeName)
			return
		}
	}

	if waitForDelete {
		var wg sync.WaitGroup
		var numErr uint32
		errs := make([]error, len(azVolumeAttachmentList.Items))
		for i := range azVolumeAttachmentList.Items {
			wg.Add(1)
			go func(index int) {
				var err error
				defer wg.Done()
				defer func() {
					if err != nil {
						_ = atomic.AddUint32(&numErr, 1)
					}
				}()

				waiter := r.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeAttachmentType, azVolumeAttachmentList.Items[index].Name, verifyObjectDeleted)
				defer waiter.Close()
				if _, err = waiter.Wait(ctx); err != nil {
					errs[index] = err
				}
			}(i)
		}
		// wait for all AzVolumeAttachments to be deleted
		wg.Wait()

		if numErr > 0 {
			err := status.Errorf(codes.Internal, "%+v", errs)
			w.Logger().Error(err, "failed to wait for replica AzVolumeAttachments cleanup.")
			return
		}
	}

	r.tryCreateFailedReplicas(ctx, "replicaController")
}

func NewReplicaController(mgr manager.Manager, controllerSharedState *SharedState) (*ReconcileReplica, error) {
	logger := mgr.GetLogger().WithValues("controller", "replica")
	reconciler := ReconcileReplica{
		SharedState:                controllerSharedState,
		timeUntilGarbageCollection: DefaultTimeUntilGarbageCollection,
		logger:                     logger,
	}
	c, err := controller.New("replica-controller", mgr, controller.Options{
		MaxConcurrentReconciles: controllerSharedState.config.ControllerConfig.WorkerThreads,
		Reconciler:              &reconciler,
		LogConstructor:          func(req *reconcile.Request) logr.Logger { return logger },
	})

	if err != nil {
		logger.Error(err, "failed to create controller")
		return nil, err
	}

	logger.Info("Starting to watch AzVolumeAttachments.")

	err = c.Watch(&source.Kind{Type: &azdiskv1beta2.AzVolumeAttachment{}}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			azVolumeAttachment, ok := e.Object.(*azdiskv1beta2.AzVolumeAttachment)
			if ok && azVolumeAttachment.Spec.RequestedRole == azdiskv1beta2.PrimaryRole {
				return true
			}
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true
		},
	})
	if err != nil {
		logger.Error(err, "failed to initialize watch for AzVolumeAttachment CRI")
		return nil, err
	}

	logger.V(2).Info("Controller set-up successful.")
	return &reconciler, nil
}
