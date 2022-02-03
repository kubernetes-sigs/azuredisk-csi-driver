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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	kubeClientSet "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
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
	client                     client.Client
	azVolumeClient             azClientSet.Interface
	kubeClient                 kubeClientSet.Interface
	controllerSharedState      *SharedState
	timeUntilGarbageCollection time.Duration
}

var _ reconcile.Reconciler = &ReconcileReplica{}

func (r *ReconcileReplica) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	azVolumeAttachment, err := azureutils.GetAzVolumeAttachment(ctx, r.client, r.azVolumeClient, request.Name, request.Namespace, true)
	if errors.IsNotFound(err) {
		klog.Infof("AzVolumeAttachment (%s) has been successfully deleted.", request.Name)
		return reconcile.Result{}, nil
	} else if err != nil {
		klog.Errorf("failed to fetch AzVolumeAttachment (%s): %v", request.Name, err)
		return reconcile.Result{Requeue: true}, err
	}

	if azVolumeAttachment.Spec.RequestedRole == v1alpha1.PrimaryRole {
		// Deletion Event
		if objectDeletionRequested(azVolumeAttachment) {
			if volumeDetachRequested(azVolumeAttachment) {
				// If primary attachment is marked for deletion, queue garbage collection for replica attachments
				r.triggerGarbageCollection(azVolumeAttachment.Spec.UnderlyingVolume) //nolint:contextcheck // Garbage collection is asynchronous; context is not inherited by design
			}
		} else {
			// If not, cancel scheduled garbage collection if there is one enqueued
			r.removeGarbageCollection(azVolumeAttachment.Spec.UnderlyingVolume)

			// If promotion event, create a replacement replica
			if isAttached(azVolumeAttachment) && azVolumeAttachment.Status.Detail.PreviousRole == v1alpha1.ReplicaRole {
				r.controllerSharedState.addToOperationQueue(
					azVolumeAttachment.Spec.UnderlyingVolume,
					replica,
					func() error {
						return r.controllerSharedState.manageReplicas(context.Background(), azVolumeAttachment.Spec.UnderlyingVolume, r)
					},
					false,
				)
			}
		}
	} else {
		// create a replacement replica if replica attachment failed or promoted
		if objectDeletionRequested(azVolumeAttachment) {
			if azVolumeAttachment.Status.State == v1alpha1.DetachmentFailed {
				if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, azVolumeAttachment, func(obj interface{}) error {
					azVolumeAttachment := obj.(*v1alpha1.AzVolumeAttachment)
					_, err = updateState(azVolumeAttachment, v1alpha1.ForceDetachPending, normalUpdate)
					return err
				}, consts.NormalUpdateMaxNetRetry); err != nil {
					return reconcile.Result{Requeue: true}, err
				}
			}
			if azVolumeAttachment.Annotations == nil || !metav1.HasAnnotation(azVolumeAttachment.ObjectMeta, consts.CleanUpAnnotation) {
				go func() {
					// wait for replica AzVolumeAttachment deletion
					conditionFunc := func() (bool, error) {
						var tmp v1alpha1.AzVolumeAttachment
						err := r.client.Get(ctx, request.NamespacedName, &tmp)
						if errors.IsNotFound(err) {
							return true, nil
						}

						return false, err
					}
					_ = wait.PollImmediateInfinite(deletionPollingInterval, conditionFunc)

					// add replica management operation to the queue
					r.controllerSharedState.addToOperationQueue(
						azVolumeAttachment.Spec.UnderlyingVolume,
						replica,
						func() error {
							return r.controllerSharedState.manageReplicas(context.Background(), azVolumeAttachment.Spec.UnderlyingVolume, r)
						},
						false,
					)
				}()
			}
		} else if azVolumeAttachment.Status.State == v1alpha1.AttachmentFailed {
			// if attachment failed for replica AzVolumeAttachment, delete the CRI so that replace replica AzVolumeAttachment can be created.
			if err := r.client.Delete(ctx, azVolumeAttachment); err != nil {
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
			r.controllerSharedState.addToOperationQueue(
				volumeName,
				replica,
				func() error {
					_, err := cleanUpAzVolumeAttachmentByVolume(context.Background(), r, volumeName, "replicaController", all, detachAndDeleteCRI, r.controllerSharedState)
					if err != nil {
						return err
					}
					r.controllerSharedState.addToGcExclusionList(volumeName, replica)
					r.removeGarbageCollection(volumeName)
					r.controllerSharedState.unmarkVolumeVisited(volumeName)
					return nil
				},
				true,
			)
		}
	}(deletionCtx)
}

func (r *ReconcileReplica) removeGarbageCollection(volumeName string) {
	v, ok := r.controllerSharedState.cleanUpMap.LoadAndDelete(volumeName)
	if ok {
		cancelFunc := v.(context.CancelFunc)
		cancelFunc()
	}
	// if there is any garbage collection enqueued in operation queue, remove it
	r.controllerSharedState.dequeueGarbageCollection(volumeName)
}

func NewReplicaController(mgr manager.Manager, azVolumeClient azClientSet.Interface, kubeClient kubeClientSet.Interface, controllerSharedState *SharedState) (*ReconcileReplica, error) {
	logger := mgr.GetLogger().WithValues("controller", "replica")
	reconciler := ReconcileReplica{
		client:                     mgr.GetClient(),
		azVolumeClient:             azVolumeClient,
		kubeClient:                 kubeClient,
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

	err = c.Watch(&source.Kind{Type: &v1alpha1.AzVolumeAttachment{}}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			azVolumeAttachment, ok := e.Object.(*v1alpha1.AzVolumeAttachment)
			if ok && azVolumeAttachment.Spec.RequestedRole == v1alpha1.PrimaryRole {
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

func (r *ReconcileReplica) getClient() client.Client {
	return r.client
}

func (r *ReconcileReplica) getAzClient() azClientSet.Interface {
	return r.azVolumeClient
}

func (r *ReconcileReplica) getSharedState() *SharedState {
	return r.controllerSharedState
}
