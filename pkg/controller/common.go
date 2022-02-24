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
	"container/list"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2021-07-01/compute"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/klog/v2"
	diskv1alpha2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha2"
	azClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"

	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/kubernetes"
	cache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/kubernetes/pkg/features"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

const (
	DefaultTimeUntilGarbageCollection = time.Duration(5) * time.Minute

	maxRetry             = 10
	defaultRetryDuration = time.Duration(1) * time.Second
	defaultRetryFactor   = 5.0
	defaultRetrySteps    = 5

	cloudTimeout = time.Duration(5) * time.Minute
)

type operationRequester string

const (
	azdrivernode     operationRequester = "azdrivernode-controller"
	azvolume         operationRequester = "azvolume-controller"
	replica          operationRequester = "replica-controller"
	nodeavailability operationRequester = "nodeavailability-controller"
	pod                                 = "pod-controller"
)

type labelPair struct {
	key   string
	entry string
}

type cleanUpMode int

const (
	deleteCRIOnly cleanUpMode = iota
	detachAndDeleteCRI
)

type roleMode int

const (
	primaryOnly roleMode = iota
	replicaOnly
	all
)

type updateMode int

const (
	normalUpdate updateMode = iota
	forceUpdate
)

var roles = map[roleMode]string{
	primaryOnly: string(diskv1alpha2.PrimaryRole),
	replicaOnly: string(diskv1alpha2.ReplicaRole),
}

type updateWithLock bool

const (
	acquireLock updateWithLock = true
	skipLock    updateWithLock = false
)

// TODO Make CloudProvisioner independent of csi types.
type CloudProvisioner interface {
	CreateVolume(
		ctx context.Context,
		volumeName string,
		capacityRange *diskv1alpha2.CapacityRange,
		volumeCapabilities []diskv1alpha2.VolumeCapability,
		parameters map[string]string,
		secrets map[string]string,
		volumeContentSource *diskv1alpha2.ContentVolumeSource,
		accessibilityTopology *diskv1alpha2.TopologyRequirement) (*diskv1alpha2.AzVolumeStatusDetail, error)
	DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error
	PublishVolume(ctx context.Context, volumeID string, nodeID string, volumeContext map[string]string) (map[string]string, error)
	UnpublishVolume(ctx context.Context, volumeID string, nodeID string) error
	ExpandVolume(ctx context.Context, volumeID string, capacityRange *diskv1alpha2.CapacityRange, secrets map[string]string) (*diskv1alpha2.AzVolumeStatusDetail, error)
	ListVolumes(ctx context.Context, maxEntries int32, startingToken string) (*diskv1alpha2.ListVolumesResult, error)
	CreateSnapshot(ctx context.Context, sourceVolumeID string, snapshotName string, secrets map[string]string, parameters map[string]string) (*diskv1alpha2.Snapshot, error)
	ListSnapshots(ctx context.Context, maxEntries int32, startingToken string, sourceVolumeID string, snapshotID string, secrets map[string]string) (*diskv1alpha2.ListSnapshotsResult, error)
	DeleteSnapshot(ctx context.Context, snapshotID string, secrets map[string]string) error
	CheckDiskExists(ctx context.Context, diskURI string) (*compute.Disk, error)
	GetCloud() *provider.Cloud
	GetMetricPrefix() string
}

type replicaOperation struct {
	requester                  operationRequester
	operationFunc              func() error
	isReplicaGarbageCollection bool
}

type operationQueue struct {
	*list.List
	gcExclusionList set
}

func newOperationQueue() *operationQueue {
	return &operationQueue{
		gcExclusionList: set{},
		List:            list.New(),
	}
}

type retryInfoEntry struct {
	backoff   *wait.Backoff
	retryLock *sync.Mutex
}

type retryInfo struct {
	retryMap *sync.Map
}

func newRetryInfo() *retryInfo {
	return &retryInfo{
		retryMap: &sync.Map{},
	}
}

func newRetryEntry() *retryInfoEntry {
	return &retryInfoEntry{
		retryLock: &sync.Mutex{},
		backoff:   &wait.Backoff{Duration: defaultRetryDuration, Factor: defaultRetryFactor, Steps: defaultRetrySteps},
	}
}

func (r *retryInfo) nextRequeue(objectName string) time.Duration {
	v, _ := r.retryMap.LoadOrStore(objectName, newRetryEntry())
	entry := v.(*retryInfoEntry)
	entry.retryLock.Lock()
	defer entry.retryLock.Unlock()
	return entry.backoff.Step()
}

func (r *retryInfo) deleteEntry(objectName string) {
	r.retryMap.Delete(objectName)
}

type emptyType struct{}

type set map[interface{}]emptyType

func (s set) add(entry interface{}) {
	s[entry] = emptyType{}
}

func (s set) has(entry interface{}) bool {
	_, ok := s[entry]
	return ok
}

func (s set) remove(entry interface{}) {
	delete(s, entry)
}

type lockableEntry struct {
	sync.RWMutex
	entry interface{}
}

func newLockableEntry(entry interface{}) *lockableEntry {
	return &lockableEntry{
		RWMutex: sync.RWMutex{},
		entry:   entry,
	}
}

type SharedState struct {
	driverName                    string
	objectNamespace               string
	topologyKey                   string
	podToClaimsMap                sync.Map
	podToInlineMap                sync.Map
	claimToPodsMap                sync.Map
	volumeToClaimMap              sync.Map
	claimToVolumeMap              sync.Map
	podLocks                      sync.Map
	visitedVolumes                sync.Map
	volumeOperationQueues         sync.Map
	cleanUpMap                    sync.Map
	priorityReplicaRequestsQueue  *VolumeReplicaRequestsPriorityQueue
	processingReplicaRequestQueue int32
	eventRecorder                 record.EventRecorder
	cachedClient                  client.Client
	azClient                      azClientSet.Interface
	kubeClient                    kubernetes.Interface
}

func NewSharedState(driverName, objectNamespace, topologyKey string, eventRecorder record.EventRecorder, cachedClient client.Client, azClient azClientSet.Interface, kubeClient kubernetes.Interface) *SharedState {
	newSharedState := &SharedState{driverName: driverName, objectNamespace: objectNamespace, topologyKey: topologyKey, eventRecorder: eventRecorder, cachedClient: cachedClient, azClient: azClient, kubeClient: kubeClient}
	newSharedState.createReplicaRequestsQueue()
	return newSharedState
}

func (c *SharedState) createOperationQueue(volumeName string) {
	_, _ = c.volumeOperationQueues.LoadOrStore(volumeName, newLockableEntry(newOperationQueue()))
}

func (c *SharedState) addToOperationQueue(volumeName string, requester operationRequester, operationFunc func() error, isReplicaGarbageCollection bool) {
	v, ok := c.volumeOperationQueues.Load(volumeName)
	if !ok {
		return
	}
	lockable := v.(*lockableEntry)
	lockable.Lock()

	isFirst := lockable.entry.(*operationQueue).Len() == 0
	_ = lockable.entry.(*operationQueue).PushBack(&replicaOperation{
		requester:                  requester,
		operationFunc:              operationFunc,
		isReplicaGarbageCollection: isReplicaGarbageCollection,
	})
	lockable.Unlock()

	// if first operation, start goroutine
	if isFirst {
		go func() {
			lockable.Lock()
			for {
				operationQueue := lockable.entry.(*operationQueue)
				// pop the first operation
				front := operationQueue.Front()
				operation := front.Value.(*replicaOperation)
				lockable.Unlock()

				// only run the operation if the operation requester is not enlisted in blacklist
				if !operationQueue.gcExclusionList.has(operation.requester) {
					if err := operation.operationFunc(); err != nil {
						klog.Error(err)
						if !operation.isReplicaGarbageCollection || !errors.Is(err, context.Canceled) {
							// if failed, push it to the end of the queue
							lockable.Lock()
							operationQueue.PushBack(operation)
							lockable.Unlock()
						}
					}
				}

				lockable.Lock()
				operationQueue.Remove(front)
				// if there is no entry remaining, exit the loop
				if operationQueue.Front() == nil {
					break
				}
			}
			lockable.Unlock()
			klog.Infof("operation queue for volume (%s) fully iterated and completed.", volumeName)
		}()
	}
}

func (c *SharedState) deleteOperationQueue(volumeName string) {
	v, ok := c.volumeOperationQueues.LoadAndDelete(volumeName)
	// if operation queue has already been deleted, return
	if !ok {
		return
	}
	// clear the queue in case, there still is an entry in queue
	lockable := v.(*lockableEntry)
	lockable.Lock()
	lockable.entry.(*operationQueue).Init()
	lockable.Unlock()
}

func (c *SharedState) createReplicaRequestsQueue() {
	c.priorityReplicaRequestsQueue = &VolumeReplicaRequestsPriorityQueue{}
	c.priorityReplicaRequestsQueue.queue = cache.NewHeap(
		func(obj interface{}) (string, error) {
			return obj.(*ReplicaRequest).VolumeName, nil
		},
		func(left, right interface{}) bool {
			return left.(*ReplicaRequest).Priority > right.(*ReplicaRequest).Priority
		})
}

func (c *SharedState) overrideAndClearOperationQueue(volumeName string) func() {
	v, ok := c.volumeOperationQueues.Load(volumeName)
	if !ok {
		return nil
	}
	lockable := v.(*lockableEntry)

	lockable.Lock()
	lockable.entry.(*operationQueue).Init()
	return lockable.Unlock
}

func (c *SharedState) addToGcExclusionList(volumeName string, target operationRequester) {
	v, ok := c.volumeOperationQueues.Load(volumeName)
	if !ok {
		return
	}
	lockable := v.(*lockableEntry)
	lockable.Lock()
	lockable.entry.(*operationQueue).gcExclusionList.add(volumeName)
	lockable.Unlock()
}

func (c *SharedState) removeFromExclusionList(volumeName string, target operationRequester) {
	v, ok := c.volumeOperationQueues.Load(volumeName)
	if !ok {
		return
	}
	lockable := v.(*lockableEntry)
	lockable.Lock()
	delete(lockable.entry.(*operationQueue).gcExclusionList, volumeName)
	lockable.Unlock()
}

func (c *SharedState) dequeueGarbageCollection(volumeName string) {
	v, ok := c.volumeOperationQueues.Load(volumeName)
	if !ok {
		return
	}
	lockable := v.(*lockableEntry)
	lockable.Lock()
	queue := lockable.entry.(*operationQueue)
	// look for garbage collection operation in the queue and remove from queue
	for cur := queue.Front(); cur != nil; cur = cur.Next() {
		if cur.Value.(*replicaOperation).isReplicaGarbageCollection {
			_ = queue.Remove(cur)
		}
	}
	lockable.Unlock()
}

func (c *SharedState) getVolumesFromPod(podName string) ([]string, error) {
	var claims []string
	klog.V(5).Infof("Getting requested volumes for pod %s.", podName)
	value, ok := c.podToClaimsMap.Load(podName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "unable to find an entry for pod (%s) in podToClaims map", podName)
	}
	claims, ok = value.([]string)
	if !ok {
		return nil, status.Errorf(codes.Internal, "wrong output type: expected []string")
	}

	volumes := []string{}
	for _, claim := range claims {
		value, ok := c.claimToVolumeMap.Load(claim)
		if !ok {
			// the pvc entry is not an azure resource
			klog.V(5).Infof("Requested volume %s for pod %s is not an azure resource", value, podName)
			continue
		}
		volume, ok := value.(string)
		klog.V(5).Infof("Requested volume %s for pod %s is an azure resource", value, podName)
		if !ok {
			return nil, status.Errorf(codes.Internal, "wrong output type: expected string")
		}
		volumes = append(volumes, volume)
		klog.V(5).Infof("Requested volumes for pod %s are now the following: Volumes: %v, Len: %d", podName, volumes, len(volumes))
	}
	return volumes, nil
}

func (c *SharedState) getPodsFromVolume(ctx context.Context, client client.Client, volumeName string) ([]v1.Pod, error) {
	pods, err := c.getPodNamesFromVolume(volumeName)
	if err != nil {
		return nil, err
	}
	podObjs := []v1.Pod{}
	for _, pod := range pods {
		namespace, name := parseQualifiedName(pod)
		var podObj v1.Pod
		if err := client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &podObj); err != nil {
			return nil, err
		}
		podObjs = append(podObjs, podObj)
	}
	return podObjs, nil
}

func (c *SharedState) getPodNamesFromVolume(volumeName string) ([]string, error) {
	v, ok := c.volumeToClaimMap.Load(volumeName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no bound persistent volume claim was found for AzVolume (%s)", volumeName)
	}
	claimName, ok := v.(string)
	if !ok {
		return nil, status.Errorf(codes.Internal, "volumeToClaimMap should should hold string")
	}

	value, ok := c.claimToPodsMap.Load(claimName)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "no pods found for PVC (%s)", claimName)
	}
	lockable, ok := value.(*lockableEntry)
	if !ok {
		return nil, status.Errorf(codes.Internal, "claimToPodsMap should hold lockable entry")
	}

	lockable.RLock()
	defer lockable.RUnlock()

	podMap, ok := lockable.entry.(set)
	if !ok {
		return nil, status.Errorf(codes.Internal, "claimToPodsMap entry should hold a set")
	}

	pods := make([]string, len(podMap))
	i := 0
	for v := range podMap {
		pod := v.(string)
		pods[i] = pod
		i++
	}

	return pods, nil
}

func (c *SharedState) getVolumesForPodObjs(pods []v1.Pod) ([]string, error) {
	volumes := []string{}
	for _, pod := range pods {
		podVolumes, err := c.getVolumesFromPod(getQualifiedName(pod.Namespace, pod.Name))
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, podVolumes...)
	}
	return volumes, nil
}

func (c *SharedState) addPod(ctx context.Context, pod *v1.Pod, updateOption updateWithLock) error {
	podKey := getQualifiedName(pod.Namespace, pod.Name)
	v, _ := c.podLocks.LoadOrStore(podKey, &sync.Mutex{})

	klog.V(5).Infof("Adding pod %s to shared map with keyName %s.", pod.Name, podKey)
	podLock := v.(*sync.Mutex)
	if updateOption == acquireLock {
		podLock.Lock()
		defer podLock.Unlock()
	}
	klog.V(5).Infof("Pod spec of pod %s is: %+v. With volumes: %+v", pod.Name, pod.Spec, pod.Spec.Volumes)

	// If the claims already exist for the podKey, add them to a set
	value, _ := c.podToClaimsMap.LoadOrStore(podKey, []string{})
	claims := value.([]string)
	claimSet := set{}
	for _, claim := range claims {
		claimSet.add(claim)
	}

	for _, volume := range pod.Spec.Volumes {
		// TODO: investigate if we need special support for CSI ephemeral volume or generic ephemeral volume
		// if csiMigration is enabled and there is an inline volume, create AzVolume CRI for the inline volume.
		if utilfeature.DefaultFeatureGate.Enabled(features.CSIMigration) &&
			utilfeature.DefaultFeatureGate.Enabled(features.CSIMigrationAzureDisk) &&
			volume.AzureDisk != nil {
			// inline volume: create AzVolume resource
			klog.V(5).Infof("Creating AzVolume instance for inline volume %s.", volume.AzureDisk.DiskName)
			if err := c.createAzVolumeFromInline(ctx, volume.AzureDisk); err != nil {
				return err
			}
			v, exists := c.podToInlineMap.Load(podKey)
			var inlines []string
			if exists {
				inlines = v.([]string)
			}
			inlines = append(inlines, volume.AzureDisk.DiskName)
			c.podToInlineMap.Store(podKey, inlines)
		}
		if volume.PersistentVolumeClaim == nil {
			klog.V(5).Infof("Pod %s: Skipping Volume %s. No persistent volume exists.", pod.Name, volume)
			continue
		}
		namespacedClaimName := getQualifiedName(pod.Namespace, volume.PersistentVolumeClaim.ClaimName)
		if _, ok := c.claimToVolumeMap.Load(namespacedClaimName); !ok {
			klog.Infof("Skipping Pod %s. Volume %s not csi. Driver: %+v", pod.Name, volume.Name, volume.CSI)
			continue
		}
		klog.V(5).Infof("Pod %s. Volume %v is csi.", pod.Name, volume)
		claimSet.add(namespacedClaimName)
		v, _ := c.claimToPodsMap.LoadOrStore(namespacedClaimName, newLockableEntry(set{}))

		lockable := v.(*lockableEntry)
		lockable.Lock()
		pods := lockable.entry.(set)
		if !pods.has(podKey) {
			pods.add(podKey)
		}
		// No need to restore the amended set to claimToPodsMap because set is a reference type
		lockable.Unlock()

		klog.V(5).Infof("Storing pod %s and claim %s to claimToPodsMap map.", pod.Name, namespacedClaimName)
	}
	klog.V(5).Infof("Storing pod %s and claim %s to podToClaimsMap map.", pod.Name, claims)

	allClaims := []string{}
	for key := range claimSet {
		allClaims = append(allClaims, key.(string))
	}
	c.podToClaimsMap.Store(podKey, allClaims)
	return nil
}

func (c *SharedState) deletePod(ctx context.Context, podKey string) error {
	value, exists := c.podLocks.LoadAndDelete(podKey)
	if !exists {
		return nil
	}
	podLock := value.(*sync.Mutex)
	podLock.Lock()
	defer podLock.Unlock()

	value, exists = c.podToInlineMap.LoadAndDelete(podKey)
	if exists {
		inlines := value.([]string)

		for _, inline := range inlines {
			if err := c.azClient.DiskV1alpha2().AzVolumes(c.objectNamespace).Delete(ctx, inline, metav1.DeleteOptions{}); err != nil && !apiErrors.IsNotFound(err) {
				klog.Errorf("failed to delete AzVolume (%s) for inline (%s): %v", inline, inline, err)
				return err
			}
		}
	}

	value, exists = c.podToClaimsMap.LoadAndDelete(podKey)
	if !exists {
		return nil
	}
	claims := value.([]string)

	for _, claim := range claims {
		value, ok := c.claimToPodsMap.Load(claim)
		if !ok {
			klog.Errorf("No pods found for PVC (%s)", claim)
		}

		// Scope the duration that we hold the lockable lock using a function.
		func() {
			lockable, ok := value.(*lockableEntry)
			if !ok {
				klog.Errorf("claimToPodsMap should hold lockable entry")
				return
			}

			lockable.Lock()
			defer lockable.Unlock()

			podSet, ok := lockable.entry.(set)
			if !ok {
				klog.Errorf("claimToPodsMap entry should hold a set")
			}

			podSet.remove(podKey)
			if len(podSet) == 0 {
				c.claimToPodsMap.Delete(claim)
			}
		}()
	}
	return nil
}

func (c *SharedState) addVolumeAndClaim(azVolumeName, pvClaimName string) {
	c.volumeToClaimMap.Store(azVolumeName, pvClaimName)
	c.claimToVolumeMap.Store(pvClaimName, azVolumeName)
}

func (c *SharedState) deleteVolumeAndClaim(azVolumeName string) {
	v, ok := c.volumeToClaimMap.LoadAndDelete(azVolumeName)
	if ok {
		pvClaimName := v.(string)
		c.claimToVolumeMap.Delete(pvClaimName)
	}
}

func (c *SharedState) markVolumeVisited(azVolumeName string) {
	c.visitedVolumes.Store(azVolumeName, struct{}{})
}

func (c *SharedState) unmarkVolumeVisited(azVolumeName string) {
	c.visitedVolumes.Delete(azVolumeName)
}

func (c *SharedState) isVolumeVisited(azVolumeName string) bool {
	_, visited := c.visitedVolumes.Load(azVolumeName)
	return visited
}

func (c *SharedState) getRankedNodesForReplicaAttachments(ctx context.Context, volumes []string, podsObjs []v1.Pod) ([]string, error) {
	klog.V(5).Info("Getting ranked list of nodes for creating AzVolumeAttachments")
	numReplica := 0
	nodes := []string{}
	nodeScores := map[string]int{}
	primaryNode := ""
	compatibleZones := []string{}

	var podNodeAffinities []*nodeaffinity.RequiredNodeAffinity
	var podTolerations []v1.Toleration
	podNodeSelector := labels.NewSelector()
	podAffinities := labels.NewSelector()

	for i, podObj := range podsObjs {
		// acknowledge that there can be duplicate entries within the slice
		podNodeAffinity := nodeaffinity.GetRequiredNodeAffinity(&podObj)
		podNodeAffinities = append(podNodeAffinities, &podNodeAffinity)

		// find intersection of tolerations among pods
		if i == 0 {
			podTolerations = append(podTolerations, podObj.Spec.Tolerations...)
		} else {
			podTolerationMap := map[string]*v1.Toleration{}
			for _, podToleration := range podObj.Spec.Tolerations {
				podToleration := &podToleration
				podTolerationMap[podToleration.Key] = podToleration
			}

			updatedTolerations := []v1.Toleration{}
			for _, podToleration := range podTolerations {
				if existingToleration, ok := podTolerationMap[podToleration.Key]; ok {
					if podToleration.MatchToleration(existingToleration) {
						updatedTolerations = append(updatedTolerations, podToleration)
					}
				}
			}
			podTolerations = updatedTolerations
		}

		nodeSelector := labels.SelectorFromSet(labels.Set(podObj.Spec.NodeSelector))
		requirements, selectable := nodeSelector.Requirements()
		if selectable {
			podNodeSelector = podNodeSelector.Add(requirements...)
		}

		if podObj.Spec.Affinity == nil || podObj.Spec.Affinity.PodAffinity == nil {
			continue
		}
		// TODO: add filtering for pod anti-affinity and more advanced affinity scenarios (e.g. Preferred affinites)
		for _, podAffinity := range podObj.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			podSelector, err := metav1.LabelSelectorAsSelector(podAffinity.LabelSelector)
			// if failed to convert pod affinity label selector to selector, log error and skip
			if err != nil {
				klog.Errorf("failed to convert pod affinity (%v) to selector", podAffinity.LabelSelector)
				continue
			}
			requirements, selectable := podSelector.Requirements()
			if selectable {
				podAffinities = podAffinities.Add(requirements...)
			}
		}
	}

	var volumeNodeSelectors []*nodeaffinity.NodeSelector
	compatibleZonesSet := set{}
	for i, volume := range volumes {

		azVolume, err := azureutils.GetAzVolume(ctx, c.cachedClient, c.azClient, volume, c.objectNamespace, true)
		if err != nil {
			klog.V(5).Infof("AzVolume for volume %s is not found.", volume)
			return nil, err
		}

		numReplica = max(numReplica, azVolume.Spec.MaxMountReplicaCount)
		klog.V(5).Infof("Number of requested replicas for azvolume %s is: %d. Max replica count is: %d.",
			volume, numReplica, azVolume.Spec.MaxMountReplicaCount)

		azVolumeAttachments, err := getAzVolumeAttachmentsForVolume(ctx, c.cachedClient, volume, all)
		if err != nil {
			klog.V(5).Infof("Error listing AzVolumeAttachments for azvolume %s. Error: %v.", volume, err)
		}

		klog.V(5).Infof("Found %d AzVolumeAttachments for volume %s. AzVolumeAttachments: %+v.", len(azVolumeAttachments), volume, azVolumeAttachments)
		for _, azVolumeAttachment := range azVolumeAttachments {
			if azVolumeAttachment.Spec.RequestedRole == diskv1alpha2.PrimaryRole {
				klog.V(5).Infof("AzVolumeAttachments %s for volume %s is primary on node %s.", azVolumeAttachment.Name, volume, azVolumeAttachment.Spec.NodeName)
				if primaryNode == "" {
					primaryNode = azVolumeAttachment.Spec.NodeName
				}
			} else {
				klog.V(5).Infof("Azvolumeattachment %s is not primary. Scoring node %s with replica attachment. Current score array is %v. Len: %d.", azVolumeAttachment.Name, azVolumeAttachment.Spec.NodeName, nodeScores, len(nodeScores))
				if _, ok := nodeScores[azVolumeAttachment.Spec.NodeName]; !ok {
					// Node is not in the array. Adding node to the array with score 0.
					nodeScores[azVolumeAttachment.Spec.NodeName] = 0
					nodes = append(nodes, azVolumeAttachment.Spec.NodeName)
				}
				nodeScores[azVolumeAttachment.Spec.NodeName]++
				klog.V(5).Infof("New score for node %s is %d. Updated score array is %v. Len: %d.", azVolumeAttachment.Spec.NodeName, nodeScores[azVolumeAttachment.Spec.NodeName], nodeScores, len(nodeScores))
			}
		}

		var pv v1.PersistentVolume
		if err := c.cachedClient.Get(ctx, types.NamespacedName{Name: azVolume.Status.PersistentVolume}, &pv); err != nil {
			return nil, err
		}
		if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
			continue
		}

		// Find the intersection of the zones for all the volumes
		topologyKey := c.topologyKey
		if i == 0 {
			compatibleZonesSet = getSupportedZones(pv.Spec.NodeAffinity.Required.NodeSelectorTerms, topologyKey)
		} else {
			commonZones := set{}
			listOfZones := getSupportedZones(pv.Spec.NodeAffinity.Required.NodeSelectorTerms, topologyKey)
			for key := range listOfZones {
				if compatibleZonesSet.has(key) {
					commonZones.add(key)
				}
			}
			compatibleZonesSet = commonZones
		}

		nodeSelector, err := nodeaffinity.NewNodeSelector(pv.Spec.NodeAffinity.Required)
		if err != nil {
			// if failed to create a new node selector return error
			return nil, err
		}
		// acknowledge that there can be duplicates in the slice
		volumeNodeSelectors = append(volumeNodeSelectors, nodeSelector)
	}

	if len(compatibleZonesSet) > 0 {
		for key := range compatibleZonesSet {
			compatibleZones = append(compatibleZones, key.(string))
		}
	}

	removeCandidacy := func(nodeName string, err error) {
		klog.Errorf("Removing node (%s) from candidate nodes for replica attachments: %v", nodeName, err)
		delete(nodeScores, nodeName)

		if len(nodes) > 0 {
			indexToRemove := -1
			for i, node := range nodes {
				if node == nodeName {
					indexToRemove = i
					break
				}
			}
			if indexToRemove != -1 {
				nodes[indexToRemove] = nodes[len(nodes)-1]
				nodes = nodes[:len(nodes)-1]
			}
		}
	}

	// remove the primary node from the candidates list as it can get added if all the volume attachments aren't promoted at the same time
	removeCandidacy(primaryNode, nil)

	for nodeName, nodeScore := range nodeScores {
		if remainingCapacity, err := getNodeRemainingCapacity(ctx, c.cachedClient, nil, nodeName); err != nil {
			// it is safe to delete entry from map while iterating
			removeCandidacy(nodeName, err)
		} else if remainingCapacity < len(volumes)-nodeScore {
			// if node is too full to take n extra attachments, remove the node from the candidate list
			err = status.Errorf(codes.Internal, "remaining node capacity: %d", remainingCapacity)
			removeCandidacy(nodeName, err)
		}
	}

	// sorting nodes array per their score in nodeScore array
	sort.Slice(nodes[:], func(i, j int) bool {
		return nodeScores[nodes[i]] > nodeScores[nodes[j]]
	})

	// select at least as many nodes as requested replicas
	if len(nodes) < numReplica {
		selected, err := c.selectNodes(ctx, numReplica-len(nodes), primaryNode, nodeScores, podNodeAffinities, podTolerations, podNodeSelector, podAffinities, volumeNodeSelectors, compatibleZones)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, selected...)
	}

	klog.Infof("Selected nodes (%+v) for replica AzVolumeAttachments for volumes (%+v)", nodes, volumes)
	return nodes[:min(len(nodes), numReplica)], nil
}

func getSupportedZones(nodeSelectorTerms []v1.NodeSelectorTerm, topologyKey string) set {
	// Get the list of supported zones for pv
	supportedZones := set{}
	if len(nodeSelectorTerms) > 0 {
		for _, term := range nodeSelectorTerms {
			if len(term.MatchExpressions) > 0 {
				for _, matchExpr := range term.MatchExpressions {
					if matchExpr.Key == topologyKey {
						for _, value := range matchExpr.Values {
							if value != "" && !supportedZones.has(value) {
								supportedZones.add(value)
							}
						}
					}
				}
			}
		}
	}
	return supportedZones
}

func getNodeRemainingCapacity(ctx context.Context, cachedClient client.Client, nodeObj *v1.Node, nodeName string) (int, error) {
	// get node if necessary
	if nodeObj == nil {
		nodeObj = &v1.Node{}
		if err := cachedClient.Get(ctx, types.NamespacedName{Name: nodeName}, nodeObj); err != nil {
			return -1, err
		}
	}

	// get all replica azvolumeattachments on the node
	attachments, err := getAzVolumeAttachmentsForNode(ctx, cachedClient, nodeName, all)
	if err != nil {
		return -1, err
	}

	// get node instance type to query node capacity
	capacity := 0
	queryAttachable := false

	if nodeObj.Labels == nil {
		queryAttachable = true
	} else {
		if _, ok := nodeObj.Labels[v1.LabelInstanceTypeStable]; !ok {
			queryAttachable = true
		} else {
			capacity, _ = azureutils.GetNodeMaxDiskCount(nodeObj.Labels)
		}
	}

	if queryAttachable {
		// check node capacity
		maxAttachables, ok := nodeObj.Status.Allocatable[consts.AttachableVolumesField]
		if !ok {
			err := status.Errorf(codes.Internal, "failed to get the max node capacity for node (%s).", nodeName)
			return -1, err
		}
		capacity = int(maxAttachables.Value())
	}

	return capacity - len(attachments), nil
}

func (c *SharedState) selectNodes(ctx context.Context, numNodes int, primaryNode string, exceptionNodes map[string]int, podNodeAffinities []*nodeaffinity.RequiredNodeAffinity, podTolerations []v1.Toleration, podNodeSelectors, podAffinities labels.Selector, volumeNodeSelectors []*nodeaffinity.NodeSelector, compatibleZones []string) ([]string, error) {
	selectedNodes := []string{}
	var nodes *v1.NodeList
	var err error

	nodes = &v1.NodeList{}
	// Select only the nodes from specific supported zones in case of a multi zone cluster
	if len(compatibleZones) > 0 {
		klog.Infof("The list of zones to select nodes from is: %s", strings.Join(compatibleZones, ","))
		nodeObj := &v1.Node{}
		// Get the zone of the primary node
		err := c.cachedClient.Get(ctx, types.NamespacedName{Name: primaryNode}, nodeObj)
		if err != nil {
			klog.Errorf("failed to retrieve the primary node: %v", err)
		}
		primaryNodeZone, ok := nodeObj.Labels[consts.WellKnownTopologyKey]
		if !ok {
			klog.Infof("failed to find zone annotations for primary node")
		}

		nodeSelector := labels.NewSelector()
		zoneRequirement, _ := labels.NewRequirement(consts.WellKnownTopologyKey, selection.In, compatibleZones)
		nodeSelector = nodeSelector.Add(*zoneRequirement)
		podNodeSelectorRequirements, _ := podNodeSelectors.Requirements()
		nodeSelector = nodeSelector.Add(podNodeSelectorRequirements...)

		if err = c.cachedClient.List(ctx, nodes, &client.ListOptions{LabelSelector: nodeSelector}); err != nil {
			klog.Errorf("failed to retrieve node list: %v", err)
			return selectedNodes, err
		}

		// Create a zone to node map
		zoneToNodeMap := map[string][]v1.Node{}
		for _, node := range nodes.Items {
			zoneName, ok := node.Labels[consts.WellKnownTopologyKey]
			if !ok {
				continue
			}
			if _, ok := zoneToNodeMap[zoneName]; !ok {
				zoneToNodeMap[zoneName] = []v1.Node{}
			}
			zoneToNodeMap[zoneName] = append(zoneToNodeMap[zoneName], node)
		}

		// Get filtered and sorted nodes per zone
		nodesPerZone := [][]string{}
		primaryZoneNodes := []string{}
		totalCount := 0
		for zone, nodeList := range zoneToNodeMap {
			filteredNodes, err := c.filterAndSortNodes(ctx, primaryNode, exceptionNodes, podNodeAffinities, podTolerations, podNodeSelectors, podAffinities, volumeNodeSelectors, v1.NodeList{Items: nodeList})
			if err != nil {
				klog.Errorf("failed to filter and sort nodes: %v", err)
				return selectedNodes, err
			}
			if len(filteredNodes) > 0 {
				totalCount += len(filteredNodes)
				if zone == primaryNodeZone {
					primaryZoneNodes = filteredNodes
					continue
				}
				nodesPerZone = append(nodesPerZone, filteredNodes)
			}
		}

		// Append the nodes from the zone of the primary node at last
		if len(primaryZoneNodes) > 0 {
			nodesPerZone = append(nodesPerZone, primaryZoneNodes)
		}

		// Select the nodes from each of the zones one by one and append to the list
		i, j, countSoFar := 0, 0, 0
		for len(selectedNodes) < numNodes && countSoFar < totalCount {
			if len(nodesPerZone[i]) > j {
				selectedNodes = append(selectedNodes, nodesPerZone[i][j])
				countSoFar++
			}
			if i < len(nodesPerZone)-1 {
				i++
			} else {
				i = 0
				j++
			}
		}

		return selectedNodes, nil
	}
	// List all nodes
	if err = c.cachedClient.List(ctx, nodes, &client.ListOptions{LabelSelector: podNodeSelectors}); err != nil {
		klog.Errorf("failed to retrieve node list: %v", err)
		return selectedNodes, err
	}

	selectedNodes, err = c.filterAndSortNodes(ctx, primaryNode, exceptionNodes, podNodeAffinities, podTolerations, podNodeSelectors, podAffinities, volumeNodeSelectors, *nodes)
	if err != nil {
		klog.Errorf("failed to filter and sort nodes: %v", err)
		return selectedNodes, err
	}

	if len(selectedNodes) > numNodes {
		return selectedNodes[:numNodes], nil
	}
	return selectedNodes, nil
}

func (c *SharedState) filterAndSortNodes(ctx context.Context, primaryNode string, exceptionNodes map[string]int, podNodeAffinities []*nodeaffinity.RequiredNodeAffinity, podTolerations []v1.Toleration, podNodeSelectors, podAffinities labels.Selector, volumeNodeSelectors []*nodeaffinity.NodeSelector, nodes v1.NodeList) ([]string, error) {
	filteredNodes := []string{}
	var err error

	type nodeEntry struct {
		node      *v1.Node
		nodeScore int
	}

	nodeMap := map[string]nodeEntry{}
	for _, node := range nodes.Items {
		nodeVar := node
		nodeMap[node.Name] = nodeEntry{node: &nodeVar, nodeScore: 0}
	}

	// if podAffinity is specified, filter nodes based on pod affinity
	// Get all replica attachments for the filtered pod and get filtered node list
	filteredPodNodes := set{}
	if podAffinities != nil && !podAffinities.Empty() {
		pods := &v1.PodList{}
		if err = c.cachedClient.List(ctx, pods, &client.ListOptions{LabelSelector: podAffinities}); err != nil {
			klog.Errorf("failed to retrieve pod list: %v", err)
			return filteredNodes, err
		}

		volumes, err := c.getVolumesForPodObjs(pods.Items)
		if err != nil {
			klog.Errorf("failed to get volumes for pods (%+v)", pods.Items)
			return filteredNodes, err
		}
		for _, volume := range volumes {
			attachments, err := getAzVolumeAttachmentsForVolume(ctx, c.cachedClient, volume, all)
			if err != nil {
				klog.Errorf("failed to get replica attachments for volume (%s): %v", volume)
				return filteredNodes, err
			}
			for _, attachment := range attachments {
				filteredPodNodes.add(attachment.Spec.NodeName)
			}
		}
	}

	// loop through nodes and filter out candidates that don't match volume's node selector, pod's node affinity, toleration
nodeLoop:
	for nodeName, nodeEntry := range nodeMap {
		for _, podNodeAffinity := range podNodeAffinities {
			if match, err := podNodeAffinity.Match(nodeEntry.node); !match || err != nil {
				klog.Infof("Removing node (%s) from replica candidates: node (%s) does not match pod node affinity", nodeName, nodeName)
				delete(nodeMap, nodeName)
				continue nodeLoop
			}
		}
		tolerable := true
		for _, taint := range nodeEntry.node.Spec.Taints {
			taintTolerable := false
			for _, podToleration := range podTolerations {
				// if any one of node's taint cannot be tolerated by pod's tolerations, break
				if podToleration.ToleratesTaint(&taint) {
					taintTolerable = true
				}
			}
			tolerable = tolerable && taintTolerable
		}

		if !tolerable {
			klog.Infof("Removing node (%s) from replica candidates: node (%s)'s taint cannot be tolerated", nodeName, nodeName)
			delete(nodeMap, nodeName)
			continue nodeLoop
		}

		for _, volumeNodeSelector := range volumeNodeSelectors {
			if !volumeNodeSelector.Match(nodeEntry.node) {
				klog.Infof("Removing node (%s) from replica candidates: volume node selector (%+v) cannot be matched with node (%s).", nodeName, volumeNodeSelector, nodeName)
				delete(nodeMap, nodeName)
				continue nodeLoop
			}
		}
	}

	for nodeName, nodeEntry := range nodeMap {
		if _, ok := exceptionNodes[nodeName]; ok || nodeName == primaryNode {
			continue
		}

		remainingCapacity, err := getNodeRemainingCapacity(ctx, c.cachedClient, nodeEntry.node, nodeName)
		if err != nil {
			// if failed to get node's remaining capacity, remove the node from the candidate list and proceed
			klog.Errorf("failed to get remaining capacity of node (%s): %v", nodeName, err)
			delete(nodeMap, nodeName)
			continue
		}

		nodeEntry.nodeScore = remainingCapacity
		nodeMap[nodeName] = nodeEntry
		filteredNodes = append(filteredNodes, nodeName)
		klog.Infof("node (%s) can accept %d more attachments", nodeName, remainingCapacity)
	}

	// sort the filteredNodes by their remaining capacity (high to low) and return a slice
	sort.Slice(filteredNodes[:], func(i, j int) bool {
		// prioritize the nodes filtered by pod affinity
		iIsFiltered := filteredPodNodes.has(filteredNodes[i])
		jIsFiltered := filteredPodNodes.has(filteredNodes[j])
		if iIsFiltered != jIsFiltered {
			return iIsFiltered
		}
		// if both satisfy or don't pod affinity, rank by their node scores
		return nodeMap[filteredNodes[i]].nodeScore > nodeMap[filteredNodes[j]].nodeScore
	})

	return filteredNodes, nil
}

func (c *SharedState) getNodesWithReplica(ctx context.Context, volumeName string) ([]string, error) {
	klog.V(5).Infof("Getting nodes with replica AzVolumeAttachments for volume %s.", volumeName)
	azVolumeAttachments, err := getAzVolumeAttachmentsForVolume(ctx, c.cachedClient, volumeName, replicaOnly)
	if err != nil {
		klog.V(5).Infof("Error getting AzVolumeAttachments for volume %s. Error: %v", volumeName, err)
		return nil, err
	}

	nodes := []string{}
	for _, azVolumeAttachment := range azVolumeAttachments {
		nodes = append(nodes, azVolumeAttachment.Spec.NodeName)
	}
	klog.V(5).Infof("Nodes with AzVolumeAttachments for volume %s are: %v, Len: %d", volumeName, nodes, len(nodes))
	return nodes, nil
}

func (c *SharedState) createReplicaAzVolumeAttachment(ctx context.Context, volumeID, node string, volumeContext map[string]string) error {
	klog.V(5).Infof("Creating replica AzVolumeAttachments for volumeId %s on node %s. ", volumeID, node)
	diskName, err := azureutils.GetDiskName(volumeID)
	if err != nil {
		klog.Warningf("Error getting Diskname for replica AzVolumeAttachments for volumeId %s on node %s. Error: %v. ", volumeID, node, err)
		return err
	}
	if volumeContext == nil {
		volumeContext = make(map[string]string)
	}
	// creating azvolumeattachment
	volumeName := strings.ToLower(diskName)
	replicaName := azureutils.GetAzVolumeAttachmentName(volumeName, node)
	_, err = c.azClient.DiskV1alpha2().AzVolumeAttachments(c.objectNamespace).Create(ctx, &diskv1alpha2.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      replicaName,
			Namespace: c.objectNamespace,
			Labels: map[string]string{
				consts.NodeNameLabel:   node,
				consts.VolumeNameLabel: volumeName,
				consts.RoleLabel:       string(diskv1alpha2.ReplicaRole),
			},
		},
		Spec: diskv1alpha2.AzVolumeAttachmentSpec{
			NodeName:      node,
			VolumeID:      volumeID,
			VolumeName:    volumeName,
			RequestedRole: diskv1alpha2.ReplicaRole,
			VolumeContext: volumeContext,
		},
		Status: diskv1alpha2.AzVolumeAttachmentStatus{
			State: diskv1alpha2.AttachmentPending,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		klog.Warning("Failed creating replica AzVolumeAttachment %s. Error: %v", replicaName, err)
		return err
	}
	klog.V(5).Infof("Replica AzVolumeAttachment %s has been successfully created.", replicaName)
	return nil
}

func (c *SharedState) cleanUpAzVolumeAttachmentByVolume(ctx context.Context, azVolumeName string, caller operationRequester, role roleMode, deleteMode cleanUpMode) (*diskv1alpha2.AzVolumeAttachmentList, error) {
	klog.Infof("AzVolumeAttachment clean up requested by %s for AzVolume (%s)", caller, azVolumeName)
	volRequirement, err := azureutils.CreateLabelRequirements(consts.VolumeNameLabel, selection.Equals, azVolumeName)
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector().Add(*volRequirement)

	attachments, err := c.azClient.DiskV1alpha2().AzVolumeAttachments(c.objectNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return nil, nil
		}
		klog.Errorf("failed to get AzVolumeAttachments: %v", err)
		return nil, err
	}

	cleanUps := []diskv1alpha2.AzVolumeAttachment{}
	for _, attachment := range attachments.Items {
		if shouldCleanUp(attachment, role) {
			cleanUps = append(cleanUps, attachment)
		}
	}

	if err := c.cleanUpAzVolumeAttachments(ctx, cleanUps, deleteMode, caller); err != nil {
		return attachments, err
	}
	c.unmarkVolumeVisited(azVolumeName)
	klog.Infof("successfully requested deletion of AzVolumeAttachments for AzVolume (%s)", azVolumeName)
	return attachments, nil
}

func (c *SharedState) cleanUpAzVolumeAttachmentByNode(ctx context.Context, azDriverNodeName string, caller operationRequester, role roleMode, deleteMode cleanUpMode) (*diskv1alpha2.AzVolumeAttachmentList, error) {
	klog.Infof("AzVolumeAttachment clean up requested by %s for AzDriverNode (%s)", caller, azDriverNodeName)
	nodeRequirement, err := azureutils.CreateLabelRequirements(consts.NodeNameLabel, selection.Equals, azDriverNodeName)
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector().Add(*nodeRequirement)

	attachments, err := c.azClient.DiskV1alpha2().AzVolumeAttachments(c.objectNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return attachments, nil
		}
		klog.Errorf("failed to get AzVolumeAttachments: %v", err)
		return nil, err
	}

	cleanUpMap := map[string][]diskv1alpha2.AzVolumeAttachment{}
	for _, attachment := range attachments.Items {
		if shouldCleanUp(attachment, role) {
			cleanUpMap[attachment.Spec.VolumeName] = append(cleanUpMap[attachment.Spec.VolumeName], attachment)
		}
	}

	for volumeName, cleanUps := range cleanUpMap {
		volumeName := volumeName
		cleanUps := cleanUps
		c.addToOperationQueue(
			volumeName,
			caller,
			func() error {
				ctx := context.Background()
				err := c.cleanUpAzVolumeAttachments(ctx, cleanUps, deleteMode, caller)
				if err == nil {
					klog.Infof("successfully requested deletion of AzVolumeAttachments for AzDriverNode (%s)", azDriverNodeName)
				}
				return err
			},
			false)
	}
	return attachments, nil
}

func (c *SharedState) cleanUpAzVolumeAttachments(ctx context.Context, attachments []diskv1alpha2.AzVolumeAttachment, cleanUp cleanUpMode, caller operationRequester) error {
	for _, attachment := range attachments {
		patched := attachment.DeepCopy()
		if patched.Annotations == nil {
			patched.Annotations = map[string]string{}
		}

		// if caller is azdrivernode, don't append cleanup annotation
		if caller != azdrivernode {
			patched.Annotations[consts.CleanUpAnnotation] = string(caller)
		}
		// replica attachments should always be detached regardless of the cleanup mode
		if cleanUp == detachAndDeleteCRI || patched.Spec.RequestedRole == diskv1alpha2.ReplicaRole {
			patched.Annotations[consts.VolumeDetachRequestAnnotation] = string(caller)
		}
		if err := c.cachedClient.Patch(ctx, patched, client.MergeFrom(&attachment)); err != nil {
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", attachment.Name, err)
			return err
		}
		if err := c.azClient.DiskV1alpha2().AzVolumeAttachments(c.objectNamespace).Delete(ctx, attachment.Name, metav1.DeleteOptions{}); err != nil {
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", attachment.Name, err)
			return err
		}
		klog.V(5).Infof("Set deletion timestamp for AzVolumeAttachment (%s)", attachment.Name)
	}
	return nil
}

func getAzVolumeAttachmentsForVolume(ctx context.Context, azclient client.Client, volumeName string, azVolumeAttachmentRole roleMode) (attachments []diskv1alpha2.AzVolumeAttachment, err error) {
	klog.V(5).Infof("Getting the list of AzVolumeAttachments for %s.", volumeName)
	if azVolumeAttachmentRole == all {
		return getAzVolumeAttachmentsWithLabel(ctx, azclient, labelPair{consts.VolumeNameLabel, volumeName})
	}
	return getAzVolumeAttachmentsWithLabel(ctx, azclient, labelPair{consts.VolumeNameLabel, volumeName}, labelPair{consts.RoleLabel, roles[azVolumeAttachmentRole]})
}

func getAzVolumeAttachmentsForNode(ctx context.Context, azclient client.Client, nodeName string, azVolumeAttachmentRole roleMode) (attachments []diskv1alpha2.AzVolumeAttachment, err error) {
	klog.V(5).Infof("Getting the list of AzVolumeAttachments for %s.", nodeName)
	if azVolumeAttachmentRole == all {
		return getAzVolumeAttachmentsWithLabel(ctx, azclient, labelPair{consts.NodeNameLabel, nodeName})
	}
	return getAzVolumeAttachmentsWithLabel(ctx, azclient, labelPair{consts.NodeNameLabel, nodeName}, labelPair{consts.RoleLabel, roles[azVolumeAttachmentRole]})
}

func getAzVolumeAttachmentsWithLabel(ctx context.Context, azclient client.Client, labelPairs ...labelPair) (attachments []diskv1alpha2.AzVolumeAttachment, err error) {
	labelSelector := labels.NewSelector()
	for _, labelPair := range labelPairs {
		var req *labels.Requirement
		req, err = azureutils.CreateLabelRequirements(labelPair.key, selection.Equals, labelPair.entry)
		if err != nil {
			klog.Errorf("failed to create label (%s, %s) for listing AzVolumeAttachment", labelPair.key, labelPair.entry)
			return
		}
		labelSelector = labelSelector.Add(*req)
	}

	klog.V(5).Infof("Label selector is: %v.", labelSelector)
	azVolumeAttachments := &diskv1alpha2.AzVolumeAttachmentList{}
	err = azclient.List(ctx, azVolumeAttachments, &client.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		klog.V(5).Infof("Error retrieving AzVolumeAttachments for label %s. Error: %v", labelSelector, err)
		return
	}
	attachments = azVolumeAttachments.Items
	return
}

func shouldCleanUp(attachment diskv1alpha2.AzVolumeAttachment, mode roleMode) bool {
	return mode == all || (attachment.Spec.RequestedRole == diskv1alpha2.PrimaryRole && mode == primaryOnly) || (attachment.Spec.RequestedRole == diskv1alpha2.ReplicaRole && mode == replicaOnly)
}

func isAttached(attachment *diskv1alpha2.AzVolumeAttachment) bool {
	return attachment != nil && attachment.Status.Detail != nil && attachment.Status.Detail.PublishContext != nil
}

func isCreated(volume *diskv1alpha2.AzVolume) bool {
	return volume != nil && volume.Status.Detail != nil
}

func objectDeletionRequested(obj runtime.Object) bool {
	meta, _ := meta.Accessor(obj)
	if meta == nil {
		return false
	}
	deletionTime := meta.GetDeletionTimestamp()

	return !deletionTime.IsZero() && deletionTime.Time.Before(time.Now())
}

func isCleanupRequested(obj runtime.Object) bool {
	meta, _ := meta.Accessor(obj)
	if meta == nil {
		return false
	}
	annotations := meta.GetAnnotations()
	if annotations == nil {
		return false
	}
	_, requested := annotations[consts.CleanUpAnnotation]
	return requested
}

func volumeDetachRequested(attachment *diskv1alpha2.AzVolumeAttachment) bool {
	return attachment != nil && attachment.Annotations != nil && metav1.HasAnnotation(attachment.ObjectMeta, consts.VolumeDetachRequestAnnotation)
}

func volumeDeleteRequested(volume *diskv1alpha2.AzVolume) bool {
	return volume != nil && volume.Annotations != nil && metav1.HasAnnotation(volume.ObjectMeta, consts.VolumeDeleteRequestAnnotation)
}

func isDemotionRequested(attachment *diskv1alpha2.AzVolumeAttachment) bool {
	return attachment != nil && attachment.Status.Detail != nil && attachment.Status.Detail.Role == diskv1alpha2.PrimaryRole && attachment.Spec.RequestedRole == diskv1alpha2.ReplicaRole
}

func isPreProvisionCleanupRequested(volume *diskv1alpha2.AzVolume) bool {
	return volume != nil && volume.Annotations != nil && metav1.HasAnnotation(volume.ObjectMeta, consts.PreProvisionedVolumeCleanupAnnotation)
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

func getQualifiedName(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

func parseQualifiedName(qualifiedName string) (namespace, name string) {
	parsed := strings.Split(qualifiedName, "/")
	if len(parsed) != 2 {
		klog.Errorf("pod's qualified name should be of <namespace>/<name>")
		return
	}
	namespace = parsed[0]
	name = parsed[1]
	return
}

func formatUpdateStateError(objectType, fromState, toState string, expectedStates ...string) string {
	return fmt.Sprintf("%s's state '%s' cannot be updated to %s. %s can only be updated to %s", objectType, fromState, toState, fromState, strings.Join(expectedStates, ", "))
}

func getOperationRequeueError(desired string, obj client.Object) error {
	return status.Errorf(codes.Aborted, "requeueing %s operation because another operation is already pending on %v (%s)", desired, reflect.TypeOf(obj), obj.GetName())
}

func reconcileReturnOnSuccess(objectName string, retryInfo *retryInfo) (reconcile.Result, error) {
	retryInfo.deleteEntry(objectName)
	return reconcile.Result{}, nil
}

func reconcileReturnOnError(obj runtime.Object, operationType string, err error, retryInfo *retryInfo) (reconcile.Result, error) {
	var (
		requeue    bool = status.Code(err) != codes.FailedPrecondition
		retryAfter time.Duration
	)

	if meta, metaErr := meta.Accessor(obj); metaErr == nil {
		objectName := meta.GetName()
		objectType := reflect.TypeOf(obj)
		if !requeue {
			klog.Errorf("failed to %s %v (%s) with no retry: %v", operationType, objectType, objectName, err)
			retryInfo.deleteEntry(objectName)
		} else {
			retryAfter = retryInfo.nextRequeue(objectName)
			klog.Errorf("failed to %s %v (%s) with retry after %v: %v", operationType, objectType, objectName, retryAfter, err)
		}
	}

	return reconcile.Result{
		Requeue:      requeue,
		RequeueAfter: retryAfter,
	}, nil
}

func isOperationInProcess(obj interface{}) bool {
	switch target := obj.(type) {
	case *diskv1alpha2.AzVolume:
		return target.Status.State == diskv1alpha2.VolumeCreating || target.Status.State == diskv1alpha2.VolumeDeleting || target.Status.State == diskv1alpha2.VolumeUpdating
	case *diskv1alpha2.AzVolumeAttachment:
		return target.Status.State == diskv1alpha2.Attaching || target.Status.State == diskv1alpha2.Detaching
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func containsString(key string, items []string) bool {
	for _, item := range items {
		if item == key {
			return true
		}
	}
	return false
}

type ReplicaRequest struct {
	VolumeName string
	Priority   int //The number of replicas that have yet to be created
}
type VolumeReplicaRequestsPriorityQueue struct {
	queue *cache.Heap
	size  int32
}

func (vq *VolumeReplicaRequestsPriorityQueue) Push(replicaRequest *ReplicaRequest) {
	err := vq.queue.Add(replicaRequest)
	atomic.AddInt32(&vq.size, 1)
	if err != nil {
		klog.Infof("Failed to add replica request for volume %s: %v", replicaRequest.VolumeName, err)
	}
}

func (vq *VolumeReplicaRequestsPriorityQueue) Pop() *ReplicaRequest {
	request, _ := vq.queue.Pop()
	atomic.AddInt32(&vq.size, -1)
	return request.(*ReplicaRequest)
}
func (vq *VolumeReplicaRequestsPriorityQueue) DrainQueue() []*ReplicaRequest {
	var listRequests []*ReplicaRequest
	for i := vq.size; i > 0; i-- {
		listRequests = append(listRequests, vq.Pop())
	}
	return listRequests
}

///Removes replica requests from the priority queue and adds to operation queue.
func (c *SharedState) tryCreateFailedReplicas(ctx context.Context, requestor operationRequester) error {
	requests := c.priorityReplicaRequestsQueue.DrainQueue()
	for i := 0; i < len(requests); i++ {
		replicaRequest := requests[i]
		c.addToOperationQueue(
			replicaRequest.VolumeName,
			requestor,
			func() error {
				return c.manageReplicas(context.Background(), replicaRequest.VolumeName)
			},
			false,
		)
	}
	return nil
}

func (c *SharedState) manageReplicas(ctx context.Context, volumeName string) error {
	azVolume, err := azureutils.GetAzVolume(ctx, c.cachedClient, c.azClient, volumeName, c.objectNamespace, true)
	if apiErrors.IsNotFound(err) {
		klog.Infof("Aborting replica management... volume (%s) does not exist", volumeName)
		return nil
	} else if err != nil {
		klog.Errorf("failed to get AzVolume (%s): %v", volumeName, err)
		return err
	}

	// replica management should not be executed or retried if AzVolume is scheduled for a deletion or not created.
	if !isCreated(azVolume) || objectDeletionRequested(azVolume) {
		klog.Errorf("azVolume (%s) is scheduled for deletion or has no underlying volume object", azVolume.Name)
		return nil
	}

	azVolumeAttachments, err := getAzVolumeAttachmentsForVolume(ctx, c.cachedClient, volumeName, replicaOnly)
	if err != nil {
		klog.Errorf("failed to list AzVolumeAttachment: %v", err)
		return err
	}

	desiredReplicaCount, currentReplicaCount := azVolume.Spec.MaxMountReplicaCount, len(azVolumeAttachments)
	klog.Infof("control number of replicas for volume (%s): desired=%d,\tcurrent:%d", azVolume.Spec.VolumeName, desiredReplicaCount, currentReplicaCount)

	if desiredReplicaCount > currentReplicaCount {
		klog.Infof("Need %d more replicas for volume (%s)", desiredReplicaCount-currentReplicaCount, azVolume.Spec.VolumeName)
		if azVolume.Status.Detail == nil || azVolume.Status.State == diskv1alpha2.VolumeDeleting || azVolume.Status.State == diskv1alpha2.VolumeDeleted {
			// underlying volume does not exist, so volume attachment cannot be made
			return nil
		}
		if err = c.createReplicas(ctx, desiredReplicaCount-currentReplicaCount, azVolume.Name, azVolume.Status.Detail.VolumeID, azVolume.Spec.Parameters); err != nil {
			klog.Errorf("failed to create %d replicas for volume (%s): %v", desiredReplicaCount-currentReplicaCount, azVolume.Spec.VolumeName, err)
			return err
		}
	}
	return nil
}
func (c *SharedState) createReplicas(ctx context.Context, remainingReplicas int, volumeName, volumeID string, volumeContext map[string]string) error {
	// if volume is scheduled for clean up, skip replica creation
	if _, cleanUpScheduled := c.cleanUpMap.Load(volumeName); cleanUpScheduled {
		return nil
	}

	// get pods linked to the volume
	pods, err := c.getPodsFromVolume(ctx, c.cachedClient, volumeName)
	if err != nil {
		return err
	}

	// acquire per-pod lock to be released upon creation of replica AzVolumeAttachment CRIs
	for _, pod := range pods {
		podKey := getQualifiedName(pod.Namespace, pod.Name)
		v, _ := c.podLocks.LoadOrStore(podKey, &sync.Mutex{})
		podLock := v.(*sync.Mutex)
		podLock.Lock()
		defer podLock.Unlock()
	}

	nodes, err := c.getNodesForReplica(ctx, volumeName, pods, remainingReplicas)
	if err != nil {
		klog.Errorf("failed to get a list of nodes for replica attachment: %v", err)
		return err
	}

	requiredReplicas := remainingReplicas
	for _, node := range nodes {
		if err := c.createReplicaAzVolumeAttachment(ctx, volumeID, node, volumeContext); err != nil {
			klog.Errorf("failed to create replica AzVolumeAttachment for volume %s: %v", volumeName, err)
			//Push to queue the failed replica number
			request := ReplicaRequest{VolumeName: volumeName, Priority: remainingReplicas}
			c.priorityReplicaRequestsQueue.Push(&request)
			return err
		}
		remainingReplicas--
	}

	if remainingReplicas > 0 {
		//no failed replica attachments, but there are still more replicas to reach MaxShares
		request := ReplicaRequest{VolumeName: volumeName, Priority: remainingReplicas}
		c.priorityReplicaRequestsQueue.Push(&request)
		for _, pod := range pods {
			c.eventRecorder.Eventf(pod.DeepCopyObject(), v1.EventTypeWarning, consts.ReplicaAttachmentFailedEvent, "Not enough suitable nodes to attach %d of %d replica mount(s) for volume %s", remainingReplicas, requiredReplicas, volumeName)
		}
	}
	return nil
}

func (c *SharedState) getNodesForReplica(ctx context.Context, volumeName string, pods []v1.Pod, numReplica int) ([]string, error) {
	var err error
	if len(pods) == 0 {
		pods, err = c.getPodsFromVolume(ctx, c.cachedClient, volumeName)
		if err != nil {
			return nil, err
		}
	}

	volumes, err := c.getVolumesForPodObjs(pods)
	if err != nil {
		return nil, err
	}

	nodes, err := c.getRankedNodesForReplicaAttachments(ctx, volumes, pods)
	if err != nil {
		return nil, err
	}

	replicaNodes, err := c.getNodesWithReplica(ctx, volumeName)
	if err != nil {
		return nil, err
	}

	skipSet := map[string]bool{}
	for _, replicaNode := range replicaNodes {
		skipSet[replicaNode] = true
	}

	filtered := []string{}
	numFiltered := 0
	for _, node := range nodes {
		if numFiltered >= numReplica {
			break
		}
		if skipSet[node] {
			continue
		}
		filtered = append(filtered, node)
		numFiltered++
	}

	return filtered, nil
}
