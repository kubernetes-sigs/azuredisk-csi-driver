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
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
	diskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
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

const (
	deletionPollingInterval = time.Duration(10) * time.Second
)

type ReconcileReplica struct {
	logger                     logr.Logger
	controllerSharedState      *SharedState
	timeUntilGarbageCollection time.Duration
}

var _ reconcile.Reconciler = &ReconcileReplica{}

func (r *ReconcileReplica) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if !r.controllerSharedState.isRecoveryComplete() {
		return reconcile.Result{Requeue: true}, nil
	}

	azVolumeAttachment, err := azureutils.GetAzVolumeAttachment(ctx, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, request.Name, request.Namespace, true)
	if errors.IsNotFound(err) {
		return reconcile.Result{}, nil
	} else if err != nil {
		r.logger.Error(err, fmt.Sprintf("failed to get AzVolumeAttachment (%s)", request.Name))
		return reconcile.Result{Requeue: true}, err
	}

	if azVolumeAttachment.Spec.RequestedRole == diskv1beta1.PrimaryRole {
		// Deletion Event
		if objectDeletionRequested(azVolumeAttachment) {
			if volumeDetachRequested(azVolumeAttachment) {
				// If primary attachment is marked for deletion, queue garbage collection for replica attachments
				r.triggerGarbageCollection(ctx, azVolumeAttachment.Spec.VolumeName) //nolint:contextcheck // Garbage collection is asynchronous; context is not inherited by design
			}
		} else {
			// If not, cancel scheduled garbage collection if there is one enqueued
			r.controllerSharedState.removeGarbageCollection(azVolumeAttachment.Spec.VolumeName)

			// If promotion event, create a replacement replica
			if isAttached(azVolumeAttachment) && azVolumeAttachment.Status.Detail.PreviousRole == diskv1beta1.ReplicaRole {
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
			if azVolumeAttachment.Status.State == diskv1beta1.DetachmentFailed {
				if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, func(obj interface{}) error {
					azVolumeAttachment := obj.(*diskv1beta1.AzVolumeAttachment)
					_, err = updateState(azVolumeAttachment, diskv1beta1.ForceDetachPending, normalUpdate)
					return err
				}, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
					return reconcile.Result{Requeue: true}, err
				}
			}
			if !isCleanupRequested(azVolumeAttachment) || !volumeDetachRequested(azVolumeAttachment) {
				go func() {
					goCtx := context.Background()

					// wait for replica AzVolumeAttachment deletion
					conditionFunc := func() (bool, error) {
						var tmp diskv1beta1.AzVolumeAttachment
						err := r.controllerSharedState.cachedClient.Get(goCtx, request.NamespacedName, &tmp)
						if errors.IsNotFound(err) {
							return true, nil
						}

						return false, err
					}
					_ = wait.PollImmediateInfinite(deletionPollingInterval, conditionFunc)

					// add replica management operation to the queue
					r.triggerManageReplica(goCtx, azVolumeAttachment.Spec.VolumeName)
				}()
			}
		} else if azVolumeAttachment.Status.State == diskv1beta1.AttachmentFailed {
			// if attachment failed for replica AzVolumeAttachment, delete the CRI so that replace replica AzVolumeAttachment can be created.
			if err := r.controllerSharedState.cachedClient.Delete(ctx, azVolumeAttachment); err != nil {
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
	r.controllerSharedState.addToOperationQueue(
		manageReplicaCtx,
		volumeName,
		replica,
		func(ctx context.Context) error {
			return r.controllerSharedState.manageReplicas(ctx, volumeName)
		},
		false,
	)
}

//nolint:contextcheck // Garbage collection is asynchronous; context is not inherited by design
func (r *ReconcileReplica) triggerGarbageCollection(ctx context.Context, volumeName string) {
	deletionCtx, cancelFunc := context.WithCancel(context.Background())
	if _, ok := r.controllerSharedState.cleanUpMap.LoadOrStore(volumeName, cancelFunc); ok {
		cancelFunc()
		return
	}

	workflowCtx, w := workflow.New(deletionCtx, workflow.WithDetails(consts.VolumeNameLabel, volumeName))
	w.Logger().V(5).Infof("Garbage collection of AzVolumeAttachments for AzVolume (%s) scheduled in %s.", volumeName, r.timeUntilGarbageCollection.String())

	go func() {
		defer w.Finish(nil)
		select {
		case <-workflowCtx.Done():
			return
		case <-time.After(r.timeUntilGarbageCollection):
			r.controllerSharedState.garbageCollectReplicas(workflowCtx, volumeName, replica)
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
		w.Logger().Errorf(err, "Failed to create label requirements.")
		return
	}
	labelSelector := labels.NewSelector().Add(*volRequirement)
	listOptions := client.ListOptions{LabelSelector: labelSelector}
	err = wait.PollImmediateWithContext(ctx, deletionPollingInterval, 10*time.Minute, func(ctx context.Context) (bool, error) {
		azVolumeAttachmentList := &diskv1beta1.AzVolumeAttachmentList{}
		err := r.controllerSharedState.cachedClient.List(ctx, azVolumeAttachmentList, &listOptions)
		if err != nil {
			if errors.IsNotFound(err) {
				return true, nil
			}
			w.Logger().Errorf(err, "Failed to get AzVolumeAttachments.")
			return false, err
		}
		return len(azVolumeAttachmentList.Items) == 0, nil
	})
	if err != nil {
		w.Logger().Errorf(err, "Failed polling for AzVolumeAttachments to be zero length.")
		return
	}
	r.controllerSharedState.tryCreateFailedReplicas(ctx, "replicaController")
}

func NewReplicaController(mgr manager.Manager, controllerSharedState *SharedState) (*ReconcileReplica, error) {
	logger := mgr.GetLogger().WithValues("controller", "replica")
	reconciler := ReconcileReplica{
		controllerSharedState:      controllerSharedState,
		timeUntilGarbageCollection: DefaultTimeUntilGarbageCollection,
		logger:                     logger,
	}
	c, err := controller.New("replica-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		Log:                     logger,
	})

	if err != nil {
		logger.Error(err, "failed to create controller")
		return nil, err
	}

	logger.Info("Starting to watch AzVolumeAttachments.")

	err = c.Watch(&source.Kind{Type: &diskv1beta1.AzVolumeAttachment{}}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			azVolumeAttachment, ok := e.Object.(*diskv1beta1.AzVolumeAttachment)
			if ok && azVolumeAttachment.Spec.RequestedRole == diskv1beta1.PrimaryRole {
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
