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
	"strings"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	kubeClientSet "k8s.io/client-go/kubernetes"
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
type ReconcileAzVolume struct {
	client         client.Client
	azVolumeClient azVolumeClientSet.Interface
	kubeClient     kubeClientSet.Interface
	// muteMap maps volume name to mutex, it is used to guarantee that only one sync call is made at a time per volume
	mutexMap map[string]*sync.Mutex
	// muteMapMutex is used when updating or reading the mutexMap
	mutexMapMutex    sync.RWMutex
	namespace        string
	cloudProvisioner CloudProvisioner
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &ReconcileAzVolume{}

func (r *ReconcileAzVolume) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	azVolume, err := azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, request.Name, request.Namespace, true)

	if err != nil {
		if errors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}

		// if the GET failure is triggered by other errors, log it and requeue the request
		klog.Errorf("failed to fetch azvolume object with namespaced name %s: %v", request.NamespacedName, err)
		return reconcile.Result{Requeue: true}, err
	}

	if now := metav1.Now(); azVolume.ObjectMeta.DeletionTimestamp.Before(&now) {
		// azVolume deletion
		klog.Infof("Beginning deletion of AzVolume object")
		if err := r.triggerDelete(ctx, azVolume.Name); err != nil {
			//If delete failed, requeue request
			return reconcile.Result{Requeue: true}, err
		}
		//azVolume creation
	} else if azVolume.Status.Detail == nil {
		if azVolume.Status.Error == nil {
			klog.Infof("Creating Volume (%s)...", azVolume.Spec.UnderlyingVolume)
			if err := r.triggerCreate(ctx, azVolume.Name); err != nil {
				klog.Errorf("failed to create AzVolume (%s): %v", azVolume.Name, err)
				return reconcile.Result{Requeue: true}, err
			}
		}
		// azVolume released, so clean up replica Attachments (primary Attachment should be deleted via UnpublishVolume request to avoid race condition)
	} else if azVolume.Status.Detail.Phase == v1alpha1.VolumeReleased {
		klog.Infof("Volume released: Initiating AzVolumeAttachment Clean-up")
		if err := cleanUpAzVolumeAttachmentByVolume(ctx, r.client, r.azVolumeClient, r.namespace, azVolume.Name, replicaOnly); err != nil {
			return reconcile.Result{Requeue: true}, err
		}
		if _, err := r.updateStatus(ctx, azVolume.Name, nil, v1alpha1.VolumeAvailable, false, azVolume.Status.Detail.ResponseObject); err != nil {
			klog.Errorf("failed to update status of AzVolume (%s): %v", azVolume.Name, err)
			return reconcile.Result{Requeue: true}, err
		}
	} else if azVolume.Status.Detail.ResponseObject != nil && azVolume.Spec.CapacityRange.RequiredBytes != azVolume.Status.Detail.ResponseObject.CapacityBytes {
		// azVolume update
		klog.Infof("Updating Volume (%s)...", azVolume.Spec.UnderlyingVolume)
		if err := r.triggerUpdate(ctx, azVolume.Name, true); err != nil {
			klog.Errorf("failed to update AzVolume (%s): %v", azVolume.Name, err)
			return reconcile.Result{Requeue: true}, err
		}
	}

	return reconcile.Result{}, nil
}

func (r *ReconcileAzVolume) triggerUpdate(ctx context.Context, volumeName string, useCache bool) error {
	azVolume, err := azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volumeName, r.namespace, useCache)
	if err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	response, err := r.expandVolume(ctx, azVolume)
	if err != nil {
		klog.Errorf("failed to update volume %s: %v", azVolume.Spec.UnderlyingVolume, err)
		_, err = r.updateStatusWithError(ctx, azVolume.Name, azVolume, err)
		return err
	}

	// Update status of the object
	if _, err = r.updateStatus(ctx, azVolume.Name, azVolume, azVolume.Status.Detail.Phase, false, response); err != nil {
		return err
	}
	klog.Infof("successfully updated volume (%s)and update status of AzVolume (%s)", azVolume.Spec.UnderlyingVolume, azVolume.Name)
	return nil
}

func (r *ReconcileAzVolume) triggerCreate(ctx context.Context, volumeName string) error {
	azVolume, err := azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volumeName, r.namespace, true)
	if err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}
	var response *v1alpha1.AzVolumeStatusParams

	if azVolume.Status.State == v1alpha1.VolumeOperationPending || azVolume.Status.State == v1alpha1.VolumeCreating || azVolume.Status.State == v1alpha1.VolumeCreated {
		if azVolume.Status.State == v1alpha1.VolumeOperationPending {
			if azVolume, err = r.updateState(ctx, volumeName, azVolume, v1alpha1.VolumeCreating); err != nil {
				return err
			}
		}

		response, err = r.createVolume(ctx, azVolume)
		if err != nil {
			klog.Errorf("failed to create volume %s: %v", azVolume.Spec.UnderlyingVolume, err)
			var derr error
			if azVolume, derr = r.updateState(ctx, volumeName, azVolume, v1alpha1.VolumeCreationFailed); derr != nil {
				return derr
			}
			_, err = r.updateStatusWithError(ctx, volumeName, azVolume, err)
			return err
		}

		//Register finalizer
		if azVolume, err = r.initializeMeta(ctx, volumeName, azVolume, true); err != nil {
			return err
		}

		if azVolume, err = r.updateState(ctx, volumeName, azVolume, v1alpha1.VolumeCreated); err != nil {
			return err
		}
		// Update status of the object
		if azVolume, err = r.updateStatus(ctx, volumeName, azVolume, v1alpha1.VolumeBound, false, response); err != nil {
			return err
		}
		klog.Infof("successfully created volume (%s)and update status of AzVolume (%s)", azVolume.Spec.UnderlyingVolume, azVolume.Name)
	}
	return nil
}

func (r *ReconcileAzVolume) triggerDelete(ctx context.Context, volumeName string) error {
	azVolume, err := azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volumeName, r.namespace, true)
	if err != nil {
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
		if err = r.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(r.namespace).Delete(ctx, attachment.Name, metav1.DeleteOptions{}); err != nil && !errors.IsNotFound(err) {
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", attachment.Name, err)
			return err
		}
		klog.V(5).Infof("Set deletion timestamp for AzVolumeAttachment (%s)", attachment.Name)
	}

	// only try deleting underlying volume 1) if volume creation was successful and 2) volumeDeleteRequestAnnotation is present
	// if the annotation is not present, only delete the CRI and not the underlying volume
	if azVolume.Annotations != nil {
		if _, ok := azVolume.Annotations[azureutils.VolumeDeleteRequestAnnotation]; ok &&
			(azVolume.Status.State == v1alpha1.VolumeCreated || azVolume.Status.State == v1alpha1.VolumeCreationFailed || azVolume.Status.State == v1alpha1.VolumeDeleting) {
			if azVolume, err = r.updateState(ctx, volumeName, azVolume, v1alpha1.VolumeDeleting); err != nil {
				return err
			}
			if err := r.deleteVolume(ctx, azVolume); err != nil {
				klog.Errorf("failed to delete volume %s: %v", azVolume.Spec.UnderlyingVolume, err)
				var derr error
				if azVolume, derr = r.updateState(ctx, volumeName, azVolume, v1alpha1.VolumeDeletionFailed); derr != nil {
					return derr
				}
				_, err = r.updateStatusWithError(ctx, azVolume.Name, azVolume, err)
				return err
			}
			if azVolume, err = r.updateState(ctx, volumeName, azVolume, v1alpha1.VolumeDeleted); err != nil {
				return err
			}
		}
	}

	// if the volume is in the process of being deleted or the deletion has failed, return
	if azVolume.Status.State == v1alpha1.VolumeDeleting || azVolume.Status.State == v1alpha1.VolumeDeletionFailed {
		return nil
	}

	// Update status of the object
	if _, err = r.updateStatus(ctx, azVolume.Name, azVolume, azVolume.Status.Detail.Phase, true, nil); err != nil {
		return err
	}

	klog.Infof("successfully deleted volume (%s) and its attachments and update status of AzVolume (%s)", azVolume.Spec.UnderlyingVolume, azVolume.Name)
	return nil
}

func (r *ReconcileAzVolume) updateState(ctx context.Context, volumeName string, azVolume *v1alpha1.AzVolume, state v1alpha1.AzVolumeState) (*v1alpha1.AzVolume, error) {
	var err error
	if azVolume == nil {
		azVolume, err = azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volumeName, r.namespace, true)
		if err != nil {
			klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
			return nil, err
		}

	}

	updated := azVolume.DeepCopy()
	if updated.Status.State == state {
		return azVolume, nil
	}
	updated.Status.State = state

	if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to update AzVolume (%s): %v", volumeName, err)
		return nil, err
	}
	return updated, nil
}

func (r *ReconcileAzVolume) updateStatus(ctx context.Context, volumeName string, azVolume *v1alpha1.AzVolume, phase v1alpha1.AzVolumePhase, isDeleted bool, status *v1alpha1.AzVolumeStatusParams) (*v1alpha1.AzVolume, error) {
	var err error
	if azVolume == nil {
		azVolume, err = azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volumeName, r.namespace, true)
		if err != nil {
			klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
			return azVolume, err
		}
	}

	if isDeleted {
		if azVolume, err = r.deleteFinalizer(ctx, volumeName, azVolume, map[string]bool{azureutils.AzVolumeFinalizer: true}, true); err != nil {
			klog.Errorf("failed to delete finalizer (%s) for AzVolume (%s): %v", azureutils.AzVolumeFinalizer, volumeName, err)
			return nil, err
		}
		return azVolume, nil
	}

	updated := azVolume.DeepCopy()
	if updated.Status.Detail == nil {
		if status != nil {
			updated.Status.Detail = &v1alpha1.AzVolumeStatusDetail{
				ResponseObject: status,
			}
		}
	} else {
		// Updating status after update operation
		if status != nil {
			if updated.Status.Detail.ResponseObject == nil {
				updated.Status.Detail.ResponseObject = &v1alpha1.AzVolumeStatusParams{}
			}
			updated.Status.Detail.ResponseObject.CapacityBytes = status.CapacityBytes
			updated.Status.Detail.ResponseObject.NodeExpansionRequired = status.NodeExpansionRequired
		} else {
			updated.Status.Detail.ResponseObject = nil
		}
	}

	updated.Status.Detail.Phase = phase

	if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to update status of AzVolume (%s): %v", volumeName, err)
		return nil, err
	}

	return updated, nil
}

func (r *ReconcileAzVolume) updateStatusWithError(ctx context.Context, volumeName string, azVolume *v1alpha1.AzVolume, err error) (*v1alpha1.AzVolume, error) {
	if azVolume == nil {
		var derr error
		azVolume, derr = azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volumeName, r.namespace, true)
		if derr != nil {
			klog.Errorf("failed to get AzVolume (%s): %v", volumeName, derr)
			return nil, derr
		}
	}

	updated := azVolume.DeepCopy()
	if err != nil {
		azVolumeError := &v1alpha1.AzError{
			ErrorCode:    util.GetStringValueForErrorCode(status.Code(err)),
			ErrorMessage: err.Error(),
		}
		updated.Status.Error = azVolumeError

		if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
			klog.Errorf("failed to update error status of AzVolume (%s): %v", volumeName, err)
			return nil, err
		}
	}
	return updated, nil
}

func (r *ReconcileAzVolume) expandVolume(ctx context.Context, azVolume *v1alpha1.AzVolume) (*v1alpha1.AzVolumeStatusParams, error) {
	if azVolume.Status.Detail == nil || azVolume.Status.Detail.ResponseObject == nil {
		err := status.Error(codes.Internal, fmt.Sprintf("Disk for expansion does not exist for AzVolume (%s).", azVolume.Name))
		klog.Errorf("skipping expandVolume operation for AzVolume (%s): %v", azVolume.Name, err)
		return nil, err
	}
	// use deep-copied version of the azVolume CRI to prevent any unwanted update to the object
	copied := azVolume.DeepCopy()
	return r.cloudProvisioner.ExpandVolume(ctx, copied.Status.Detail.ResponseObject.VolumeID, copied.Spec.CapacityRange, copied.Spec.Secrets)
}

func (r *ReconcileAzVolume) createVolume(ctx context.Context, azVolume *v1alpha1.AzVolume) (*v1alpha1.AzVolumeStatusParams, error) {
	// use deep-copied version of the azVolume CRI to prevent any unwanted update to the object
	copied := azVolume.DeepCopy()
	return r.cloudProvisioner.CreateVolume(ctx, copied.Spec.UnderlyingVolume, copied.Spec.CapacityRange, copied.Spec.VolumeCapability, copied.Spec.Parameters, copied.Spec.Secrets, copied.Spec.ContentVolumeSource, copied.Spec.AccessibilityRequirements)
}

func (r *ReconcileAzVolume) deleteVolume(ctx context.Context, azVolume *v1alpha1.AzVolume) error {
	if azVolume.Status.Detail == nil || azVolume.Status.Detail.ResponseObject == nil {
		klog.Infof("skipping deleteVolume operation for AzVolume (%s): No volume to delete for AzVolume (%s)", azVolume.Name, azVolume.Name)
		return nil
	}
	// use deep-copied version of the azVolume CRI to prevent any unwanted update to the object
	copied := azVolume.DeepCopy()
	err := r.cloudProvisioner.DeleteVolume(ctx, copied.Status.Detail.ResponseObject.VolumeID, copied.Spec.Secrets)
	return err
}

func (r *ReconcileAzVolume) recoverAzVolumes(ctx context.Context) error {
	// Get PV list and create AzVolume for PV with azuredisk CSI spec
	pvs, err := r.kubeClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("failed to get PV list: %v", err)
	}

	for _, pv := range pvs.Items {
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == azureutils.DriverName {
			diskName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(pv.Spec.CSI.VolumeHandle)
			if err != nil {
				klog.Warningf("skipping restoration, failed to extract diskName from volume handle (%s): %v", pv.Spec.CSI.VolumeHandle, err)
				continue
			}
			klog.Infof("Recovering AzVolume (%s)", diskName)
			requiredBytes, _ := pv.Spec.Capacity.Storage().AsInt64()
			storageClassName := pv.Spec.StorageClassName
			maxMountReplicaCount := 0
			if storageClassName == "" {
				klog.Warningf("defaulting to 0 mount replica, PV (%s) has an empty named storage class", pv.Name)
			} else {
				storageClass, err := r.kubeClient.StorageV1().StorageClasses().Get(ctx, pv.Spec.StorageClassName, metav1.GetOptions{})
				if err != nil {
					klog.Warningf("defaulting to 0 mount replica, failed to get storage class (%s): %v", pv.Spec.StorageClassName, err)
					continue
				}
				_, maxMountReplicaCount = azureutils.GetMaxSharesAndMaxMountReplicaCount(storageClass.Parameters)
			}
			if _, err := r.azVolumeClient.DiskV1alpha1().AzVolumes(r.namespace).Create(ctx, &v1alpha1.AzVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:       strings.ToLower(diskName),
					Finalizers: []string{azureutils.AzVolumeFinalizer},
				},
				Spec: v1alpha1.AzVolumeSpec{
					MaxMountReplicaCount: maxMountReplicaCount,
					UnderlyingVolume:     pv.Name,
					CapacityRange: &v1alpha1.CapacityRange{
						RequiredBytes: requiredBytes,
					},
					VolumeCapability: []v1alpha1.VolumeCapability{},
				},
				Status: v1alpha1.AzVolumeStatus{
					Detail: &v1alpha1.AzVolumeStatusDetail{
						Phase: azureutils.GetAzVolumePhase(pv.Status.Phase),
					},
					State: v1alpha1.VolumeCreated,
				},
			}, metav1.CreateOptions{}); err != nil {
				klog.Errorf("failed to recover AzVolume (%s): %v", diskName, err)
			}
		}
	}
	return nil
}

func (r *ReconcileAzVolume) Recover(ctx context.Context) error {
	// recover CRI if possible
	klog.Info("Recovering AzVolume CRIs...")
	_ = r.recoverAzVolumes(ctx)
	return nil
}

func NewAzVolumeController(mgr manager.Manager, azVolumeClient azVolumeClientSet.Interface, kubeClient kubeClientSet.Interface, namespace string, cloudProvisioner CloudProvisioner) (*ReconcileAzVolume, error) {
	reconciler := ReconcileAzVolume{
		client:           mgr.GetClient(),
		azVolumeClient:   azVolumeClient,
		kubeClient:       kubeClient,
		mutexMap:         map[string]*sync.Mutex{},
		mutexMapMutex:    sync.RWMutex{},
		namespace:        namespace,
		cloudProvisioner: cloudProvisioner,
	}
	logger := mgr.GetLogger().WithValues("controller", "azvolume")

	c, err := controller.New("azvolume-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		Log:                     logger,
	})

	if err != nil {
		klog.Errorf("Failed to create azvolume controller. Error: (%v)", err)
		return nil, err
	}

	klog.V(2).Info("Starting to watch cluster AzVolumes.")

	// Watch for CRUD events on azVolume objects
	err = c.Watch(&source.Kind{Type: &v1alpha1.AzVolume{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		klog.Errorf("Failed to watch AzVolume. Error: %v", err)
		return nil, err
	}

	klog.V(2).Info("Controller set-up successful.")

	return &reconciler, nil
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

func (r *ReconcileAzVolume) initializeMeta(ctx context.Context, volumeName string, azVolume *v1alpha1.AzVolume, useCache bool) (*v1alpha1.AzVolume, error) {
	var err error
	if azVolume == nil {
		azVolume, err = azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volumeName, r.namespace, useCache)
		if err != nil {
			klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
			return nil, err
		}
	}

	if volumeFinalizerExists(*azVolume, azureutils.AzVolumeFinalizer) {
		return azVolume, nil
	}

	patched := azVolume.DeepCopy()

	// add finalizer
	if patched.ObjectMeta.Finalizers == nil {
		patched.ObjectMeta.Finalizers = []string{}
	}

	if !volumeFinalizerExists(*azVolume, azureutils.AzVolumeFinalizer) {
		patched.ObjectMeta.Finalizers = append(patched.ObjectMeta.Finalizers, azureutils.AzVolumeFinalizer)
	}

	if err := r.client.Patch(ctx, patched, client.MergeFrom(azVolume)); err != nil {
		klog.Errorf("failed to initialize finalizer (%s) for AzVolume (%s): %v", azureutils.AzVolumeFinalizer, patched.Name, err)
		return nil, err
	}

	klog.Infof("successfully added finalizer (%s) to AzVolume (%s)", azureutils.AzVolumeFinalizer, volumeName)
	return patched, nil
}

func (r *ReconcileAzVolume) deleteFinalizer(ctx context.Context, volumeName string, azVolume *v1alpha1.AzVolume, finalizersToDelete map[string]bool, useCache bool) (*v1alpha1.AzVolume, error) {
	var err error
	if azVolume == nil {
		azVolume, err = azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volumeName, r.namespace, useCache)
		if err != nil {
			klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
			return nil, err
		}
	}

	updated := azVolume.DeepCopy()
	if updated.ObjectMeta.Finalizers == nil {
		return azVolume, nil
	}

	finalizers := []string{}
	for _, finalizer := range updated.ObjectMeta.Finalizers {
		if exists := finalizersToDelete[finalizer]; exists {
			continue
		}
		finalizers = append(finalizers, finalizer)
	}
	updated.ObjectMeta.Finalizers = finalizers
	if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to delete finalizer (%s) for AzVolume (%s): %v", azureutils.AzVolumeFinalizer, updated.Name, err)
		return nil, err
	}
	klog.Infof("successfully deleted finalizer (%s) from AzVolume (%s)", azureutils.AzVolumeFinalizer, volumeName)
	return updated, nil
}
