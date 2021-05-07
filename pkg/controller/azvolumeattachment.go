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
	"sync"
	"time"

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

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	// 1. AzVolumeAttachmentFinalizer for AzVolumeAttachment objects handles deletion of AzVolumeAttachment CRIs
	// 2. AzVolumeAttachmentFinalizer for AzVolume prevents AzVolume CRI from being deleted before all AzVolumeAttachments attached to that volume is deleted as well
	AzVolumeAttachmentFinalizer = "disk.csi.azure.com/azvolumeattachment-finalizer"
	NodeNameLabel               = "node-name"
	VolumeNameLabel             = "volume-name"
	defaultNumSyncWorkers       = 10
	// defaultMaxReplicaUpdateCount refers to the maximum number of creation or deletion of AzVolumeAttachment objects in a single ManageReplica call
	defaultMaxReplicaUpdateCount = 1
	defaultTimeUntilDeletion     = time.Duration(5) * time.Minute
)

type Event int

const (
	AttachEvent Event = iota
	DetachEvent
	DeleteEvent
	PVCDeleteEvent
	SyncEvent
)

type reconcileAzVolumeAttachment struct {
	client         client.Client
	azVolumeClient azVolumeClientSet.Interface
	namespace      string

	cloudProvisioner CloudProvisioner

	// syncMutex is used to prevent other syncVolume calls to be performed during SyncAll routine
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

var _ reconcile.Reconciler = &reconcileAzVolumeAttachment{}

func (r *reconcileAzVolumeAttachment) Reconcile(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	return r.HandleAzVolumeAttachmentEvent(ctx, request)
}

func (r *reconcileAzVolumeAttachment) HandleAzVolumeAttachmentEvent(ctx context.Context, request reconcile.Request) (reconcile.Result, error) {
	var azVolumeAttachment v1alpha1.AzVolumeAttachment
	err := r.client.Get(ctx, request.NamespacedName, &azVolumeAttachment)
	// if object is not found, it means the object has been deleted. Log the deletion and do not requeue
	if errors.IsNotFound(err) {
		klog.Infof("AzVolumeAttachment (%s) has successfully been deleted.", request.Name)
		r.volumeMapMutex.RLock()
		underlyingVolume, ok := r.volumeMap[request.Name]
		r.volumeMapMutex.RUnlock()
		if ok {
			if err = r.ManageReplicas(ctx, underlyingVolume, DeleteEvent, false); err != nil {
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

	// this is a creation event
	if azVolumeAttachment.Status == nil {
		// attach the volume to the specified node
		klog.Infof("Initiating Attach operation for AzVolumeAttachment (%s)", azVolumeAttachment.Name)
		if err := r.triggerAttach(ctx, azVolumeAttachment.Name); err != nil {
			klog.Errorf("failed to attach AzVolumeAttachment (%s): %v", azVolumeAttachment.Name, err)
			return reconcile.Result{Requeue: true}, err
		}
		// if the azVolumeAttachment's deletion timestamp has been set, and is before the current time, detach the disk from the node and delete the finalizer
	} else if now := metav1.Now(); azVolumeAttachment.ObjectMeta.DeletionTimestamp.Before(&now) {
		klog.Infof("Initiating Detach operation for AzVolumeAttachment (%s)", azVolumeAttachment.Name)
		if err := r.triggerDetach(ctx, azVolumeAttachment.Name); err != nil {
			// if detach failed, requeue the request
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", azVolumeAttachment.Name, err)
			return reconcile.Result{Requeue: true}, err
		}
		// if the role in status and spec are different, it is an update event where replica should be turned into a primary
	} else if azVolumeAttachment.Spec.RequestedRole != azVolumeAttachment.Status.Role {
		klog.Infof("Promoting AzVolumeAttachment (%s) from replica to primary", azVolumeAttachment.Name)
		if err := r.UpdateStatus(ctx, azVolumeAttachment.Name, azVolumeAttachment.Status.PublishContext); err != nil {
			klog.Errorf("failed to promote AzVolumeAttachment (%s) from replica to primary: %v", azVolumeAttachment.Name, err)
			return reconcile.Result{Requeue: true}, err
		}
	}
	return reconcile.Result{}, nil
}

func (r *reconcileAzVolumeAttachment) GetReplicaCount(ctx context.Context, underlyingVolume string) (int, error) {
	// get azVolume for the specified underlying volume
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: underlyingVolume}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", underlyingVolume, err)
		return 0, err
	}

	return azVolume.Spec.MaxMountReplicaCount, nil
}

// ManageAttachmentsForVolume will be runing on a separate channel
func (r *reconcileAzVolumeAttachment) SyncAll(ctx context.Context) error {
	r.syncMutex.Lock()
	defer r.syncMutex.Unlock()

	// Get all AzVolume
	azVolumes, err := r.azVolumeClient.DiskV1alpha1().AzVolumes(r.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("failed to list AzVolumes: %v", err)
		return err
	}

	numWorkers := defaultNumSyncWorkers
	if numWorkers > len(azVolumes.Items) {
		numWorkers = len(azVolumes.Items)
	}

	type resultStruct struct {
		err    error
		volume string
	}
	results := make(chan resultStruct, len(azVolumes.Items))
	workerControl := make(chan struct{}, numWorkers)
	defer close(results)
	defer close(workerControl)

	// Sync all volumes and reset mutexMap in case some volumes had been deleted
	r.mutexMapMutex.Lock()
	r.mutexMap = make(map[string]*sync.Mutex)
	for _, azVolume := range azVolumes.Items {
		r.mutexMap[azVolume.Spec.UnderlyingVolume] = &sync.Mutex{}
		workerControl <- struct{}{}
		go func(azv v1alpha1.AzVolume) {
			results <- resultStruct{err: r.SyncVolume(ctx, azv, SyncEvent, false, false), volume: azv.Name}
			<-workerControl
		}(azVolume)
	}
	r.mutexMapMutex.Unlock()

	// Collect results
	for range azVolumes.Items {
		result := <-results
		if result.err != nil {
			klog.Errorf("failed in process of syncing AzVolume (%s): %v", result.volume, err)
		}
	}
	return nil
}

func (r *reconcileAzVolumeAttachment) SyncVolume(ctx context.Context, azVolume v1alpha1.AzVolume, eventType Event, isPrimary, useCache bool) error {
	// this is to prevent multiple sync volume operation to be performed on a single volume concurrently as it can create or delete more attachments than necessary
	r.mutexMapMutex.RLock()
	volMutex, ok := r.mutexMap[azVolume.Spec.UnderlyingVolume]
	r.mutexMapMutex.RUnlock()
	if ok {
		volMutex.Lock()
		defer volMutex.Unlock()
	}

	// fetch AzVolumeAttachment with AzVolume
	volRequirement, _ := labels.NewRequirement(VolumeNameLabel, selection.Equals, []string{azVolume.Spec.UnderlyingVolume})
	labelSelector := labels.NewSelector().Add(*volRequirement)
	var azVolumeAttachments *v1alpha1.AzVolumeAttachmentList
	var err error

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
		// If primary detachment event, set deletion timestamp
	} else if eventType == DetachEvent && azVolume.Spec.MaxMountReplicaCount > 0 {
		if isPrimary {
			r.cleanUpMapMutex.Lock()
			emptyCtx := context.TODO()
			deletionCtx, cancelFunc := context.WithCancel(emptyCtx)
			r.cleanUpMap[azVolume.Name] = cancelFunc
			r.cleanUpMapMutex.Unlock()

			go func(ctx context.Context) {
				// Sleep
				time.Sleep(defaultTimeUntilDeletion)
				_ = CleanUpAzVolumeAttachment(ctx, r.client, r.azVolumeClient, r.namespace, azVolume.Name)
			}(deletionCtx)
		}
		return nil
	}

	desiredAttachmentCount, currentAttachmentCount := azVolume.Spec.MaxMountReplicaCount+1, len(azVolumeAttachments.Items)
	klog.Infof("control number of attachments for volume (%s): desired=%d,\tcurrent:%d", azVolume.Spec.UnderlyingVolume, desiredAttachmentCount, currentAttachmentCount)

	// if there is no AzVolumeAttachment object for the specified underlying volume, remove AzVolumeAttachment finalizer from AzVolume
	if currentAttachmentCount == 0 {
		if err := r.DeleteFinalizerFromAzVolume(ctx, azVolume.Name); err != nil {
			return err
		}
	}

	// if the azVolume is marked deleted, do not create more azvolume attachment objects
	now := metav1.Now()
	azVolumeDeleted := azVolume.DeletionTimestamp.Before(&now)

	if !azVolumeDeleted && desiredAttachmentCount > currentAttachmentCount {
		klog.Infof("Create %d more replicas for volume (%s)", desiredAttachmentCount-currentAttachmentCount, azVolume.Spec.UnderlyingVolume)
		if err = r.CreateReplicas(ctx, min(defaultMaxReplicaUpdateCount, desiredAttachmentCount-currentAttachmentCount), azVolume.Spec.UnderlyingVolume, useCache); err != nil {
			klog.Errorf("failed to create %d replicas for volume (%s): %v", desiredAttachmentCount-currentAttachmentCount, azVolume.Spec.UnderlyingVolume, err)
			return err
		}
	} else if desiredAttachmentCount < currentAttachmentCount {
		klog.Infof("Delete %d replicas for volume (%s)", currentAttachmentCount-desiredAttachmentCount, azVolume.Spec.UnderlyingVolume)
		i := 0
		for _, azVolumeAttachment := range azVolumeAttachments.Items {
			if i >= min(defaultMaxReplicaUpdateCount, currentAttachmentCount-desiredAttachmentCount) {
				break
			}
			// if the volume has not yet been attached to any node or is a primary node, skip
			if azVolumeAttachment.Spec.RequestedRole == v1alpha1.PrimaryRole || azVolumeAttachment.Status == nil {
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

func (r *reconcileAzVolumeAttachment) ManageReplicas(ctx context.Context, underlyingVolume string, eventType Event, isPrimary bool) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: underlyingVolume}, &azVolume); err != nil {
		// if AzVolume is not found, the volume is deleted, so do not requeue and do not return error
		// AzVolumeAttachment objects for the volume will be triggered to be deleted.
		if errors.IsNotFound(err) {
			return nil
		}
		klog.Errorf("failed to get AzVolume (%s): %v", underlyingVolume, err)
		return err
	}

	// manage replica calls should not block each other but should block sync all calls and vice versa
	r.syncMutex.RLock()
	defer r.syncMutex.RUnlock()
	return r.SyncVolume(ctx, azVolume, eventType, isPrimary, true)
}

func (r *reconcileAzVolumeAttachment) CreateReplicas(ctx context.Context, numReplica int, underlyingVolume string, useCache bool) error {
	// if volume is scheduled for clean up, skip replica creation
	r.cleanUpMapMutex.Lock()
	_, cleanUpScheduled := r.cleanUpMap[underlyingVolume]
	r.cleanUpMapMutex.Unlock()

	if cleanUpScheduled {
		return nil
	}

	nodes, err := r.GetNodesForReplica(ctx, numReplica, underlyingVolume, false, useCache)
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
					NodeNameLabel:   node.azDriverNode.Name,
					VolumeNameLabel: underlyingVolume,
				},
			},
			Spec: v1alpha1.AzVolumeAttachmentSpec{
				NodeName:         node.azDriverNode.Spec.NodeName,
				UnderlyingVolume: underlyingVolume,
				RequestedRole:    v1alpha1.ReplicaRole,
			},
		}, &client.CreateOptions{})

		if err != nil {
			klog.Errorf("failed to create replica azVolumeAttachment for volume %s: %v", underlyingVolume, err)
			return err
		}
	}
	return nil
}

func (r *reconcileAzVolumeAttachment) GetNodesForReplica(ctx context.Context, numReplica int, underlyingVolume string, reverse, useCache bool) ([]filteredNode, error) {
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
			volRequirement, _ := labels.NewRequirement(VolumeNameLabel, selection.Equals, []string{underlyingVolume})
			nodeRequirement, _ := labels.NewRequirement(NodeNameLabel, selection.Equals, []string{string(node.Name)})
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

// Deprecated
func (r *reconcileAzVolumeAttachment) ListReplicasByVolume(ctx context.Context, volume string) ([]v1alpha1.AzVolumeAttachment, error) {
	replicas := []v1alpha1.AzVolumeAttachment{}
	attachments, err := r.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(r.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("failed to get attachment list for namespace %s: %v", r.namespace, err)
		return replicas, err
	}
	if attachments != nil {
		for _, attachment := range attachments.Items {
			if attachment.Spec.UnderlyingVolume == volume && attachment.Spec.RequestedRole == v1alpha1.ReplicaRole {
				replicas = append(replicas, attachment)
			}
		}
	}
	return replicas, nil
}

func (r *reconcileAzVolumeAttachment) ListAzVolumeAttachmentsByNodeName(ctx context.Context, nodeName string) ([]v1alpha1.AzVolumeAttachment, error) {
	filteredAttachments := []v1alpha1.AzVolumeAttachment{}
	attachments, err := r.azVolumeClient.DiskV1alpha1().AzVolumeAttachments(r.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		klog.Errorf("failed to get attachment list for namespace %s: %v", r.namespace, err)
		return filteredAttachments, err
	}
	if attachments != nil {
		for _, attachment := range attachments.Items {
			if attachment.Spec.NodeName == nodeName {
				filteredAttachments = append(filteredAttachments, attachment)
			}
		}
	}
	return filteredAttachments, nil
}

func (r *reconcileAzVolumeAttachment) InitializeMeta(ctx context.Context, attachmentName string) error {
	var azVolumeAttachment v1alpha1.AzVolumeAttachment
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: attachmentName}, &azVolumeAttachment); err != nil {
		klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, err)
		return err
	}

	// if the required metadata already exists return
	if finalizerExists(azVolumeAttachment.Finalizers, AzVolumeAttachmentFinalizer) && labelExists(azVolumeAttachment.Labels, NodeNameLabel) && labelExists(azVolumeAttachment.Labels, VolumeNameLabel) {
		return nil
	}

	patched := azVolumeAttachment.DeepCopy()

	// add finalizer
	if patched.Finalizers == nil {
		patched.Finalizers = []string{}
	}

	if !finalizerExists(azVolumeAttachment.Finalizers, AzVolumeAttachmentFinalizer) {
		patched.Finalizers = append(patched.Finalizers, AzVolumeAttachmentFinalizer)
	}

	// add label
	if patched.Labels == nil {
		patched.Labels = make(map[string]string)
	}
	patched.Labels[NodeNameLabel] = azVolumeAttachment.Spec.NodeName
	patched.Labels[VolumeNameLabel] = azVolumeAttachment.Spec.UnderlyingVolume

	if err := r.client.Patch(ctx, patched, client.MergeFrom(&azVolumeAttachment)); err != nil {
		klog.Errorf("failed to initialize finalizer (%s) for AzVolumeAttachment (%s): %v", AzVolumeAttachmentFinalizer, patched.Name, err)
		return err
	}

	klog.Infof("successfully added finalizer (%s) to AzVolumeAttachment (%s)", AzVolumeAttachmentFinalizer, attachmentName)
	return nil
}

func (r *reconcileAzVolumeAttachment) DeleteFinalizer(ctx context.Context, attachmentName string) error {
	var azVolumeAttachment v1alpha1.AzVolumeAttachment
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: attachmentName}, &azVolumeAttachment); err != nil {
		klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, err)
		return err
	}
	updated := azVolumeAttachment.DeepCopy()
	if updated.ObjectMeta.Finalizers == nil {
		return nil
	}

	finalizers := []string{}
	for _, finalizer := range updated.ObjectMeta.Finalizers {
		if finalizer == AzVolumeAttachmentFinalizer {
			continue
		}
		finalizers = append(finalizers, finalizer)
	}
	updated.ObjectMeta.Finalizers = finalizers
	if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to delete finalizer (%s) for AzVolumeAttachment (%s): %v", AzVolumeAttachmentFinalizer, updated.Name, err)
		return err
	}
	klog.Infof("successfully deleted finalizer (%s) from AzVolumeAttachment (%s)", AzVolumeAttachmentFinalizer, attachmentName)
	return nil
}

func (r *reconcileAzVolumeAttachment) AddFinalizerToAzVolume(ctx context.Context, volumeName string) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	updated := azVolume.DeepCopy()

	if updated.Finalizers == nil {
		updated.Finalizers = []string{}
	}

	if finalizerExists(updated.Finalizers, AzVolumeAttachmentFinalizer) {
		return nil
	}

	updated.Finalizers = append(updated.Finalizers, AzVolumeAttachmentFinalizer)
	if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to add finalizer (%s) to AzVolume(%s): %v", AzVolumeAttachmentFinalizer, updated.Name, err)
		return err
	}
	klog.Infof("successfully added finalizer (%s) to AzVolume (%s)", AzVolumeAttachmentFinalizer, updated.Name)
	return nil
}

func (r *reconcileAzVolumeAttachment) DeleteFinalizerFromAzVolume(ctx context.Context, volumeName string) error {
	var azVolume v1alpha1.AzVolume
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: volumeName}, &azVolume); err != nil {
		if errors.IsNotFound(err) {
			return nil
		}
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	updated := azVolume.DeepCopy()
	updatedFinalizers := []string{}

	for _, finalizer := range updated.Finalizers {
		if finalizer == AzVolumeAttachmentFinalizer {
			continue
		}
		updatedFinalizers = append(updatedFinalizers, finalizer)
	}
	updated.Finalizers = updatedFinalizers

	if err := r.client.Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to delete finalizer (%s) from AzVolume(%s): %v", AzVolumeAttachmentFinalizer, updated.Name, err)
		return err
	}
	klog.Infof("successfully deleted finalizer (%s) from AzVolume (%s)", AzVolumeAttachmentFinalizer, updated.Name)
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

func (r *reconcileAzVolumeAttachment) triggerAttach(ctx context.Context, attachmentName string) error {
	var azVolumeAttachment v1alpha1.AzVolumeAttachment
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: attachmentName}, &azVolumeAttachment); err != nil {
		klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, err)
		return err
	}

	response, err := r.attachVolume(ctx, azVolumeAttachment.Spec.UnderlyingVolume, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Spec.VolumeContext)
	if err != nil {
		klog.Errorf("failed to attach volume %s to node %s: %v", azVolumeAttachment.Spec.UnderlyingVolume, azVolumeAttachment.Spec.NodeName, err)
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

	// Initialize finalizer and add label to the object
	if err := r.InitializeMeta(ctx, azVolumeAttachment.Name); err != nil {
		return err
	}

	if err := r.AddFinalizerToAzVolume(ctx, azVolumeAttachment.Spec.UnderlyingVolume); err != nil {
		return err
	}

	// Update status of the object
	if err := r.UpdateStatus(ctx, azVolumeAttachment.Name, response); err != nil {
		return err
	}
	klog.Infof("successfully attached volume (%s) to node (%s) and update status of AzVolumeAttachment (%s)", azVolumeAttachment.Spec.UnderlyingVolume, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Name)
	return nil
}

func (r *reconcileAzVolumeAttachment) triggerDetach(ctx context.Context, attachmentName string) error {
	var azVolumeAttachment v1alpha1.AzVolumeAttachment
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: attachmentName}, &azVolumeAttachment); err != nil {
		klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, err)
		return err
	}

	if err := r.detachVolume(ctx, azVolumeAttachment.Spec.UnderlyingVolume, azVolumeAttachment.Spec.NodeName); err != nil {
		klog.Errorf("failed to detach volume %s from node %s: %v", azVolumeAttachment.Spec.UnderlyingVolume, azVolumeAttachment.Spec.NodeName, err)
		return err
	}

	if err := r.ManageReplicas(ctx, azVolumeAttachment.Spec.UnderlyingVolume, DetachEvent, azVolumeAttachment.Spec.RequestedRole == v1alpha1.PrimaryRole); err != nil {
		klog.Errorf("failed to manage replicas for volume (%s): %v", azVolumeAttachment.Spec.UnderlyingVolume, err)
		return err
	}

	// If above procedures were successful, remove finalizer from the object
	if err := r.DeleteFinalizer(ctx, azVolumeAttachment.Name); err != nil {
		klog.Errorf("failed to delete finalizer %s for azvolumeattachment %s: %v", AzVolumeAttachmentFinalizer, azVolumeAttachment.Name, err)
		return err
	}

	// add (azVolumeAttachment, underlyingVolume) to volumeMap so that a replacement replica can be created in next iteration of reconciliation
	r.volumeMapMutex.Lock()
	r.volumeMap[azVolumeAttachment.Name] = azVolumeAttachment.Spec.UnderlyingVolume
	r.volumeMapMutex.Unlock()

	klog.Infof("successfully detached volume %s from node %s and deleted %s", azVolumeAttachment.Spec.UnderlyingVolume, azVolumeAttachment.Spec.NodeName, azVolumeAttachment.Name)
	return nil
}

func (r *reconcileAzVolumeAttachment) UpdateStatus(ctx context.Context, attachmentName string, status map[string]string) error {
	var azVolumeAttachment v1alpha1.AzVolumeAttachment
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: attachmentName}, &azVolumeAttachment); err != nil {
		klog.Errorf("failed to get AzVolumeAttachment (%s): %v", attachmentName, err)
		return err
	}

	// Create replica if necessary
	if err := r.ManageReplicas(ctx, azVolumeAttachment.Spec.UnderlyingVolume, AttachEvent, azVolumeAttachment.Spec.RequestedRole == v1alpha1.PrimaryRole); err != nil {
		klog.Errorf("failed creating replicas for AzVolume (%s): %v")
		return err
	}

	updated := azVolumeAttachment.DeepCopy()
	updated.Status = azVolumeAttachment.Status.DeepCopy()
	if updated.Status == nil {
		updated.Status = &v1alpha1.AzVolumeAttachmentStatus{}
	}
	updated.Status.Role = azVolumeAttachment.Spec.RequestedRole
	updated.Status.PublishContext = status

	if err := r.client.Status().Update(ctx, updated, &client.UpdateOptions{}); err != nil {
		klog.Errorf("failed to update status of AzVolumeAttachment (%s): %v", attachmentName, err)
		return err
	}

	return nil
}

func (r *reconcileAzVolumeAttachment) CleanUpAzVolumeAttachment(ctx context.Context, azVolumeName string) error {
	var azVolume v1alpha1.AzVolume
	err := r.client.Get(ctx, types.NamespacedName{Namespace: r.namespace, Name: azVolumeName}, &azVolume)
	if err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", azVolumeName, err)
		return err
	}

	volRequirement, _ := labels.NewRequirement(VolumeNameLabel, selection.Equals, []string{azVolume.Spec.UnderlyingVolume})
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

	klog.Infof("successfully deleted AzVolumeAttachments for AzVolume (%s)", azVolume.Name)
	return nil
}

func (r *reconcileAzVolumeAttachment) attachVolume(ctx context.Context, volume, node string, volumeContext map[string]string) (map[string]string, error) {
	return r.cloudProvisioner.PublishVolume(ctx, volume, node, volumeContext)
}

func (r *reconcileAzVolumeAttachment) detachVolume(ctx context.Context, volume, node string) error {
	return r.cloudProvisioner.UnpublishVolume(ctx, volume, node)
}

func (r *reconcileAzVolumeAttachment) GetDiskInfo(ctx context.Context, volume string) (diskURI, diskName string, err error) {
	// Get Disk URI
	var pv corev1.PersistentVolume
	err = r.client.Get(ctx, types.NamespacedName{Name: volume}, &pv)
	if err != nil {
		klog.Errorf("failed to find a pv (%s): %v", volume)
		return
	}
	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != azureutils.DriverName {
		err = status.Errorf(codes.InvalidArgument, "pv (%s) needs to be a azure disk CSI driver", volume)
		klog.Errorf("%v", err)
		return
	}
	diskURI = pv.Spec.CSI.VolumeHandle

	// TODO uncomment below when controller provisioner PR is merged
	/*
		// Check if it is a valid disk URI
		if err = controllerProvisioner.ValidateDiskURI(diskURI); err != nil {
			klog.Errorf("diskURI (%s) is not in a valid format: %v", diskURI, err)
			return
		}

		// Get disk name
		diskName, err = controllerProvisioner.GetDiskNameFromDiskURI(diskURI)
		if err != nil {
			klog.Errorf("diskName could not fetched from the diskURI (%s): %v", diskURI, err)
			return
		}
	*/
	return
}

func NewAzVolumeAttachmentController(ctx context.Context, mgr manager.Manager, azVolumeClient *azVolumeClientSet.Interface, namespace string, cloudProvisioner CloudProvisioner) error {
	reconciler := reconcileAzVolumeAttachment{
		client:           mgr.GetClient(),
		azVolumeClient:   *azVolumeClient,
		namespace:        namespace,
		syncMutex:        sync.RWMutex{},
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
		return err
	}

	// Watch for CRUD events on azVolumeAttachment objects
	err = c.Watch(&source.Kind{Type: &v1alpha1.AzVolumeAttachment{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		klog.Errorf("failed to initialize watch for azvolumeattachment object: %v", err)
		return err
	}

	// initial sync
	err = reconciler.SyncAll(ctx)
	if err != nil {
		klog.Warningf("failed to complete initial AzVolumeAttachment sync: %v", err)
	}

	klog.V(2).Info("AzVolumeAttachment Controller successfully initialized.")
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
