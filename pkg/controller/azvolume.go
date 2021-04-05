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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azVolumeClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	/* commenting out until CreateVolume is done , for the golint
	cachingModeField        = "cachingmode"
	storageAccountTypeField = "storageaccounttype"
	storageAccountField     = "storageaccount"
	skuNameField            = "skuname"
	locationField           = "location"
	resourceGroupField      = "resourcegroup"
	diskIOPSReadWriteField  = "diskiopsreadwrite"
	diskMBPSReadWriteField  = "diskmbpsreadwrite"
	diskNameField           = "diskname"
	desIDField              = "diskencryptionsetid"
	tagsField               = "tags"
	maxSharesField          = "maxshares"
	incrementalField        = "incremental"
	logicalSectorSizeField  = "logicalsectorsize"
	*/
	AzVolumeFinalizer = "disk.csi.azure.com/azvolume-finalizer"
)

//Struct for the reconciler
type reconcileAzVolume struct {
	client         client.Client
	azVolumeClient azVolumeClientSet.Interface
	namespace      string
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
		if err := r.TriggerCreate(ctx, azVolume.Name); err != nil {
			klog.Errorf("failed to create AzVolume (%s): %v", azVolume.Name, err)
			return reconcile.Result{Requeue: true}, err
		}

	} else if !azVolume.ObjectMeta.DeletionTimestamp.IsZero() {
		if err := r.TriggerDelete(ctx, azVolume.Name); err != nil {
			//If delete failed, requeue request
			return reconcile.Result{Requeue: true}, err
		}
	}

	return reconcile.Result{Requeue: true}, nil
}

func (r *reconcileAzVolume) TriggerCreate(ctx context.Context, volumeName string) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	//Register finalizer
	if err := r.InitializeMeta(ctx, volumeName); err != nil {
		return err
	}

	if err := r.CreateAzVolume(ctx, &azVolume); err != nil {
		klog.Errorf("failed to create volume %s: %v", azVolume.Spec.VolumeID, err)
		return err
	}

	// Update status of the object
	if err := r.UpdateStatus(ctx, azVolume.Name, false); err != nil {
		return err
	}
	klog.Infof("successfully created volume (%s)and update status of AzVolume (%s)", azVolume.Spec.VolumeID, azVolume.Name)
	return nil

}

func (r *reconcileAzVolume) TriggerDelete(ctx context.Context, volumeName string) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	if err := r.DeleteAzVolume(ctx, &azVolume); err != nil {
		klog.Errorf("failed to delete volume %s: %v", azVolume.Spec.VolumeID, err)
		return err
	}

	// Update status of the object
	if err := r.UpdateStatus(ctx, azVolume.Name, true); err != nil {
		return err
	}
	klog.Infof("successfully deleted volume (%s)and update status of AzVolume (%s)", azVolume.Spec.VolumeID, azVolume.Name)
	return nil
}

func (r *reconcileAzVolume) UpdateStatus(ctx context.Context, volumeName string, isDeleted bool) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	if isDeleted {
		if err := r.DeleteFinalizer(ctx, volumeName); err != nil {
			klog.Errorf("failed to delete finalizer %s for azVolume %s: %v", AzVolumeFinalizer, azVolume.Name, err)
			return err
		}
		return nil
	}

	updated := azVolume.DeepCopy()
	if updated.Status == nil {
		updated.Status = &v1alpha1.AzVolumeStatus{}
	}
	if err := r.client.Status().Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to update status of AzVolume (%s): %v", volumeName, err)
		return err
	}

	return nil
}

func (r *reconcileAzVolume) CreateAzVolume(ctx context.Context, azVolume *v1alpha1.AzVolume) error {
	/* //TODO uncomment this function when able to access cloud provider */
	/*
		name := azVolume.Name

		requiredBytes := azVolume.Spec.CapacityRange.RequiredBytes
		volSizeBytes := int64(requiredBytes)

		var (
			location                string
			storageAccountType      string
			//cachingMode             v1.AzureDataDiskCachingMode
			err                     error
			resourceGroup           string
			diskIopsReadWrite       string
			diskMbpsReadWrite       string
			logicalSectorSize       int
			diskName                string
			diskEncryptionSetID     string
			customTags              string
			writeAcceleratorEnabled string
			maxShares               int
		)

		parameters := azVolume.Spec.Parameters

		if parameters == nil {
			parameters = make(map[string]string)
		}
		for k, v := range parameters {
			switch strings.ToLower(k) {
			case skuNameField:
				storageAccountType = v
			case locationField:
				location = v
			case storageAccountTypeField:
				storageAccountType = v
			case cachingModeField:
				//cachingMode = v1.AzureDataDiskCachingMode(v)
			case resourceGroupField:
				resourceGroup = v
			case diskIOPSReadWriteField:
				diskIopsReadWrite = v
			case diskMBPSReadWriteField:
				diskMbpsReadWrite = v
			case logicalSectorSizeField:
				logicalSectorSize, err = strconv.Atoi(v)
				if err != nil {
					return errors.NewBadRequest(fmt.Sprintf("parse %s failed with error: %v", v, err)) //status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s failed with error: %v", v, err))
				}
			case diskNameField:
				diskName = v
			case desIDField:
				diskEncryptionSetID = v
			case tagsField:
				customTags = v
			case azure.WriteAcceleratorEnabled:
				writeAcceleratorEnabled = v
			case maxSharesField:
				maxShares, err = strconv.Atoi(v)
				if err != nil {
					return errors.NewBadRequest(fmt.Sprintf("parse %s failed with error: %v", v, err))
				}
				if maxShares < 1 {
					return errors.NewBadRequest(fmt.Sprintf("parse %s returned with invalid value: %d", v, maxShares))
				}
			default:
				//don't return error here since there are some parameters(e.g. fsType) used in disk mount process
				//return nil, fmt.Errorf("AzureDisk - invalid option %s in storage class", k)
			}
		}
		//TODO: validate parameters

		volumeOptions := &azure.ManagedDiskOptions{
			DiskName:            diskName,
			StorageAccountType:  skuName,
			ResourceGroup:       resourceGroup,
			PVCName:             "",
			SizeGB:              requestGiB,
			Tags:                tags,
			AvailabilityZone:    selectedAvailabilityZone,
			DiskIOPSReadWrite:   diskIopsReadWrite,
			DiskMBpsReadWrite:   diskMbpsReadWrite,
			SourceResourceID:    sourceID,
			SourceType:          sourceType,
			DiskEncryptionSetID: diskEncryptionSetID,
			MaxShares:           int32(maxShares),
			LogicalSectorSize:   int32(logicalSectorSize),
		}

		//diskURI, err := azure.CreateManagedDisk(volumeOptions)

		/*if err != nil {
			if strings.Contains(err.Error(), NotFound) {
				return nil, status.Error(codes.NotFound, err.Error())
			} // */

	return nil
}

func (r *reconcileAzVolume) DeleteAzVolume(ctx context.Context, azVolume *v1alpha1.AzVolume) error {

	volumeID := azVolume.Spec.VolumeID
	if len(volumeID) == 0 {
		return errors.NewBadRequest("Volume ID Missing")
	}

	diskURI := volumeID
	//TODO: isValidDiskURI

	//isOperationSucceeded := false

	klog.V(2).Infof("deleting azure disk(%s)", diskURI)
	/* uncomment when deletion operation available
	//TODO err:= azDeleteManagedDisk(diskURI)
	//klog.V(2).Infof("delete azure disk(%s) returned with %v", diskURI, err)
	//isOperationSucceeded = (err == nil)
	return err
	*/
	return nil
}

func NewAzVolumeController(mgr manager.Manager, azVolumeClient *azVolumeClientSet.Interface, namespace string) error {
	logger := mgr.GetLogger().WithValues("controller", "azvolume")

	c, err := controller.New("azvolume-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconcileAzVolume{client: mgr.GetClient(), azVolumeClient: *azVolumeClient, namespace: namespace},
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
	klog.V(2).Info("Controller set-up successfull.")
	return nil
}

// Helper functions to check if finalizer exists
func finalizerExists(azVolume v1alpha1.AzVolume, finalizerName string) bool {
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

	if finalizerExists(azVolume, AzVolumeFinalizer) {
		return nil
	}

	patched := azVolume.DeepCopy()

	// add finalizer
	if patched.ObjectMeta.Finalizers == nil {
		patched.ObjectMeta.Finalizers = []string{}
	}

	if !finalizerExists(azVolume, AzVolumeFinalizer) {
		patched.ObjectMeta.Finalizers = append(patched.ObjectMeta.Finalizers, AzVolumeFinalizer)
	}

	if err := r.client.Patch(ctx, patched, client.MergeFrom(&azVolume)); err != nil {
		klog.Errorf("failed to initialize finalizer (%s) for AzVolume (%s): %v", AzVolumeFinalizer, patched.Name, err)
		return err
	}

	klog.Infof("successfully added finalizer (%s) to AzVolume (%s)", AzVolumeFinalizer, volumeName)
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
		if finalizer == AzVolumeFinalizer {
			continue
		}
		finalizers = append(finalizers, finalizer)
	}
	updated.ObjectMeta.Finalizers = finalizers
	if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to delete finalizer (%s) for AzVolume (%s): %v", AzVolumeFinalizer, updated.Name, err)
		return err
	}
	klog.Infof("successfully deleted finalizer (%s) from AzVolume (%s)", AzVolumeFinalizer, volumeName)
	return nil
}
