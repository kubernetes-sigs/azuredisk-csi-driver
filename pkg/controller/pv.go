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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/kubernetes/pkg/features"
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
type ReconcilePV struct {
	*SharedState
	logger logr.Logger
	// retryMap allows volumeAttachment controller to retry Get operation for AzVolume in case the CRI has not been created yet
	controllerRetryInfo *retryInfo
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &ReconcilePV{}

func (r *ReconcilePV) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if !r.isRecoveryComplete() {
		return reconcile.Result{Requeue: true}, nil
	}

	logger := r.logger.WithValues(consts.PvNameKey, request.Name)
	ctx = logr.NewContext(ctx, logger)

	var pv corev1.PersistentVolume
	var azVolume azdiskv1beta2.AzVolume
	var diskName string
	var err error

	// Ignore not found errors as they cannot be fixed by a requeue
	if err := r.cachedClient.Get(ctx, request.NamespacedName, &pv); err != nil {
		if errors.IsNotFound(err) {
			r.deletePV(request.Name)
			return reconcileReturnOnSuccess(request.Name, r.controllerRetryInfo)
		}
		logger.Error(err, "failed to get PV")
		return reconcileReturnOnError(ctx, &pv, "get", err, r.controllerRetryInfo)
	}

	// migration case
	if utilfeature.DefaultFeatureGate.Enabled(features.CSIMigration) &&
		utilfeature.DefaultFeatureGate.Enabled(features.CSIMigrationAzureDisk) &&
		pv.Spec.AzureDisk != nil {
		diskName = pv.Spec.AzureDisk.DiskName
	} else {
		// ignore PV-s for non-csi volumes
		if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != r.config.DriverName {
			return reconcileReturnOnSuccess(pv.Name, r.controllerRetryInfo)
		}
		diskName, err = azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
		// ignoring cases when we can't get the disk name from volumehandle as this error will not be fixed with a requeue
		if err != nil {
			logger.Error(err, "failed to extract proper diskName from PV's volume handle (%s)", pv.Spec.CSI.VolumeHandle)
			return reconcileReturnOnError(ctx, &pv, "get", err, r.controllerRetryInfo)
		}
	}

	// get the AzVolume name for the PV
	azVolumeName := strings.ToLower(diskName)
	logger = logger.WithValues(consts.VolumeNameLabel, azVolumeName)
	ctx = logr.NewContext(ctx, logger)

	// PV is deleted
	if deleteRequested, deleteAfter := objectDeletionRequested(&pv); deleteRequested {
		if deleteAfter > 0 {
			return reconcileAfter(deleteAfter, request.Name, r.controllerRetryInfo)
		}
		if err := r.cachedClient.Get(ctx, types.NamespacedName{Namespace: r.config.ObjectNamespace, Name: azVolumeName}, &azVolume); err != nil {
			// AzVolume doesn't exist, so there is nothing for us to do
			if errors.IsNotFound(err) {
				return reconcileReturnOnSuccess(pv.Name, r.controllerRetryInfo)
			}
			// getting AzVolume failed, for unknown reason, we requeue and retry deleting it on the next cycle
			logger.Error(err, "failed to get AzVolume")
			return reconcileReturnOnError(ctx, &pv, "get", err, r.controllerRetryInfo)

		}
		// AzVolume does exist and needs to be deleted
		// add annotation to mark AzVolumeAttachment cleanup
		updateFunc := func(obj client.Object) error {
			azv := obj.(*azdiskv1beta2.AzVolume)
			azv.Status.Annotations = azureutils.AddToMap(azv.Status.Annotations, consts.PreProvisionedVolumeCleanupAnnotation, "true")
			return nil
		}
		if _, err := azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, &azVolume, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
			return reconcileReturnOnError(ctx, &pv, "delete", err, r.controllerRetryInfo)
		}

		if err := r.azClient.DiskV1beta2().AzVolumes(r.config.ObjectNamespace).Delete(ctx, azVolumeName, metav1.DeleteOptions{}); err != nil {
			logger.Error(err, "failed to set the deletion timestamp for AzVolume")
			return reconcileReturnOnError(ctx, &pv, "delete", err, r.controllerRetryInfo)
		}
		// deletion timestamp is set and AzVolume reconcliler will handle the delete request.
		// For pre-provisioned case when only PV is deleted, we will only be deleting the CRI as VolumeDeleteRequestAnnotation will not be set.
		// The volume itself will not be deleted.
		logger.Info("Deletion timestamp for AzVolume set")
		return reconcileReturnOnSuccess(pv.Name, r.controllerRetryInfo)
	}

	// PV exists but AzVolume doesn't
	if err := r.cachedClient.Get(ctx, types.NamespacedName{Namespace: r.config.ObjectNamespace, Name: azVolumeName}, &azVolume); err != nil {
		// if getting AzVolume failed due to errors other than it doesn't exist, we requeue and retry
		if !errors.IsNotFound(err) {
			logger.Error(err, "failed to get AzVolume")
			return reconcileReturnOnError(ctx, &pv, "get", err, r.controllerRetryInfo)
		}
		// when underlying PV was found but AzVolume was not, then this is a pre-provisioned volume case and
		// we need to create the AzVolume
		annotation := map[string]string{
			consts.PreProvisionedVolumeAnnotation: "true",
		}
		if err := r.createAzVolumeFromPv(ctx, pv, annotation); err != nil {
			if !errors.IsAlreadyExists(err) {
				// if creating AzVolume failed, retry with exponential back off
				logger.Error(err, "failed to create AzVolume")
				return reconcileReturnOnError(ctx, &pv, "create", err, r.controllerRetryInfo)
			}
		}
		return reconcileReturnOnSuccess(pv.Name, r.controllerRetryInfo)
	}

	var azVolumeUpdateFunc azureutils.UpdateCRIFunc

	if azVolume.Spec.PersistentVolume != pv.Name {
		azVolumeUpdateFunc = func(obj client.Object) error {
			azVolume := obj.(*azdiskv1beta2.AzVolume)
			azVolume.Spec.PersistentVolume = pv.Name
			return nil
		}
	}

	// if AzVolume's PVC Labels are not up to date, update them
	pvcName, pvcLabelExists := azureutils.GetFromMap(azVolume.Labels, consts.PvcNameLabel)
	pvcNamespace, pvcNamespaceLabelExists := azureutils.GetFromMap(azVolume.Labels, consts.PvcNamespaceLabel)
	if pv.Spec.ClaimRef != nil {
		if !pvcLabelExists || pv.Spec.ClaimRef.Name != pvcName || !pvcNamespaceLabelExists || pv.Spec.ClaimRef.Namespace != pvcNamespace {
			azVolumeUpdateFunc = azureutils.AppendToUpdateCRIFunc(azVolumeUpdateFunc, func(obj client.Object) error {
				azv := obj.(*azdiskv1beta2.AzVolume)
				azv.Labels = azureutils.AddToMap(azv.Labels, consts.PvcNameLabel, pv.Spec.ClaimRef.Name, consts.PvcNamespaceLabel, pv.Spec.ClaimRef.Namespace)
				return nil
			})
		}
	} else {
		if pvcLabelExists || pvcNamespaceLabelExists {
			azVolumeUpdateFunc = azureutils.AppendToUpdateCRIFunc(azVolumeUpdateFunc, func(obj client.Object) error {
				azv := obj.(*azdiskv1beta2.AzVolume)
				delete(azv.Labels, consts.PvcNameLabel)
				delete(azv.Labels, consts.PvcNamespaceLabel)
				return nil
			})
		}
	}

	if azVolumeUpdateFunc != nil {
		_, err := azureutils.UpdateCRIWithRetry(ctx, nil, r.cachedClient, r.azClient, &azVolume, azVolumeUpdateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRI)
		if err != nil {
			return reconcileReturnOnError(ctx, &pv, "update", err, r.controllerRetryInfo)
		}
	}

	// both PV and AzVolume exist. Remove entry from retryMap and retryLocks
	r.controllerRetryInfo.deleteEntry(azVolumeName)

	switch phase := pv.Status.Phase; phase {
	case corev1.VolumeBound:
		pvClaimName := getQualifiedName(pv.Spec.ClaimRef.Namespace, pv.Spec.ClaimRef.Name)
		r.addVolumeAndClaim(azVolumeName, pv.Name, pvClaimName)
	case corev1.VolumeReleased:
		if err := r.triggerRelease(ctx, &azVolume); err != nil {
			logger.Error(err, "failed to release AzVolume")
			return reconcileReturnOnError(ctx, &pv, "release", err, r.controllerRetryInfo)
		}
		r.deleteVolumeAndClaim(azVolumeName)
	}

	return reconcileReturnOnSuccess(pv.Name, r.controllerRetryInfo)
}

//nolint:contextcheck // Garbage collection is asynchronous; context is not inherited by design
func (r *ReconcilePV) triggerRelease(ctx context.Context, azVolume *azdiskv1beta2.AzVolume) error {
	gcCtx, w := workflow.New(context.Background(), workflow.WithDetails(consts.VolumeNameLabel, azVolume.Spec.VolumeName))
	defer w.Finish(nil)
	r.garbageCollectReplicas(gcCtx, azVolume.Name, pv)
	return nil
}

func (r *ReconcilePV) Recover(ctx context.Context) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	var pvs *v1.PersistentVolumeList
	pvs, err = r.kubeClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	for _, pv := range pvs.Items {
		if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != r.config.DriverName || pv.Spec.ClaimRef == nil {
			continue
		}

		var diskName string
		diskName, err = azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
		if err != nil {
			return err
		}
		azVolumeName := strings.ToLower(diskName)
		pvClaimName := getQualifiedName(pv.Spec.ClaimRef.Namespace, pv.Spec.ClaimRef.Name)
		r.addVolumeAndClaim(azVolumeName, pv.Name, pvClaimName)
	}
	return nil
}

func NewPVController(mgr manager.Manager, controllerSharedState *SharedState) (*ReconcilePV, error) {
	logger := mgr.GetLogger().WithValues("controller", "pv")
	reconciler := ReconcilePV{
		controllerRetryInfo: newRetryInfo(),
		SharedState:         controllerSharedState,
		logger:              logger,
	}

	c, err := controller.New("pv-controller", mgr, controller.Options{
		MaxConcurrentReconciles: consts.DefaultWorkerThreads,
		Reconciler:              &reconciler,
		LogConstructor:          func(req *reconcile.Request) logr.Logger { return logger },
	})

	if err != nil {
		logger.Error(err, "failed to create controller")
		return nil, err
	}

	logger.V(2).Info("Starting to watch PV.")

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
		logger.Error(err, "failed to initialize watch for PV")
		return nil, err
	}

	logger.V(2).Info("Controller set-up successful.")
	return &reconciler, nil
}
