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
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
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
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
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
	azdrivernode operationRequester = "azdrivernode-controller"
	azvolume     operationRequester = "azvolume-controller"
	replica      operationRequester = "replica-controller"
	pod                             = "pod-controller"
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
	primaryOnly: string(v1alpha1.PrimaryRole),
	replicaOnly: string(v1alpha1.ReplicaRole),
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
		capacityRange *v1alpha1.CapacityRange,
		volumeCapabilities []v1alpha1.VolumeCapability,
		parameters map[string]string,
		secrets map[string]string,
		volumeContentSource *v1alpha1.ContentVolumeSource,
		accessibilityTopology *v1alpha1.TopologyRequirement) (*v1alpha1.AzVolumeStatusParams, error)
	DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error
	PublishVolume(ctx context.Context, volumeID string, nodeID string, volumeContext map[string]string) (map[string]string, error)
	UnpublishVolume(ctx context.Context, volumeID string, nodeID string) error
	ExpandVolume(ctx context.Context, volumeID string, capacityRange *v1alpha1.CapacityRange, secrets map[string]string) (*v1alpha1.AzVolumeStatusParams, error)
	ListVolumes(ctx context.Context, maxEntries int32, startingToken string) (*v1alpha1.ListVolumesResult, error)
	CreateSnapshot(ctx context.Context, sourceVolumeID string, snapshotName string, secrets map[string]string, parameters map[string]string) (*v1alpha1.Snapshot, error)
	ListSnapshots(ctx context.Context, maxEntries int32, startingToken string, sourceVolumeID string, snapshotID string, secrets map[string]string) (*v1alpha1.ListSnapshotsResult, error)
	DeleteSnapshot(ctx context.Context, snapshotID string, secrets map[string]string) error
	CheckDiskExists(ctx context.Context, diskURI string) (*compute.Disk, error)
	GetCloud() *provider.Cloud
	GetMetricPrefix() string
}

type azReconciler interface {
	getClient() client.Client
	getAzClient() azClientSet.Interface
	getSharedState() *SharedState
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
	podToClaimsMap        sync.Map
	claimToPodsMap        sync.Map
	volumeToClaimMap      sync.Map
	claimToVolumeMap      sync.Map
	podLocks              sync.Map
	visitedVolumes        sync.Map
	volumeOperationQueues sync.Map
}

func NewSharedState() *SharedState {
	return &SharedState{}
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

func (c *SharedState) getPodsFromVolume(volumeName string) ([]string, error) {
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

func (c *SharedState) getVolumesForPods(pods ...string) ([]string, error) {
	volumes := []string{}
	for _, pod := range pods {
		podVolumes, err := c.getVolumesFromPod(pod)
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, podVolumes...)
	}
	return volumes, nil
}

func (c *SharedState) getVolumesForPodObjs(pods ...v1.Pod) ([]string, error) {
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

func (c *SharedState) addPod(pod *v1.Pod, updateOption updateWithLock) {

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
}

func (c *SharedState) deletePod(podKey string) {
	value, exists := c.podLocks.LoadAndDelete(podKey)
	if !exists {
		return
	}
	podLock := value.(*sync.Mutex)
	podLock.Lock()
	defer podLock.Unlock()

	value, exists = c.podToClaimsMap.LoadAndDelete(podKey)
	if !exists {
		return
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

func getRankedNodesForReplicaAttachments(ctx context.Context, azr azReconciler, volumes, pods []string) ([]string, error) {
	klog.V(5).Info("Getting ranked list of nodes for creating AzVolumeAttachments")
	numReplica := 0
	nodes := []string{}
	nodeScores := map[string]int{}
	primaryNode := ""

	var podNodeAffinities []*nodeaffinity.RequiredNodeAffinity
	var podTolerations []v1.Toleration
	podNodeSelector := labels.NewSelector()
	podAffinities := labels.NewSelector()

	for i, pod := range pods {
		namespace, name := parseQualifiedName(pod)
		var podObj v1.Pod
		if err := azr.getClient().Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &podObj); err != nil {
			return nil, err
		}
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
	for _, volume := range volumes {
		azVolume, err := azureutils.GetAzVolume(ctx, azr.getClient(), azr.getAzClient(), volume, consts.AzureDiskCrdNamespace, true)
		if err != nil {
			klog.V(5).Infof("AzVolume for volume %s is not found.", volume)
			return nil, err
		}

		numReplica = max(numReplica, azVolume.Spec.MaxMountReplicaCount)
		klog.V(5).Infof("Number of requested replicas for azvolume %s is: %d. Max replica count is: %d.",
			volume, numReplica, azVolume.Spec.MaxMountReplicaCount)

		azVolumeAttachments, err := getAzVolumeAttachmentsForVolume(ctx, azr.getClient(), volume, all)
		if err != nil {
			klog.V(5).Infof("Error listing AzVolumeAttachments for azvolume %s. Error: %v.", volume, err)
		}

		klog.V(5).Infof("Found %d AzVolumeAttachments for volume %s. AzVolumeAttachments: %+v.", len(azVolumeAttachments), volume, azVolumeAttachments)
		for _, azVolumeAttachment := range azVolumeAttachments {
			if azVolumeAttachment.Spec.RequestedRole == v1alpha1.PrimaryRole {
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
		if err := azr.getClient().Get(ctx, types.NamespacedName{Name: azVolume.Status.PersistentVolume}, &pv); err != nil {
			return nil, err
		}
		if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
			continue
		}
		nodeSelector, err := nodeaffinity.NewNodeSelector(pv.Spec.NodeAffinity.Required)
		if err != nil {
			// if failed to create a new node selector return error
			return nil, err
		}
		// acknowledge that there can be duplicates in the slice
		volumeNodeSelectors = append(volumeNodeSelectors, nodeSelector)
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
		if remainingCapacity, err := getNodeRemainingCapacity(ctx, azr.getClient(), nil, nodeName); err != nil {
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
		selected, err := selectNodes(ctx, azr, numReplica-len(nodes), primaryNode, nodeScores, podNodeAffinities, podTolerations, podNodeSelector, podAffinities, volumeNodeSelectors)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, selected...)
	}

	klog.Infof("Selected nodes (%+v) for replica AzVolumeAttachments for volumes (%+v)", nodes, volumes)
	return nodes[:min(len(nodes), numReplica)], nil
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
		if nodeVMType, ok := nodeObj.Labels[v1.LabelInstanceTypeStable]; !ok {
			queryAttachable = true
		} else {
			for vmType, vmCapacity := range azureutils.MaxDataDiskCountMap {
				if strings.EqualFold(nodeVMType, vmType) {
					capacity = int(vmCapacity)
					break
				}
			}
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

func selectNodes(ctx context.Context, azr azReconciler, numNodes int, primaryNode string, exceptionNodes map[string]int, podNodeAffinities []*nodeaffinity.RequiredNodeAffinity, podTolerations []v1.Toleration, podNodeSelectors, podAffinities labels.Selector, volumeNodeSelectors []*nodeaffinity.NodeSelector) ([]string, error) {
	filteredNodes := []string{}
	var nodes *v1.NodeList
	var err error

	// List all nodes
	nodes = &v1.NodeList{}
	if err = azr.getClient().List(ctx, nodes, &client.ListOptions{LabelSelector: podNodeSelectors}); err != nil {
		klog.Errorf("failed to retrieve node list: %v", err)
		return filteredNodes, err
	}

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
		if err = azr.getClient().List(ctx, pods, &client.ListOptions{LabelSelector: podAffinities}); err != nil {
			klog.Errorf("failed to retrieve pod list: %v", err)
			return filteredNodes, err
		}

		volumes, err := azr.getSharedState().getVolumesForPodObjs(pods.Items...)
		if err != nil {
			klog.Errorf("failed to get volumes for pods (%+v)", pods.Items)
			return filteredNodes, err
		}
		for _, volume := range volumes {
			attachments, err := getAzVolumeAttachmentsForVolume(ctx, azr.getClient(), volume, all)
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
				klog.Infof("Removing node (%s) from replica candidates: node (%s) does not match pod affinity", nodeName, nodeName)
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
				klog.Infof("Removing node (%s) from replica candidates: pod node selector cannot be matched with node (%s).", nodeName, nodeName)
				delete(nodeMap, nodeName)
				continue nodeLoop
			}
		}
	}

	for nodeName, nodeEntry := range nodeMap {
		if _, ok := exceptionNodes[nodeName]; ok || nodeName == primaryNode {
			continue
		}

		remainingCapacity, err := getNodeRemainingCapacity(ctx, azr.getClient(), nodeEntry.node, nodeName)
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

	if len(filteredNodes) > numNodes {
		return filteredNodes[:numNodes], nil
	}
	return filteredNodes, nil
}

func getNodesWithReplica(ctx context.Context, azr azReconciler, volumeName string) ([]string, error) {
	klog.V(5).Infof("Getting nodes with replica AzVolumeAttachments for volume %s.", volumeName)
	azVolumeAttachments, err := getAzVolumeAttachmentsForVolume(ctx, azr.getClient(), volumeName, replicaOnly)
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

func createReplicaAzVolumeAttachment(ctx context.Context, azr azReconciler, volumeID, node string, volumeContext map[string]string) error {
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
	_, err = azr.getAzClient().DiskV1alpha1().AzVolumeAttachments(consts.AzureDiskCrdNamespace).Create(ctx, &v1alpha1.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      replicaName,
			Namespace: consts.AzureDiskCrdNamespace,
			Labels: map[string]string{
				consts.NodeNameLabel:   node,
				consts.VolumeNameLabel: volumeName,
				consts.RoleLabel:       string(v1alpha1.ReplicaRole),
			},
		},
		Spec: v1alpha1.AzVolumeAttachmentSpec{
			NodeName:         node,
			VolumeID:         volumeID,
			UnderlyingVolume: volumeName,
			RequestedRole:    v1alpha1.ReplicaRole,
			VolumeContext:    volumeContext,
		},
		Status: v1alpha1.AzVolumeAttachmentStatus{
			State: v1alpha1.AttachmentPending,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		klog.Warning("Failed creating replica AzVolumeAttachment %s. Error: %v", replicaName, err)
		return err
	}
	klog.V(5).Infof("Replica AzVolumeAttachment %s has been successfully created.", replicaName)
	return nil
}

func cleanUpAzVolumeAttachmentByVolume(ctx context.Context, azr azReconciler, azVolumeName string, caller operationRequester, role roleMode, deleteMode cleanUpMode, controllerSharedState *SharedState) (*v1alpha1.AzVolumeAttachmentList, error) {
	klog.Infof("AzVolumeAttachment clean up requested by %s for AzVolume (%s)", caller, azVolumeName)
	volRequirement, err := CreateLabelRequirements(consts.VolumeNameLabel, selection.Equals, azVolumeName)
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector().Add(*volRequirement)

	attachments, err := azr.getAzClient().DiskV1alpha1().AzVolumeAttachments(consts.AzureDiskCrdNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return nil, nil
		}
		klog.Errorf("failed to get AzVolumeAttachments: %v", err)
		return nil, err
	}

	cleanUps := []v1alpha1.AzVolumeAttachment{}
	for _, attachment := range attachments.Items {
		if shouldCleanUp(attachment, role) {
			cleanUps = append(cleanUps, attachment)
		}
	}

	if err := cleanUpAzVolumeAttachments(ctx, azr, cleanUps, deleteMode, caller); err != nil {
		return attachments, err
	}
	controllerSharedState.unmarkVolumeVisited(azVolumeName)
	klog.Infof("successfully requested deletion of AzVolumeAttachments for AzVolume (%s)", azVolumeName)
	return attachments, nil
}

func cleanUpAzVolumeAttachmentByNode(ctx context.Context, azr azReconciler, azDriverNodeName string, caller operationRequester, role roleMode, deleteMode cleanUpMode) (*v1alpha1.AzVolumeAttachmentList, error) {
	klog.Infof("AzVolumeAttachment clean up requested by %s for AzDriverNode (%s)", caller, azDriverNodeName)
	nodeRequirement, err := CreateLabelRequirements(consts.NodeNameLabel, selection.Equals, azDriverNodeName)
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector().Add(*nodeRequirement)

	attachments, err := azr.getAzClient().DiskV1alpha1().AzVolumeAttachments(consts.AzureDiskCrdNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return attachments, nil
		}
		klog.Errorf("failed to get AzVolumeAttachments: %v", err)
		return nil, err
	}

	cleanUpMap := map[string][]v1alpha1.AzVolumeAttachment{}
	for _, attachment := range attachments.Items {
		if shouldCleanUp(attachment, role) {
			cleanUpMap[attachment.Spec.UnderlyingVolume] = append(cleanUpMap[attachment.Spec.UnderlyingVolume], attachment)
		}
	}

	for v, c := range cleanUpMap {
		volumeName := v
		cleanUps := c
		azr.getSharedState().addToOperationQueue(
			volumeName,
			caller,
			func() error {
				ctx := context.Background()
				err := cleanUpAzVolumeAttachments(ctx, azr, cleanUps, deleteMode, caller)
				if err == nil {
					klog.Infof("successfully requested deletion of AzVolumeAttachments for AzDriverNode (%s)", azDriverNodeName)
				}
				return err
			},
			false)
	}
	return attachments, nil
}

func cleanUpAzVolumeAttachments(ctx context.Context, azr azReconciler, attachments []v1alpha1.AzVolumeAttachment, cleanUp cleanUpMode, caller operationRequester) error {
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
		if cleanUp == detachAndDeleteCRI || patched.Spec.RequestedRole == v1alpha1.ReplicaRole {
			patched.Annotations[consts.VolumeDetachRequestAnnotation] = string(caller)
		}
		if err := azr.getClient().Patch(ctx, patched, client.MergeFrom(&attachment)); err != nil {
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", attachment.Name, err)
			return err
		}
		if err := azr.getAzClient().DiskV1alpha1().AzVolumeAttachments(consts.AzureDiskCrdNamespace).Delete(ctx, attachment.Name, metav1.DeleteOptions{}); err != nil {
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", attachment.Name, err)
			return err
		}
		klog.V(5).Infof("Set deletion timestamp for AzVolumeAttachment (%s)", attachment.Name)
	}
	return nil
}

func getAzVolumeAttachmentsForVolume(ctx context.Context, azclient client.Client, volumeName string, azVolumeAttachmentRole roleMode) (attachments []v1alpha1.AzVolumeAttachment, err error) {
	klog.V(5).Infof("Getting the list of AzVolumeAttachments for %s.", volumeName)
	if azVolumeAttachmentRole == all {
		return getAzVolumeAttachmentsWithLabel(ctx, azclient, labelPair{consts.VolumeNameLabel, volumeName})
	}
	return getAzVolumeAttachmentsWithLabel(ctx, azclient, labelPair{consts.VolumeNameLabel, volumeName}, labelPair{consts.RoleLabel, roles[azVolumeAttachmentRole]})
}

func getAzVolumeAttachmentsForNode(ctx context.Context, azclient client.Client, nodeName string, azVolumeAttachmentRole roleMode) (attachments []v1alpha1.AzVolumeAttachment, err error) {
	klog.V(5).Infof("Getting the list of AzVolumeAttachments for %s.", nodeName)
	if azVolumeAttachmentRole == all {
		return getAzVolumeAttachmentsWithLabel(ctx, azclient, labelPair{consts.NodeNameLabel, nodeName})
	}
	return getAzVolumeAttachmentsWithLabel(ctx, azclient, labelPair{consts.NodeNameLabel, nodeName}, labelPair{consts.RoleLabel, roles[azVolumeAttachmentRole]})
}

func getAzVolumeAttachmentsWithLabel(ctx context.Context, azclient client.Client, labelPairs ...labelPair) (attachments []v1alpha1.AzVolumeAttachment, err error) {
	labelSelector := labels.NewSelector()
	for _, labelPair := range labelPairs {
		var req *labels.Requirement
		req, err = CreateLabelRequirements(labelPair.key, selection.Equals, labelPair.entry)
		if err != nil {
			klog.Errorf("failed to create label (%s, %s) for listing AzVolumeAttachment", labelPair.key, labelPair.entry)
			return
		}
		labelSelector = labelSelector.Add(*req)
	}

	klog.V(5).Infof("Label selector is: %v.", labelSelector)
	azVolumeAttachments := &v1alpha1.AzVolumeAttachmentList{}
	err = azclient.List(ctx, azVolumeAttachments, &client.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		klog.V(5).Infof("Error retrieving AzVolumeAttachments for label %s. Error: %v", labelSelector, err)
		return
	}
	attachments = azVolumeAttachments.Items
	return
}

func shouldCleanUp(attachment v1alpha1.AzVolumeAttachment, mode roleMode) bool {
	return mode == all || (attachment.Spec.RequestedRole == v1alpha1.PrimaryRole && mode == primaryOnly) || (attachment.Spec.RequestedRole == v1alpha1.ReplicaRole && mode == replicaOnly)
}

func isAttached(attachment *v1alpha1.AzVolumeAttachment) bool {
	return attachment != nil && attachment.Status.Detail != nil && attachment.Status.Detail.PublishContext != nil
}

func isCreated(volume *v1alpha1.AzVolume) bool {
	return volume != nil && volume.Status.Detail != nil && volume.Status.Detail.ResponseObject != nil
}

func criDeletionRequested(objectMeta *metav1.ObjectMeta) bool {
	if objectMeta == nil {
		return false
	}
	return !objectMeta.DeletionTimestamp.IsZero() && objectMeta.DeletionTimestamp.Time.Before(time.Now())
}

func volumeDetachRequested(attachment *v1alpha1.AzVolumeAttachment) bool {
	return attachment != nil && attachment.Annotations != nil && metav1.HasAnnotation(attachment.ObjectMeta, consts.VolumeDetachRequestAnnotation)
}

func volumeDeleteRequested(volume *v1alpha1.AzVolume) bool {
	return volume != nil && volume.Annotations != nil && metav1.HasAnnotation(volume.ObjectMeta, consts.VolumeDeleteRequestAnnotation)
}

func preProvisionCleanupRequested(volume *v1alpha1.AzVolume) bool {
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

func CreateLabelRequirements(label string, operator selection.Operator, values ...string) (*labels.Requirement, error) {
	req, err := labels.NewRequirement(label, operator, values)
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("Unable to create Requirement to for label key : (%s) and label value: (%s)", label, values))
	}
	return req, nil
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
	case *v1alpha1.AzVolume:
		return target.Status.State == v1alpha1.VolumeCreating || target.Status.State == v1alpha1.VolumeDeleting || target.Status.State == v1alpha1.VolumeUpdating
	case *v1alpha1.AzVolumeAttachment:
		return target.Status.State == v1alpha1.Attaching || target.Status.State == v1alpha1.Detaching
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
