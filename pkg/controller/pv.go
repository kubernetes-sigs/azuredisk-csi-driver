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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	diskv1alpha2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha2"
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

//Struct for the reconciler
type ReconcilePV struct {
	// retryMap allows volumeAttachment controller to retry Get operation for AzVolume in case the CRI has not been created yet
	controllerRetryInfo   *retryInfo
	controllerSharedState *SharedState
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &ReconcilePV{}

func (r *ReconcilePV) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if !r.controllerSharedState.isRecoveryComplete() {
		return reconcile.Result{Requeue: true}, nil
	}

	var pv corev1.PersistentVolume
	var azVolume diskv1alpha2.AzVolume
	// Ignore not found errors as they cannot be fixed by a requeue
	if err := r.controllerSharedState.cachedClient.Get(ctx, request.NamespacedName, &pv); err != nil {
		if errors.IsNotFound(err) {
			return reconcileReturnOnSuccess(request.Name, r.controllerRetryInfo)
		}
		klog.Errorf("failed to get PV (%s): %v", request.Name, err)
		return reconcileReturnOnError(&pv, "get", err, r.controllerRetryInfo)
	}

	// ignore PV-s for non-csi volumes
	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != r.controllerSharedState.driverName {
		return reconcileReturnOnSuccess(pv.Name, r.controllerRetryInfo)
	}

	// get the AzVolume name for the PV
	diskName, err := azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
	azVolumeName := strings.ToLower(diskName)
	// ignoring cases when we can't get the disk name from volumehandle as this error will not be fixed with a requeue
	if err != nil {
		klog.Errorf("failed to extract proper diskName from pv(%s)'s volume handle (%s): %v", pv.Name, pv.Spec.CSI.VolumeHandle, err)
		return reconcileReturnOnError(&pv, "get", err, r.controllerRetryInfo)
	}

	// PV is deleted
	if objectDeletionRequested(&pv) {
		if err := r.controllerSharedState.cachedClient.Get(ctx, types.NamespacedName{Namespace: r.controllerSharedState.objectNamespace, Name: azVolumeName}, &azVolume); err != nil {
			// AzVolume doesn't exist, so there is nothing for us to do
			if errors.IsNotFound(err) {
				return reconcileReturnOnSuccess(pv.Name, r.controllerRetryInfo)
			}
			// getting AzVolume failed, for unknown reason, we requeue and retry deleting it on the next cycle
			klog.Infof("Failed to get AzVolume (%s). Err: %v. ", azVolumeName, err)
			return reconcileReturnOnError(&pv, "get", err, r.controllerRetryInfo)

		}
		// AzVolume does exist and needs to be deleted
		// add annotation to mark AzVolumeAttachment cleanup
		updateFunc := func(obj interface{}) error {
			azv := obj.(*diskv1alpha2.AzVolume)
			if azv.Annotations == nil {
				azv.Annotations = make(map[string]string, 1)
			}
			azv.Annotations[consts.PreProvisionedVolumeCleanupAnnotation] = "true"
			return nil
		}
		if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.controllerSharedState.cachedClient, r.controllerSharedState.azClient, &azVolume, updateFunc, consts.NormalUpdateMaxNetRetry); err != nil {
			return reconcileReturnOnError(&pv, "delete", err, r.controllerRetryInfo)
		}

		if err := r.controllerSharedState.azClient.DiskV1alpha2().AzVolumes(r.controllerSharedState.objectNamespace).Delete(ctx, azVolumeName, metav1.DeleteOptions{}); err != nil {
			klog.Errorf("failed to set the deletion timestamp for AzVolume (%s): %v", azVolumeName, err)
			return reconcileReturnOnError(&pv, "delete", err, r.controllerRetryInfo)
		}
		// deletion timestamp is set and AzVolume reconcliler will handle the delete request.
		// For pre-provisioned case when only PV is deleted, we will only be deleting the CRI as VolumeDeleteRequestAnnotation will not be set.
		// The volume itself will not be deleted.
		klog.Infof("deletion timestamp for AzVolume (%s) is set.", azVolumeName)
		return reconcileReturnOnSuccess(pv.Name, r.controllerRetryInfo)
	}

	// PV exists but AzVolume doesn't
	if err := r.controllerSharedState.cachedClient.Get(ctx, types.NamespacedName{Namespace: r.controllerSharedState.objectNamespace, Name: azVolumeName}, &azVolume); err != nil {
		// if getting AzVolume failed due to errors other than it doesn't exist, we requeue and retry
		if !errors.IsNotFound(err) {
			klog.V(5).Infof("failed to get AzVolume (%s): %v", diskName, err)
			return reconcileReturnOnError(&pv, "get", err, r.controllerRetryInfo)
		}
		// when underlying PV was found but AzVolume was not, then this is a pre-provisioned volume case and
		// we need to create the AzVolume
		annotation := map[string]string{
			consts.PreProvisionedVolumeAnnotation: "true",
		}
		if err := r.controllerSharedState.createAzVolumeFromPv(ctx, pv, annotation); err != nil {
			if !errors.IsAlreadyExists(err) {
				// if creating AzVolume failed, retry with exponential back off
				klog.Infof("Failed to create AzVolume (%s). Err: %v.", azVolumeName, err)
				return reconcileReturnOnError(&pv, "create", err, r.controllerRetryInfo)
			}
		}
		return reconcileReturnOnSuccess(pv.Name, r.controllerRetryInfo)
	}

	// both PV and AzVolume exist. Remove entry from retryMap and retryLocks
	r.controllerRetryInfo.deleteEntry(azVolumeName)

	switch phase := pv.Status.Phase; phase {
	case corev1.VolumeBound:
		pvClaimName := getQualifiedName(pv.Spec.ClaimRef.Namespace, pv.Spec.ClaimRef.Name)
		r.controllerSharedState.addVolumeAndClaim(azVolumeName, pvClaimName)
	case corev1.VolumeReleased:
		if err := r.triggerRelease(ctx, &azVolume); err != nil {
			klog.Errorf("failed to release AzVolume (%s): %v", azVolume.Name, err)
			return reconcileReturnOnError(&pv, "release", err, r.controllerRetryInfo)
		}
		r.controllerSharedState.deleteVolumeAndClaim(azVolumeName)
	}

	return reconcileReturnOnSuccess(pv.Name, r.controllerRetryInfo)
}

func (r *ReconcilePV) triggerRelease(ctx context.Context, azVolume *diskv1alpha2.AzVolume) error {
	klog.Infof("Volume released: Initiating AzVolumeAttachment Clean-up")

	if _, err := r.controllerSharedState.cleanUpAzVolumeAttachmentByVolume(ctx, azVolume.Name, "pv controller", replicaOnly, detachAndDeleteCRI); err != nil {
		return err
	}

	return nil
}

func (r *ReconcilePV) Recover(ctx context.Context) error {
	volumes, err := r.controllerSharedState.kubeClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, volume := range volumes.Items {
		if volume.Spec.CSI == nil || volume.Spec.CSI.Driver != r.controllerSharedState.driverName || volume.Spec.ClaimRef == nil {
			continue
		}
		diskName, err := azureutils.GetDiskName(volume.Spec.CSI.VolumeHandle)
		if err != nil {
			return err
		}
		azVolumeName := strings.ToLower(diskName)
		pvClaimName := getQualifiedName(volume.Spec.ClaimRef.Namespace, volume.Spec.ClaimRef.Name)
		r.controllerSharedState.addVolumeAndClaim(azVolumeName, pvClaimName)
	}
	return nil
}

func NewPVController(mgr manager.Manager, controllerSharedState *SharedState) (*ReconcilePV, error) {
	logger := mgr.GetLogger().WithValues("controller", "azvolume")
	reconciler := ReconcilePV{
		controllerRetryInfo:   newRetryInfo(),
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
			return true
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
