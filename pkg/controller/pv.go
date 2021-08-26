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
	"reflect"
	"strings"
	"sync"
	"sync/atomic"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	kubeClientSet "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azVolumeClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
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
type ReconcilePV struct {
	client         client.Client
	azVolumeClient azVolumeClientSet.Interface
	kubeClient     kubeClientSet.Interface
	// retryMap allows volumeAttachment controller to retry Get operation for AzVolume in case the CRI has not been created yet
	retryMap              sync.Map
	controllerSharedState *SharedState
	namespace             string
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &ReconcilePV{}

func (r *ReconcilePV) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	var pv corev1.PersistentVolume
	if err := r.client.Get(ctx, request.NamespacedName, &pv); err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		klog.Errorf("failed to get PV (%s): %v", request.Name, err)
		return reconcile.Result{Requeue: true}, err
	}

	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != azureutils.DriverName {
		return reconcile.Result{}, nil
	}

	var azVolume v1alpha1.AzVolume
	diskName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(pv.Spec.CSI.VolumeHandle)
	azVolumeName := strings.ToLower(diskName)
	if err != nil {
		klog.Errorf("failed to extract proper diskName from pv(%s)'s volume handle (%s): %v", pv.Name, pv.Spec.CSI.VolumeHandle, err)
		// if disk name cannot be extracted from volumehandle, there is no point of requeueing
		return reconcile.Result{}, err
	}
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: azVolumeName}, &azVolume); err != nil {
		// if underlying PV was found but AzVolume was not. Might be due to stale cache, so try until found, or maxTry Reached
		if !errors.IsNotFound(err) {
			klog.V(5).Infof("failed to get AzVolume (%s): %v", diskName, err)
			return reconcile.Result{Requeue: true}, err
		}

		var zero uint32
		v, _ := r.retryMap.LoadOrStore(azVolumeName, &zero)
		numRetry := v.(*uint32)

		if *numRetry < maxRetry {
			klog.V(5).Infof("Waiting for AzVolume (%s) to be created...", azVolumeName)
			atomic.AddUint32(numRetry, 1)
			return reconcile.Result{Requeue: true}, nil
		}

		klog.V(5).Infof("Max Retry (%d) for Get AzVolume (%s) exceeded. The CRI is probably deleted.", maxRetry, azVolumeName)
		r.retryMap.Delete(azVolumeName)
		return reconcile.Result{}, nil
	}

	if azVolume.Status.Detail != nil {
		updated := azVolume.DeepCopy()
		switch phase := pv.Status.Phase; phase {
		case corev1.VolumeBound:
			pvClaimName := getQualifiedName(pv.Spec.ClaimRef.Namespace, pv.Spec.ClaimRef.Name)
			updated.Status.Detail.Phase = v1alpha1.VolumeBound
			r.controllerSharedState.addVolumeAndClaim(azVolumeName, pvClaimName)
		case corev1.VolumeReleased:
			updated.Status.Detail.Phase = v1alpha1.VolumeReleased
			r.controllerSharedState.deleteVolumeAndClaim(azVolumeName)
		}

		if !reflect.DeepEqual(updated, azVolume) {
			if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
				klog.Errorf("failed to update AzVolume (%s): %v", pv.Name, err)
				return reconcile.Result{Requeue: true}, err
			}
		}
	}
	return reconcile.Result{}, nil
}

func (r *ReconcilePV) Recover(ctx context.Context) error {
	volumes, err := r.kubeClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, volume := range volumes.Items {
		if volume.Spec.CSI == nil || volume.Spec.CSI.Driver != azureutils.DriverName {
			continue
		}
		diskName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volume.Spec.CSI.VolumeHandle)
		if err != nil {
			return err
		}
		azVolumeName := strings.ToLower(diskName)
		pvClaimName := getQualifiedName(volume.Spec.ClaimRef.Namespace, volume.Spec.ClaimRef.Name)
		r.controllerSharedState.volumeToClaimMap.Store(azVolumeName, pvClaimName)
		r.controllerSharedState.claimToVolumeMap.Store(pvClaimName, azVolumeName)
	}
	return nil
}

func NewPVController(mgr manager.Manager, azVolumeClient azVolumeClientSet.Interface, kubeClient kubeClientSet.Interface, namespace string, controllerSharedState *SharedState) (*ReconcilePV, error) {
	logger := mgr.GetLogger().WithValues("controller", "azvolume")
	reconciler := ReconcilePV{
		client:                mgr.GetClient(),
		kubeClient:            kubeClient,
		retryMap:              sync.Map{},
		azVolumeClient:        azVolumeClient,
		namespace:             namespace,
		controllerSharedState: controllerSharedState,
	}

	c, err := controller.New("pv-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		Log:                     logger,
	})

	if err != nil {
		klog.Errorf("Failed to create azvolume controller. Error: (%v)", err)
		return nil, err
	}

	klog.V(2).Info("Starting to watch PV.")

	p := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return true
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
	}

	// Watch for Update events on PV objects
	err = c.Watch(&source.Kind{Type: &corev1.PersistentVolume{}}, &handler.EnqueueRequestForObject{}, p)
	if err != nil {
		klog.Errorf("Failed to watch PV. Error: %v", err)
		return nil, err
	}

	klog.V(2).Info("Controller set-up successful.")
	return &reconciler, nil
}
