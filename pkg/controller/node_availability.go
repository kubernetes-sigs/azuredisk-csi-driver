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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type ReconcileNodeAvailability struct {
	*SharedState
	logger logr.Logger
}

var _ reconcile.Reconciler = &ReconcileNodeAvailability{}

func (r *ReconcileNodeAvailability) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if !r.isRecoveryComplete() {
		return reconcile.Result{Requeue: true}, nil
	}

	n := &corev1.Node{}
	err := r.cachedClient.Get(ctx, request.NamespacedName, n)

	if errors.IsNotFound(err) {
		return reconcile.Result{}, nil
	}

	logger := r.logger.WithValues(consts.NodeNameLabel, request.Name)

	if err == nil {
		if !n.Spec.Unschedulable {
			//Node is schedulable, proceed to attempt creation of replica attachment
			logger.Info("Node is now available. Will requeue failed replica creation requests.")
			r.tryCreateFailedReplicas(ctx, nodeavailability)
			return reconcile.Result{Requeue: false}, nil
		}
	}
	return reconcile.Result{Requeue: false}, err
}

func NewNodeAvailabilityController(mgr manager.Manager, controllerSharedState *SharedState) (*ReconcileNodeAvailability, error) {
	logger := mgr.GetLogger().WithValues("controller", "nodeavailability")
	reconciler := ReconcileNodeAvailability{
		SharedState: controllerSharedState,
		logger:      logger,
	}

	c, err := controller.New("nodeavailability-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		LogConstructor:          func(req *reconcile.Request) logr.Logger { return logger },
	})

	if err != nil {
		logger.Error(err, "failed to create controller")
		return nil, err
	}

	//Predicate
	p := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			//If new node is unschedulable, do not proceed
			object, objectOk := e.Object.(*corev1.Node)
			if objectOk && !object.Spec.Unschedulable {
				logger.V(2).Info("aliceyu? predicate CreateFunc Node" + object.ObjectMeta.Name)
				return true
			}
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// make sure only update event from node taint changed from "unschedulable" gets enqueued to reconciler queue
			old, oldOk := e.ObjectOld.(*corev1.Node)
			new, newOk := e.ObjectNew.(*corev1.Node)

			wasUnschedulable := false
			nowSchedulable := true
			for _, taint := range old.Spec.Taints {
				if taint.Key == "node.kubernetes.io/unschedulable" {
					wasUnschedulable = true
				}
			}
			for _, taint := range new.Spec.Taints {
				if taint.Key == "node.kubernetes.io/unschedulable" {
					nowSchedulable = false
				}
			}

			if oldOk && newOk && wasUnschedulable && nowSchedulable {
				return true
			}
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
	}

	logger.V(2).Info("Starting to watch Node")

	err = c.Watch(&source.Kind{Type: &corev1.Node{}}, &handler.EnqueueRequestForObject{}, p)
	if err != nil {
		logger.Error(err, "failed to initialize watch for Node")
		return nil, err
	}
	logger.V(2).Info("Controller set-up successful.")
	return &reconciler, err
}
