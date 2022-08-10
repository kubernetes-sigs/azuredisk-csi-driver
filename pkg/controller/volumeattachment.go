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

	"github.com/go-logr/logr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/kubernetes/pkg/features"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// Struct for the reconciler
type ReconcileVolumeAttachment struct {
	logger              logr.Logger
	controllerRetryInfo *retryInfo
	*sharedState
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &ReconcileVolumeAttachment{}

func (r *ReconcileVolumeAttachment) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	if !r.isRecoveryComplete() {
		return reconcile.Result{Requeue: true}, nil
	}

	var volumeAttachment storagev1.VolumeAttachment
	if err := r.cachedClient.Get(ctx, request.NamespacedName, &volumeAttachment); err != nil {
		if errors.IsNotFound(err) {
			return reconcileReturnOnSuccess(request.Name, r.controllerRetryInfo)
		}
		return reconcileReturnOnError(ctx, &volumeAttachment, "get", err, r.controllerRetryInfo)
	}

	var diskName string
	if utilfeature.DefaultFeatureGate.Enabled(features.CSIMigration) &&
		utilfeature.DefaultFeatureGate.Enabled(features.CSIMigrationAzureDisk) &&
		volumeAttachment.Spec.Source.InlineVolumeSpec != nil {
		if volumeAttachment.Spec.Source.InlineVolumeSpec.AzureDisk != nil {
			diskName = volumeAttachment.Spec.Source.InlineVolumeSpec.AzureDisk.DiskName
		} else if volumeAttachment.Spec.Source.InlineVolumeSpec.CSI != nil {
			var err error
			if diskName, err = azureutils.GetDiskName(volumeAttachment.Spec.Source.InlineVolumeSpec.CSI.VolumeHandle); err != nil {
				return reconcileReturnOnError(ctx, &volumeAttachment, "get disk name", err, r.controllerRetryInfo)
			}
		} else {
			return reconcile.Result{}, nil
		}
	} else if pv := volumeAttachment.Spec.Source.PersistentVolumeName; pv != nil {
		val, ok := r.pvToVolumeMap.Load(*pv)
		if !ok {
			return reconcileReturnOnError(ctx, &volumeAttachment, "get disk name", status.Errorf(codes.Internal, "failed to find disk name for the provided pv"), r.controllerRetryInfo)
		}
		diskName = val.(string)
	}

	azVolumeAttachmentName := azureutils.GetAzVolumeAttachmentName(diskName, volumeAttachment.Spec.NodeName)
	r.azVolumeAttachmentToVaMap.Store(azVolumeAttachmentName, volumeAttachment.Name)

	return reconcile.Result{}, nil
}

func NewVolumeAttachmentController(mgr manager.Manager, controllerSharedState *sharedState) (*ReconcileVolumeAttachment, error) {
	logger := mgr.GetLogger().WithValues("controller", "volumeattachment")
	reconciler := ReconcileVolumeAttachment{
		controllerRetryInfo: newRetryInfo(),
		sharedState:         controllerSharedState,
		logger:              logger,
	}

	c, err := controller.New("va-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		LogConstructor:          func(req *reconcile.Request) logr.Logger { return logger },
	})

	if err != nil {
		logger.Error(err, "failed to create controller")
		return nil, err
	}

	logger.V(2).Info("Starting to watch VolumeAttachment.")

	p := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return false
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			old, oldOk := e.ObjectOld.(*storagev1.VolumeAttachment)
			new, newOk := e.ObjectNew.(*storagev1.VolumeAttachment)
			if !newOk {
				return false
			}
			if !oldOk {
				return new.Status.Attached
			}
			return !old.Status.Attached && new.Status.Attached
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false
		},
	}

	// Watch for Update events on VolumeAttachment objects
	err = c.Watch(&source.Kind{Type: &storagev1.VolumeAttachment{}}, &handler.EnqueueRequestForObject{}, p)
	if err != nil {
		logger.Error(err, "failed to initialize watch for volumeAttachment")
		return nil, err
	}

	logger.V(2).Info("Controller set-up successful.")
	return &reconciler, nil
}
