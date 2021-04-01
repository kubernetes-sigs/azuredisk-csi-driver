package controller

import (
	"context"
	"fmt"

	"sort"
	"strconv"

	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azVolumeClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)


//Struct for the reconciler 
type reconcileAzVolume {
	client.Client
	azVolumeClient azVolumeClientSet.Interface
	namespace string
}


// Implement reconcile.Reconciler so the controller can reconcile objects
var _ reconcile.Reconciler = &reconcileAzVolume{}


func (r *reconcileAzVolume) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
    _ = context.Background()
    _ = r.Log.WithValues("azVolume", req.NamespacedName)

	var azVolume v1alpha1.AzVolume
	err := r.client.Get(ctx, request.NamespacedName, &azVolume); err != nil {

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
		if err := r.DeleteAzVolume(ctx , azVolume.Name); err != nil {
			//If delete failed, requeue request
			return reconcile.Result{Requeue: true}, err
		}
	}

	return reconcile.Result{Requeue: true}, nil}
 

func NewAzVolumeController (mgr manager.Manager,  azVolumeClient *azVolumeClientSet.Interface, namespace string) error {
	logger := mgr.GetLogger().WithValues("controller", "azvolume")

	c, err := controller.New("azvolume-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconcileAzDriverNode{client: mgr.GetClient(), azVolumeClient: *azVolumeClient, namespace: namespace},
		Log:                     logger,
	})


	if err != nil {
		klog.Errorf("Failed to create azvolume controller. Error: (%v)", err)
		return err
	}
	
	klog.V(2).Info("Starting to watch cluster nodes.")

	// Watch for CRUD events on azVolume objects
	err = c.Watch(&source.Kind{Type: &v1alpha1.AzVolume}, &handler.EnqueueRequestForObject{})
	if err != nil {
		klog.Errorf("Failed to watch nodes. Error: (%v)", err)
		return err
	}
	klog.V(2).Info("Controller set-up successfull.")
	return err
}
func (r * reconcileAzVolume) TriggerCreate (ctx context.Context, volumeName string) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	if err := r.CreateAzVolume(ctx, azVolume); err != nil {
		klog.Errorf("failed to create volume %s: %v", azVolume.Spec.UnderlyingVolume, err)
		return err
	}

	// Update status of the object
	//TODO: Should UpdateStatus happen before CreateAzVolume to "creating" or similar?? 
	if err := r.UpdateStatus(ctx, azVolume.Name); err != nil {
		return err
	}
	klog.Infof("successfully created volume (%s)and update status of AzVolume (%s)", azVolumeAttachment.Spec.UnderlyingVolume, azVolumeAttachment.Name)
	return nil

}

func (r *reconcileAzVolumeAttachment) UpdateStatus(ctx context.Context, volumeName string) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	updated := azVolume.DeepCopy()
	updated.Status = azVolume.Status.DeepCopy()
	if updated.Status == nil {
		updated.Status = &v1alpha1.AzVolumeAttachmentStatus{}
	}
	updated.Status.UnderlyingVolume = azVolumeAttachment.Spec.UnderlyingVolume
	
	/* what other fields do I need? 	*/

	if err := r.client.Status().Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to update status of AzVolume (%s): %v", volumeName, err)
		return err
	}
	
	return nil
}



func (r * reconcileAzVolume) CreateAzVolume(ctx Context, azVolume *AzVolume)  {

	name := azVolume.Spec.Name
	
	requiredBytes := azVolume.Spec.RequiredBytes //Need to get requiredBytes from VolumeCapability
	volSizeBytes := int64(requiredBytes)

	var (
		location                string
		storageAccountType      string
		cachingMode             v1.AzureDataDiskCachingMode
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

	parameters := azVolume.Spec.Parameters //TODO Should format this into something mappable 

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
			cachingMode = v1.AzureDataDiskCachingMode(v)
		case resourceGroupField:
			resourceGroup = v
		case diskIOPSReadWriteField:
			diskIopsReadWrite = v
		case diskMBPSReadWriteField:
			diskMbpsReadWrite = v
		case logicalSectorSizeField:
			logicalSectorSize, err = strconv.Atoi(v)
			if err != nil {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s failed with error: %v", v, err))
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
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s failed with error: %v", v, err))
			}
			if maxShares < 1 {
				return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("parse %s returned with invalid value: %d", v, maxShares))
			}
		default:
			//don't return error here since there are some parameters(e.g. fsType) used in disk mount process
			//return nil, fmt.Errorf("AzureDisk - invalid option %s in storage class", k)
		}
	}

	//TODO: validate the parameters


	// TODO: azCreateManagedDisk(volumeOptions)
	//TODO: update diskURI / azVolume.Spec.underlyingVolume ?


	/*if err != nil {
		if strings.Contains(err.Error(), NotFound) {
			return nil, status.Error(codes.NotFound, err.Error())
		} // */

		return nil
	}
					
}

func (r * reconcileAzVolume) DeleteAzVolume (ctx context.Context, azVolume *v1alpha1.AzVolume) error {
	//TODO: Does this need a TriggerDelete function as well that updates the status? 
	
	volumeID := azVolume.Spec.UnderlyingVolume
	if len(volumeID) == 0 {
		return nil, status.Error(codes.InvalidArgument, "Volume ID missing")
	}

	diskURI := volumeID
	//TODO: isValidDiskURI
	//TODO: acquire volumelock?

	isOperationSucceeded := false

	klog.V(2).Infof("deleting azure disk(%s)", diskURI)
	//TODO err:= azDeleteManagedDisk

	klog.V(2).Infof("delete azure disk(%s) returned with %v", diskURI, err)
	isOperationSucceeded = (err == nil)
	return err
}

