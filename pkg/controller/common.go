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
	diskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
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

	nodeScoreHighCoefficient = 10
	nodeScoreLowCoefficient  = 1
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
	primaryOnly: string(diskv1beta1.PrimaryRole),
	replicaOnly: string(diskv1beta1.ReplicaRole),
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
		capacityRange *diskv1beta1.CapacityRange,
		volumeCapabilities []diskv1beta1.VolumeCapability,
		parameters map[string]string,
		secrets map[string]string,
		volumeContentSource *diskv1beta1.ContentVolumeSource,
		accessibilityTopology *diskv1beta1.TopologyRequirement) (*diskv1beta1.AzVolumeStatusDetail, error)
	DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error
	PublishVolume(ctx context.Context, volumeID string, nodeID string, volumeContext map[string]string) (map[string]string, error)
	UnpublishVolume(ctx context.Context, volumeID string, nodeID string) error
	ExpandVolume(ctx context.Context, volumeID string, capacityRange *diskv1beta1.CapacityRange, secrets map[string]string) (*diskv1beta1.AzVolumeStatusDetail, error)
	ListVolumes(ctx context.Context, maxEntries int32, startingToken string) (*diskv1beta1.ListVolumesResult, error)
	CreateSnapshot(ctx context.Context, sourceVolumeID string, snapshotName string, secrets map[string]string, parameters map[string]string) (*diskv1beta1.Snapshot, error)
	ListSnapshots(ctx context.Context, maxEntries int32, startingToken string, sourceVolumeID string, snapshotID string, secrets map[string]string) (*diskv1beta1.ListSnapshotsResult, error)
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

func (s set) toStringSlice() []string {
	entries := make([]string, len(s))
	i := 0
	for entry := range s {
		entries[i] = entry.(string)
		i++
	}
	return entries
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
			if err := c.azClient.DiskV1beta1().AzVolumes(c.objectNamespace).Delete(ctx, inline, metav1.DeleteOptions{}); err != nil && !apiErrors.IsNotFound(err) {
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

type filterPlugin interface {
	name() string
	setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, sharedState *SharedState)
	filter(ctx context.Context, nodes []v1.Node) ([]v1.Node, error)
}

// interPodAffinityFilter selects nodes that either meets inter-pod affinity rules or has replica mounts of volumes of pods with matching labels
type interPodAffinityFilter struct {
	pods        []v1.Pod
	sharedState *SharedState
}

func (p *interPodAffinityFilter) name() string {
	return "inter-pod affinity filter"
}

func (p *interPodAffinityFilter) setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, sharedState *SharedState) {
	p.pods = pods
	p.sharedState = sharedState
}

func (p *interPodAffinityFilter) filter(ctx context.Context, nodes []v1.Node) ([]v1.Node, error) {
	nodeMap := map[string]int{}
	candidateNodes := set{}
	for i, node := range nodes {
		nodeMap[node.Name] = i
		candidateNodes.add(node.Name)
	}

	replicaNodes := set{}
	for i, pod := range p.pods {
		if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAffinity == nil {
			continue
		}

		for _, podAffinity := range pod.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			podSelector, err := metav1.LabelSelectorAsSelector(podAffinity.LabelSelector)
			// if failed to convert pod affinity label selector to selector, log error and skip
			if err != nil {
				klog.Errorf("failed to convert pod affinity (%v) to selector", podAffinity.LabelSelector)
				continue
			}
			requirements, selectable := podSelector.Requirements()
			if selectable {
				podAffinities := labels.NewSelector().Add(requirements...)
				pods := []v1.Pod{}

				if len(podAffinity.Namespaces) > 0 {
					for _, namespace := range podAffinity.Namespaces {
						podList := &v1.PodList{}
						if err = p.sharedState.cachedClient.List(ctx, podList, &client.ListOptions{LabelSelector: podAffinities, Namespace: namespace}); err != nil {
							klog.Errorf("failed to retrieve pod list: %v", err)
							continue
						}
						pods = append(pods, podList.Items...)
					}
				} else {
					podList := &v1.PodList{}
					if err = p.sharedState.cachedClient.List(ctx, podList, &client.ListOptions{LabelSelector: podAffinities}); err != nil {
						klog.Errorf("failed to retrieve pod list: %v", err)
						continue
					}
					pods = podList.Items
				}

				nodesWithSelectedPods := set{}
				for _, pod := range pods {
					klog.Infof("Pod (%s) has matching label for pod affinity (%v)", getQualifiedName(pod.Namespace, pod.Name), podAffinities)
					nodesWithSelectedPods.add(pod.Spec.NodeName)
				}

				// add nodes, to which replica attachments of matching pods' volumes are attached, to replicaNodes
				if volumes, err := p.sharedState.getVolumesForPodObjs(pods); err == nil {
					for _, volume := range volumes {
						attachments, err := getAzVolumeAttachmentsForVolume(ctx, p.sharedState.cachedClient, volume, replicaOnly)
						if err != nil {
							continue
						}

						nodeChecker := set{}
						for _, attachment := range attachments {
							if i == 0 {
								klog.V(5).Infof("Adding node (%s) to the replica node set: replica mounts of volume (%s) found on node", attachment.Spec.NodeName, volume)
								replicaNodes.add(attachment.Spec.NodeName)
							} else {
								nodeChecker.add(attachment.Spec.NodeName)
							}
						}
						if i > 0 {
							// take an intersection of the current pod list's replica nodes with those of preceding pod lists.
							for node := range replicaNodes {
								if !nodeChecker.has(node) {
									klog.V(5).Infof("Removing node (%s) from the replica node set: replica mounts of volume (%s) cannot be found on node", node, volume)
									replicaNodes.remove(node)
								}
							}
						}
					}
				}

				qualifiedLabelSet := map[string]set{}
				for node := range nodesWithSelectedPods {
					var nodeInstance v1.Node
					if err = p.sharedState.cachedClient.Get(ctx, types.NamespacedName{Name: node.(string)}, &nodeInstance); err != nil {
						klog.Errorf("failed to get node (%s)", node.(string))
						continue
					}
					if value, exists := nodeInstance.GetLabels()[podAffinity.TopologyKey]; exists {
						labelSet, exists := qualifiedLabelSet[podAffinity.TopologyKey]
						if !exists {
							labelSet = set{}
						}
						labelSet.add(value)
						qualifiedLabelSet[podAffinity.TopologyKey] = labelSet
					} else {
						klog.Warningf("node (%s) doesn't have label value for topologyKey (%s)", nodeInstance.Name, podAffinity.TopologyKey)
					}
				}

				selector := labels.NewSelector()
				for key, values := range qualifiedLabelSet {
					labelValues := values.toStringSlice()
					requirements, err := azureutils.CreateLabelRequirements(key, selection.In, labelValues...)
					if err != nil {
						klog.Errorf("failed to create label for key (%s) and values (%+v) :%v", key, labelValues, err)
						continue
					}
					selector = selector.Add(*requirements)
				}

				// remove any candidate node which does not satisfy the pod affinity
				for candidateNode := range candidateNodes {
					if i, exists := nodeMap[candidateNode.(string)]; exists {
						node := nodes[i]
						nodeLabels := labels.Set(node.Labels)
						if !selector.Matches(nodeLabels) {
							klog.Infof("Removing node (%s) from candidate nodes: node does not satisfy inter-pod-affinity (%v), label (%v)", candidateNode, podAffinity, selector)
							candidateNodes.remove(candidateNode)
						}
					}
				}
			}
		}
	}

	filteredNodes := []v1.Node{}
	for replicaNode := range replicaNodes {
		klog.Infof("Adding node (%s) to candidate node list: node has a replica mount of qualifying pods", replicaNode)
		candidateNodes.add(replicaNode)
	}
	for candidateNode := range candidateNodes {
		if i, exists := nodeMap[candidateNode.(string)]; exists {
			filteredNodes = append(filteredNodes, nodes[i])
		}
	}
	return filteredNodes, nil
}

type interPodAntiAffinityFilter struct {
	pods        []v1.Pod
	sharedState *SharedState
}

func (p *interPodAntiAffinityFilter) name() string {
	return "inter-pod anti-affinity filter"
}

func (p *interPodAntiAffinityFilter) setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, sharedState *SharedState) {
	p.pods = pods
	p.sharedState = sharedState
}

func (p *interPodAntiAffinityFilter) filter(ctx context.Context, nodes []v1.Node) ([]v1.Node, error) {
	nodeMap := map[string]int{}
	candidateNodes := set{}

	for i, node := range nodes {
		nodeMap[node.Name] = i
		candidateNodes.add(node.Name)
	}

	for _, pod := range p.pods {
		if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAntiAffinity == nil {
			continue
		}

		for _, podAntiAffinity := range pod.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			podSelector, err := metav1.LabelSelectorAsSelector(podAntiAffinity.LabelSelector)
			// if failed to convert pod affinity label selector to selector, log error and skip
			if err != nil {
				klog.Errorf("failed to convert pod anti-affinity (%v) to selector", podAntiAffinity.LabelSelector)
				continue
			}
			requirements, selectable := podSelector.Requirements()
			if selectable {
				podAntiAffinities := labels.NewSelector().Add(requirements...)
				pods := []v1.Pod{}

				if len(podAntiAffinity.Namespaces) > 0 {
					for _, namespace := range podAntiAffinity.Namespaces {
						podList := &v1.PodList{}
						if err = p.sharedState.cachedClient.List(ctx, podList, &client.ListOptions{LabelSelector: podAntiAffinities, Namespace: namespace}); err != nil {
							klog.Errorf("failed to retrieve pod list: %v", err)
							continue
						}
						pods = append(pods, podList.Items...)
					}
				} else {
					podList := &v1.PodList{}
					if err = p.sharedState.cachedClient.List(ctx, podList, &client.ListOptions{LabelSelector: podAntiAffinities}); err != nil {
						klog.Errorf("failed to retrieve pod list: %v", err)
						continue
					}
					pods = podList.Items
				}

				nodesWithSelectedPods := set{}
				for _, pod := range pods {
					nodesWithSelectedPods.add(pod.Spec.NodeName)
				}

				qualifiedLabelSet := map[string]set{}
				for node := range nodesWithSelectedPods {
					var nodeInstance v1.Node
					if err = p.sharedState.cachedClient.Get(ctx, types.NamespacedName{Name: node.(string)}, &nodeInstance); err != nil {
						klog.Errorf("failed to get node (%s)", node.(string))
						continue
					}
					if value, exists := nodeInstance.Labels[podAntiAffinity.TopologyKey]; exists {
						labelSet, exists := qualifiedLabelSet[podAntiAffinity.TopologyKey]
						if !exists {
							labelSet = set{}
						}
						labelSet.add(value)
						qualifiedLabelSet[podAntiAffinity.TopologyKey] = labelSet
					}
				}

				selector := labels.NewSelector()
				for key, values := range qualifiedLabelSet {
					labelValues := values.toStringSlice()
					requirements, err := azureutils.CreateLabelRequirements(key, selection.In, labelValues...)
					if err != nil {
						klog.Errorf("failed to create label for key (%s) and values (%+v) :%v", key, labelValues, err)
						continue
					}
					selector = selector.Add(*requirements)
				}

				// remove any candidate node which does not satisfy the pod affinity
				for candidateNode := range candidateNodes {
					if i, exists := nodeMap[candidateNode.(string)]; exists {
						node := nodes[i]
						nodeLabels := labels.Set(node.Labels)
						if selector.Matches(nodeLabels) {
							klog.Infof("Removing node (%s) from candidate nodes: node satisfies inter-pod anti-affinity (%v), label (%v)", candidateNode, podAntiAffinity, selector)
							candidateNodes.remove(candidateNode)
						}
					}
				}
			}
		}
	}

	filteredNodes := []v1.Node{}
	for candidateNode := range candidateNodes {
		if i, exists := nodeMap[candidateNode.(string)]; exists {
			filteredNodes = append(filteredNodes, nodes[i])
		}
	}
	return filteredNodes, nil
}

type podTolerationFilter struct {
	pods []v1.Pod
}

func (p *podTolerationFilter) name() string {
	return "pod toleration filter"
}

func (p *podTolerationFilter) setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, sharedState *SharedState) {
	p.pods = pods
}

func (p *podTolerationFilter) filter(ctx context.Context, nodes []v1.Node) ([]v1.Node, error) {
	candidateNodes := set{}
	for i := range nodes {
		candidateNodes.add(i)
	}

	podTolerations := set{}

	for i, pod := range p.pods {
		podTolerationMap := map[string]*v1.Toleration{}
		for _, podToleration := range pod.Spec.Tolerations {
			podToleration := &podToleration
			if i == 0 {
				podTolerations.add(podToleration)
			} else {
				podTolerationMap[podToleration.Key] = podToleration
			}
		}
		if i > 0 {
			for podToleration := range podTolerations {
				if existingToleration, ok := podTolerationMap[podToleration.(v1.Toleration).Key]; ok {
					if !podToleration.(*v1.Toleration).MatchToleration(existingToleration) {
						podTolerations.remove(podToleration)
					}
				}
			}
		}
	}

	for candidateNode := range candidateNodes {
		tolerable := true
		node := nodes[candidateNode.(int)]
		for _, taint := range node.Spec.Taints {
			taintTolerable := false
			for podToleration := range podTolerations {
				// if any one of node's taint cannot be tolerated by pod's tolerations, break
				if podToleration.(*v1.Toleration).ToleratesTaint(&taint) {
					taintTolerable = true
				}
			}
			if tolerable = tolerable && taintTolerable; !tolerable {
				klog.Infof("Removing node (%s) from replica candidates: node (%s)'s taint cannot be tolerated", node.Name, node.Name)
				candidateNodes.remove(candidateNode)
				break
			}
		}
	}

	filteredNodes := make([]v1.Node, len(candidateNodes))
	i := 0
	for candidateNode := range candidateNodes {
		filteredNodes[i] = nodes[candidateNode.(int)]
		i++
	}
	return filteredNodes, nil
}

type podNodeAffinityFilter struct {
	pods []v1.Pod
}

func (p *podNodeAffinityFilter) name() string {
	return "pod node-affinity filter"
}

func (p *podNodeAffinityFilter) setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, sharedState *SharedState) {
	p.pods = pods
}

func (p *podNodeAffinityFilter) filter(ctx context.Context, nodes []v1.Node) ([]v1.Node, error) {
	var podNodeAffinities []nodeaffinity.RequiredNodeAffinity

	candidateNodes := set{}
	for i := range nodes {
		candidateNodes.add(i)
	}

	for _, pod := range p.pods {
		// acknowledge that there can be duplicate entries within the slice
		podNodeAffinity := nodeaffinity.GetRequiredNodeAffinity(&pod)
		podNodeAffinities = append(podNodeAffinities, podNodeAffinity)
	}

	for i, node := range nodes {
		for _, podNodeAffinity := range podNodeAffinities {
			if match, err := podNodeAffinity.Match(&node); !match || err != nil {
				klog.Infof("Removing node (%s) from replica candidates: node does not match pod node affinity (%+v)", node.Name, podNodeAffinity)
				candidateNodes.remove(i)
			}
		}
	}

	filteredNodes := make([]v1.Node, len(candidateNodes))
	i := 0
	for candidateNode := range candidateNodes {
		filteredNodes[i] = nodes[candidateNode.(int)]
		i++
	}
	return filteredNodes, nil
}

type podNodeSelectorFilter struct {
	pods        []v1.Pod
	sharedState *SharedState
}

func (p *podNodeSelectorFilter) name() string {
	return "pod node-selector filter"
}

func (p *podNodeSelectorFilter) setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, sharedState *SharedState) {
	p.pods = pods
	p.sharedState = sharedState
}

func (p *podNodeSelectorFilter) filter(ctx context.Context, nodes []v1.Node) ([]v1.Node, error) {
	candidateNodes := set{}
	for i := range nodes {
		candidateNodes.add(i)
	}

	podNodeSelector := labels.NewSelector()
	for _, pod := range p.pods {
		nodeSelector := labels.SelectorFromSet(labels.Set(pod.Spec.NodeSelector))
		requirements, selectable := nodeSelector.Requirements()
		if selectable {
			podNodeSelector = podNodeSelector.Add(requirements...)
		}
	}

	filteredNodes := []v1.Node{}
	for candidateNode := range candidateNodes {
		node := nodes[candidateNode.(int)]
		nodeLabels := labels.Set(node.Labels)
		if podNodeSelector.Matches(nodeLabels) {
			filteredNodes = append(filteredNodes, node)
		} else {
			klog.Infof("Removing node (%s) from replica candidate: node does not match pod node selector (%v)", podNodeSelector)
		}
	}

	return filteredNodes, nil
}

type volumeNodeSelectorFilter struct {
	persistentVolumes []*v1.PersistentVolume
}

func (v *volumeNodeSelectorFilter) name() string {
	return "volume node-selector filter"
}

func (v *volumeNodeSelectorFilter) setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, sharedState *SharedState) {
	v.persistentVolumes = persistentVolumes
}

func (v *volumeNodeSelectorFilter) filter(ctx context.Context, nodes []v1.Node) ([]v1.Node, error) {
	candidateNodes := set{}
	for i := range nodes {
		candidateNodes.add(i)
	}

	var volumeNodeSelectors []*nodeaffinity.NodeSelector
	for _, pv := range v.persistentVolumes {
		if pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
			continue
		}
		nodeSelector, err := nodeaffinity.NewNodeSelector(pv.Spec.NodeAffinity.Required)
		if err != nil {
			klog.Errorf("failed to get node selector from node affinity (%v): %v", pv.Spec.NodeAffinity.Required, err)
			continue
		}
		// acknowledge that there can be duplicates in the slice
		volumeNodeSelectors = append(volumeNodeSelectors, nodeSelector)
	}

	for candidateNode := range candidateNodes {
		node := nodes[candidateNode.(int)]
		for _, volumeNodeSelector := range volumeNodeSelectors {
			if !volumeNodeSelector.Match(&node) {
				klog.Infof("Removing node (%s) from replica candidates: volume node selector (%+v) cannot be matched with the node.", node.Name, volumeNodeSelector)
				candidateNodes.remove(candidateNode)
			}
		}
	}

	filteredNodes := make([]v1.Node, len(candidateNodes))
	i := 0
	for candidateNode := range candidateNodes {
		filteredNodes[i] = nodes[candidateNode.(int)]
		i++
	}
	return filteredNodes, nil
}

type nodeScorerPlugin interface {
	name() string
	setup(pods []v1.Pod, volumes []string, sharedState *SharedState)
	score(ctx context.Context, nodeScores map[string]int) (map[string]int, error)
}

type scoreByNodeCapacity struct {
	volumes     []string
	sharedState *SharedState
}

func (s *scoreByNodeCapacity) name() string {
	return "score by node capacity"
}

func (s *scoreByNodeCapacity) setup(pods []v1.Pod, volumes []string, sharedState *SharedState) {
	s.volumes = volumes
	s.sharedState = sharedState
}

func (s *scoreByNodeCapacity) score(ctx context.Context, nodeScores map[string]int) (map[string]int, error) {
	for nodeName, score := range nodeScores {
		remainingCapacity, err := getNodeRemainingCapacity(ctx, s.sharedState.cachedClient, nil, nodeName)
		if err != nil {
			// if failed to get node's remaining capacity, remove the node from the candidate list and proceed
			klog.Errorf("failed to get remaining capacity of node (%s): %v", nodeName, err)
			delete(nodeScores, nodeName)
		}
		if remainingCapacity-len(s.volumes) <= 0 {
			delete(nodeScores, nodeName)
		}

		nodeScores[nodeName] = score + (nodeScoreLowCoefficient * remainingCapacity)
		klog.Infof("node (%s) can accept %d more attachments", nodeName, remainingCapacity)
	}
	return nodeScores, nil
}

type scoreByReplicaCount struct {
	volumes     []string
	sharedState *SharedState
}

func (s *scoreByReplicaCount) name() string {
	return "score by replica count"
}

func (s *scoreByReplicaCount) setup(pods []v1.Pod, volumes []string, sharedState *SharedState) {
	s.volumes = volumes
	s.sharedState = sharedState
}

func (s *scoreByReplicaCount) score(ctx context.Context, nodeScores map[string]int) (map[string]int, error) {
	for _, volume := range s.volumes {
		azVolumeAttachments, err := getAzVolumeAttachmentsForVolume(ctx, s.sharedState.cachedClient, volume, all)
		if err != nil {
			klog.V(5).Infof("Error listing AzVolumeAttachments for azvolume %s. Error: %v.", volume, err)
			continue
		}

		for _, azVolumeAttachment := range azVolumeAttachments {
			if score, exists := nodeScores[azVolumeAttachment.Spec.NodeName]; exists {
				if azVolumeAttachment.Spec.RequestedRole == diskv1beta1.PrimaryRole {
					delete(nodeScores, azVolumeAttachment.Spec.NodeName)
				} else {
					nodeScores[azVolumeAttachment.Spec.NodeName] = score + nodeScoreHighCoefficient
				}
			}
		}
	}
	return nodeScores, nil
}

func (c *SharedState) getRankedNodesForReplicaAttachments(ctx context.Context, volumes []string, podObjs []v1.Pod) ([]string, error) {
	klog.V(5).Info("Getting ranked list of nodes for creating AzVolumeAttachments")

	nodeList := &v1.NodeList{}
	if err := c.cachedClient.List(ctx, nodeList); err != nil {
		return nil, err
	}

	selectedNodeObjs, err := c.selectNodesPerTopology(ctx, nodeList.Items, podObjs, volumes)
	if err != nil {
		klog.Errorf("failed to select nodes for volumes (%+v): %v", volumes, err)
		return nil, err
	}

	selectedNodes := make([]string, len(selectedNodeObjs))
	for i, selectedNodeObj := range selectedNodeObjs {
		selectedNodes[i] = selectedNodeObj.Name
	}

	klog.Infof("Selected nodes (%+v) for replica AzVolumeAttachments for volumes (%+v)", selectedNodes, volumes)
	return selectedNodes, nil
}

func (c *SharedState) filterNodes(ctx context.Context, nodes []v1.Node, pods []v1.Pod, volumes []string) ([]v1.Node, error) {
	pvs := make([]*v1.PersistentVolume, len(volumes))
	for i, volume := range volumes {
		azVolume, err := azureutils.GetAzVolume(ctx, c.cachedClient, c.azClient, volume, c.objectNamespace, true)
		if err != nil {
			klog.V(5).Infof("AzVolume for volume %s is not found.", volume)
			return nil, err
		}

		var pv v1.PersistentVolume
		if err := c.cachedClient.Get(ctx, types.NamespacedName{Name: azVolume.Status.PersistentVolume}, &pv); err != nil {
			return nil, err
		}
		pvs[i] = &pv
	}

	var filterPlugins = []filterPlugin{
		&interPodAffinityFilter{},
		&interPodAntiAffinityFilter{},
		&podTolerationFilter{},
		&podNodeAffinityFilter{},
		&podNodeSelectorFilter{},
		&volumeNodeSelectorFilter{},
	}

	filteredNodes := nodes
	for _, filterPlugin := range filterPlugins {
		filterPlugin.setup(pods, pvs, c)
		if updatedFilteredNodes, err := filterPlugin.filter(ctx, filteredNodes); err != nil {
			klog.Errorf("failed to filter node with filter plugin (%s): %v", filterPlugin.name(), err)
		} else {
			filteredNodes = updatedFilteredNodes
			nodeStrs := make([]string, len(filteredNodes))
			for i, filteredNode := range filteredNodes {
				nodeStrs[i] = filteredNode.Name
			}
			klog.Infof("Filtered node list from filter plugin (%s): %+v", filterPlugin.name(), nodeStrs)
		}
	}

	return filteredNodes, nil
}

func (c *SharedState) prioritizeNodes(ctx context.Context, pods []v1.Pod, volumes []string, nodes []v1.Node) []v1.Node {
	nodeScores := map[string]int{}
	for _, node := range nodes {
		nodeScores[node.Name] = 0
	}

	var nodeScorerPlugins = []nodeScorerPlugin{
		&scoreByNodeCapacity{},
		&scoreByReplicaCount{},
	}

	for _, nodeScorerPlugin := range nodeScorerPlugins {
		nodeScorerPlugin.setup(pods, volumes, c)
		if updatedNodeScores, err := nodeScorerPlugin.score(ctx, nodeScores); err != nil {
			klog.Errorf("failed to score nodes by node scorer (%s): %v", nodeScorerPlugin.name(), err)
		} else {
			// update node scores if scorer plugin returned success
			nodeScores = updatedNodeScores
		}
	}

	// normalize score
	for _, node := range nodes {
		if _, exists := nodeScores[node.Name]; !exists {
			nodeScores[node.Name] = -1
		}
	}

	sort.Slice(nodes[:], func(i, j int) bool {
		return nodeScores[nodes[i].Name] > nodeScores[nodes[j].Name]
	})

	return nodes
}

func (c *SharedState) filterAndSortNodes(ctx context.Context, nodes []v1.Node, pods []v1.Pod, volumes []string) ([]v1.Node, error) {
	filteredNodes, err := c.filterNodes(ctx, nodes, pods, volumes)
	if err != nil {
		klog.Errorf("failed to filter nodes for volumes (%+v): %v", volumes, err)
		return nil, err
	}
	sortedNodes := c.prioritizeNodes(ctx, pods, volumes, filteredNodes)
	return sortedNodes, nil
}

func (c *SharedState) selectNodesPerTopology(ctx context.Context, nodes []v1.Node, pods []v1.Pod, volumes []string) ([]v1.Node, error) {
	selectedNodes := []v1.Node{}
	numReplicas := 0

	// disperse node topology if possible
	compatibleZonesSet := set{}
	var primaryNode string
	for i, volume := range volumes {
		azVolume, err := azureutils.GetAzVolume(ctx, c.cachedClient, c.azClient, volume, c.objectNamespace, true)
		if err != nil {
			klog.V(5).Infof("AzVolume for volume %s is not found.", volume)
			return nil, err
		}

		numReplicas = max(numReplicas, azVolume.Spec.MaxMountReplicaCount)
		klog.V(5).Infof("Number of requested replicas for azvolume %s is: %d. Max replica count is: %d.",
			volume, numReplicas, azVolume.Spec.MaxMountReplicaCount)

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
			listOfZones := getSupportedZones(pv.Spec.NodeAffinity.Required.NodeSelectorTerms, topologyKey)
			for key := range compatibleZonesSet {
				if !listOfZones.has(key) {
					compatibleZonesSet.remove(key)
				}
			}
		}

		// find primary node if not already found
		if primaryNode == "" {
			if primaryAttachment, err := getAzVolumeAttachmentsForVolume(ctx, c.cachedClient, volume, primaryOnly); err != nil || len(primaryAttachment) == 0 {
				continue
			} else {
				primaryNode = primaryAttachment[0].Spec.NodeName
			}
		}
	}

	var compatibleZones []string
	if len(compatibleZonesSet) > 0 {
		for key := range compatibleZonesSet {
			compatibleZones = append(compatibleZones, key.(string))
		}
	}

	if len(compatibleZones) == 0 {
		var err error
		selectedNodes, err = c.filterAndSortNodes(ctx, nodes, pods, volumes)
		if err != nil {
			klog.Errorf("failed to select nodes for volumes (%+v): %v", volumes, err)
			return nil, err
		}
	} else {
		klog.Infof("The list of zones to select nodes from is: %s", strings.Join(compatibleZones, ","))

		var primaryNodeZone string
		if primaryNode != "" {
			nodeObj := &v1.Node{}
			err := c.cachedClient.Get(ctx, types.NamespacedName{Name: primaryNode}, nodeObj)
			if err != nil {
				klog.Errorf("failed to retrieve the primary node: %v", err)
			}

			var ok bool
			if primaryNodeZone, ok = nodeObj.Labels[consts.WellKnownTopologyKey]; ok {
				klog.Infof("failed to find zone annotations for primary node")
			}
		}

		nodeSelector := labels.NewSelector()
		zoneRequirement, _ := labels.NewRequirement(consts.WellKnownTopologyKey, selection.In, compatibleZones)
		nodeSelector = nodeSelector.Add(*zoneRequirement)

		compatibleNodes := &v1.NodeList{}
		if err := c.cachedClient.List(ctx, compatibleNodes, &client.ListOptions{LabelSelector: nodeSelector}); err != nil {
			klog.Errorf("failed to retrieve node list: %v", err)
			return nodes, err
		}

		// Create a zone to node map
		zoneToNodeMap := map[string][]v1.Node{}
		for _, node := range compatibleNodes.Items {
			zoneName := node.Labels[consts.WellKnownTopologyKey]
			zoneToNodeMap[zoneName] = append(zoneToNodeMap[zoneName], node)
		}

		// Get prioritized nodes per zone
		nodesPerZone := [][]v1.Node{}
		primaryZoneNodes := []v1.Node{}
		totalCount := 0
		for zone, nodeList := range zoneToNodeMap {
			sortedNodes, err := c.filterAndSortNodes(ctx, nodeList, pods, volumes)
			if err != nil {
				klog.Errorf("failed to select nodes for volumes (%+v): %v", volumes, err)
				return nil, err
			}

			totalCount += len(sortedNodes)
			if zone == primaryNodeZone {
				primaryZoneNodes = sortedNodes
				continue
			}
			nodesPerZone = append(nodesPerZone, sortedNodes)
		}
		// Append the nodes from the zone of the primary node at last
		if len(primaryZoneNodes) > 0 {
			nodesPerZone = append(nodesPerZone, primaryZoneNodes)
		}
		// Select the nodes from each of the zones one by one and append to the list
		i, j, countSoFar := 0, 0, 0
		for len(selectedNodes) < numReplicas && countSoFar < totalCount {
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
	}

	return selectedNodes[:min(len(selectedNodes), numReplicas)], nil
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
	_, err = c.azClient.DiskV1beta1().AzVolumeAttachments(c.objectNamespace).Create(ctx, &diskv1beta1.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      replicaName,
			Namespace: c.objectNamespace,
			Labels: map[string]string{
				consts.NodeNameLabel:   node,
				consts.VolumeNameLabel: volumeName,
				consts.RoleLabel:       string(diskv1beta1.ReplicaRole),
			},
		},
		Spec: diskv1beta1.AzVolumeAttachmentSpec{
			NodeName:      node,
			VolumeID:      volumeID,
			VolumeName:    volumeName,
			RequestedRole: diskv1beta1.ReplicaRole,
			VolumeContext: volumeContext,
		},
		Status: diskv1beta1.AzVolumeAttachmentStatus{
			State: diskv1beta1.AttachmentPending,
		},
	}, metav1.CreateOptions{})
	if err != nil {
		klog.Warning("Failed creating replica AzVolumeAttachment %s. Error: %v", replicaName, err)
		return err
	}
	klog.V(5).Infof("Replica AzVolumeAttachment %s has been successfully created.", replicaName)
	return nil
}

func (c *SharedState) cleanUpAzVolumeAttachmentByVolume(ctx context.Context, azVolumeName string, caller operationRequester, role roleMode, deleteMode cleanUpMode) (*diskv1beta1.AzVolumeAttachmentList, error) {
	klog.Infof("AzVolumeAttachment clean up requested by %s for AzVolume (%s)", caller, azVolumeName)
	volRequirement, err := azureutils.CreateLabelRequirements(consts.VolumeNameLabel, selection.Equals, azVolumeName)
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector().Add(*volRequirement)

	attachments, err := c.azClient.DiskV1beta1().AzVolumeAttachments(c.objectNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return nil, nil
		}
		klog.Errorf("failed to get AzVolumeAttachments: %v", err)
		return nil, err
	}

	cleanUps := []diskv1beta1.AzVolumeAttachment{}
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

func (c *SharedState) cleanUpAzVolumeAttachmentByNode(ctx context.Context, azDriverNodeName string, caller operationRequester, role roleMode, deleteMode cleanUpMode) (*diskv1beta1.AzVolumeAttachmentList, error) {
	klog.Infof("AzVolumeAttachment clean up requested by %s for AzDriverNode (%s)", caller, azDriverNodeName)
	nodeRequirement, err := azureutils.CreateLabelRequirements(consts.NodeNameLabel, selection.Equals, azDriverNodeName)
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector().Add(*nodeRequirement)

	attachments, err := c.azClient.DiskV1beta1().AzVolumeAttachments(c.objectNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			return attachments, nil
		}
		klog.Errorf("failed to get AzVolumeAttachments: %v", err)
		return nil, err
	}

	cleanUpMap := map[string][]diskv1beta1.AzVolumeAttachment{}
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

func (c *SharedState) cleanUpAzVolumeAttachments(ctx context.Context, attachments []diskv1beta1.AzVolumeAttachment, cleanUp cleanUpMode, caller operationRequester) error {
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
		if cleanUp == detachAndDeleteCRI || patched.Spec.RequestedRole == diskv1beta1.ReplicaRole {
			patched.Annotations[consts.VolumeDetachRequestAnnotation] = string(caller)
		}
		if err := c.cachedClient.Patch(ctx, patched, client.MergeFrom(&attachment)); err != nil {
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", attachment.Name, err)
			return err
		}
		if err := c.azClient.DiskV1beta1().AzVolumeAttachments(c.objectNamespace).Delete(ctx, attachment.Name, metav1.DeleteOptions{}); err != nil {
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", attachment.Name, err)
			return err
		}
		klog.V(5).Infof("Set deletion timestamp for AzVolumeAttachment (%s)", attachment.Name)
	}
	return nil
}

func getAzVolumeAttachmentsForVolume(ctx context.Context, azclient client.Client, volumeName string, azVolumeAttachmentRole roleMode) (attachments []diskv1beta1.AzVolumeAttachment, err error) {
	klog.V(5).Infof("Getting the list of AzVolumeAttachments for %s.", volumeName)
	if azVolumeAttachmentRole == all {
		return getAzVolumeAttachmentsWithLabel(ctx, azclient, labelPair{consts.VolumeNameLabel, volumeName})
	}
	return getAzVolumeAttachmentsWithLabel(ctx, azclient, labelPair{consts.VolumeNameLabel, volumeName}, labelPair{consts.RoleLabel, roles[azVolumeAttachmentRole]})
}

func getAzVolumeAttachmentsForNode(ctx context.Context, azclient client.Client, nodeName string, azVolumeAttachmentRole roleMode) (attachments []diskv1beta1.AzVolumeAttachment, err error) {
	klog.V(5).Infof("Getting the list of AzVolumeAttachments for %s.", nodeName)
	if azVolumeAttachmentRole == all {
		return getAzVolumeAttachmentsWithLabel(ctx, azclient, labelPair{consts.NodeNameLabel, nodeName})
	}
	return getAzVolumeAttachmentsWithLabel(ctx, azclient, labelPair{consts.NodeNameLabel, nodeName}, labelPair{consts.RoleLabel, roles[azVolumeAttachmentRole]})
}

func getAzVolumeAttachmentsWithLabel(ctx context.Context, azclient client.Client, labelPairs ...labelPair) (attachments []diskv1beta1.AzVolumeAttachment, err error) {
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
	azVolumeAttachments := &diskv1beta1.AzVolumeAttachmentList{}
	err = azclient.List(ctx, azVolumeAttachments, &client.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		klog.V(5).Infof("Error retrieving AzVolumeAttachments for label %s. Error: %v", labelSelector, err)
		return
	}
	attachments = azVolumeAttachments.Items
	return
}

func shouldCleanUp(attachment diskv1beta1.AzVolumeAttachment, mode roleMode) bool {
	return mode == all || (attachment.Spec.RequestedRole == diskv1beta1.PrimaryRole && mode == primaryOnly) || (attachment.Spec.RequestedRole == diskv1beta1.ReplicaRole && mode == replicaOnly)
}

func isAttached(attachment *diskv1beta1.AzVolumeAttachment) bool {
	return attachment != nil && attachment.Status.Detail != nil && attachment.Status.Detail.PublishContext != nil
}

func isCreated(volume *diskv1beta1.AzVolume) bool {
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

func volumeDetachRequested(attachment *diskv1beta1.AzVolumeAttachment) bool {
	return attachment != nil && attachment.Annotations != nil && metav1.HasAnnotation(attachment.ObjectMeta, consts.VolumeDetachRequestAnnotation)
}

func volumeDeleteRequested(volume *diskv1beta1.AzVolume) bool {
	return volume != nil && volume.Annotations != nil && metav1.HasAnnotation(volume.ObjectMeta, consts.VolumeDeleteRequestAnnotation)
}

func isDemotionRequested(attachment *diskv1beta1.AzVolumeAttachment) bool {
	return attachment != nil && attachment.Status.Detail != nil && attachment.Status.Detail.Role == diskv1beta1.PrimaryRole && attachment.Spec.RequestedRole == diskv1beta1.ReplicaRole
}

func isPreProvisionCleanupRequested(volume *diskv1beta1.AzVolume) bool {
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
	case *diskv1beta1.AzVolume:
		return target.Status.State == diskv1beta1.VolumeCreating || target.Status.State == diskv1beta1.VolumeDeleting || target.Status.State == diskv1beta1.VolumeUpdating
	case *diskv1beta1.AzVolumeAttachment:
		return target.Status.State == diskv1beta1.Attaching || target.Status.State == diskv1beta1.Detaching
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
		if azVolume.Status.Detail == nil || azVolume.Status.State == diskv1beta1.VolumeDeleting || azVolume.Status.State == diskv1beta1.VolumeDeleted {
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
