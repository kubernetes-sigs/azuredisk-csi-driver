/*
Copyright 2021 The Kubernetes Authors.

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
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

type ReconcileVolumeAttachment struct {
	client         client.Client
	azVolumeClient azVolumeClientSet.Interface
	kubeClient     kubeClientSet.Interface
	// retryMap allows volumeAttachment controller to retry Get operation for AzVolumeAttachment in case the CRI has not been created yet
	retryMap  sync.Map
	namespace string
}

var _ reconcile.Reconciler = &ReconcileVolumeAttachment{}

func (r *ReconcileVolumeAttachment) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	var volumeAttachment storagev1.VolumeAttachment
	if err := r.client.Get(ctx, request.NamespacedName, &volumeAttachment); err != nil {
		if !errors.IsNotFound(err) {
			klog.Errorf("failed to get VolumeAttachment (%s): %v", request.Name, err)
			return reconcile.Result{Requeue: true}, err
		}
		return reconcile.Result{}, nil
	}

	volumeAttachmentExists := true
	if now := metav1.Now(); volumeAttachment.DeletionTimestamp.Before(&now) {
		volumeAttachmentExists = false
	}

	volumeName := volumeAttachment.Spec.Source.PersistentVolumeName
	if volumeName == nil {
		return reconcile.Result{Requeue: false}, status.Error(codes.Aborted, fmt.Sprintf("PV name is set nil for VolumeAttachment (%s)", volumeAttachment.Name))
	}

	var pv corev1.PersistentVolume
	if err := r.client.Get(ctx, types.NamespacedName{Name: *volumeName}, &pv); err != nil {
		if !errors.IsNotFound(err) {
			klog.Errorf("failed to get PersistentVolume (%s): %v", volumeName, err)
			return reconcile.Result{Requeue: true}, err
		}
		return reconcile.Result{}, nil
	}

	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != azureutils.DriverName {
		return reconcile.Result{}, nil
	}

	diskName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(pv.Spec.CSI.VolumeHandle)
	if err != nil {
		klog.Errorf("failed to extract disk name from volume handle (%s): %v", pv.Spec.CSI.VolumeHandle, err)
		return reconcile.Result{Requeue: false}, err
	}

	azVolumeName := strings.ToLower(diskName)
	nodeName := volumeAttachment.Spec.NodeName
	return r.AnnotateAzVolumeAttachment(ctx, volumeAttachment, azureutils.GetAzVolumeAttachmentName(azVolumeName, nodeName), volumeAttachment.Name, volumeAttachmentExists)
}

func (r *ReconcileVolumeAttachment) AnnotateAzVolumeAttachment(ctx context.Context, volumeAttachment storagev1.VolumeAttachment, azVolumeAttachmentName, volumeAttachmentName string, volumeAttachmentExists bool) (reconcile.Result, error) {
	var azVolumeAttachment v1alpha1.AzVolumeAttachment
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: azVolumeAttachmentName}, &azVolumeAttachment); err != nil {
		if !errors.IsNotFound(err) {
			klog.Errorf("failed to get AzVolumeAttachment (%s): %v", azVolumeAttachmentName, err)
			return reconcile.Result{Requeue: true}, err
		}

		var zero uint32
		v, _ := r.retryMap.LoadOrStore(azVolumeAttachmentName, &zero)
		numRetry := v.(*uint32)

		if *numRetry < maxRetry {
			_ = atomic.AddUint32(numRetry, 1)
			klog.V(5).Infof("Waiting for AzVolumeAttachment (%s) to be created...", azVolumeAttachmentName)
			return reconcile.Result{Requeue: true}, nil
		}

		klog.V(5).Infof("Max Retry (%d) for Get AzVolumeAttachment (%s) exceeded. The CRI is probably deleted.", maxRetry, azVolumeAttachmentName)
		r.retryMap.Delete(azVolumeAttachmentName)
		return reconcile.Result{}, nil
	}

	patched := azVolumeAttachment.DeepCopy()
	if patched.Annotations == nil {
		patched.Annotations = make(map[string]string)
	}

	_, ok := patched.Annotations[azureutils.VolumeAttachmentExistsAnnotation]
	if volumeAttachmentExists {
		if ok {
			return reconcile.Result{}, nil
		}
		patched.Annotations[azureutils.VolumeAttachmentExistsAnnotation] = volumeAttachmentName
	} else {
		if !ok {
			return reconcile.Result{}, nil
		}
		delete(patched.Annotations, azureutils.VolumeAttachmentExistsAnnotation)
	}

	if err := r.client.Patch(ctx, patched, client.MergeFrom(&azVolumeAttachment)); err != nil {
		klog.Errorf("failed to update AzVolumeAttachment (%s): %v", azVolumeAttachmentName, err)
		return reconcile.Result{Requeue: true}, err
	}
	klog.V(2).Infof("successfully updated AzVolumeAttachment (%s) with annotation (%s)", azVolumeAttachmentName, azureutils.VolumeAttachmentExistsAnnotation)
	return reconcile.Result{}, nil
}

func (r *ReconcileVolumeAttachment) Recover(ctx context.Context) error {
	// Get all volumeAttachments
	volumeAttachments, err := r.kubeClient.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Warningf("failed to list volume attachments: %v", err)
		return err
	}

	// Loop through volumeAttachments and create Primary AzVolumeAttachments in correspondence
	for _, volumeAttachment := range volumeAttachments.Items {
		if volumeAttachment.Spec.Attacher == azureutils.DriverName {
			volumeName := volumeAttachment.Spec.Source.PersistentVolumeName
			if volumeName == nil {
				continue
			}
			// get PV and retrieve diskName
			pv, err := r.kubeClient.CoreV1().PersistentVolumes().Get(ctx, *volumeName, metav1.GetOptions{})
			if err != nil {
				klog.Errorf("failed to get PV (%s): %v", volumeName, err)
				return err
			}

			if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != azureutils.DriverName {
				continue
			}

			diskName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(pv.Spec.CSI.VolumeHandle)
			if err != nil {
				klog.Warningf("failed to extract disk name from volumehandle (%s): %v", pv.Spec.CSI.VolumeHandle, err)
				continue
			}
			nodeName := volumeAttachment.Spec.NodeName
			azVolumeAttachmentName := azureutils.GetAzVolumeAttachmentName(diskName, nodeName)

			for i := 0; i < maxRetry; i++ {
				// check if the CRI exists already
				azVolumeAttachment, err := azureutils.GetAzVolumeAttachment(ctx, r.client, r.azVolumeClient, azVolumeAttachmentName, r.namespace, false)
				if err != nil {
					klog.Warningf("failed to get AzVolumeAttachment (%s): %v", azVolumeAttachmentName, err)
				}

				updated := azVolumeAttachment.DeepCopy()
				if updated.Annotations == nil {
					updated.Annotations = map[string]string{}
				}
				updated.Annotations[azureutils.VolumeAttachmentExistsAnnotation] = volumeAttachment.Name
				_, err = r.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(r.namespace).Update(ctx, updated, metav1.UpdateOptions{})
				if err != nil {
					klog.Warningf("failed to update AzVolumeAttachment (%s) with annotation (%s): %v", azVolumeAttachmentName, azureutils.VolumeAttachmentExistsAnnotation, err)
					time.Sleep(time.Duration(15) * time.Second)
					continue
				}
				break
			}
		}
	}
	return nil
}

func NewVolumeAttachmentController(ctx context.Context, mgr manager.Manager, azVolumeClient azVolumeClientSet.Interface, kubeClient kubeClientSet.Interface, namespace string) (*ReconcileVolumeAttachment, error) {
	reconciler := ReconcileVolumeAttachment{
		client:         mgr.GetClient(),
		namespace:      namespace,
		azVolumeClient: azVolumeClient,
		retryMap:       sync.Map{},
		kubeClient:     kubeClient,
	}

	c, err := controller.New("volumeattachment-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		Log:                     mgr.GetLogger().WithValues("controller", "volumeattachment"),
	})

	if err != nil {
		klog.Errorf("failed to create a new volumeattachment controller: %v", err)
		return nil, err
	}

	// Watch for CRUD events on VolumeAttachment objects
	err = c.Watch(&source.Kind{Type: &storagev1.VolumeAttachment{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		klog.Errorf("failed to initialize watch for volumeattachment object: %v", err)
		return nil, err
	}

	klog.V(2).Info("VolumeAttachment Controller successfully initialized.")
	return &reconciler, nil
}
