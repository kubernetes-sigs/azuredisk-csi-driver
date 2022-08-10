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

	"github.com/go-logr/logr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"

	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
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

// Struct for the reconciler
type ReconcilePod struct {
	logger    logr.Logger
	namespace string
	*sharedState
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &ReconcilePod{}

func (r *ReconcilePod) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if !r.isRecoveryComplete() {
		return reconcile.Result{Requeue: true}, nil
	}

	var pod corev1.Pod
	podKey := getQualifiedName(request.Namespace, request.Name)
	logger := r.logger.WithValues(consts.PodNameKey, request.Name)

	if err := r.cachedClient.Get(ctx, request.NamespacedName, &pod); err != nil {
		// if the pod has been deleted, remove entry from podToClaimsMap and claimToPodMap
		if errors.IsNotFound(err) {
			logger.V(5).Info("No need to reconcile. Pod was deleted.")
			if err := r.deletePod(ctx, podKey); err != nil {
				return reconcile.Result{Requeue: true}, err
			}
			return reconcile.Result{}, nil
		}
		r.logger.Error(err, fmt.Sprintf("failed to get pod (%s)", podKey))
		return reconcile.Result{Requeue: true}, err
	}
	if err := r.addPod(ctx, &pod, acquireLock); err != nil {
		return reconcile.Result{Requeue: true}, err
	}

	if pod.Status.Phase == corev1.PodRunning {
		if err := r.createReplicas(ctx, podKey); err != nil {
			return reconcile.Result{Requeue: true}, err
		}
	}
	return reconcile.Result{}, nil
}

func (r *ReconcilePod) createReplicas(ctx context.Context, podKey string) error {
	var err error
	ctx, w := workflow.New(ctx, workflow.WithDetails(consts.PodNameKey, podKey))
	defer func() { w.Finish(err) }()

	w.Logger().V(5).Infof("Creating replicas for pod %s.", podKey)

	var volumes []string
	volumes, err = r.getVolumesFromPod(ctx, podKey)
	if err != nil {
		w.Logger().V(5).Errorf(err, "failed to get volumes for pod %s", podKey)
		return err
	}
	w.Logger().V(5).Infof("Pod %s has %d volumes. Volumes: %v", podKey, len(volumes), volumes)

	// creating replica attachments for each volume
	for _, volume := range volumes {
		volume := volume
		r.addToOperationQueue(
			ctx,
			volume,
			pod,
			func(ctx context.Context) error {
				var err error
				var azVolume *azdiskv1beta2.AzVolume
				w, _ := workflow.GetWorkflowFromContext(ctx)
				azVolume, err = azureutils.GetAzVolume(ctx, r.cachedClient, r.azClient, volume, r.objectNamespace, true)
				if err != nil {
					w.Logger().V(5).Errorf(err, "failed to get AzVolumes for pod %s", podKey)
					return err
				}
				// if underlying volume is not present, abort operation
				if !isCreated(azVolume) {
					err = status.Errorf(codes.Aborted, "azVolume (%s) has no underlying volume object", azVolume.Name)
					return err
				}

				// get all replica attachments for the given volume
				if r.isVolumeVisited(volume) {
					w.Logger().V(5).Infof("No need to create replica attachment for volume (%s). Replica controller is responsible for it", volume)
					return nil
				}

				err = r.manageReplicas(ctx, azVolume.Spec.VolumeName)
				if err != nil {
					err = status.Errorf(codes.Internal, "failed to create replica AzVolumeAttachment for pod %s and volume %s: %v", podKey, volume, err)
					return err
				}

				// once replica attachment batch is created by pod controller, future replica reconciliation needs to be handled by replica controller
				r.markVolumeVisited(volume)
				// remove replica controller from the blacklist
				r.removeFromExclusionList(volume, replica)
				return nil
			},
			false,
		)
	}
	return nil
}

func (r *ReconcilePod) Recover(ctx context.Context) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	w.Logger().V(5).Info("Recover pod state.")
	// recover replica AzVolumeAttachments
	var pods corev1.PodList
	if err = r.cachedClient.List(ctx, &pods, &client.ListOptions{}); err != nil {
		err = status.Errorf(codes.Internal, "failed to list pods: %v", err)
		return err
	}

	for _, pod := range pods.Items {
		// update the shared map
		podKey := getQualifiedName(pod.Namespace, pod.Name)
		if err := r.addPod(ctx, &pod, skipLock); err != nil {
			w.Logger().V(5).Infof("failed to add necessary components for pod (%s)", podKey)
			continue
		}
		if err := r.createReplicas(ctx, podKey); err != nil {
			w.Logger().V(5).Infof("failed to create replica AzVolumeAttachments for pod (%s)", podKey)
		}
	}

	return nil
}

func NewPodController(mgr manager.Manager, controllerSharedState *sharedState) (*ReconcilePod, error) {
	logger := mgr.GetLogger().WithValues("controller", "pod")
	reconciler := ReconcilePod{
		sharedState: controllerSharedState,
		logger:      logger,
	}
	c, err := controller.New("pod-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		LogConstructor:          func(req *reconcile.Request) logr.Logger { return logger },
	})

	if err != nil {
		logger.Error(err, "failed to create controller")
		return nil, err
	}

	logger.V(2).Info("Starting to watch Pod.")

	// Watch for Update events on Pod objects
	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// make sure only update event from pod status change to "running" gets enqueued to reconciler queue
			old, oldOk := e.ObjectOld.(*corev1.Pod)
			new, newOk := e.ObjectNew.(*corev1.Pod)
			if oldOk && newOk && old.Status.Phase != new.Status.Phase && new.Status.Phase == corev1.PodRunning {
				return true
			}
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true
		},
	})
	if err != nil {
		logger.Error(err, "failed to initialize watch for Pod")
		return nil, err
	}

	logger.V(2).Info("Controller set-up successful.")
	return &reconciler, nil
}
