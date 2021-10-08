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
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubeClientSet "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	util "sigs.k8s.io/azuredisk-csi-driver/pkg/util"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

// VolumeProvisioner defines the subset of Cloud Provisioner functions used by the AzVolume controller.
type VolumeProvisioner interface {
	CreateVolume(
		ctx context.Context,
		volumeName string,
		capacityRange *v1alpha1.CapacityRange,
		volumeCapabilities []v1alpha1.VolumeCapability,
		parameters map[string]string,
		secrets map[string]string,
		volumeContentSource *v1alpha1.ContentVolumeSource,
		accessibilityTopology *v1alpha1.TopologyRequirement) (*v1alpha1.AzVolumeStatusParams, error)
	DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error
	ExpandVolume(ctx context.Context, volumeID string, capacityRange *v1alpha1.CapacityRange, secrets map[string]string) (*v1alpha1.AzVolumeStatusParams, error)
}

//Struct for the reconciler
type ReconcileAzVolume struct {
	client            client.Client
	azVolumeClient    azClientSet.Interface
	kubeClient        kubeClientSet.Interface
	namespace         string
	volumeProvisioner VolumeProvisioner
	// stateLock prevents concurrent cloud operation for same volume to be executed due to state update race
	stateLock *sync.Map
	retryInfo *retryInfo
}

// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &ReconcileAzVolume{}

var allowedTargetVolumeStates = map[string][]string{
	string(v1alpha1.VolumeOperationPending): {string(v1alpha1.VolumeCreating), string(v1alpha1.VolumeDeleting)},
	string(v1alpha1.VolumeCreating):         {string(v1alpha1.VolumeCreated), string(v1alpha1.VolumeCreationFailed)},
	string(v1alpha1.VolumeDeleting):         {string(v1alpha1.VolumeDeleted), string(v1alpha1.VolumeDeletionFailed)},
	string(v1alpha1.VolumeUpdating):         {string(v1alpha1.VolumeUpdated), string(v1alpha1.VolumeUpdateFailed)},
	string(v1alpha1.VolumeCreated):          {string(v1alpha1.VolumeUpdating), string(v1alpha1.VolumeDeleting)},
	string(v1alpha1.VolumeUpdated):          {string(v1alpha1.VolumeUpdating), string(v1alpha1.VolumeDeleting)},
	string(v1alpha1.VolumeCreationFailed):   {},
	string(v1alpha1.VolumeDeletionFailed):   {},
}

func (r *ReconcileAzVolume) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	azVolume, err := azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, request.Name, request.Namespace, true)
	if err != nil {
		// ignore not found error as they will not be fixed with a requeue
		if errors.IsNotFound(err) {
			return reconcileReturnOnSuccess(request.Name, r.retryInfo)
		}

		// if the GET failure is triggered by other errors, requeue the request
		azVolume.Name = request.Name
		return reconcileReturnOnError(azVolume, "get", err, r.retryInfo)
	}

	// if underlying cloud operation already in process, skip until operation is completed
	if isOperationInProcess(azVolume) {
		klog.V(5).Infof("Another operation (%s) is already in process for the AzVolume (%s). Will be requeued once complete.", azVolume.Name, azVolume.Status.State)
		return reconcileReturnOnSuccess(azVolume.Name, r.retryInfo)
	}

	// azVolume deletion
	if criDeletionRequested(&azVolume.ObjectMeta) {
		if err := r.triggerDelete(ctx, azVolume); err != nil {
			//If delete failed, requeue request
			return reconcileReturnOnError(azVolume, "delete", err, r.retryInfo)
		}
		//azVolume creation
	} else if azVolume.Status.Detail == nil {
		if err := r.triggerCreate(ctx, azVolume); err != nil {
			klog.Errorf("failed to create volume (%s): %v", azVolume.Spec.UnderlyingVolume, err)
			return reconcileReturnOnError(azVolume, "create", err, r.retryInfo)
		}
		// azVolume update
	} else if azVolume.Status.Detail.ResponseObject != nil && azVolume.Spec.CapacityRange.RequiredBytes != azVolume.Status.Detail.ResponseObject.CapacityBytes {
		if err := r.triggerUpdate(ctx, azVolume); err != nil {
			klog.Errorf("failed to update volume (%s): %v", azVolume.Spec.UnderlyingVolume, err)
			return reconcileReturnOnError(azVolume, "update", err, r.retryInfo)
		}
		// azVolume released, so clean up replica Attachments (primary Attachment should be deleted via UnpublishVolume request to avoid race condition)
	} else if azVolume.Status.Detail.Phase == v1alpha1.VolumeReleased {
		if err := r.triggerRelease(ctx, azVolume); err != nil {
			klog.Errorf("failed to release AzVolume and clean up all attachments (%s): %v", azVolume.Name, err)
			return reconcileReturnOnError(azVolume, "release", err, r.retryInfo)
		}
	}

	return reconcileReturnOnSuccess(azVolume.Name, r.retryInfo)
}

func (r *ReconcileAzVolume) triggerCreate(ctx context.Context, azVolume *v1alpha1.AzVolume) error {
	// requeue if AzVolume's state is being updated by a different worker
	defer r.stateLock.Delete(azVolume.Name)
	if _, ok := r.stateLock.LoadOrStore(azVolume.Name, nil); ok {
		return getOperationRequeueError("create", azVolume)
	}

	// add finalizer and update state
	updateFunc := func(obj interface{}) error {
		azv := obj.(*v1alpha1.AzVolume)
		azv = r.initializeMeta(ctx, azv)
		_, err := r.updateState(ctx, azv, v1alpha1.VolumeCreating, normalUpdate)
		return err
	}
	if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, azVolume, updateFunc); err != nil {
		return err
	}

	klog.Infof("Creating Volume (%s)...", azVolume.Spec.UnderlyingVolume)

	// create volume
	go func() {
		var updateFunc func(interface{}) error
		response, err := r.createVolume(ctx, azVolume)
		if err != nil {
			klog.Errorf("failed to create volume %s: %v", azVolume.Spec.UnderlyingVolume, err)
			updateFunc = func(obj interface{}) error {
				azv := obj.(*v1alpha1.AzVolume)
				azv = r.updateError(ctx, azv, err)
				_, derr := r.updateState(ctx, azv, v1alpha1.VolumeCreationFailed, forceUpdate)
				return derr
			}
		} else {
			klog.Infof("Successfully created volume %s: %v", azVolume.Spec.UnderlyingVolume, response)
			updateFunc = func(obj interface{}) error {
				azv := obj.(*v1alpha1.AzVolume)
				if response == nil {
					return status.Errorf(codes.Internal, "non-nil AzVolumeStatusParams expected but nil given")
				}
				azv = r.updateStatusDetail(ctx, azv, response, response.CapacityBytes, response.NodeExpansionRequired)
				azv = r.updateStatusPhase(ctx, azv, v1alpha1.VolumeBound)
				_, derr := r.updateState(ctx, azv, v1alpha1.VolumeCreated, forceUpdate)
				return derr
			}
		}
		if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, azVolume, updateFunc); err != nil {
			klog.Errorf("failed to update AzVolume (%s) with volume creation result (response: %v, error: %v)", azVolume.Name, response, err)
		} else {
			klog.Infof("successfully created volume (%s) with volume creation result (response: %v, error: %v)", azVolume.Spec.UnderlyingVolume, response, err)
		}
	}()

	return nil
}

func (r *ReconcileAzVolume) triggerDelete(ctx context.Context, azVolume *v1alpha1.AzVolume) error {
	// Determine if this is a controller server requested deletion or driver clean up
	volumeDeleteRequested := volumeDeleteRequested(azVolume)

	mode := deleteCRIOnly
	if volumeDeleteRequested {
		mode = detachAndDeleteCRI
	}

	// Delete all AzVolumeAttachment objects bound to the deleted AzVolume
	attachments, err := cleanUpAzVolumeAttachmentByVolume(ctx, r, azVolume.Name, "azVolumeController", all, mode)
	if err != nil {
		return err
	}

	if len(attachments.Items) > 0 {
		return status.Errorf(codes.Aborted, "volume deletion requeued until attached azVolumeAttachments are entirely detached...")
	}

	// only try deleting underlying volume 1) if volume creation was successful and 2) volumeDeleteRequestAnnotation is present
	// if the annotation is not present, only delete the CRI and not the underlying volume
	if isCreated(azVolume) && volumeDeleteRequested {
		// requeue if AzVolume's state is being updated by a different worker
		defer r.stateLock.Delete(azVolume.Name)
		if _, ok := r.stateLock.LoadOrStore(azVolume.Name, nil); ok {
			return getOperationRequeueError("delete", azVolume)
		}
		updateFunc := func(obj interface{}) error {
			azv := obj.(*v1alpha1.AzVolume)
			_, derr := r.updateState(ctx, azv, v1alpha1.VolumeDeleting, normalUpdate)
			return derr
		}

		klog.Infof("Deleting Volume (%s)...", azVolume.Spec.UnderlyingVolume)

		if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, azVolume, updateFunc); err != nil {
			return err
		}

		go func() {
			var updateFunc func(interface{}) error
			err := r.deleteVolume(ctx, azVolume)
			if err != nil {
				klog.Errorf("failed to delete volume (%s): %v", azVolume.Spec.UnderlyingVolume, err)
				updateFunc = func(obj interface{}) error {
					azv := obj.(*v1alpha1.AzVolume)
					azv = r.updateError(ctx, azv, err)
					_, derr := r.updateState(ctx, azv, v1alpha1.VolumeDeletionFailed, forceUpdate)
					return derr
				}
			} else {
				klog.Infof("successfully deleted volume (%s)", azVolume.Spec.UnderlyingVolume)
				updateFunc = func(obj interface{}) error {
					azv := obj.(*v1alpha1.AzVolume)
					azv = r.deleteFinalizer(ctx, azv, map[string]bool{consts.AzVolumeFinalizer: true})
					_, derr := r.updateState(ctx, azv, v1alpha1.VolumeDeleted, forceUpdate)
					return derr
				}
			}
			if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, azVolume, updateFunc); err != nil {
				klog.Errorf("failed to update AzVolume (%s) with volume deletion result: %v", azVolume.Name, err)
			} else {
				klog.Infof("successfully updated AzVolume (%s) with volume deletion result", azVolume.Name)
			}
		}()
	} else {
		updateFunc := func(obj interface{}) error {
			azv := obj.(*v1alpha1.AzVolume)
			_ = r.deleteFinalizer(ctx, azv, map[string]bool{consts.AzVolumeFinalizer: true})
			return nil
		}
		if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, azVolume, updateFunc); err != nil {
			return err
		}
	}
	return nil
}

func (r *ReconcileAzVolume) triggerUpdate(ctx context.Context, azVolume *v1alpha1.AzVolume) error {
	// requeue if AzVolume's state is being updated by a different worker
	defer r.stateLock.Delete(azVolume.Name)
	if _, ok := r.stateLock.LoadOrStore(azVolume.Name, nil); ok {
		return getOperationRequeueError("update", azVolume)
	}
	updateFunc := func(obj interface{}) error {
		azv := obj.(*v1alpha1.AzVolume)
		_, derr := r.updateState(ctx, azv, v1alpha1.VolumeUpdating, normalUpdate)
		return derr
	}
	if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, azVolume, updateFunc); err != nil {
		return err
	}

	klog.Infof("Updating Volume (%s)...", azVolume.Spec.UnderlyingVolume)

	go func() {
		var updateFunc func(interface{}) error
		response, err := r.expandVolume(ctx, azVolume)
		if err != nil {
			klog.Errorf("failed to update volume %s: %v", azVolume.Spec.UnderlyingVolume, err)
			updateFunc = func(obj interface{}) error {
				azv := obj.(*v1alpha1.AzVolume)
				azv = r.updateError(ctx, azv, err)
				_, derr := r.updateState(ctx, azv, v1alpha1.VolumeUpdateFailed, forceUpdate)
				return derr
			}
		} else {
			klog.Infof("Successfully updated volume %s: %v", azVolume.Spec.UnderlyingVolume, response)
			updateFunc = func(obj interface{}) error {
				azv := obj.(*v1alpha1.AzVolume)
				if response == nil {
					return status.Errorf(codes.Internal, "non-nil AzVolumeStatusParams expected but nil given")
				}
				azv = r.updateStatusDetail(ctx, azv, response, response.CapacityBytes, response.NodeExpansionRequired)
				_, derr := r.updateState(ctx, azv, v1alpha1.VolumeUpdated, forceUpdate)
				return derr
			}
		}
		if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, azVolume, updateFunc); err != nil {
			klog.Errorf("failed to update AzVolume (%s) with volume update result (response: %v, error: %v)", azVolume.Name, response, err)
		} else {
			klog.Infof("successfully created volume (%s) with volume update result (response: %v, error: %v)", azVolume.Spec.UnderlyingVolume, response, err)
		}
	}()

	return nil
}

func (r *ReconcileAzVolume) triggerRelease(ctx context.Context, azVolume *v1alpha1.AzVolume) error {
	klog.Infof("Volume released: Initiating AzVolumeAttachment Clean-up")

	if _, err := cleanUpAzVolumeAttachmentByVolume(ctx, r, azVolume.Name, "azVolumeController", replicaOnly, detachAndDeleteCRI); err != nil {
		return err
	}
	updateFunc := func(obj interface{}) error {
		azv := obj.(*v1alpha1.AzVolume)
		_ = r.updateStatusPhase(ctx, azv, v1alpha1.VolumeAvailable)
		return nil
	}

	if err := azureutils.UpdateCRIWithRetry(ctx, nil, r.client, r.azVolumeClient, azVolume, updateFunc); err != nil {
		return err
	}

	return nil
}

func (r *ReconcileAzVolume) initializeMeta(ctx context.Context, azVolume *v1alpha1.AzVolume) *v1alpha1.AzVolume {
	if azVolume == nil {
		return nil
	}
	if finalizerExists(azVolume.Finalizers, consts.AzVolumeFinalizer) {
		return azVolume
	}
	// add finalizer
	if azVolume.ObjectMeta.Finalizers == nil {
		azVolume.ObjectMeta.Finalizers = []string{}
	}
	azVolume.ObjectMeta.Finalizers = append(azVolume.ObjectMeta.Finalizers, consts.AzVolumeFinalizer)

	return azVolume
}

func (r *ReconcileAzVolume) deleteFinalizer(ctx context.Context, azVolume *v1alpha1.AzVolume, finalizersToDelete map[string]bool) *v1alpha1.AzVolume {
	if azVolume == nil {
		return nil
	}

	if azVolume.ObjectMeta.Finalizers == nil {
		return azVolume
	}

	finalizers := []string{}
	for _, finalizer := range azVolume.ObjectMeta.Finalizers {
		if exists := finalizersToDelete[finalizer]; exists {
			continue
		}
		finalizers = append(finalizers, finalizer)
	}
	azVolume.ObjectMeta.Finalizers = finalizers
	return azVolume
}

func (r *ReconcileAzVolume) updateState(ctx context.Context, azVolume *v1alpha1.AzVolume, state v1alpha1.AzVolumeState, mode updateMode) (*v1alpha1.AzVolume, error) {
	var err error
	if azVolume == nil {
		return nil, status.Errorf(codes.FailedPrecondition, "function `updateState` requires non-nil AzVolume object.")
	}
	if mode == normalUpdate {
		expectedStates := allowedTargetVolumeStates[string(azVolume.Status.State)]
		if !containsString(string(state), expectedStates) {
			err = status.Error(codes.FailedPrecondition, formatUpdateStateError("azVolume", string(azVolume.Status.State), string(state), expectedStates...))
		}
	}
	if err == nil {
		azVolume.Status.State = state
	}
	return azVolume, err
}

func (r *ReconcileAzVolume) updateStatusDetail(ctx context.Context, azVolume *v1alpha1.AzVolume, status *v1alpha1.AzVolumeStatusParams, capacityBytes int64, nodeExpansionRequired bool) *v1alpha1.AzVolume {
	if azVolume == nil {
		return nil
	}
	if azVolume.Status.Detail == nil {
		azVolume.Status.Detail = &v1alpha1.AzVolumeStatusDetail{
			ResponseObject: status,
		}
	} else {
		if azVolume.Status.Detail.ResponseObject == nil {
			azVolume.Status.Detail.ResponseObject = &v1alpha1.AzVolumeStatusParams{}
		}
		azVolume.Status.Detail.ResponseObject.CapacityBytes = capacityBytes
		azVolume.Status.Detail.ResponseObject.NodeExpansionRequired = nodeExpansionRequired
	}
	return azVolume
}

func (r *ReconcileAzVolume) updateStatusPhase(ctx context.Context, azVolume *v1alpha1.AzVolume, phase v1alpha1.AzVolumePhase) *v1alpha1.AzVolume {
	if azVolume == nil {
		return nil
	}
	if azVolume.Status.Detail == nil {
		azVolume.Status.Detail = &v1alpha1.AzVolumeStatusDetail{}
	}
	azVolume.Status.Detail.Phase = phase
	return azVolume
}

func (r *ReconcileAzVolume) updateError(ctx context.Context, azVolume *v1alpha1.AzVolume, err error) *v1alpha1.AzVolume {
	if azVolume == nil {
		return nil
	}

	azVolume.Status.Error = util.NewAzError(err)

	return azVolume
}

func (r *ReconcileAzVolume) expandVolume(ctx context.Context, azVolume *v1alpha1.AzVolume) (*v1alpha1.AzVolumeStatusParams, error) {
	if azVolume.Status.Detail == nil || azVolume.Status.Detail.ResponseObject == nil {
		err := status.Errorf(codes.Internal, "Disk for expansion does not exist for AzVolume (%s).", azVolume.Name)
		klog.Errorf("skipping expandVolume operation for AzVolume (%s): %v", azVolume.Name, err)
		return nil, err
	}
	// use deep-copied version of the azVolume CRI to prevent any unwanted update to the object
	copied := azVolume.DeepCopy()
	return r.volumeProvisioner.ExpandVolume(ctx, copied.Status.Detail.ResponseObject.VolumeID, copied.Spec.CapacityRange, copied.Spec.Secrets)
}

func (r *ReconcileAzVolume) createVolume(ctx context.Context, azVolume *v1alpha1.AzVolume) (*v1alpha1.AzVolumeStatusParams, error) {
	if azVolume.Status.Detail != nil && azVolume.Status.Detail.ResponseObject != nil {
		return azVolume.Status.Detail.ResponseObject, nil
	}
	// use deep-copied version of the azVolume CRI to prevent any unwanted update to the object
	copied := azVolume.DeepCopy()
	return r.volumeProvisioner.CreateVolume(ctx, copied.Spec.UnderlyingVolume, copied.Spec.CapacityRange, copied.Spec.VolumeCapability, copied.Spec.Parameters, copied.Spec.Secrets, copied.Spec.ContentVolumeSource, copied.Spec.AccessibilityRequirements)
}

func (r *ReconcileAzVolume) deleteVolume(ctx context.Context, azVolume *v1alpha1.AzVolume) error {
	if azVolume.Status.Detail == nil || azVolume.Status.Detail.ResponseObject == nil {
		klog.Infof("skipping deleteVolume operation for AzVolume (%s): No volume to delete for AzVolume (%s)", azVolume.Name, azVolume.Name)
		return nil
	}
	// use deep-copied version of the azVolume CRI to prevent any unwanted update to the object
	copied := azVolume.DeepCopy()
	err := r.volumeProvisioner.DeleteVolume(ctx, copied.Status.Detail.ResponseObject.VolumeID, copied.Spec.Secrets)
	return err
}

func (r *ReconcileAzVolume) recoverAzVolumes(ctx context.Context) error {
	// Get PV list and create AzVolume for PV with azuredisk CSI spec
	pvs, err := r.kubeClient.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("failed to get PV list: %v", err)
	}

	for _, pv := range pvs.Items {
		if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == azureconstants.DefaultDriverName {
			diskName, err := azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
			if err != nil {
				klog.Warningf("skipping restoration, failed to extract diskName from volume handle (%s): %v", pv.Spec.CSI.VolumeHandle, err)
				continue
			}
			azVolumeName := strings.ToLower(diskName)
			klog.Infof("Recovering AzVolume (%s)", azVolumeName)
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
					Name:       azVolumeName,
					Finalizers: []string{consts.AzVolumeFinalizer},
				},
				Spec: v1alpha1.AzVolumeSpec{
					MaxMountReplicaCount: maxMountReplicaCount,
					UnderlyingVolume:     diskName,
					CapacityRange: &v1alpha1.CapacityRange{
						RequiredBytes: requiredBytes,
					},
					VolumeCapability: []v1alpha1.VolumeCapability{},
				},
				Status: v1alpha1.AzVolumeStatus{
					Detail: &v1alpha1.AzVolumeStatusDetail{
						Phase: azureutils.GetAzVolumePhase(pv.Status.Phase),
						ResponseObject: &v1alpha1.AzVolumeStatusParams{
							VolumeID: pv.Spec.CSI.VolumeHandle,
						},
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

func NewAzVolumeController(mgr manager.Manager, azVolumeClient azClientSet.Interface, kubeClient kubeClientSet.Interface, namespace string, volumeProvisioner VolumeProvisioner) (*ReconcileAzVolume, error) {
	reconciler := ReconcileAzVolume{
		client:            mgr.GetClient(),
		azVolumeClient:    azVolumeClient,
		kubeClient:        kubeClient,
		namespace:         namespace,
		volumeProvisioner: volumeProvisioner,
		stateLock:         &sync.Map{},
		retryInfo:         newRetryInfo(),
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

func (r *ReconcileAzVolume) getClient() client.Client {
	return r.client
}

func (r *ReconcileAzVolume) getAzClient() azClientSet.Interface {
	return r.azVolumeClient
}
