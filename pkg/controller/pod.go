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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	kubeClientSet "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

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

//Struct for the reconciler
type ReconcilePod struct {
	client                client.Client
	kubeClient            kubeClientSet.Interface
	azVolumeClient        azClientSet.Interface
	namespace             string
	controllerSharedState *SharedState
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &ReconcilePod{}

func (r *ReconcilePod) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	var pod corev1.Pod
	klog.V(5).Infof("Reconcile pod %s.", request.Name)
	podKey := getQualifiedName(request.Namespace, request.Name)
	if err := r.client.Get(ctx, request.NamespacedName, &pod); err != nil {
		// if the pod has been deleted, remove entry from podToClaimsMap and claimToPodMap
		if errors.IsNotFound(err) {
			klog.V(5).Infof("Failed to reconcile pod %s. Pod was deleted.", request.Name)
			r.controllerSharedState.deletePod(podKey)
			return reconcile.Result{}, nil
		}
		klog.Errorf("Error getting the pod %s. Error: %v", podKey, err)
		return reconcile.Result{Requeue: true}, err
	}

	r.controllerSharedState.addPod(&pod, acquireLock)

	if err := r.createReplicas(ctx, podKey); err != nil {
		klog.V(5).Infof("Error creating replicas for pod %s. Error: %v. Requeuing reconciliation.", request.Name, err)
		return reconcile.Result{Requeue: true}, err
	}
	klog.V(5).Infof("Successfully created replicas for pod %s. Reconciliation succeeded.", request.Name)
	return reconcile.Result{}, nil
}

func (r *ReconcilePod) createReplicas(ctx context.Context, podKey string) error {
	klog.V(5).Infof("Creating replicas for pod %s.", podKey)
	volumes, err := r.controllerSharedState.getVolumesFromPod(podKey)
	if err != nil {
		klog.V(5).Infof("Error getting volumes for pod %s. Error: %v", podKey, err)
		return err
	}
	klog.V(5).Infof("Pod %s has %d volumes. Volumes: %v", podKey, len(volumes), volumes)

	nodes, err := getRankedNodesForReplicaAttachments(ctx, r, volumes)
	if err != nil {
		klog.V(5).Infof("Error getting nodes for replicas for pod %s. Error: %v", podKey, err)
		return err
	}

	// creating replica attachments for each volume
	for _, volume := range volumes {
		azVolume, err := azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volume, consts.AzureDiskCrdNamespace, true)
		if err != nil {
			klog.V(5).Infof("Error getting azvolumes for pod %s. Error: %v", podKey, err)
			return err
		}
		// if underlying volume is not present, abort operation
		if !isCreated(azVolume) {
			return status.Errorf(codes.Aborted, "azVolume (%s) has no underlying volume object", azVolume.Name)
		}

		// get all replica attachments for the given volume
		if r.controllerSharedState.isVolumeVisited(volume) {
			klog.Infof("No need to create replica attachment for volume (%s). Replica controller is responsible for it")
			continue
		}

		numCreated := 0
		// loop over all eligible nodes to create replicas.
		for _, node := range nodes {
			if numCreated >= azVolume.Spec.MaxMountReplicaCount {
				break
			}
			if err := createReplicaAzVolumeAttachment(ctx, r, azVolume.Status.Detail.ResponseObject.VolumeID, node, azVolume.Spec.Parameters); err != nil {
				klog.Warningf("Error creating %d/%d replicas azvolumeattachment for pod %s and volume %s on node %s. Error: %v", azVolume.Spec.MaxMountReplicaCount, numCreated, podKey, volume, node, err)
				return err
			}
			numCreated++
		}

		// once replica attachment batch is created by pod controller, future replica reconciliation needs to be handled by replica controller
		r.controllerSharedState.markVolumeVisited(volume)
	}
	return nil
}

func (r *ReconcilePod) Recover(ctx context.Context) error {
	klog.V(5).Info("Recover pod state.")
	// recover replica AzVolumeAttachments
	var pods corev1.PodList
	if err := r.client.List(ctx, &pods, &client.ListOptions{}); err != nil {
		klog.Errorf("failed to list pods")
		return err
	}

	for _, pod := range pods.Items {
		// update the shared map
		podKey := getQualifiedName(pod.Namespace, pod.Name)
		r.controllerSharedState.addPod(&pod, skipLock)
		if err := r.createReplicas(ctx, podKey); err != nil {
			klog.Warningf("failed to create replica AzVolumeAttachments for pod (%s)", podKey)
		}
	}

	return nil
}

func NewPodController(mgr manager.Manager, azVolumeClient azClientSet.Interface, kubeClient kubeClientSet.Interface, controllerSharedState *SharedState) (*ReconcilePod, error) {
	logger := mgr.GetLogger().WithValues("controller", "pod")
	reconciler := ReconcilePod{
		client:                mgr.GetClient(),
		azVolumeClient:        azVolumeClient,
		kubeClient:            kubeClient,
		controllerSharedState: controllerSharedState,
	}
	c, err := controller.New("pod-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		Log:                     logger,
	})

	if err != nil {
		klog.Errorf("Failed to create pod controller. Error: (%v)", err)
		return nil, err
	}

	klog.V(2).Info("Starting to watch Pod.")

	// Watch for Update events on Pod objects
	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return false
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
		klog.Errorf("Failed to watch Pod. Error: %v", err)
		return nil, err
	}

	klog.V(2).Info("Controller set-up successful.")
	return &reconciler, nil
}

func (r *ReconcilePod) getClient() client.Client {
	return r.client
}

func (r *ReconcilePod) getAzClient() azClientSet.Interface {
	return r.azVolumeClient
}
