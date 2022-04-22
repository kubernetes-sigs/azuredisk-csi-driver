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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// ReconcileAzDriverNode reconciles AzDriverNode
type ReconcileAzDriverNode struct {
	controllerSharedState *SharedState
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &ReconcileAzDriverNode{}

func (r *ReconcileAzDriverNode) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if !r.controllerSharedState.isRecoveryComplete() {
		return reconcile.Result{Requeue: true}, nil
	}

	klog.V(2).Info("Checking to see if node (%v) exists.", request.NamespacedName)
	n := &corev1.Node{}
	err := r.controllerSharedState.cachedClient.Get(ctx, request.NamespacedName, n)

	// If the node still exists don't delete the AzDriverNode
	if err == nil {
		klog.Errorf("Node still exists. Skip deleting azDriverNode for Node: (%v)", n)
		return reconcile.Result{}, nil
	}

	// If the node is not found, delete the corresponding AzDriverNode
	if errors.IsNotFound(err) {

		klog.V(2).Info("Deleting AzDriverNode (%s).", request.Name)

		// Delete the azDriverNode, since corresponding node is deleted
		azN := r.controllerSharedState.azClient.DiskV1beta1().AzDriverNodes(r.controllerSharedState.objectNamespace)
		err = azN.Delete(ctx, request.Name, metav1.DeleteOptions{})

		// If there is an issue in deleting the AzDriverNode, requeue
		if err != nil && !errors.IsNotFound(err) {
			klog.Errorf("Will retry. Failed to delete AzDriverNode: (%s). Error: (%v)", request.Name, err)
			return reconcile.Result{Requeue: true}, nil
		}

		// Delete all volumeAttachments attached to this node, if failed, requeue
		if _, err = r.controllerSharedState.cleanUpAzVolumeAttachmentByNode(ctx, request.Name, azdrivernode, all, detachAndDeleteCRI); err != nil {
			return reconcile.Result{Requeue: true}, nil
		}
		return reconcile.Result{}, nil
	}

	klog.Errorf("Failed to query node. Error: %v. Will retry...", err)

	return reconcile.Result{Requeue: true}, err
}

// NewAzDriverNodeController initializes azdrivernode-controller
func NewAzDriverNodeController(mgr manager.Manager, controllerSharedState *SharedState) (*ReconcileAzDriverNode, error) {
	logger := mgr.GetLogger().WithValues("controller", "azdrivernode")
	reconciler := ReconcileAzDriverNode{
		controllerSharedState: controllerSharedState,
	}
	c, err := controller.New("azdrivernode-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		Log:                     logger,
	})

	if err != nil {
		klog.Errorf("Failed to create azdrivernode controller. Error: %v", err)
		return nil, err
	}

	// Predicate to only reconcile deleted nodes
	p := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return false
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return true
		},
	}

	klog.V(2).Info("Starting to watch cluster nodes.")
	// Watch the nodes
	err = c.Watch(&source.Kind{Type: &corev1.Node{}}, &handler.EnqueueRequestForObject{}, p)
	if err != nil {
		klog.Errorf("Failed to watch nodes. Error: %v", err)
		return nil, err
	}
	klog.V(2).Info("Controller set-up successful.")

	return &reconciler, err
}
