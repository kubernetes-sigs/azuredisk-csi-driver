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
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	diskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
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
		klog.Infof("AzVolumeAttachment (%s) has been successfully deleted.", request.Name)
		return reconcile.Result{}, nil
	} else if err != nil {
		klog.Errorf("failed to fetch AzVolumeAttachment (%s): %v", request.Name, err)
		return reconcile.Result{Requeue: true}, err
	}

	if azVolumeAttachment.Spec.RequestedRole == diskv1beta1.PrimaryRole {
		// Deletion Event
		if objectDeletionRequested(azVolumeAttachment) {
			if volumeDetachRequested(azVolumeAttachment) {
				// If primary attachment is marked for deletion, queue garbage collection for replica attachments
				//nolint:contextcheck // Garbage collection is asynchronous; context is not inherited by design
				r.triggerGarbageCollection(azVolumeAttachment.Spec.VolumeName) //nolint:contextcheck // Garbage collection is asynchronous; context is not inherited by design
			}
		} else {
			// If not, cancel scheduled garbage collection if there is one enqueued
			r.controllerSharedState.removeGarbageCollection(azVolumeAttachment.Spec.VolumeName)

			// If promotion event, create a replacement replica
			if isAttached(azVolumeAttachment) && azVolumeAttachment.Status.Detail.PreviousRole == diskv1beta1.ReplicaRole {
				r.controllerSharedState.addToOperationQueue(
					azVolumeAttachment.Spec.VolumeName,
					replica,
					func() error {
						return r.controllerSharedState.manageReplicas(context.Background(), azVolumeAttachment.Spec.VolumeName)
					},
					false,
				)
			}
		}
	} else {
		// queue garbage collection if a primary attachment is being demoted
		if isDemotionRequested(azVolumeAttachment) {
			//nolint:contextcheck // Garbage collection is asynchronous; context is not inherited by design
			r.triggerGarbageCollection(azVolumeAttachment.Spec.VolumeName)
			return reconcile.Result{}, nil
		}
		// create a replacement replica if replica attachment failed
		if objectDeletionRequested(azVolumeAttachment) {
			if azVolumeAttachment.Status.State == diskv1beta1.DetachmentFailed {
				if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, azVolumeAttachment, func(obj interface{}) error {
					azVolumeAttachment := obj.(*diskv1beta1.AzVolumeAttachment)
					_, err = updateState(azVolumeAttachment, diskv1beta1.ForceDetachPending, normalUpdate)
					return err
				}, consts.NormalUpdateMaxNetRetry); err != nil {
					return reconcile.Result{Requeue: true}, err
				}
			}
			if !isCleanupRequested(azVolumeAttachment) || !volumeDetachRequested(azVolumeAttachment) {
				go func() {
					// wait for replica AzVolumeAttachment deletion
					conditionFunc := func() (bool, error) {
						var tmp diskv1beta1.AzVolumeAttachment
						err := r.controllerSharedState.cachedClient.Get(ctx, request.NamespacedName, &tmp)
						if errors.IsNotFound(err) {
							return true, nil
						}

						return false, err
					}
					_ = wait.PollImmediateInfinite(deletionPollingInterval, conditionFunc)

					// add replica management operation to the queue
					r.controllerSharedState.addToOperationQueue(
						azVolumeAttachment.Spec.VolumeName,
						replica,
						func() error {
							return r.controllerSharedState.manageReplicas(context.Background(), azVolumeAttachment.Spec.VolumeName)
						},
						false,
					)
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

func (r *ReconcileReplica) triggerGarbageCollection(volumeName string) {
	emptyCtx := context.Background()
	deletionCtx, cancelFunc := context.WithCancel(emptyCtx)
	if _, ok := r.controllerSharedState.cleanUpMap.LoadOrStore(volumeName, cancelFunc); ok {
		klog.Infof("There already is a scheduled garbage collection for AzVolume (%s)", volumeName)
		cancelFunc()
		return
	}
	klog.Infof("garbage collection of AzVolumeAttachments for AzVolume (%s) scheduled in %s.", volumeName, r.timeUntilGarbageCollection.String())

	go func(ctx context.Context) {
		select {
		case <-ctx.Done():
			klog.Infof("garbage collection for AzVolume (%s) cancelled", volumeName)
			return
		case <-time.After(r.timeUntilGarbageCollection):
			klog.Infof("Initiating garbage collection for AzVolume (%s)", volumeName)
			r.controllerSharedState.garbageCollectReplicas(ctx, volumeName, replica)
		}
	}(deletionCtx)
}

func NewReplicaController(mgr manager.Manager, controllerSharedState *SharedState) (*ReconcileReplica, error) {
	logger := mgr.GetLogger().WithValues("controller", "replica")
	reconciler := ReconcileReplica{
		controllerSharedState:      controllerSharedState,
		timeUntilGarbageCollection: DefaultTimeUntilGarbageCollection,
	}
	c, err := controller.New("replica-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		Log:                     logger,
	})

	if err != nil {
		klog.Errorf("Failed to create replica controller. Error: (%v)", err)
		return nil, err
	}

	klog.V(2).Info("Starting to watch AzVolumeAttachments.")

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
		klog.Errorf("Failed to watch AzVolumeAttachment. Error: %v", err)
		return nil, err
	}

	klog.V(2).Info("Controller set-up successful.")
	return &reconciler, nil
}
