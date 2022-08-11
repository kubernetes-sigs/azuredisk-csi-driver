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
		// Deletion Event
		if objectDeletionRequested(azVolumeAttachment) {
			if volumeDetachRequested(azVolumeAttachment) {
				// If primary attachment is marked for deletion, queue garbage collection for replica attachments
				r.triggerGarbageCollection(ctx, azVolumeAttachment.Spec.VolumeName) //nolint:contextcheck // Garbage collection is asynchronous; context is not inherited by design
			}
		} else {
			// If not, cancel scheduled garbage collection if there is one enqueued
			r.removeGarbageCollection(azVolumeAttachment.Spec.VolumeName)

			// If promotion event, create a replacement replica
			if isAttached(azVolumeAttachment) && azVolumeAttachment.Status.Detail.PreviousRole == azdiskv1beta2.ReplicaRole {
				r.triggerManageReplica(ctx, azVolumeAttachment.Spec.VolumeName)
			}
		}
	} else {
		// queue garbage collection if a primary attachment is being demoted
		if isDemotionRequested(azVolumeAttachment) {
			//nolint:contextcheck // Garbage collection is asynchronous; context is not inherited by design
			r.triggerGarbageCollection(ctx, azVolumeAttachment.Spec.VolumeName)
			return reconcile.Result{}, nil
		}

		// create a replacement replica if replica attachment failed
		if objectDeletionRequested(azVolumeAttachment) {
			switch azVolumeAttachment.Status.State {
			case azdiskv1beta2.DetachmentFailed:
				if _, err := azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, azVolumeAttachment, func(obj client.Object) error {
					azVolumeAttachment := obj.(*azdiskv1beta2.AzVolumeAttachment)
					_, err = updateState(azVolumeAttachment, azdiskv1beta2.ForceDetachPending, normalUpdate)
					return err
				}, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
					return reconcile.Result{Requeue: true}, err
				}
			}
			if !isCleanupRequested(azVolumeAttachment) || !volumeDetachRequested(azVolumeAttachment) {
				go func() {
					goCtx := context.Background()

					// wait for replica AzVolumeAttachment deletion
					waiter, _ := r.conditionWatcher.NewConditionWaiter(goCtx, watcher.AzVolumeAttachmentType, azVolumeAttachment.Name, verifyObjectDeleted)
					defer waiter.Close()
					_, _ = waiter.Wait(goCtx)

					// add replica management operation to the queue
					r.triggerManageReplica(goCtx, azVolumeAttachment.Spec.VolumeName)
				}()
			}
		} else if azVolumeAttachment.Status.State == azdiskv1beta2.AttachmentFailed {
			// if attachment failed for replica AzVolumeAttachment, delete the CRI so that replace replica AzVolumeAttachment can be created.
			if err := r.cachedClient.Delete(ctx, azVolumeAttachment); err != nil {
				return reconcile.Result{Requeue: true}, err
			}
		}
	}
	return reconcile.Result{}, nil
}

//nolint:contextcheck // context is not inherited by design
func (r *ReconcileReplica) triggerManageReplica(ctx context.Context, volumeName string) {
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

	workflowCtx, w := workflow.New(context.Background(), workflow.WithDetails(consts.VolumeNameLabel, volumeName))
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

				var waiter *watcher.ConditionWaiter
				waiter, err = r.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeAttachmentType, azVolumeAttachmentList.Items[index].Name, verifyObjectDeleted)
				if err != nil {
					errs[index] = err
					return
				}
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
		MaxConcurrentReconciles: 10,
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
