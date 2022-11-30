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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
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
	*SharedState
	logger logr.Logger
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &ReconcileAzDriverNode{}

func (r *ReconcileAzDriverNode) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if !r.isRecoveryComplete() {
		return reconcile.Result{Requeue: true}, nil
	}

	n := &corev1.Node{}
	err := r.cachedClient.Get(ctx, request.NamespacedName, n)

	if err == nil {
		if n.ObjectMeta.DeletionTimestamp.IsZero() {
			// for create event, add the new node in availableAttachmentsMap
			r.addNodeToAvailableAttachmentsMap(ctx, n.Name, n.GetLabels())
		}
		// for delete even, if the node still exists don't delete the AzDriverNode
		return reconcile.Result{}, nil
	}

	// If the node is not found, delete the corresponding AzDriverNode
	if errors.IsNotFound(err) {
		// Delete the azDriverNode, since corresponding node is deleted
		azN := r.azClient.DiskV1beta2().AzDriverNodes(r.config.ObjectNamespace)
		err = azN.Delete(ctx, request.Name, metav1.DeleteOptions{})

		// If there is an issue in deleting the AzDriverNode, requeue
		if err != nil && !errors.IsNotFound(err) {
			return reconcile.Result{Requeue: true}, err
		}
		r.deleteNodeFromAvailableAttachmentsMap(ctx, request.Name)

		// Delete all volumeAttachments attached to this node, if failed, requeue
		if _, err = r.cleanUpAzVolumeAttachmentByNode(ctx, request.Name, azdrivernode, azureutils.AllRoles, cleanUpAttachment); err != nil {
			return reconcile.Result{Requeue: true}, err
		}
		return reconcile.Result{}, nil
	}

	return reconcile.Result{Requeue: true}, err
}

// run an update on existing azdrivernode objects to store them under new version if necessary
func (r *ReconcileAzDriverNode) Recover(ctx context.Context) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	var nodes *azdiskv1beta2.AzDriverNodeList
	if nodes, err = r.azClient.DiskV1beta2().AzDriverNodes(r.config.ObjectNamespace).List(ctx, metav1.ListOptions{}); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		return err
	}

	for _, node := range nodes.Items {
		updated := node.DeepCopy()
		updated.Annotations = azureutils.AddToMap(updated.Annotations, consts.RecoverAnnotation, "azDriverNode")
		if _, err = r.azClient.DiskV1beta2().AzDriverNodes(r.config.ObjectNamespace).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return err
		}
		r.addNodeToAvailableAttachmentsMap(ctx, node.Name, node.GetLabels())
	}
	return nil
}

// NewAzDriverNodeController initializes azdrivernode-controller
func NewAzDriverNodeController(mgr manager.Manager, controllerSharedState *SharedState) (*ReconcileAzDriverNode, error) {
	logger := mgr.GetLogger().WithValues("controller", "azdrivernode")
	reconciler := ReconcileAzDriverNode{
		SharedState: controllerSharedState,
		logger:      logger,
	}

	c, err := controller.New("azdrivernode-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		LogConstructor:          func(req *reconcile.Request) logr.Logger { return logger },
	})

	if err != nil {
		logger.Error(err, "failed to create controller")
		return nil, err
	}

	// Predicate to only reconcile deleted nodes
	p := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true
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

	logger.V(2).Info("Starting to watch cluster nodes.")
	// Watch the nodes
	err = c.Watch(&source.Kind{Type: &corev1.Node{}}, &handler.EnqueueRequestForObject{}, p)
	if err != nil {
		logger.Error(err, "failed to initialize watch for Node")
		return nil, err
	}
	logger.V(2).Info("Controller set-up successful.")

	return &reconciler, err
}
