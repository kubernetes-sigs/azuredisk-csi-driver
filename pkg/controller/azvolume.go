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

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azVolumeClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	util "sigs.k8s.io/azuredisk-csi-driver/pkg/util"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

//Struct for the reconciler
type reconcileAzVolume struct {
	client           client.Client
	azVolumeClient   azVolumeClientSet.Interface
	namespace        string
	cloudProvisioner CloudProvisioner
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &reconcileAzVolume{}

func (r *reconcileAzVolume) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	var azVolume v1alpha1.AzVolume

	err := r.client.Get(ctx, request.NamespacedName, &azVolume)

	if err != nil {

		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}

		// if the GET failure is triggered by other errors, log it and requeue the request
		klog.Errorf("failed to fetch azvolume object with namespaced name %s: %v", request.NamespacedName, err)
		return reconcile.Result{Requeue: true}, err
	}

	//azVolume creation
	if azVolume.Status == nil {
		if err := r.triggerCreate(ctx, azVolume.Name); err != nil {
			klog.Errorf("failed to create AzVolume (%s): %v", azVolume.Name, err)
			return reconcile.Result{Requeue: true}, err
		}
	} else if now := metav1.Now(); azVolume.ObjectMeta.DeletionTimestamp.Before(&now) {
		// azVolume deletion
		klog.Infof("Beginning deletion of AzVolume object")
		if err := r.triggerDelete(ctx, azVolume.Name); err != nil {
			//If delete failed, requeue request
			return reconcile.Result{Requeue: true}, err
		}
		// azVolume released, so clean up all attachments
	} else if azVolume.Status.Phase == v1alpha1.VolumeReleased {
		klog.Infof("Volume released: Initiating AzVolumeAttachment Clean-up")
		if err := CleanUpAzVolumeAttachment(ctx, r.client, r.azVolumeClient, r.namespace, azVolume.Name); err != nil {
			return reconcile.Result{Requeue: true}, err
		}
		if err := r.UpdateStatus(ctx, azVolume.Name, v1alpha1.VolumeAvailable, false, azVolume.Status.ResponseObject); err != nil {
			klog.Errorf("failed to update status of AzVolume (%s): %v", azVolume.Name, err)
			return reconcile.Result{Requeue: true}, err
		}
	} else if azVolume.Status.ResponseObject != nil && azVolume.Spec.CapacityRange.RequiredBytes != azVolume.Status.ResponseObject.CapacityBytes {
		// azVolume update
		if err := r.triggerUpdate(ctx, azVolume.Name); err != nil {
			klog.Errorf("failed to update AzVolume (%s): %v", azVolume.Name, err)
			return reconcile.Result{Requeue: true}, err
		}
	}

	return reconcile.Result{}, nil
}

func (r *reconcileAzVolume) triggerUpdate(ctx context.Context, volumeName string) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	response, err := r.expandVolume(ctx, &azVolume)
	if err != nil {
		klog.Errorf("failed to update volume %s: %v", azVolume.Spec.UnderlyingVolume, err)
		return r.updateStatusWithError(ctx, azVolume.Name, err)
	}

	// Update status of the object
	if err := r.UpdateStatus(ctx, azVolume.Name, azVolume.Status.Phase, false, response); err != nil {
		return err
	}
	klog.Infof("successfully updated volume (%s)and update status of AzVolume (%s)", azVolume.Spec.UnderlyingVolume, azVolume.Name)
	return nil
}

func (r *reconcileAzVolume) triggerCreate(ctx context.Context, volumeName string) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	//Register finalizer
	if err := r.InitializeMeta(ctx, volumeName); err != nil {
		return err
	}

	response, err := r.createVolume(ctx, &azVolume)
	if err != nil {
		klog.Errorf("failed to create volume %s: %v", azVolume.Spec.UnderlyingVolume, err)
		return r.updateStatusWithError(ctx, azVolume.Name, err)
	}

	// Update status of the object
	if err := r.UpdateStatus(ctx, azVolume.Name, v1alpha1.VolumeBound, false, response); err != nil {
		return err
	}
	klog.Infof("successfully created volume (%s)and update status of AzVolume (%s)", azVolume.Spec.UnderlyingVolume, azVolume.Name)
	return nil
}

func (r *reconcileAzVolume) triggerDelete(ctx context.Context, volumeName string) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}
	// Delete all AzVolumeAttachment objects bound to the deleted AzVolume
	volRequirement, err := labels.NewRequirement(VolumeNameLabel, selection.Equals, []string{azVolume.Spec.UnderlyingVolume})
	if err != nil {
		return err
	}
	if volRequirement == nil {
		return status.Error(codes.Internal, fmt.Sprintf("Unable to create Requirement to for label key : (%s) and label value: (%s)", VolumeNameLabel, azVolume.Spec.UnderlyingVolume))
	}

	labelSelector := labels.NewSelector().Add(*volRequirement)

	attachments, err := r.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil && !errors.IsNotFound(err) {
		klog.Errorf("failed to get AzVolumeAttachments: %v", err)
		return err
	}

	klog.V(5).Infof("number of attachments found: %d", len(attachments.Items))
	for _, attachment := range attachments.Items {
		if err = r.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(r.namespace).Delete(ctx, attachment.Name, metav1.DeleteOptions{}); err != nil {
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", attachment.Name, err)
			return err
		}
		klog.V(5).Infof("Set deletion timestamp for AzVolumeAttachment (%s)", attachment.Name)
	}

	if azVolume.Status.ResponseObject != nil {
		if err := r.deleteVolume(ctx, &azVolume); err != nil {
			klog.Errorf("failed to delete volume %s: %v", azVolume.Spec.UnderlyingVolume, err)
			return r.updateStatusWithError(ctx, azVolume.Name, err)
		}
	}
	// Update status of the object
	if err := r.UpdateStatus(ctx, azVolume.Name, azVolume.Status.Phase, true, nil); err != nil {
		return err
	}

	klog.Infof("successfully deleted volume (%s) and its attachments and update status of AzVolume (%s)", azVolume.Spec.UnderlyingVolume, azVolume.Name)
	return nil
}

func (r *reconcileAzVolume) UpdateStatus(ctx context.Context, volumeName string, phase v1alpha1.AzVolumePhase, isDeleted bool, status *v1alpha1.AzVolumeStatusParams) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	if isDeleted {
		if err := r.DeleteFinalizer(ctx, volumeName); err != nil {
			klog.Errorf("failed to delete finalizer %s for azVolume %s: %v", azureutils.AzVolumeFinalizer, azVolume.Name, err)
			return err
		}
		return nil
	}

	updated := azVolume.DeepCopy()
	if updated.Status == nil {
		if status != nil {
			updated.Status = &v1alpha1.AzVolumeStatus{
				ResponseObject: status,
			}
		}
	} else {
		// Updating status after update operation
		if status != nil {
			if updated.Status.ResponseObject == nil {
				updated.Status.ResponseObject = &v1alpha1.AzVolumeStatusParams{}
			}
			updated.Status.ResponseObject.CapacityBytes = status.CapacityBytes
			updated.Status.ResponseObject.NodeExpansionRequired = status.NodeExpansionRequired
		} else {
			updated.Status.ResponseObject = nil
		}
	}

	updated.Status.Phase = phase

	if err := r.client.Status().Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to update status of AzVolume (%s): %v", volumeName, err)
		return err
	}

	return nil
}

func (r *reconcileAzVolume) updateStatusWithError(ctx context.Context, volumeName string, err error) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	if err != nil {
		azVolumeError := &v1alpha1.AzError{
			ErrorCode:    util.GetStringValueForErrorCode(status.Code(err)),
			ErrorMessage: err.Error(),
		}
		updated := azVolume.DeepCopy()

		if updated.Status == nil {
			updated.Status = &v1alpha1.AzVolumeStatus{
				Error: azVolumeError,
			}
		} else if updated.Status.Error == nil {
			updated.Status.Error = azVolumeError
		} else {
			updated.Status.Error.ErrorCode = azVolumeError.ErrorCode
			updated.Status.Error.ErrorMessage = azVolumeError.ErrorMessage
		}

		if err := r.client.Status().Update(ctx, updated, &client.UpdateOptions{}); err != nil {
			klog.Errorf("failed to update error status of AzVolume (%s): %v", volumeName, err)
			return err
		}
	}
	return nil
}

func (r *reconcileAzVolume) expandVolume(ctx context.Context, azVolume *v1alpha1.AzVolume) (*v1alpha1.AzVolumeStatusParams, error) {
	return r.cloudProvisioner.ExpandVolume(ctx, azVolume.Status.ResponseObject.VolumeID, azVolume.Spec.CapacityRange, azVolume.Spec.Secrets)
}

func (r *reconcileAzVolume) createVolume(ctx context.Context, azVolume *v1alpha1.AzVolume) (*v1alpha1.AzVolumeStatusParams, error) {
	return r.cloudProvisioner.CreateVolume(ctx, azVolume.Spec.UnderlyingVolume, azVolume.Spec.CapacityRange, azVolume.Spec.VolumeCapability, azVolume.Spec.Parameters, azVolume.Spec.Secrets, azVolume.Spec.ContentVolumeSource, azVolume.Spec.AccessibilityRequirements)
}

func (r *reconcileAzVolume) deleteVolume(ctx context.Context, azVolume *v1alpha1.AzVolume) error {
	err := r.cloudProvisioner.DeleteVolume(ctx, azVolume.Status.ResponseObject.VolumeID, azVolume.Spec.Secrets)
	return err
}

func (r *reconcileAzVolume) CleanUpAzVolumeAttachment(ctx context.Context, azVolumeName string) error {
	var azVolume v1alpha1.AzVolume
	err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: azVolumeName}, &azVolume)
	if err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", azVolumeName, err)
		return err
	}

	volRequirement, err := labels.NewRequirement(VolumeNameLabel, selection.Equals, []string{azVolume.Spec.UnderlyingVolume})
	if err != nil {
		return err
	}
	if volRequirement == nil {
		return status.Error(codes.Internal, fmt.Sprintf("Unable to create Requirement to for label key : (%s) and label value: (%s)", VolumeNameLabel, azVolume.Spec.UnderlyingVolume))
	}

	labelSelector := labels.NewSelector().Add(*volRequirement)

	attachments, err := r.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil && !errors.IsNotFound(err) {
		klog.Errorf("failed to get AzVolumeAttachments: %v", err)
		return err
	}

	for _, attachment := range attachments.Items {
		if err = r.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(r.namespace).Delete(ctx, attachment.Name, metav1.DeleteOptions{}); err != nil {
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", attachment.Name, err)
			return err
		}
		klog.V(5).Infof("Set deletion timestamp for AzVolumeAttachment (%s)", attachment.Name)
	}

	var response *v1alpha1.AzVolumeStatusParams
	if azVolume.Status == nil {
		response = azVolume.Status.ResponseObject
	}

	if err := r.UpdateStatus(ctx, azVolumeName, v1alpha1.VolumeReleased, false, response); err != nil {
		klog.Errorf("failed to update status of AzVolume (%s): %v", azVolumeName, err)
		return err
	}

	klog.Infof("successfully deleted AzVolumeAttachments for AzVolume (%s)", azVolume.Name)
	return nil
}

func NewAzVolumeController(mgr manager.Manager, azVolumeClient *azVolumeClientSet.Interface, namespace string, cloudProvisioner CloudProvisioner) error {
	logger := mgr.GetLogger().WithValues("controller", "azvolume")

	c, err := controller.New("azvolume-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconcileAzVolume{client: mgr.GetClient(), azVolumeClient: *azVolumeClient, namespace: namespace, cloudProvisioner: cloudProvisioner},
		Log:                     logger,
	})

	if err != nil {
		klog.Errorf("Failed to create azvolume controller. Error: (%v)", err)
		return err
	}

	klog.V(2).Info("Starting to watch cluster AzVolumes.")

	// Watch for CRUD events on azVolume objects
	err = c.Watch(&source.Kind{Type: &v1alpha1.AzVolume{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		klog.Errorf("Failed to watch AzVolume. Error: %v", err)
		return err
	}

	// Watch for Update events on PV objects
	err = c.Watch(&source.Kind{Type: &corev1.PersistentVolume{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		klog.Errorf("Failed to watch PV. Error: %v", err)
		return err
	}

	klog.V(2).Info("Controller set-up successfull.")
	return nil
}

// Helper functions to check if finalizer exists
func volumeFinalizerExists(azVolume v1alpha1.AzVolume, finalizerName string) bool {
	if azVolume.ObjectMeta.Finalizers != nil {
		for _, finalizer := range azVolume.ObjectMeta.Finalizers {
			if finalizer == finalizerName {
				return true
			}
		}
	}
	return false
}

func (r *reconcileAzVolume) InitializeMeta(ctx context.Context, volumeName string) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	if volumeFinalizerExists(azVolume, azureutils.AzVolumeFinalizer) {
		return nil
	}

	patched := azVolume.DeepCopy()

	// add finalizer
	if patched.ObjectMeta.Finalizers == nil {
		patched.ObjectMeta.Finalizers = []string{}
	}

	if !volumeFinalizerExists(azVolume, azureutils.AzVolumeFinalizer) {
		patched.ObjectMeta.Finalizers = append(patched.ObjectMeta.Finalizers, azureutils.AzVolumeFinalizer)
	}

	if err := r.client.Patch(ctx, patched, client.MergeFrom(&azVolume)); err != nil {
		klog.Errorf("failed to initialize finalizer (%s) for AzVolume (%s): %v", azureutils.AzVolumeFinalizer, patched.Name, err)
		return err
	}

	klog.Infof("successfully added finalizer (%s) to AzVolume (%s)", azureutils.AzVolumeFinalizer, volumeName)
	return nil
}

func (r *reconcileAzVolume) DeleteFinalizer(ctx context.Context, volumeName string) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}
	updated := azVolume.DeepCopy()
	if updated.ObjectMeta.Finalizers == nil {
		return nil
	}

	finalizers := []string{}
	for _, finalizer := range updated.ObjectMeta.Finalizers {
		if finalizer == azureutils.AzVolumeFinalizer {
			continue
		}
		finalizers = append(finalizers, finalizer)
	}
	updated.ObjectMeta.Finalizers = finalizers
	if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to delete finalizer (%s) for AzVolume (%s): %v", azureutils.AzVolumeFinalizer, updated.Name, err)
		return err
	}
	klog.Infof("successfully deleted finalizer (%s) from AzVolume (%s)", azureutils.AzVolumeFinalizer, volumeName)
	return nil
}
