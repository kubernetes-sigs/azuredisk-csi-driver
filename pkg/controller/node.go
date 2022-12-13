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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"

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

// ReconcileNode reconciles AzDriverNode
type ReconcileNode struct {
	*SharedState
	logger logr.Logger
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &ReconcileNode{}

func (r *ReconcileNode) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if !r.isRecoveryComplete() {
		return reconcile.Result{Requeue: true}, nil
	}
	logger := r.logger.WithValues(consts.NodeNameLabel, request.Name)

	n := &corev1.Node{}
	err := r.cachedClient.Get(ctx, request.NamespacedName, n)

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
		if _, err = r.cleanUpAzVolumeAttachmentByNode(ctx, request.Name, azdrivernode, azureutils.AllRoles, cleanUpAttachment, deleteOnly); err != nil {
			return reconcile.Result{Requeue: true}, err
		}
		return reconcile.Result{}, nil
	}

	if err != nil {
		return reconcile.Result{Requeue: true}, err
	}

	// If the node has no DeletionTimestamp, it means it's either create event or update event
	if n.ObjectMeta.DeletionTimestamp.IsZero() && !n.Spec.Unschedulable {
		// Add the new schedulable node in availableAttachmentsMap
		r.addNodeToAvailableAttachmentsMap(ctx, n.Name, n.GetLabels())

		// Node is schedulable, proceed to attempt creation of replica attachment
		logger.Info("Node is now available. Will requeue failed replica creation requests.")
		r.tryCreateFailedReplicas(ctx, nodeavailability)
	}

	return reconcile.Result{}, nil
}

// run an update on existing azdrivernode objects to store them under new version if necessary
func (r *ReconcileNode) Recover(ctx context.Context, recoveryID string) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	var azNodes *azdiskv1beta2.AzDriverNodeList
	if azNodes, err = r.azClient.DiskV1beta2().AzDriverNodes(r.config.ObjectNamespace).List(ctx, metav1.ListOptions{}); err != nil {
		return err
	}

	errCount := 0
	for _, azNode := range azNodes.Items {
		// if the corresponding node has been deleted, delete the azdrivernode object
		_, err = r.kubeClient.CoreV1().Nodes().Get(ctx, azNode.Spec.NodeName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				n := r.azClient.DiskV1beta2().AzDriverNodes(r.config.ObjectNamespace)
				if err = n.Delete(ctx, azNode.Name, metav1.DeleteOptions{}); err != nil {
					w.Logger().Errorf(err, "failed to delete azDriverNode (%s)", azNode.Name)
					errCount++
				}
			} else {
				w.Logger().Errorf(err, "failed to find node (%s)", azNode.Spec.NodeName)
				errCount++
			}
		} else {
			updateFunc := func(obj client.Object) error {
				azNode := obj.(*azdiskv1beta2.AzDriverNode)
				azNode.Annotations = azureutils.AddToMap(azNode.Annotations, consts.RecoverAnnotation, recoveryID)
				return nil
			}
			if _, err = azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, &azNode, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRI); err != nil {
				w.Logger().Errorf(err, "failed to recover azDriverNode (%s) with annotation", azNode.Name)
				errCount++
				continue
			}
			r.addNodeToAvailableAttachmentsMap(ctx, azNode.Name, azNode.GetLabels())
		}
	}

	if errCount > 0 {
		err = fmt.Errorf("failed to recover all azDriverNodes")
	}
	return err
}

// NewNodeController initializes node-controller
func NewNodeController(mgr manager.Manager, controllerSharedState *SharedState) (*ReconcileNode, error) {
	logger := mgr.GetLogger().WithValues("controller", "node")
	reconciler := ReconcileNode{
		SharedState: controllerSharedState,
		logger:      logger,
	}

	c, err := controller.New("node-controller", mgr, controller.Options{
		MaxConcurrentReconciles: consts.DefaultWorkerThreads,
		Reconciler:              &reconciler,
		LogConstructor:          func(req *reconcile.Request) logr.Logger { return logger },
	})

	if err != nil {
		logger.Error(err, "failed to create controller")
		return nil, err
	}

	// Predicate to reconcile created, new schedulable and deleted nodes
	p := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return true
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
