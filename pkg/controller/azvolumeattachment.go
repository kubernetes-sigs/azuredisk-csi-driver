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
	"sort"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	kubeClientSet "k8s.io/client-go/kubernetes"
	volerr "k8s.io/cloud-provider/volume/errors"
	"k8s.io/klog/v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azVolumeClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	defaultNumSyncWorkers = 10
	// defaultMaxReplicaUpdateCount refers to the maximum number of creation or deletion of AzVolumeAttachment objects in a single ManageReplica call
	defaultMaxReplicaUpdateCount = 1
	DefaultTimeUntilDeletion     = time.Duration(5) * time.Minute
)

type Event int

const (
	AttachEvent Event = iota
	DetachEvent
	DeleteEvent
	PVCDeleteEvent
	SyncEvent
)

type ReconcileAzVolumeAttachment struct {
	client         client.Client
	azVolumeClient azVolumeClientSet.Interface
	kubeClient     kubeClientSet.Interface
	namespace      string

	cloudProvisioner CloudProvisioner

	// syncMutex is used to prevent other syncVolume calls to be performed during syncAll routine
	syncMutex sync.RWMutex
	// muteMap maps volume name to mutex, it is used to guarantee that only one sync call is made at a time per volume
	mutexMap map[string]*sync.Mutex
	// muteMapMutex is used when updating or reading the mutexMap
	mutexMapMutex sync.RWMutex
	// volueMap maps AzVolumeAttachment name to volume, it is only used when an AzVolumeAttachment is deleted,
	// so that the controller knows which volume to manage replica for in next iteration of reconciliation
	volumeMap map[string]string
	// volumeMapMutex is used when updating oreading the volumeMap
	volumeMapMutex sync.RWMutex
	// cleanUpMap stores name of AzVolumes that is currently scheduld for a clean up
	cleanUpMap map[string]context.CancelFunc
	// cleanUpMapMutex is used when updating or reading the cleanUpMap
	cleanUpMapMutex sync.RWMutex
}

type filteredNode struct {
	azDriverNode v1alpha1.AzDriverNode
	numAttached  int
}

var _ reconcile.Reconciler = &ReconcileAzVolumeAttachment{}

func (r *ReconcileAzVolumeAttachment) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	return r.handleAzVolumeAttachmentEvent(ctx, request)
}

func (r *ReconcileAzVolumeAttachment) handleAzVolumeAttachmentEvent(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	azVolumeAttachment, err := azureutils.GetAzVolumeAttachment(ctx, r.client, r.azVolumeClient, request.Name, request.Namespace, true)
	// if object is not found, it means the object has been deleted. Log the deletion and do not requeue
	if errors.IsNotFound(err) {
		klog.Infof("AzVolumeAttachment (%s) has successfully been deleted.", request.Name)
		r.volumeMapMutex.RLock()
		underlyingVolume, ok := r.volumeMap[request.Name]
		r.volumeMapMutex.RUnlock()
		if ok {
			if err = r.manageReplicas(ctx, strings.ToLower(underlyingVolume), DeleteEvent, false); err != nil {
				klog.Errorf("failed to manage replicas for volume (%s): %v", underlyingVolume, err)
				return reconcile.Result{Requeue: true}, err
			}
			// delete (azVolumeAttachment, volume) from the volumeMap
			r.volumeMapMutex.Lock()
			delete(r.volumeMap, request.Name)
			r.volumeMapMutex.Unlock()
		}
		return reconcile.Result{}, nil
		// if the GET failure is not triggered by not found error, log it and requeue the request
	} else if err != nil {
		klog.Errorf("failed to fetch azvolumeattachment object with namespaced name %s: %v", request.NamespacedName, err)
		return reconcile.Result{Requeue: true}, err
	}

	// if bound AzVolume not found, or is scheduled for deletion, detach and delete AzVolumeAttachment
	azVolume, err := azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, strings.ToLower(azVolumeAttachment.Spec.UnderlyingVolume), r.namespace, true)
	if now := metav1.Now(); errors.IsNotFound(err) || azVolume.DeletionTimestamp.Before(&now) {
		klog.Infof("Initiating detachment and deletion of AzVolumeAttachment (%s): bound AzVolume (%s) is either deleted or scheduled for deletion now.", azVolumeAttachment.Name, azVolumeAttachment.Spec.UnderlyingVolume)
		if err := r.triggerDetach(ctx, azVolumeAttachment.Name, true); err != nil {
			// if detach failed, requeue the request
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", azVolumeAttachment.Name, err)
			return reconcile.Result{Requeue: true}, err
		}
		return reconcile.Result{}, nil
	}

	// if the azVolumeAttachment's deletion timestamp has been set, and is before the current time, detach the disk from the node and delete the finalizer
	if now := metav1.Now(); azVolumeAttachment.ObjectMeta.DeletionTimestamp.Before(&now) {
		klog.Infof("Initiating Detach operation for AzVolumeAttachment (%s)", azVolumeAttachment.Name)
		if err := r.triggerDetach(ctx, azVolumeAttachment.Name, true); err != nil {
			// if detach failed, requeue the request
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", azVolumeAttachment.Name, err)
			return reconcile.Result{Requeue: true}, err
		}
		// this is a creation event
	} else if azVolumeAttachment.Status.Detail == nil {
		if azVolumeAttachment.Status.Error == nil {
			// attach the volume to the specified node and only proceed if the current attachmen
			klog.Infof("Initiating Attach operation for AzVolumeAttachment (%s)", azVolumeAttachment.Name)
			if err := r.triggerAttach(ctx, azVolumeAttachment.Name); err != nil {
				klog.Errorf("failed to attach AzVolumeAttachment (%s): %v", azVolumeAttachment.Name, err)
				return reconcile.Result{Requeue: true}, err
			}
		}
		// if the role in status and spec are different, it is an update event where replica should be turned into a primary
	} else if azVolumeAttachment.Spec.RequestedRole != azVolumeAttachment.Status.Detail.Role {
		klog.Infof("Promoting AzVolumeAttachment (%s) from replica to primary", azVolumeAttachment.Name)
		if _, err := r.updateStatus(ctx, azVolumeAttachment.Name, nil, azVolumeAttachment.Status.Detail.PublishContext); err != nil {
			klog.Errorf("failed to promote AzVolumeAttachment (%s) from replica to primary: %v", azVolumeAttachment.Name, err)
			return reconcile.Result{Requeue: true}, err
		}
	}
	return reconcile.Result{}, nil
}

func (r *ReconcileAzVolumeAttachment) getRoleCount(ctx context.Context, azVolumeAttachments v1alpha1.AzVolumeAttachmentList, role v1alpha1.Role) int {
	roleCount := 0
	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		if azVolumeAttachment.Spec.RequestedRole == role {
			roleCount++
		}
	}
	return roleCount
}

// ManageAttachmentsForVolume will be running on a separate channel
func (r *ReconcileAzVolumeAttachment) syncAll(ctx context.Context, syncedVolumeAttachments map[string]bool, volumesToSync map[string]bool) (bool, map[string]bool, map[string]bool, error) {
	r.syncMutex.Lock()
	defer r.syncMutex.Unlock()

	// Get all volumeAttachments
	volumeAttachments, err := r.kubeClient.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
	if err != nil {
		return true, syncedVolumeAttachments, volumesToSync, err
	}

	if syncedVolumeAttachments == nil {
		syncedVolumeAttachments = map[string]bool{}
	}
	if volumesToSync == nil {
		volumesToSync = map[string]bool{}
	}

	// Loop through volumeAttachments and create Primary AzVolumeAttachments in correspondence
	for _, volumeAttachment := range volumeAttachments.Items {
		// skip if sync has been completed volumeAttachment
		if syncedVolumeAttachments[volumeAttachment.Name] {
			continue
		}
		if volumeAttachment.Spec.Attacher == azureutils.DriverName {
			volumeName := volumeAttachment.Spec.Source.PersistentVolumeName
			if volumeName == nil {
				continue
			}
			// get PV and retrieve diskName
			pv, err := r.kubeClient.CoreV1().PersistentVolumes().Get(ctx, *volumeName, metav1.GetOptions{})
			if err != nil {
				klog.Errorf("failed to get PV (%s): %v", *volumeName, err)
				return true, syncedVolumeAttachments, volumesToSync, err
			}

			if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != azureutils.DriverName {
				continue
			}
			volumesToSync[pv.Spec.CSI.VolumeHandle] = true

			diskName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(pv.Spec.CSI.VolumeHandle)
			if err != nil {
				klog.Warningf("failed to extract disk name from volumehandle (%s): %v", pv.Spec.CSI.VolumeHandle, err)
				delete(volumesToSync, pv.Spec.CSI.VolumeHandle)
				continue
			}
			nodeName := volumeAttachment.Spec.NodeName
			azVolumeAttachmentName := azureutils.GetAzVolumeAttachmentName(diskName, nodeName)

			// check if the CRI exists already
			_, err = azureutils.GetAzVolumeAttachment(ctx, r.client, r.azVolumeClient, azVolumeAttachmentName, r.namespace, false)
			klog.Infof("Recovering AzVolumeAttachment(%s)", azVolumeAttachmentName)
			// if CRI already exists, append finalizer to it
			if err == nil {
				_, err = r.initializeMeta(ctx, azVolumeAttachmentName, nil, false)
				if err != nil {
					klog.Errorf("failed to add finalizer to AzVolumeAttachment (%s): %v", azVolumeAttachmentName, err)
					return true, syncedVolumeAttachments, volumesToSync, err
				}
				// if not found, create one
			} else if errors.IsNotFound(err) {
				azVolumeAttachment := v1alpha1.AzVolumeAttachment{
					ObjectMeta: metav1.ObjectMeta{
						Name: azVolumeAttachmentName,
						Labels: map[string]string{
							azureutils.NodeNameLabel:   nodeName,
							azureutils.VolumeNameLabel: *volumeName,
						},
					},
					Spec: v1alpha1.AzVolumeAttachmentSpec{
						UnderlyingVolume: *volumeName,
						VolumeID:         pv.Spec.CSI.VolumeHandle,
						NodeName:         nodeName,
						RequestedRole:    v1alpha1.PrimaryRole,
						VolumeContext:    map[string]string{},
					},
					Status: v1alpha1.AzVolumeAttachmentStatus{
						State: azureutils.GetAzVolumeAttachmentState(volumeAttachment.Status),
					},
				}
				if azVolumeAttachment.Status.State == v1alpha1.Attached {
					azVolumeAttachment.Status.Detail = &v1alpha1.AzVolumeAttachmentStatusDetail{
						Role: v1alpha1.PrimaryRole,
					}
				}
				_, err := r.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(r.namespace).Create(ctx, &azVolumeAttachment, metav1.CreateOptions{})
				if err != nil {
					klog.Errorf("failed to create AzVolumeAttachment (%s) for volume (%s) and node (%s): %v", azVolumeAttachmentName, *volumeName, nodeName, err)
					return true, syncedVolumeAttachments, volumesToSync, err
				}
			} else {
				klog.Errorf("failed to get AzVolumeAttachment (%s): %v", azVolumeAttachmentName, err)
				return true, syncedVolumeAttachments, volumesToSync, err
			}

			syncedVolumeAttachments[volumeAttachment.Name] = true
		}
	}

	numWorkers := defaultNumSyncWorkers
	if numWorkers > len(volumesToSync) {
		numWorkers = len(volumesToSync)
	}

	type resultStruct struct {
		err    error
		volume string
	}
	results := make(chan resultStruct, len(volumesToSync))
	workerControl := make(chan struct{}, numWorkers)
	defer close(results)
	defer close(workerControl)

	// Sync all volumes and reset mutexMap in case some volumes had been deleted
	r.mutexMapMutex.Lock()
	r.mutexMap = make(map[string]*sync.Mutex)
	for volume := range volumesToSync {
		r.mutexMap[volume] = &sync.Mutex{}
		workerControl <- struct{}{}
		go func(vol string) {
			diskName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(vol)
			if err == nil {
				results <- resultStruct{err: r.syncVolume(ctx, strings.ToLower(diskName), SyncEvent, false, false), volume: diskName}
			} else {
				klog.Warningf("failed to extract diskname from disk URI (%s): %v", vol, err)
			}
			<-workerControl
		}(volume)
	}
	r.mutexMapMutex.Unlock()

	// Collect results
	for range volumesToSync {
		result := <-results
		if result.err != nil {
			klog.Errorf("failed in process of syncing AzVolume (%s): %v", result.volume, result.err)
		} else {
			delete(volumesToSync, result.volume)
		}
	}
	return false, syncedVolumeAttachments, volumesToSync, nil
}

func (r *ReconcileAzVolumeAttachment) syncVolume(ctx context.Context, volume string, eventType Event, isPrimary, useCache bool) error {
	// this is to prevent multiple sync volume operation to be performed on a single volume concurrently as it can create or delete more attachments than necessary
	r.mutexMapMutex.RLock()
	volMutex, ok := r.mutexMap[volume]
	r.mutexMapMutex.RUnlock()
	if ok {
		volMutex.Lock()
		defer volMutex.Unlock()
	}

	var azVolume *v1alpha1.AzVolume
	azVolume, err := azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volume, r.namespace, useCache)
	// if AzVolume is not found, the volume is deleted, so do not requeue and do not return error
	// AzVolumeAttachment objects for the volume will be triggered to be deleted.
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		klog.Errorf("failed to get AzVolume (%s): %v", volume, err)
		return err
	}

	// fetch AzVolumeAttachment with AzVolume
	volRequirement, err := labels.NewRequirement(azureutils.VolumeNameLabel, selection.Equals, []string{azVolume.Spec.UnderlyingVolume})
	if err != nil {
		return err
	}
	if volRequirement == nil {
		return status.Error(codes.Internal, fmt.Sprintf("Unable to create Requirement to for label key : (%s) and label value: (%s)", azureutils.VolumeNameLabel, azVolume.Spec.UnderlyingVolume))
	}

	labelSelector := labels.NewSelector().Add(*volRequirement)
	var azVolumeAttachments *v1alpha1.AzVolumeAttachmentList

	if useCache {
		azVolumeAttachments = &v1alpha1.AzVolumeAttachmentList{}
		err = r.client.List(ctx, azVolumeAttachments, &client.ListOptions{LabelSelector: labelSelector})
	} else {
		azVolumeAttachments, err = r.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(r.namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	}
	if err != nil {
		klog.Errorf("failed to list AzVolumeAttachments for AzVolume (%s): %v", azVolume.Name, err)
		return err
	}

	numReplicas := r.getRoleCount(ctx, *azVolumeAttachments, v1alpha1.ReplicaRole)

	desiredReplicaCount, currentReplicaCount, currentAttachmentCount := azVolume.Spec.MaxMountReplicaCount, numReplicas, len(azVolumeAttachments.Items)
	klog.Infof("control number of replicas for volume (%s): desired=%d,\tcurrent:%d", azVolume.Spec.UnderlyingVolume, desiredReplicaCount, currentReplicaCount)

	// if there is no AzVolumeAttachment object for the specified underlying volume, remove AzVolumeAttachment finalizer from AzVolume
	if currentAttachmentCount == 0 {
		if err := r.deleteFinalizerFromAzVolume(ctx, azVolume.Name); err != nil {
			return err
		}
	}

	// If primary attachment event, reset deletion timestamp
	if eventType == AttachEvent {
		if isPrimary {
			// cancel context of the scheduled deletion goroutine
			r.cleanUpMapMutex.Lock()
			cancelFunc, ok := r.cleanUpMap[azVolume.Name]
			if ok {
				cancelFunc()
				delete(r.cleanUpMap, azVolume.Name)
			}
			r.cleanUpMapMutex.Unlock()
		}
		// If primary detachment event, delete the attachment after waiting a set amount of time
	} else if eventType == DetachEvent && azVolume.Spec.MaxMountReplicaCount > 0 {
		if isPrimary {
			r.cleanUpMapMutex.Lock()
			emptyCtx := context.TODO()
			deletionCtx, cancelFunc := context.WithCancel(emptyCtx)
			r.cleanUpMap[azVolume.Name] = cancelFunc
			r.cleanUpMapMutex.Unlock()

			go func(ctx context.Context) {
				// Sleep
				time.Sleep(DefaultTimeUntilDeletion)
				_ = cleanUpAzVolumeAttachmentByVolume(ctx, r.client, r.azVolumeClient, r.namespace, azVolume.Name, all)
			}(deletionCtx)
		}
		return nil
		// for all other events, no need to manage replicas
	} else if eventType != DeleteEvent {
		return nil
	}

	// if the azVolume is marked deleted, do no create more azvolumeattachment objects
	if azVolume.DeletionTimestamp == nil && desiredReplicaCount > currentReplicaCount {
		klog.Infof("Create %d more replicas for volume (%s)", desiredReplicaCount-currentReplicaCount, azVolume.Spec.UnderlyingVolume)
		if azVolume.Status.Detail == nil || azVolume.Status.State == v1alpha1.VolumeDeleting || azVolume.Status.State == v1alpha1.VolumeDeleted || azVolume.Status.Detail.ResponseObject == nil {
			// underlying volume does not exist, so volume attachment cannot be made
			return nil
		}
		if err = r.createReplicas(ctx, min(defaultMaxReplicaUpdateCount, desiredReplicaCount-currentReplicaCount), azVolume.Spec.UnderlyingVolume, azVolume.Status.Detail.ResponseObject.VolumeID, useCache); err != nil {
			klog.Errorf("failed to create %d replicas for volume (%s): %v", desiredReplicaCount-currentReplicaCount, azVolume.Spec.UnderlyingVolume, err)
			return err
		}
	} else if desiredReplicaCount < currentReplicaCount {
		klog.Infof("Delete %d replicas for volume (%s)", currentReplicaCount-desiredReplicaCount, azVolume.Spec.UnderlyingVolume)
		i := 0
		for _, azVolumeAttachment := range azVolumeAttachments.Items {
			if i >= min(defaultMaxReplicaUpdateCount, currentReplicaCount-desiredReplicaCount) {
				break
			}
			// if the volume has not yet been attached to any node or is a primary node, skip
			if azVolumeAttachment.Spec.RequestedRole == v1alpha1.PrimaryRole || azVolumeAttachment.Status.Detail == nil {
				continue
			}
			// otherwise delete the attachment and increment the counter
			if err := r.client.Delete(ctx, &azVolumeAttachment, &client.DeleteOptions{}); err != nil {
				klog.Errorf("failed to delete azvolumeattachment %s: %v", azVolumeAttachment.Name, err)
				return err
			}
			i++
		}
	}
	return nil
}

func (r *ReconcileAzVolumeAttachment) manageReplicas(ctx context.Context, underlyingVolume string, eventType Event, isPrimary bool) error {
	var azVolume *v1alpha1.AzVolume
	azVolume, err := azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, underlyingVolume, r.namespace, true)
	// if AzVolume is not found, the volume is deleted, so do not requeue and do not return error
	// AzVolumeAttachment objects for the volume will be triggered to be deleted.
	if errors.IsNotFound(err) {
		return nil
	} else if err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", underlyingVolume, err)
		return err
	}

	// manage replica calls should not block each other but should block sync all calls and vice versa
	r.syncMutex.RLock()
	defer r.syncMutex.RUnlock()
	return r.syncVolume(ctx, azVolume.Name, eventType, isPrimary, true)
}

func (r *ReconcileAzVolumeAttachment) createReplicas(ctx context.Context, numReplica int, underlyingVolume, volumeID string, useCache bool) error {
	// if volume is scheduled for clean up, skip replica creation
	r.cleanUpMapMutex.Lock()
	_, cleanUpScheduled := r.cleanUpMap[underlyingVolume]
	r.cleanUpMapMutex.Unlock()

	if cleanUpScheduled {
		return nil
	}

	nodes, err := r.getNodesForReplica(ctx, numReplica, underlyingVolume, false, useCache)
	if err != nil {
		klog.Errorf("failed to get a list of nodes for replica attachment: %v", err)
		return err
	}

	for _, node := range nodes {
		err := r.client.Create(ctx, &v1alpha1.AzVolumeAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      fmt.Sprintf("%s-%s-attachment", underlyingVolume, node.azDriverNode.Spec.NodeName),
				Namespace: r.namespace,
				Labels: map[string]string{
					azureutils.NodeNameLabel:   node.azDriverNode.Name,
					azureutils.VolumeNameLabel: underlyingVolume,
					azureutils.RoleLabel:       string(v1alpha1.ReplicaRole),
				},
			},
			Spec: v1alpha1.AzVolumeAttachmentSpec{
				NodeName:         node.azDriverNode.Spec.NodeName,
				VolumeID:         volumeID,
				UnderlyingVolume: underlyingVolume,
				RequestedRole:    v1alpha1.ReplicaRole,
				VolumeContext:    map[string]string{},
			},
			Status: v1alpha1.AzVolumeAttachmentStatus{
				State: v1alpha1.AttachmentPending,
			},
		}, &client.CreateOptions{})

		if err != nil {
			klog.Errorf("failed to create replica azVolumeAttachment for volume %s: %v", underlyingVolume, err)
			return err
		}
	}
	return nil
}

func (r *ReconcileAzVolumeAttachment) getNodesForReplica(ctx context.Context, numReplica int, underlyingVolume string, reverse, useCache bool) ([]filteredNode, error) {
	filteredNodes := []filteredNode{}
	var nodes *v1alpha1.AzDriverNodeList
	var err error
	// List all AzDriverNodes
	if useCache {
		nodes = &v1alpha1.AzDriverNodeList{}
		err = r.client.List(ctx, nodes, &client.ListOptions{})
	} else {
		nodes, err = r.azVolumeClient.DiskV1alpha1().AzDriverNodes(r.namespace).List(ctx, metav1.ListOptions{})
	}
	if err != nil {
		klog.Errorf("failed to retrieve azDriverNode List for namespace %s: %v", r.namespace, err)
		return filteredNodes, err
	}
	if nodes != nil {
		for _, node := range nodes.Items {
			// filter out attachments labeled with specified node and volume
			var attachmentList v1alpha1.AzVolumeAttachmentList
			volRequirement, err := labels.NewRequirement(azureutils.VolumeNameLabel, selection.Equals, []string{underlyingVolume})
			if err != nil {
				klog.Errorf("Encountered error while creating Requirement: %+v", err)
				continue
			}
			if volRequirement == nil {
				klog.Errorf("Unable to create Requirement to for label key : (%s) and label value: (%s)", azureutils.VolumeNameLabel, underlyingVolume)
				continue
			}

			nodeRequirement, err := labels.NewRequirement(azureutils.NodeNameLabel, selection.Equals, []string{string(node.Name)})
			if err != nil {
				klog.Errorf("Encountered error while creating Requirement: %+v", err)
				continue
			}
			if nodeRequirement == nil {
				klog.Errorf("Unable to create Requirement to for label key : (%s) and label value: (%s)", azureutils.NodeNameLabel, node.Name)
				continue
			}

			labelSelector := labels.NewSelector().Add(*nodeRequirement).Add(*volRequirement)
			if err := r.client.List(ctx, &attachmentList, &client.ListOptions{LabelSelector: labelSelector}); err != nil {
				klog.Warningf("failed to get AzVolumeAttachmentList labeled with volume (%s) and node (%s): %v", underlyingVolume, node.Name, err)
				continue
			}
			// only proceed if there is no AzVolumeAttachment object already for the node and volume
			if len(attachmentList.Items) > 0 {
				continue
			}
			labelSelector = labels.NewSelector().Add(*nodeRequirement)
			if err := r.client.List(ctx, &attachmentList, &client.ListOptions{LabelSelector: labelSelector}); err != nil {
				klog.Warningf("failed to get AzVolumeAttachmentList labeled with node (%s): %v", node.Name, err)
				continue
			}

			filteredNodes = append(filteredNodes, filteredNode{azDriverNode: node, numAttached: len(attachmentList.Items)})
			klog.Infof("node (%s) has %d attachments", node.Name, len(attachmentList.Items))
		}
	}

	// sort the filteredNodes by their number of attachments (low to high) and return a slice
	sort.Slice(filteredNodes[:], func(i, j int) bool {
		if reverse {
			return filteredNodes[i].numAttached > filteredNodes[j].numAttached

		}
		return filteredNodes[i].numAttached < filteredNodes[j].numAttached
	})

	if len(filteredNodes) > numReplica {
		return filteredNodes[:numReplica], nil
	}
	return filteredNodes, nil
}

func (r *ReconcileAzVolumeAttachment) initializeMeta(ctx context.Context, attachmentName string, azVolumeAttachment *v1alpha1.AzVolumeAttachment, useCache bool) (*v1alpha1.AzVolumeAttachment, error) {
	var err error
	if azVolumeAttachment == nil {
		azVolumeAttachment, err = azureutils.GetAzVolumeAttachment(ctx, r.client, r.azVolumeClient, attachmentName, r.namespace, useCache)
		if err != nil {
			klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, err)
			return nil, err
		}
	}

	// if the required metadata already exists return
	if finalizerExists(azVolumeAttachment.Finalizers, azureutils.AzVolumeAttachmentFinalizer) && labelExists(azVolumeAttachment.Labels, azureutils.NodeNameLabel) && labelExists(azVolumeAttachment.Labels, azureutils.VolumeNameLabel) {
		return azVolumeAttachment, nil
	}

	patched := azVolumeAttachment.DeepCopy()

	// add finalizer
	if patched.Finalizers == nil {
		patched.Finalizers = []string{}
	}

	if !finalizerExists(azVolumeAttachment.Finalizers, azureutils.AzVolumeAttachmentFinalizer) {
		patched.Finalizers = append(patched.Finalizers, azureutils.AzVolumeAttachmentFinalizer)
	}

	// add label
	if patched.Labels == nil {
		patched.Labels = make(map[string]string)
	}
	patched.Labels[azureutils.NodeNameLabel] = azVolumeAttachment.Spec.NodeName
	patched.Labels[azureutils.VolumeNameLabel] = azVolumeAttachment.Spec.UnderlyingVolume
	patched.Labels[azureutils.RoleLabel] = string(azVolumeAttachment.Spec.RequestedRole)

	if err = r.client.Patch(ctx, patched, client.MergeFrom(azVolumeAttachment)); err != nil {
		klog.Errorf("failed to initialize finalizer (%s) for AzVolumeAttachment (%s): %v", azureutils.AzVolumeAttachmentFinalizer, patched.Name, err)
		return nil, err
	}

	klog.Infof("successfully added finalizer (%s) to AzVolumeAttachment (%s)", azureutils.AzVolumeAttachmentFinalizer, attachmentName)
	return patched, nil
}

func (r *ReconcileAzVolumeAttachment) deleteFinalizer(ctx context.Context, attachmentName string, azVolumeAttachment *v1alpha1.AzVolumeAttachment, useCache bool) (*v1alpha1.AzVolumeAttachment, error) {
	var err error
	if azVolumeAttachment == nil {
		azVolumeAttachment, err = azureutils.GetAzVolumeAttachment(ctx, r.client, r.azVolumeClient, attachmentName, r.namespace, useCache)
		if err != nil {
			klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, err)
			return nil, err
		}
	}

	updated := azVolumeAttachment.DeepCopy()
	if updated.ObjectMeta.Finalizers == nil {
		return updated, nil
	}

	finalizers := []string{}
	for _, finalizer := range updated.ObjectMeta.Finalizers {
		if finalizer == azureutils.AzVolumeAttachmentFinalizer {
			continue
		}
		finalizers = append(finalizers, finalizer)
	}
	updated.ObjectMeta.Finalizers = finalizers
	if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to delete finalizer (%s) for AzVolumeAttachment (%s): %v", azureutils.AzVolumeAttachmentFinalizer, updated.Name, err)
		return nil, err
	}
	klog.Infof("successfully deleted finalizer (%s) from AzVolumeAttachment (%s)", azureutils.AzVolumeAttachmentFinalizer, attachmentName)
	return updated, nil
}

func (r *ReconcileAzVolumeAttachment) addFinalizerToAzVolume(ctx context.Context, volumeName string) error {
	var azVolume *v1alpha1.AzVolume
	azVolume, err := azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volumeName, r.namespace, true)
	if err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	updated := azVolume.DeepCopy()

	if updated.Finalizers == nil {
		updated.Finalizers = []string{}
	}

	if finalizerExists(updated.Finalizers, azureutils.AzVolumeAttachmentFinalizer) {
		return nil
	}

	updated.Finalizers = append(updated.Finalizers, azureutils.AzVolumeAttachmentFinalizer)
	if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to add finalizer (%s) to AzVolume(%s): %v", azureutils.AzVolumeAttachmentFinalizer, updated.Name, err)
		return err
	}
	klog.Infof("successfully added finalizer (%s) to AzVolume (%s)", azureutils.AzVolumeAttachmentFinalizer, updated.Name)
	return nil
}

func (r *ReconcileAzVolumeAttachment) deleteFinalizerFromAzVolume(ctx context.Context, volumeName string) error {
	var azVolume *v1alpha1.AzVolume
	azVolume, err := azureutils.GetAzVolume(ctx, r.client, r.azVolumeClient, volumeName, r.namespace, true)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	updated := azVolume.DeepCopy()
	updatedFinalizers := []string{}

	for _, finalizer := range updated.Finalizers {
		if finalizer == azureutils.AzVolumeAttachmentFinalizer {
			continue
		}
		updatedFinalizers = append(updatedFinalizers, finalizer)
	}
	updated.Finalizers = updatedFinalizers

	if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to delete finalizer (%s) from AzVolume(%s): %v", azureutils.AzVolumeAttachmentFinalizer, updated.Name, err)
		return err
	}
	klog.Infof("successfully deleted finalizer (%s) from AzVolume (%s)", azureutils.AzVolumeAttachmentFinalizer, updated.Name)
	return nil
}

func finalizerExists(finalizers []string, finalizerName string) bool {
	for _, finalizer := range finalizers {
		if finalizer == finalizerName {
			return true
		}
	}
	return false
}

func labelExists(labels map[string]string, label string) bool {
	if labels != nil {
		_, ok := labels[label]
		return ok
	}
	return false
}

func (r *ReconcileAzVolumeAttachment) triggerAttach(ctx context.Context, attachmentName string) error {
	var azVolumeAttachment *v1alpha1.AzVolumeAttachment
	var err error
	azVolumeAttachment, err = azureutils.GetAzVolumeAttachment(ctx, r.client, r.azVolumeClient, attachmentName, r.namespace, true)
	if err != nil {
		klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, err)
		return err
	}

	// Initialize finalizer and add label to the object
	if azVolumeAttachment, err = r.initializeMeta(ctx, azVolumeAttachment.Name, azVolumeAttachment, true); err != nil {
		return err
	}

	if err := r.addFinalizerToAzVolume(ctx, strings.ToLower(azVolumeAttachment.Spec.UnderlyingVolume)); err != nil {
		return err
	}

	if azVolumeAttachment.Status.State == v1alpha1.AttachmentPending || azVolumeAttachment.Status.State == v1alpha1.Attaching || azVolumeAttachment.Status.State == v1alpha1.AttachmentFailed || azVolumeAttachment.Status.State == v1alpha1.Attached {
		if azVolumeAttachment, err = r.updateState(ctx, attachmentName, azVolumeAttachment, v1alpha1.Attaching); err != nil {
			return err
		}
		response, err := r.attachVolume(ctx, azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Spec.VolumeContext)
		if err != nil {
			klog.Errorf("failed to attach volume %s to node %s: %v", azVolumeAttachment.Spec.UnderlyingVolume, azVolumeAttachment.Spec.NodeName, err)

			var derr error
			if azVolumeAttachment, derr = r.updateState(ctx, attachmentName, azVolumeAttachment, v1alpha1.AttachmentFailed); derr != nil {
				return derr
			}

			_, err = r.updateStatusWithError(ctx, azVolumeAttachment.Name, azVolumeAttachment, err)
			return err
		}

		r.mutexMapMutex.RLock()
		_, ok := r.mutexMap[azVolumeAttachment.Spec.UnderlyingVolume]
		r.mutexMapMutex.RUnlock()

		if !ok {
			r.mutexMapMutex.Lock()
			r.mutexMap[azVolumeAttachment.Spec.UnderlyingVolume] = &sync.Mutex{}
			r.mutexMapMutex.Unlock()
		}

		if azVolumeAttachment, err = r.updateState(ctx, attachmentName, azVolumeAttachment, v1alpha1.Attached); err != nil {
			return err
		}
		// Update status of the object
		if azVolumeAttachment, err = r.updateStatus(ctx, azVolumeAttachment.Name, azVolumeAttachment, response); err != nil {
			return err
		}

		klog.Infof("successfully attached volume (%s) to node (%s) and update status of AzVolumeAttachment (%s)", azVolumeAttachment.Spec.UnderlyingVolume, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Name)
	}

	return nil
}

func (r *ReconcileAzVolumeAttachment) triggerDetach(ctx context.Context, attachmentName string, useCache bool) error {
	azVolumeAttachment, err := azureutils.GetAzVolumeAttachment(ctx, r.client, r.azVolumeClient, attachmentName, r.namespace, useCache)
	if err != nil {
		klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, err)
		return err
	}

	if azVolumeAttachment.Status.State == v1alpha1.Attached || azVolumeAttachment.Status.State == v1alpha1.AttachmentFailed || azVolumeAttachment.Status.State == v1alpha1.Detaching || azVolumeAttachment.Status.State == v1alpha1.DetachmentFailed || azVolumeAttachment.Status.State == v1alpha1.Detached {
		detachmentRequested := false
		if azVolumeAttachment.Annotations != nil && metav1.HasAnnotation(azVolumeAttachment.ObjectMeta, azureutils.VolumeAttachmentExistsAnnotation) {
			vaName := azVolumeAttachment.Annotations[azureutils.VolumeAttachmentExistsAnnotation]
			var volumeAttachment storagev1.VolumeAttachment
			err = r.client.Get(ctx, types.NamespacedName{Name: vaName}, &volumeAttachment)
			if err != nil && !errors.IsNotFound(err) {
				klog.Errorf("failed to get volumeAttachment (%s): %v", vaName, err)
				return err
			}
			detachmentRequested = errors.IsNotFound(err) || volumeAttachment.DeletionTimestamp != nil
		}
		detachmentRequested = detachmentRequested || metav1.HasAnnotation(azVolumeAttachment.ObjectMeta, azureutils.VolumeDeleteRequestAnnotation)

		// only detach if
		// 1) detachment request was made for underling volume attachment object
		// 2) volume attachment is marked for deletion or does not exist
		// 3) no annotation has been set (in this case, this is a replica that can safely be detached)
		if detachmentRequested || azVolumeAttachment.Annotations == nil || !metav1.HasAnnotation(azVolumeAttachment.ObjectMeta, azureutils.VolumeAttachmentExistsAnnotation) {
			if azVolumeAttachment, err = r.updateState(ctx, attachmentName, azVolumeAttachment, v1alpha1.Detaching); err != nil {
				return err
			}

			if err := r.detachVolume(ctx, azVolumeAttachment.Spec.VolumeID, azVolumeAttachment.Spec.NodeName); err != nil {
				klog.Errorf("failed to detach volume %s from node %s: %v", azVolumeAttachment.Spec.UnderlyingVolume, azVolumeAttachment.Spec.NodeName, err)
				var derr error
				if azVolumeAttachment, derr = r.updateState(ctx, attachmentName, azVolumeAttachment, v1alpha1.DetachmentFailed); derr != nil {
					return derr
				}
				_, err = r.updateStatusWithError(ctx, azVolumeAttachment.Name, azVolumeAttachment, err)
				return err
			}

			if azVolumeAttachment, err = r.updateState(ctx, attachmentName, azVolumeAttachment, v1alpha1.Detached); err != nil {
				return err
			}
		}

		if err := r.manageReplicas(ctx, strings.ToLower(azVolumeAttachment.Spec.UnderlyingVolume), DetachEvent, azVolumeAttachment.Spec.RequestedRole == v1alpha1.PrimaryRole); err != nil {
			klog.Errorf("failed to manage replicas for volume (%s): %v", azVolumeAttachment.Spec.UnderlyingVolume, err)
			return err
		}

		// If above procedures were successful, remove finalizer from the object
		if azVolumeAttachment, err = r.deleteFinalizer(ctx, attachmentName, azVolumeAttachment, true); err != nil {
			klog.Errorf("failed to delete finalizer %s for azvolumeattachment %s: %v", azureutils.AzVolumeAttachmentFinalizer, attachmentName, err)
			return err
		}

		// add (azVolumeAttachment, underlyingVolume) to volumeMap so that a replacement replica can be created in next iteration of reconciliation
		r.volumeMapMutex.Lock()
		r.volumeMap[azVolumeAttachment.Name] = azVolumeAttachment.Spec.UnderlyingVolume
		r.volumeMapMutex.Unlock()

		klog.Infof("successfully detached volume %s from node %s and deleted %s", azVolumeAttachment.Spec.UnderlyingVolume, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Name)
	}
	return nil
}

func (r *ReconcileAzVolumeAttachment) updateStatus(ctx context.Context, attachmentName string, azVolumeAttachment *v1alpha1.AzVolumeAttachment, status map[string]string) (*v1alpha1.AzVolumeAttachment, error) {
	var err error
	if azVolumeAttachment == nil {
		azVolumeAttachment, err = azureutils.GetAzVolumeAttachment(ctx, r.client, r.azVolumeClient, attachmentName, r.namespace, true)
		if err != nil {
			klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, err)
			return nil, err
		}
	}

	// Create replica if necessary
	if err = r.manageReplicas(ctx, strings.ToLower(azVolumeAttachment.Spec.UnderlyingVolume), AttachEvent, azVolumeAttachment.Spec.RequestedRole == v1alpha1.PrimaryRole); err != nil {
		klog.Errorf("failed creating replicas for AzVolume (%s): %v")
		return nil, err
	}

	updated := azVolumeAttachment.DeepCopy()
	if updated.Status.Detail == nil {
		updated.Status.Detail = &v1alpha1.AzVolumeAttachmentStatusDetail{}
	}
	updated.Status.Detail.Role = azVolumeAttachment.Spec.RequestedRole
	updated.Status.Detail.PublishContext = status

	if err = r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to update status of AzVolumeAttachment (%s): %v", attachmentName, err)
		return nil, err
	}

	return updated, nil
}

func (r *ReconcileAzVolumeAttachment) updateStatusWithError(ctx context.Context, attachmentName string, azVolumeAttachment *v1alpha1.AzVolumeAttachment, err error) (*v1alpha1.AzVolumeAttachment, error) {
	if azVolumeAttachment == nil {
		var derr error
		azVolumeAttachment, derr = azureutils.GetAzVolumeAttachment(ctx, r.client, r.azVolumeClient, attachmentName, r.namespace, true)
		if derr != nil {
			klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, derr)
			return nil, derr
		}
	}

	updated := azVolumeAttachment.DeepCopy()

	if err != nil {
		azVolumeAttachmentError := &v1alpha1.AzError{
			ErrorCode:    util.GetStringValueForErrorCode(status.Code(err)),
			ErrorMessage: err.Error(),
		}
		if derr, ok := err.(*volerr.DanglingAttachError); ok {
			azVolumeAttachmentError.ErrorCode = util.DanglingAttachErrorCode
			azVolumeAttachmentError.CurrentNode = derr.CurrentNode
			azVolumeAttachmentError.DevicePath = derr.DevicePath
		}

		updated.Status.Error = azVolumeAttachmentError
		if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
			klog.Errorf("failed to update error status of AzVolumeAttachment (%s): %v", attachmentName, err)
			return nil, err
		}
	}

	klog.Infof("Successfully updated AzVolumeAttachment (%s) with Error (%v)", attachmentName, updated.Status.Error)
	return updated, nil
}

func (r *ReconcileAzVolumeAttachment) updateState(ctx context.Context, attachmentName string, azVolumeAttachment *v1alpha1.AzVolumeAttachment, state v1alpha1.AzVolumeAttachmentAttachmentState) (*v1alpha1.AzVolumeAttachment, error) {
	var err error
	if azVolumeAttachment == nil {
		azVolumeAttachment, err = azureutils.GetAzVolumeAttachment(ctx, r.client, r.azVolumeClient, attachmentName, r.namespace, true)
		if err != nil {
			klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, err)
			return nil, err
		}
	}

	patched := azVolumeAttachment.DeepCopy()
	if patched.Status.State == state {
		return azVolumeAttachment, nil
	}
	patched.Status.State = state

	if err = r.client.Patch(ctx, patched, client.MergeFrom(azVolumeAttachment)); err != nil {
		klog.Errorf("failed to update AzVolumeAttachment (%s): %v", attachmentName, err)
		return nil, err
	}
	return patched, nil
}

func (r *ReconcileAzVolumeAttachment) attachVolume(ctx context.Context, volumeID, node string, volumeContext map[string]string) (map[string]string, error) {
	return r.cloudProvisioner.PublishVolume(ctx, volumeID, node, volumeContext)
}

func (r *ReconcileAzVolumeAttachment) detachVolume(ctx context.Context, volumeID, node string) error {
	return r.cloudProvisioner.UnpublishVolume(ctx, volumeID, node)
}

func (r *ReconcileAzVolumeAttachment) Recover(ctx context.Context) error {
	klog.Info("Recovering AzVolumeAttachment CRIs...")
	// try to recover states
	var syncedVolumeAttachments, volumesToSync map[string]bool
	for i := 0; i < maxRetry; i++ {
		var retry bool
		var err error

		retry, syncedVolumeAttachments, volumesToSync, err = r.syncAll(ctx, syncedVolumeAttachments, volumesToSync)
		if err != nil {
			klog.Warningf("failed to complete initial AzVolumeAttachment sync: %v", err)
		}
		if !retry {
			break
		}
	}

	return nil
}

func NewAzVolumeAttachmentController(mgr manager.Manager, azVolumeClient azVolumeClientSet.Interface, kubeClient kubeClientSet.Interface, namespace string, cloudProvisioner CloudProvisioner) (*ReconcileAzVolumeAttachment, error) {
	reconciler := ReconcileAzVolumeAttachment{
		client:           mgr.GetClient(),
		azVolumeClient:   azVolumeClient,
		kubeClient:       kubeClient,
		syncMutex:        sync.RWMutex{},
		namespace:        namespace,
		mutexMap:         make(map[string]*sync.Mutex),
		mutexMapMutex:    sync.RWMutex{},
		volumeMap:        make(map[string]string),
		volumeMapMutex:   sync.RWMutex{},
		cleanUpMap:       make(map[string]context.CancelFunc),
		cleanUpMapMutex:  sync.RWMutex{},
		cloudProvisioner: cloudProvisioner,
	}

	c, err := controller.New("azvolumeattachment-controller", mgr, controller.Options{
		MaxConcurrentReconciles: 10,
		Reconciler:              &reconciler,
		Log:                     mgr.GetLogger().WithValues("controller", "azvolumeattachment"),
	})

	if err != nil {
		klog.Errorf("failed to create a new azvolumeattachment controller: %v", err)
		return nil, err
	}

	// Watch for CRUD events on azVolumeAttachment objects
	err = c.Watch(&source.Kind{Type: &v1alpha1.AzVolumeAttachment{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		klog.Errorf("failed to initialize watch for azvolumeattachment object: %v", err)
		return nil, err
	}

	klog.V(2).Info("AzVolumeAttachment Controller successfully initialized.")
	return &reconciler, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
