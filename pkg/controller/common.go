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
	"os"
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
	crd "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	crdClientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	crdInformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
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
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azdiskinformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/provisioner"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/watcher"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"

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
	pv               operationRequester = "pv-controller"
	replica          operationRequester = "replica-controller"
	nodeavailability operationRequester = "nodeavailability-controller"
	pod                                 = "pod-controller"
)

type cleanUpMode int

const (
	deleteCRIOnly cleanUpMode = iota
	detachAndDeleteCRI
)

type updateMode int

const (
	normalUpdate updateMode = iota
	forceUpdate
)

type updateWithLock bool

const (
	acquireLock updateWithLock = true
	skipLock    updateWithLock = false
)

type goSignal struct{}

// TODO Make CloudProvisioner independent of csi types.
type CloudProvisioner interface {
	CreateVolume(
		ctx context.Context,
		volumeName string,
		capacityRange *azdiskv1beta2.CapacityRange,
		volumeCapabilities []azdiskv1beta2.VolumeCapability,
		parameters map[string]string,
		secrets map[string]string,
		volumeContentSource *azdiskv1beta2.ContentVolumeSource,
		accessibilityTopology *azdiskv1beta2.TopologyRequirement) (*azdiskv1beta2.AzVolumeStatusDetail, error)
	DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error
	PublishVolume(ctx context.Context, volumeID string, nodeID string, volumeContext map[string]string) provisioner.CloudAttachResult
	UnpublishVolume(ctx context.Context, volumeID string, nodeID string) error
	ExpandVolume(ctx context.Context, volumeID string, capacityRange *azdiskv1beta2.CapacityRange, secrets map[string]string) (*azdiskv1beta2.AzVolumeStatusDetail, error)
	ListVolumes(ctx context.Context, maxEntries int32, startingToken string) (*azdiskv1beta2.ListVolumesResult, error)
	CreateSnapshot(ctx context.Context, sourceVolumeID string, snapshotName string, secrets map[string]string, parameters map[string]string) (*azdiskv1beta2.Snapshot, error)
	ListSnapshots(ctx context.Context, maxEntries int32, startingToken string, sourceVolumeID string, snapshotID string, secrets map[string]string) (*azdiskv1beta2.ListSnapshotsResult, error)
	DeleteSnapshot(ctx context.Context, snapshotID string, secrets map[string]string) error
	CheckDiskExists(ctx context.Context, diskURI string) (*compute.Disk, error)
	GetCloud() *provider.Cloud
	GetMetricPrefix() string
}

type replicaOperation struct {
	ctx                        context.Context
	requester                  operationRequester
	operationFunc              func(context.Context) error
	isReplicaGarbageCollection bool
}

type operationQueue struct {
	*list.List
	gcExclusionList set
	isActive        bool
}

func (q *operationQueue) remove(element *list.Element) {
	// operationQueue might have been cleared before the lock was acquired
	// so always check if the list is empty or not before removing object from the queue, otherwise it would set the underlying length of the queue to be < 0, causing issues
	if q.Front() != nil {
		_ = q.Remove(element)
	}
}

func newOperationQueue() *operationQueue {
	return &operationQueue{
		gcExclusionList: set{},
		List:            list.New(),
		isActive:        true,
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
	recoveryComplete              uint32
	driverUninstall               uint32
	driverName                    string
	objectNamespace               string
	topologyKey                   string
	podToClaimsMap                sync.Map
	podToInlineMap                sync.Map
	claimToPodsMap                sync.Map
	volumeToClaimMap              sync.Map
	claimToVolumeMap              sync.Map
	azVolumeAttachmentToVaMap     sync.Map
	pvToVolumeMap                 sync.Map
	podLocks                      sync.Map
	visitedVolumes                sync.Map
	volumeOperationQueues         sync.Map
	cleanUpMap                    sync.Map
	priorityReplicaRequestsQueue  *VolumeReplicaRequestsPriorityQueue
	processingReplicaRequestQueue int32
	eventRecorder                 record.EventRecorder
	cachedClient                  client.Client
	azClient                      azdisk.Interface
	kubeClient                    kubernetes.Interface
	conditionWatcher              *watcher.ConditionWatcher
}

func NewSharedState(ctx context.Context, driverName, objectNamespace, topologyKey string, eventRecorder record.EventRecorder, cachedClient client.Client, azClient azdisk.Interface, kubeClient kubernetes.Interface, crdClient crdClientset.Interface) *SharedState {
	newSharedState := &SharedState{driverName: driverName, objectNamespace: objectNamespace, topologyKey: topologyKey, eventRecorder: eventRecorder, cachedClient: cachedClient, azClient: azClient, kubeClient: kubeClient, conditionWatcher: watcher.New(ctx, azClient, azdiskinformers.NewSharedInformerFactory(azClient, consts.DefaultInformerResync), objectNamespace)}
	if crdClient != nil {
		crdInformerFactory := crdInformers.NewSharedInformerFactoryWithOptions(crdClient, consts.DefaultInformerResync)
		crdInformer := crdInformerFactory.Apiextensions().V1().CustomResourceDefinitions().Informer()
		crdInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc: nil,
			UpdateFunc: func(_ interface{}, new interface{}) {
				crdObj := new.(*crd.CustomResourceDefinition)
				if objectDeletionRequested(crdObj) && (crdObj.Name == consts.AzVolumeAttachmentCRDName || crdObj.Name == consts.AzVolumeCRDName) {
					atomic.AddUint32(&newSharedState.driverUninstall, 1)
				}
			},
			DeleteFunc: nil,
		})

		go crdInformerFactory.Start(ctx.Done())

		synced := cache.WaitForCacheSync(ctx.Done(), crdInformer.HasSynced)
		if !synced {
			klog.Fatalf("Unable to sync caches for CRDs")
			os.Exit(1)
		}
	}
	newSharedState.createReplicaRequestsQueue()
	return newSharedState
}

func (c *SharedState) isDriverUninstall() bool {
	return atomic.LoadUint32(&c.driverUninstall) > 0
}

func (c *SharedState) isRecoveryComplete() bool {
	return atomic.LoadUint32(&c.recoveryComplete) == 1
}

func (c *SharedState) MarkRecoveryComplete() {
	atomic.StoreUint32(&c.recoveryComplete, 1)
}

func (c *SharedState) createOperationQueue(volumeName string) {
	_, _ = c.volumeOperationQueues.LoadOrStore(volumeName, newLockableEntry(newOperationQueue()))
}

func (c *SharedState) addToOperationQueue(ctx context.Context, volumeName string, requester operationRequester, operationFunc func(context.Context) error, isReplicaGarbageCollection bool) {
	// It is expected for caller to provide parent workflow via context.
	// The child workflow will be created below and be fed to the queued operation for necessary workflow information.
	ctx, w := workflow.New(ctx, workflow.WithDetails(consts.VolumeNameLabel, volumeName))

	v, ok := c.volumeOperationQueues.Load(volumeName)
	if !ok {
		return
	}
	lockable := v.(*lockableEntry)
	lockable.Lock()

	isFirst := lockable.entry.(*operationQueue).Len() <= 0
	_ = lockable.entry.(*operationQueue).PushBack(&replicaOperation{
		ctx:       ctx,
		requester: requester,
		operationFunc: func(ctx context.Context) (err error) {
			defer func() {
				if !shouldRequeueReplicaOperation(isReplicaGarbageCollection, err) {
					w.Finish(err)
				}
			}()
			err = operationFunc(ctx)
			return
		},
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
					if err := operation.operationFunc(operation.ctx); err != nil {
						if shouldRequeueReplicaOperation(operation.isReplicaGarbageCollection, err) {
							// if failed, push it to the end of the queue
							lockable.Lock()
							if operationQueue.isActive {
								operationQueue.PushBack(operation)
							}
							lockable.Unlock()
						}
					}
				}

				lockable.Lock()
				operationQueue.remove(front)
				// there is no entry remaining, exit the loop
				if operationQueue.Front() == nil {
					break
				}
			}
			lockable.Unlock()
		}()
	}
}

func shouldRequeueReplicaOperation(isReplicaGarbageCollection bool, err error) bool {
	return !isReplicaGarbageCollection || !errors.Is(err, context.Canceled)
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

func (c *SharedState) closeOperationQueue(volumeName string) func() {
	v, ok := c.volumeOperationQueues.Load(volumeName)
	if !ok {
		return nil
	}
	lockable := v.(*lockableEntry)

	lockable.Lock()
	lockable.entry.(*operationQueue).isActive = false
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
	lockable.entry.(*operationQueue).gcExclusionList.add(target)
	lockable.Unlock()
}

func (c *SharedState) removeFromExclusionList(volumeName string, target operationRequester) {
	v, ok := c.volumeOperationQueues.Load(volumeName)
	if !ok {
		return
	}
	lockable := v.(*lockableEntry)
	lockable.Lock()
	delete(lockable.entry.(*operationQueue).gcExclusionList, target)
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
	var next *list.Element
	for cur := queue.Front(); cur != nil; cur = next {
		next = cur.Next()
		if cur.Value.(*replicaOperation).isReplicaGarbageCollection {
			queue.remove(cur)
		}
	}
	lockable.Unlock()
}

func (c *SharedState) getVolumesFromPod(ctx context.Context, podName string) ([]string, error) {
	w, _ := workflow.GetWorkflowFromContext(ctx)

	var claims []string
	w.Logger().V(5).Infof("Getting requested volumes for pod (%s).", podName)
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
			w.Logger().V(5).Infof("Requested volume %s for pod %s is not an azure resource", value, podName)
			continue
		}
		volume, ok := value.(string)
		if !ok {
			return nil, status.Errorf(codes.Internal, "wrong output type: expected string")
		}
		volumes = append(volumes, volume)
		w.Logger().V(5).Infof("Requested volumes for pod %s are now the following: Volumes: %v, Len: %d", podName, volumes, len(volumes))
	}
	return volumes, nil
}

func (c *SharedState) getPodsFromVolume(ctx context.Context, client client.Client, volumeName string) ([]v1.Pod, error) {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	pods, err := c.getPodNamesFromVolume(volumeName)
	if err != nil {
		return nil, err
	}
	podObjs := []v1.Pod{}
	for _, pod := range pods {
		namespace, name, err := parseQualifiedName(pod)
		if err != nil {
			w.Logger().Errorf(err, "cannot get podObj for pod (%s)", pod)
			continue
		}
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

func (c *SharedState) getVolumesForPodObjs(ctx context.Context, pods []v1.Pod) ([]string, error) {
	volumes := []string{}
	for _, pod := range pods {
		podVolumes, err := c.getVolumesFromPod(ctx, getQualifiedName(pod.Namespace, pod.Name))
		if err != nil {
			return nil, err
		}
		volumes = append(volumes, podVolumes...)
	}
	return volumes, nil
}

func (c *SharedState) addPod(ctx context.Context, pod *v1.Pod, updateOption updateWithLock) error {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	podKey := getQualifiedName(pod.Namespace, pod.Name)
	v, _ := c.podLocks.LoadOrStore(podKey, &sync.Mutex{})

	w.Logger().V(5).Infof("Adding pod %s to shared map with keyName %s.", pod.Name, podKey)
	podLock := v.(*sync.Mutex)
	if updateOption == acquireLock {
		podLock.Lock()
		defer podLock.Unlock()
	}
	w.Logger().V(5).Infof("Pod spec of pod %s is: %+v. With volumes: %+v", pod.Name, pod.Spec, pod.Spec.Volumes)

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
			w.Logger().V(5).Infof("Creating AzVolume instance for inline volume %s.", volume.AzureDisk.DiskName)
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
			w.Logger().V(5).Infof("Pod %s: Skipping Volume %s. No persistent volume exists.", pod.Name, volume)
			continue
		}
		namespacedClaimName := getQualifiedName(pod.Namespace, volume.PersistentVolumeClaim.ClaimName)
		if _, ok := c.claimToVolumeMap.Load(namespacedClaimName); !ok {
			w.Logger().V(5).Infof("Skipping Pod %s. Volume %s not csi. Driver: %+v", pod.Name, volume.Name, volume.CSI)
			continue
		}
		w.Logger().V(5).Infof("Pod %s. Volume %v is csi.", pod.Name, volume)
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

		w.Logger().V(5).Infof("Storing pod %s and claim %s to claimToPodsMap map.", pod.Name, namespacedClaimName)
	}
	w.Logger().V(5).Infof("Storing pod %s and claim %s to podToClaimsMap map.", pod.Name, claims)

	allClaims := []string{}
	for key := range claimSet {
		allClaims = append(allClaims, key.(string))
	}
	c.podToClaimsMap.Store(podKey, allClaims)
	return nil
}

func (c *SharedState) deletePod(ctx context.Context, podKey string) error {
	w, _ := workflow.GetWorkflowFromContext(ctx)
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
			if err := c.azClient.DiskV1beta2().AzVolumes(c.objectNamespace).Delete(ctx, inline, metav1.DeleteOptions{}); err != nil && !apiErrors.IsNotFound(err) {
				w.Logger().Errorf(err, "failed to delete AzVolume (%s) for inline (%s): %v", inline, inline, err)
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
			w.Logger().Errorf(nil, "No pods found for PVC (%s)", claim)
		}

		// Scope the duration that we hold the lockable lock using a function.
		func() {
			lockable, ok := value.(*lockableEntry)
			if !ok {
				w.Logger().Error(nil, "claimToPodsMap should hold lockable entry")
				return
			}

			lockable.Lock()
			defer lockable.Unlock()

			podSet, ok := lockable.entry.(set)
			if !ok {
				w.Logger().Error(nil, "claimToPodsMap entry should hold a set")
			}

			podSet.remove(podKey)
			if len(podSet) == 0 {
				c.claimToPodsMap.Delete(claim)
			}
		}()
	}
	return nil
}

func (c *SharedState) addVolumeAndClaim(azVolumeName, pvName, pvClaimName string) {
	c.pvToVolumeMap.Store(pvName, azVolumeName)
	c.volumeToClaimMap.Store(azVolumeName, pvClaimName)
	c.claimToVolumeMap.Store(pvClaimName, azVolumeName)
}

func (c *SharedState) deletePV(pvName string) {
	c.pvToVolumeMap.Delete(pvName)
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
	ctx, w := workflow.New(ctx, workflow.WithDetails("filter-plugin", p.name()))
	defer w.Finish(nil)
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
				w.Logger().Errorf(err, "failed to convert pod affinity (%v) to selector", podAffinity.LabelSelector)
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
							w.Logger().Errorf(err, "failed to retrieve pod list: %v", err)
							continue
						}
						pods = append(pods, podList.Items...)
					}
				} else {
					podList := &v1.PodList{}
					if err = p.sharedState.cachedClient.List(ctx, podList, &client.ListOptions{LabelSelector: podAffinities}); err != nil {
						w.Logger().Errorf(err, "failed to retrieve pod list: %v", err)
						continue
					}
					pods = podList.Items
				}

				nodesWithSelectedPods := set{}
				for _, pod := range pods {
					w.Logger().V(5).Infof("Pod (%s) has matching label for pod affinity (%v)", getQualifiedName(pod.Namespace, pod.Name), podAffinities)
					nodesWithSelectedPods.add(pod.Spec.NodeName)
				}

				// add nodes, to which replica attachments of matching pods' volumes are attached, to replicaNodes
				if volumes, err := p.sharedState.getVolumesForPodObjs(ctx, pods); err == nil {
					for _, volume := range volumes {
						attachments, err := azureutils.GetAzVolumeAttachmentsForVolume(ctx, p.sharedState.cachedClient, volume, azureutils.ReplicaOnly)
						if err != nil {
							continue
						}

						nodeChecker := set{}
						for _, attachment := range attachments {
							if i == 0 {
								w.Logger().V(5).Infof("Adding node (%s) to the replica node set: replica mounts of volume (%s) found on node", attachment.Spec.NodeName, volume)
								replicaNodes.add(attachment.Spec.NodeName)
							} else {
								nodeChecker.add(attachment.Spec.NodeName)
							}
						}
						if i > 0 {
							// take an intersection of the current pod list's replica nodes with those of preceding pod lists.
							for node := range replicaNodes {
								if !nodeChecker.has(node) {
									w.Logger().V(5).Infof("Removing node (%s) from the replica node set: replica mounts of volume (%s) cannot be found on node", node, volume)
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
						w.Logger().Errorf(err, "failed to get node (%s)", node.(string))
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
						w.Logger().V(5).Infof("node (%s) doesn't have label value for topologyKey (%s)", nodeInstance.Name, podAffinity.TopologyKey)
					}
				}

				selector := labels.NewSelector()
				for key, values := range qualifiedLabelSet {
					labelValues := values.toStringSlice()
					requirements, err := azureutils.CreateLabelRequirements(key, selection.In, labelValues...)
					if err != nil {
						w.Logger().Errorf(err, "failed to create label for key (%s) and values (%+v) :%v", key, labelValues, err)
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
							w.Logger().V(5).Infof("Removing node (%s) from candidate nodes: node does not satisfy inter-pod-affinity (%v), label (%v)", candidateNode, podAffinity, selector)
							candidateNodes.remove(candidateNode)
						}
					}
				}
			}
		}
	}

	filteredNodes := []v1.Node{}
	for replicaNode := range replicaNodes {
		w.Logger().V(5).Infof("Adding node (%s) to candidate node list: node has a replica mount of qualifying pods", replicaNode)
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
	ctx, w := workflow.New(ctx, workflow.WithDetails("filter-plugin", p.name()))
	defer w.Finish(nil)
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
				w.Logger().Errorf(err, "failed to convert pod anti-affinity (%v) to selector", podAntiAffinity.LabelSelector)
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
							w.Logger().Errorf(err, "failed to retrieve pod list: %v", err)
							continue
						}
						pods = append(pods, podList.Items...)
					}
				} else {
					podList := &v1.PodList{}
					if err = p.sharedState.cachedClient.List(ctx, podList, &client.ListOptions{LabelSelector: podAntiAffinities}); err != nil {
						w.Logger().Errorf(err, "failed to retrieve pod list: %v", err)
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
						w.Logger().Errorf(err, "failed to get node (%s)", node.(string))
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
						w.Logger().Errorf(err, "failed to create label for key (%s) and values (%+v) :%v", key, labelValues, err)
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
							w.Logger().V(5).Infof("Removing node (%s) from candidate nodes: node satisfies inter-pod anti-affinity (%v), label (%v)", candidateNode, podAntiAffinity, selector)
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
	_, w := workflow.New(ctx, workflow.WithDetails("filter-plugin", p.name()))
	defer w.Finish(nil)
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
				w.Logger().V(5).Infof("Removing node (%s) from replica candidates: node (%s)'s taint cannot be tolerated", node.Name, node.Name)
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
	_, w := workflow.New(ctx, workflow.WithDetails("filter-plugin", p.name()))
	defer w.Finish(nil)
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
				w.Logger().V(5).Infof("Removing node (%s) from replica candidates: node does not match pod node affinity (%+v)", node.Name, podNodeAffinity)
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
	_, w := workflow.New(ctx, workflow.WithDetails("filter-plugin", p.name()))
	defer w.Finish(nil)
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
			w.Logger().V(5).Infof("Removing node (%s) from replica candidate: node does not match pod node selector (%v)", node.Name, podNodeSelector)
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
	_, w := workflow.New(ctx, workflow.WithDetails("filter-plugin", v.name()))
	defer w.Finish(nil)
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
			w.Logger().Errorf(err, "failed to get node selector from node affinity (%v)", pv.Spec.NodeAffinity.Required)
			continue
		}
		// acknowledge that there can be duplicates in the slice
		volumeNodeSelectors = append(volumeNodeSelectors, nodeSelector)
	}

	for candidateNode := range candidateNodes {
		node := nodes[candidateNode.(int)]
		for _, volumeNodeSelector := range volumeNodeSelectors {
			if !volumeNodeSelector.Match(&node) {
				w.Logger().V(5).Infof("Removing node (%s) from replica candidates: volume node selector (%+v) cannot be matched with the node.", node.Name, volumeNodeSelector)
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
	ctx, w := workflow.New(ctx, workflow.WithDetails("score-plugin", s.name()))
	defer w.Finish(nil)
	for nodeName, score := range nodeScores {
		remainingCapacity, err := azureutils.GetNodeRemainingDiskCount(ctx, s.sharedState.cachedClient, nodeName)
		if err != nil {
			// if failed to get node's remaining capacity, remove the node from the candidate list and proceed
			w.Logger().Errorf(err, "failed to get remaining capacity of node (%s)", nodeName)
			delete(nodeScores, nodeName)
			continue
		}

		nodeScores[nodeName] = score + (nodeScoreLowCoefficient * remainingCapacity)

		if remainingCapacity-len(s.volumes) < 0 {
			delete(nodeScores, nodeName)
		}

		w.Logger().V(5).Infof("node (%s) can accept %d more attachments", nodeName, remainingCapacity)
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
	ctx, w := workflow.New(ctx, workflow.WithDetails("score-plugin", s.name()))
	defer w.Finish(nil)
	for _, volume := range s.volumes {
		azVolumeAttachments, err := azureutils.GetAzVolumeAttachmentsForVolume(ctx, s.sharedState.cachedClient, volume, azureutils.AllRoles)
		if err != nil {
			w.Logger().V(5).Errorf(err, "Error listing AzVolumeAttachments for azvolume %s", volume)
			continue
		}

		for _, azVolumeAttachment := range azVolumeAttachments {
			if score, exists := nodeScores[azVolumeAttachment.Spec.NodeName]; exists {
				if azVolumeAttachment.Spec.RequestedRole == azdiskv1beta2.PrimaryRole {
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
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	w.Logger().V(5).Info("Getting ranked list of nodes for creating AzVolumeAttachments")

	nodeList := &v1.NodeList{}
	if err := c.cachedClient.List(ctx, nodeList); err != nil {
		return nil, err
	}

	var selectedNodeObjs []v1.Node
	selectedNodeObjs, err = c.selectNodesPerTopology(ctx, nodeList.Items, podObjs, volumes)
	if err != nil {
		w.Logger().Errorf(err, "failed to select nodes for volumes (%+v)", volumes)
		return nil, err
	}

	selectedNodes := make([]string, len(selectedNodeObjs))
	for i, selectedNodeObj := range selectedNodeObjs {
		selectedNodes[i] = selectedNodeObj.Name
	}

	w.Logger().V(5).Infof("Selected nodes (%+v) for replica AzVolumeAttachments for volumes (%+v)", selectedNodes, volumes)
	return selectedNodes, nil
}

func (c *SharedState) filterNodes(ctx context.Context, nodes []v1.Node, pods []v1.Pod, volumes []string) ([]v1.Node, error) {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	pvs := make([]*v1.PersistentVolume, len(volumes))
	for i, volume := range volumes {
		var azVolume *azdiskv1beta2.AzVolume
		azVolume, err = azureutils.GetAzVolume(ctx, c.cachedClient, c.azClient, volume, c.objectNamespace, true)
		if err != nil {
			w.Logger().V(5).Errorf(err, "AzVolume for volume %s is not found.", volume)
			return nil, err
		}

		var pv v1.PersistentVolume
		if err = c.cachedClient.Get(ctx, types.NamespacedName{Name: azVolume.Spec.PersistentVolume}, &pv); err != nil {
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
			w.Logger().Errorf(err, "failed to filter node with filter plugin (%s). Ignoring filtered results.", filterPlugin.name())
		} else {
			filteredNodes = updatedFilteredNodes
			nodeStrs := make([]string, len(filteredNodes))
			for i, filteredNode := range filteredNodes {
				nodeStrs[i] = filteredNode.Name
			}
			w.Logger().V(2).Infof("Filtered node list from filter plugin (%s): %+v", filterPlugin.name(), nodeStrs)
		}
	}

	return filteredNodes, nil
}

func (c *SharedState) prioritizeNodes(ctx context.Context, pods []v1.Pod, volumes []string, nodes []v1.Node) []v1.Node {
	ctx, w := workflow.New(ctx)
	defer w.Finish(nil)

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
			w.Logger().Errorf(err, "failed to score nodes by node scorer (%s)", nodeScorerPlugin.name())
		} else {
			// update node scores if scorer plugin returned success
			nodeScores = updatedNodeScores
		}
	}

	// normalize score
	numFiltered := 0
	for _, node := range nodes {
		if _, exists := nodeScores[node.Name]; !exists {
			nodeScores[node.Name] = -1
			numFiltered++
		}
	}

	sort.Slice(nodes[:], func(i, j int) bool {
		return nodeScores[nodes[i].Name] > nodeScores[nodes[j].Name]
	})

	return nodes[:len(nodes)-numFiltered]
}

func (c *SharedState) filterAndSortNodes(ctx context.Context, nodes []v1.Node, pods []v1.Pod, volumes []string) ([]v1.Node, error) {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	var filteredNodes []v1.Node
	filteredNodes, err = c.filterNodes(ctx, nodes, pods, volumes)
	if err != nil {
		w.Logger().Errorf(err, "failed to filter nodes for volumes (%+v): %v", volumes, err)
		return nil, err
	}
	sortedNodes := c.prioritizeNodes(ctx, pods, volumes, filteredNodes)
	return sortedNodes, nil
}

func (c *SharedState) selectNodesPerTopology(ctx context.Context, nodes []v1.Node, pods []v1.Pod, volumes []string) ([]v1.Node, error) {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	selectedNodes := []v1.Node{}
	numReplicas := 0

	// disperse node topology if possible
	compatibleZonesSet := set{}
	var primaryNode string
	for i, volume := range volumes {
		var azVolume *azdiskv1beta2.AzVolume
		azVolume, err = azureutils.GetAzVolume(ctx, c.cachedClient, c.azClient, volume, c.objectNamespace, true)
		if err != nil {
			err = status.Errorf(codes.Aborted, "failed to get AzVolume CRI (%s)", volume)
			return nil, err
		}

		numReplicas = max(numReplicas, azVolume.Spec.MaxMountReplicaCount)
		w.Logger().V(5).Infof("Number of requested replicas for Azvolume (%s) is: %d. Max replica count is: %d.",
			volume, numReplicas, azVolume.Spec.MaxMountReplicaCount)

		var pv v1.PersistentVolume
		if err = c.cachedClient.Get(ctx, types.NamespacedName{Name: azVolume.Spec.PersistentVolume}, &pv); err != nil {
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
			if primaryAttachment, err := azureutils.GetAzVolumeAttachmentsForVolume(ctx, c.cachedClient, volume, azureutils.PrimaryOnly); err != nil || len(primaryAttachment) == 0 {
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
		selectedNodes, err = c.filterAndSortNodes(ctx, nodes, pods, volumes)
		if err != nil {
			err = status.Errorf(codes.Aborted, "failed to select nodes for volumes (%+v): %v", volumes, err)
			return nil, err
		}
	} else {
		w.Logger().V(5).Infof("The list of zones to select nodes from is: %s", strings.Join(compatibleZones, ","))

		var primaryNodeZone string
		if primaryNode != "" {
			nodeObj := &v1.Node{}
			err = c.cachedClient.Get(ctx, types.NamespacedName{Name: primaryNode}, nodeObj)
			if err != nil {
				w.Logger().Errorf(err, "failed to retrieve the primary node")
			}

			var ok bool
			if primaryNodeZone, ok = nodeObj.Labels[consts.WellKnownTopologyKey]; ok {
				w.Logger().V(5).Infof("failed to find zone annotations for primary node")
			}
		}

		nodeSelector := labels.NewSelector()
		zoneRequirement, _ := labels.NewRequirement(consts.WellKnownTopologyKey, selection.In, compatibleZones)
		nodeSelector = nodeSelector.Add(*zoneRequirement)

		compatibleNodes := &v1.NodeList{}
		if err = c.cachedClient.List(ctx, compatibleNodes, &client.ListOptions{LabelSelector: nodeSelector}); err != nil {
			err = status.Errorf(codes.Aborted, "failed to retrieve node list: %v", err)
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
			var sortedNodes []v1.Node
			sortedNodes, err = c.filterAndSortNodes(ctx, nodeList, pods, volumes)
			if err != nil {
				err = status.Errorf(codes.Aborted, "failed to select nodes for volumes (%+v): %v", volumes, err)
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

func (c *SharedState) getNodesWithReplica(ctx context.Context, volumeName string) ([]string, error) {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	w.Logger().V(5).Infof("Getting nodes with replica AzVolumeAttachments for volume %s.", volumeName)
	azVolumeAttachments, err := azureutils.GetAzVolumeAttachmentsForVolume(ctx, c.cachedClient, volumeName, azureutils.ReplicaOnly)
	if err != nil {
		w.Logger().V(5).Errorf(err, "failed to get AzVolumeAttachments for volume %s.", volumeName)
		return nil, err
	}

	nodes := []string{}
	for _, azVolumeAttachment := range azVolumeAttachments {
		nodes = append(nodes, azVolumeAttachment.Spec.NodeName)
	}
	w.Logger().V(5).Infof("Nodes with AzVolumeAttachments for volume %s are: %v, Len: %d", volumeName, nodes, len(nodes))
	return nodes, nil
}

func (c *SharedState) createReplicaAzVolumeAttachment(ctx context.Context, volumeID, node string, volumeContext map[string]string) error {
	var err error
	ctx, w := workflow.New(ctx, workflow.WithDetails(consts.NodeNameLabel, node))
	defer func() { w.Finish(err) }()

	var diskName string
	diskName, err = azureutils.GetDiskName(volumeID)
	if err != nil {
		err = status.Errorf(codes.Internal, "failed to extract volume name from volumeID (%s)", volumeID)
		return err
	}
	w.AddDetailToLogger(consts.VolumeNameLabel, diskName)

	w.Logger().V(5).Info("Creating replica AzVolumeAttachments")
	if volumeContext == nil {
		volumeContext = make(map[string]string)
	}
	// creating azvolumeattachment
	volumeName := strings.ToLower(diskName)
	replicaName := azureutils.GetAzVolumeAttachmentName(volumeName, node)
	azVolumeAttachment := azdiskv1beta2.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      replicaName,
			Namespace: c.objectNamespace,
			Labels: map[string]string{
				consts.NodeNameLabel:   node,
				consts.VolumeNameLabel: volumeName,
				consts.RoleLabel:       string(azdiskv1beta2.ReplicaRole),
			},
			Annotations: map[string]string{consts.VolumeAttachRequestAnnotation: "controller"},
			Finalizers:  []string{consts.AzVolumeAttachmentFinalizer},
		},
		Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
			NodeName:      node,
			VolumeID:      volumeID,
			VolumeName:    volumeName,
			RequestedRole: azdiskv1beta2.ReplicaRole,
			VolumeContext: volumeContext,
		},
	}
	w.AnnotateObject(&azVolumeAttachment)
	_, err = c.azClient.DiskV1beta2().AzVolumeAttachments(c.objectNamespace).Create(ctx, &azVolumeAttachment, metav1.CreateOptions{})
	if err != nil {
		err = status.Errorf(codes.Internal, "failed to create replica AzVolumeAttachment %s.", replicaName)
		return err
	}
	return nil
}

func (c *SharedState) cleanUpAzVolumeAttachmentByVolume(ctx context.Context, azVolumeName string, caller operationRequester, role azureutils.AttachmentRoleMode, deleteMode cleanUpMode) ([]azdiskv1beta2.AzVolumeAttachment, error) {
	var err error
	ctx, w := workflow.New(ctx, workflow.WithDetails(consts.VolumeNameLabel, azVolumeName))
	defer func() { w.Finish(err) }()

	w.Logger().Infof("AzVolumeAttachment clean up requested by %s for AzVolume (%s)", caller, azVolumeName)

	var attachments []azdiskv1beta2.AzVolumeAttachment
	attachments, err = azureutils.GetAzVolumeAttachmentsForVolume(ctx, c.cachedClient, azVolumeName, role)
	if err != nil {
		if apiErrors.IsNotFound(err) {
			err = nil
			return nil, nil
		}
		err = status.Errorf(codes.Aborted, "failed to get AzVolumeAttachments: %v", err)
		return nil, err
	}

	if err = c.cleanUpAzVolumeAttachments(ctx, attachments, deleteMode, caller); err != nil {
		return attachments, err
	}
	c.unmarkVolumeVisited(azVolumeName)
	return attachments, nil
}

func (c *SharedState) cleanUpAzVolumeAttachmentByNode(ctx context.Context, azDriverNodeName string, caller operationRequester, role azureutils.AttachmentRoleMode, deleteMode cleanUpMode) ([]azdiskv1beta2.AzVolumeAttachment, error) {
	var err error
	ctx, w := workflow.New(ctx, workflow.WithDetails(consts.NodeNameLabel, azDriverNodeName))
	defer func() { w.Finish(err) }()
	w.Logger().Infof("AzVolumeAttachment clean up requested by %s for AzDriverNode (%s)", caller, azDriverNodeName)

	var nodeRequirement *labels.Requirement
	nodeRequirement, err = azureutils.CreateLabelRequirements(consts.NodeNameLabel, selection.Equals, azDriverNodeName)
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector().Add(*nodeRequirement)

	var attachments *azdiskv1beta2.AzVolumeAttachmentList
	attachments, err = c.azClient.DiskV1beta2().AzVolumeAttachments(c.objectNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		if apiErrors.IsNotFound(err) {
			err = nil
			return nil, nil
		}
		err = status.Errorf(codes.Aborted, "failed to get AzVolumeAttachments: %v", err)
		return nil, err
	}

	cleanUpMap := map[string][]azdiskv1beta2.AzVolumeAttachment{}
	for _, attachment := range attachments.Items {
		if shouldCleanUp(attachment, role) {
			cleanUpMap[attachment.Spec.VolumeName] = append(cleanUpMap[attachment.Spec.VolumeName], attachment)
		}
	}

	for volumeName, cleanUps := range cleanUpMap {
		volumeName := volumeName
		defer w.Finish(nil)
		c.addToOperationQueue(ctx,
			volumeName,
			caller,
			func(ctx context.Context) error {
				return c.cleanUpAzVolumeAttachments(ctx, cleanUps, deleteMode, caller)
			},
			false)
	}
	return attachments.Items, nil
}

func (c *SharedState) cleanUpAzVolumeAttachments(ctx context.Context, attachments []azdiskv1beta2.AzVolumeAttachment, cleanUp cleanUpMode, caller operationRequester) error {
	var err error
	w, _ := workflow.GetWorkflowFromContext(ctx)

	for _, attachment := range attachments {
		var patchRequired bool
		patched := attachment.DeepCopy()

		// if caller is azdrivernode, don't append cleanup annotation
		if (caller != azdrivernode && !metav1.HasAnnotation(patched.ObjectMeta, consts.CleanUpAnnotation)) ||
			// replica attachments should always be detached regardless of the cleanup mode
			((cleanUp == detachAndDeleteCRI || patched.Spec.RequestedRole == azdiskv1beta2.ReplicaRole) && !metav1.HasAnnotation(patched.ObjectMeta, consts.VolumeDetachRequestAnnotation)) {
			patchRequired = true
			if caller != azdrivernode {
				patched.Status.Annotations = azureutils.AddToMap(patched.Status.Annotations, consts.CleanUpAnnotation, string(caller))
			}
			if cleanUp == detachAndDeleteCRI || patched.Spec.RequestedRole == azdiskv1beta2.ReplicaRole {
				patched.Status.Annotations = azureutils.AddToMap(patched.Status.Annotations, consts.VolumeDetachRequestAnnotation, string(caller))
			}
		}

		if patchRequired {
			if err = c.cachedClient.Status().Patch(ctx, patched, client.MergeFrom(&attachment)); err != nil && apiErrors.IsNotFound(err) {
				err = status.Errorf(codes.Internal, "failed to patch AzVolumeAttachment (%s)", attachment.Name)
				return err
			}
		}
		if !objectDeletionRequested(patched) {
			if err := c.cachedClient.Delete(ctx, patched); err != nil && apiErrors.IsNotFound(err) {
				err = status.Errorf(codes.Internal, "failed to delete AzVolumeAttachment (%s)", attachment.Name)
				return err
			}
			w.Logger().V(5).Infof("Set deletion timestamp for AzVolumeAttachment (%s)", attachment.Name)
		}
	}
	return nil
}

func shouldCleanUp(attachment azdiskv1beta2.AzVolumeAttachment, mode azureutils.AttachmentRoleMode) bool {
	return mode == azureutils.AllRoles || (attachment.Spec.RequestedRole == azdiskv1beta2.PrimaryRole && mode == azureutils.PrimaryOnly) || (attachment.Spec.RequestedRole == azdiskv1beta2.ReplicaRole && mode == azureutils.ReplicaOnly)
}

func isAttached(attachment *azdiskv1beta2.AzVolumeAttachment) bool {
	return attachment != nil && attachment.Status.Detail != nil && attachment.Status.Detail.PublishContext != nil
}

func isCreated(volume *azdiskv1beta2.AzVolume) bool {
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

func isCleanupRequested(attachment *azdiskv1beta2.AzVolumeAttachment) bool {
	return attachment != nil && azureutils.MapContains(attachment.Status.Annotations, consts.CleanUpAnnotation)
}

func volumeAttachRequested(attachment *azdiskv1beta2.AzVolumeAttachment) bool {
	return attachment != nil && azureutils.MapContains(attachment.Annotations, consts.VolumeAttachRequestAnnotation)
}

func volumeDetachRequested(attachment *azdiskv1beta2.AzVolumeAttachment) bool {
	return attachment != nil && azureutils.MapContains(attachment.Status.Annotations, consts.VolumeDetachRequestAnnotation)
}

func volumeDeleteRequested(volume *azdiskv1beta2.AzVolume) bool {
	return volume != nil && azureutils.MapContains(volume.Status.Annotations, consts.VolumeDeleteRequestAnnotation)
}

func isDemotionRequested(attachment *azdiskv1beta2.AzVolumeAttachment) bool {
	return attachment != nil && attachment.Status.Detail != nil && attachment.Status.Detail.Role == azdiskv1beta2.PrimaryRole && attachment.Spec.RequestedRole == azdiskv1beta2.ReplicaRole
}

func isPreProvisioned(volume *azdiskv1beta2.AzVolume) bool {
	return volume != nil && azureutils.MapContains(volume.Status.Annotations, consts.PreProvisionedVolumeAnnotation)
}

func getQualifiedName(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}

func parseQualifiedName(qualifiedName string) (namespace, name string, err error) {
	parsed := strings.Split(qualifiedName, "/")
	if len(parsed) != 2 {
		err = status.Errorf(codes.Internal, "pod's qualified name (%s) should be of <namespace>/<name>", qualifiedName)
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

func reconcileReturnOnError(ctx context.Context, obj runtime.Object, operationType string, err error, retryInfo *retryInfo) (reconcile.Result, error) {
	var (
		requeue    bool = status.Code(err) != codes.FailedPrecondition
		retryAfter time.Duration
	)

	w := workflow.GetWorkflow(ctx, obj)

	if meta, metaErr := meta.Accessor(obj); metaErr == nil {
		objectName := meta.GetName()
		objectType := reflect.TypeOf(obj)
		if !requeue {
			w.Logger().Errorf(err, "failed to %s %v (%s) with no retry", operationType, objectType, objectName)
			retryInfo.deleteEntry(objectName)
		} else {
			retryAfter = retryInfo.nextRequeue(objectName)
			w.Logger().Errorf(err, "failed to %s %v (%s) with retry after %v", operationType, objectType, objectName, retryAfter)
		}
	}

	return reconcile.Result{
		Requeue:      requeue,
		RequeueAfter: retryAfter,
	}, nil
}

func isOperationInProcess(obj interface{}) bool {
	switch target := obj.(type) {
	case *azdiskv1beta2.AzVolume:
		return target.Status.State == azdiskv1beta2.VolumeCreating || target.Status.State == azdiskv1beta2.VolumeDeleting || target.Status.State == azdiskv1beta2.VolumeUpdating
	case *azdiskv1beta2.AzVolumeAttachment:
		return target.Status.State == azdiskv1beta2.Attaching || target.Status.State == azdiskv1beta2.Detaching
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

func (vq *VolumeReplicaRequestsPriorityQueue) Push(ctx context.Context, replicaRequest *ReplicaRequest) {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	err := vq.queue.Add(replicaRequest)
	atomic.AddInt32(&vq.size, 1)
	if err != nil {
		w.Logger().Errorf(err, "failed to add replica request for volume %s", replicaRequest.VolumeName)
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

// Removes replica requests from the priority queue and adds to operation queue.
func (c *SharedState) tryCreateFailedReplicas(ctx context.Context, requestor operationRequester) {
	if atomic.SwapInt32(&c.processingReplicaRequestQueue, 1) == 0 {
		ctx, w := workflow.New(ctx)
		defer w.Finish(nil)
		requests := c.priorityReplicaRequestsQueue.DrainQueue()
		for i := 0; i < len(requests); i++ {
			replicaRequest := requests[i]
			c.addToOperationQueue(ctx,
				replicaRequest.VolumeName,
				requestor,
				func(ctx context.Context) error {
					return c.manageReplicas(ctx, replicaRequest.VolumeName)
				},
				false,
			)
		}
		atomic.StoreInt32(&c.processingReplicaRequestQueue, 0)
	}
}

func (c *SharedState) garbageCollectReplicas(ctx context.Context, volumeName string, requester operationRequester) {
	c.addToOperationQueue(
		ctx,
		volumeName,
		replica,
		func(ctx context.Context) error {
			if _, err := c.cleanUpAzVolumeAttachmentByVolume(ctx, volumeName, requester, azureutils.ReplicaOnly, detachAndDeleteCRI); err != nil {
				return err
			}
			c.addToGcExclusionList(volumeName, replica)
			c.removeGarbageCollection(volumeName)
			c.unmarkVolumeVisited(volumeName)
			return nil
		},
		true,
	)
}

func (c *SharedState) removeGarbageCollection(volumeName string) {
	v, ok := c.cleanUpMap.LoadAndDelete(volumeName)
	if ok {
		cancelFunc := v.(context.CancelFunc)
		cancelFunc()
	}
	// if there is any garbage collection enqueued in operation queue, remove it
	c.dequeueGarbageCollection(volumeName)
}

func (c *SharedState) manageReplicas(ctx context.Context, volumeName string) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	var azVolume *azdiskv1beta2.AzVolume
	azVolume, err = azureutils.GetAzVolume(ctx, c.cachedClient, c.azClient, volumeName, c.objectNamespace, true)
	if apiErrors.IsNotFound(err) {
		w.Logger().Info("Volume no longer exists. Aborting manage replica operation")
		return nil
	} else if err != nil {
		w.Logger().Error(err, "failed to get AzVolume")
		return err
	}

	// replica management should not be executed or retried if AzVolume is scheduled for a deletion or not created.
	if !isCreated(azVolume) || objectDeletionRequested(azVolume) {
		w.Logger().Errorf(err, "azVolume is scheduled for deletion or has no underlying volume object")
		return nil
	}

	azVolumeAttachments, err := azureutils.GetAzVolumeAttachmentsForVolume(ctx, c.cachedClient, volumeName, azureutils.ReplicaOnly)
	if err != nil {
		w.Logger().Errorf(err, "failed to list replica AzVolumeAttachments")
		return err
	}

	desiredReplicaCount, currentReplicaCount := azVolume.Spec.MaxMountReplicaCount, len(azVolumeAttachments)
	w.Logger().Infof("Control number of replicas for volume (%s): desired=%d,\tcurrent:%d", azVolume.Spec.VolumeName, desiredReplicaCount, currentReplicaCount)

	if desiredReplicaCount > currentReplicaCount {
		w.Logger().Infof("Need %d more replicas for volume (%s)", desiredReplicaCount-currentReplicaCount, azVolume.Spec.VolumeName)
		if azVolume.Status.Detail == nil || azVolume.Status.State == azdiskv1beta2.VolumeDeleting || azVolume.Status.State == azdiskv1beta2.VolumeDeleted {
			// underlying volume does not exist, so volume attachment cannot be made
			return nil
		}
		if err = c.createReplicas(ctx, desiredReplicaCount-currentReplicaCount, azVolume.Name, azVolume.Status.Detail.VolumeID, azVolume.Spec.Parameters); err != nil {
			w.Logger().Errorf(err, "failed to create %d replicas for volume (%s): %v", desiredReplicaCount-currentReplicaCount, azVolume.Spec.VolumeName, err)
			return err
		}
	}
	return nil
}
func (c *SharedState) createReplicas(ctx context.Context, remainingReplicas int, volumeName, volumeID string, volumeContext map[string]string) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	// if volume is scheduled for clean up, skip replica creation
	if _, cleanUpScheduled := c.cleanUpMap.Load(volumeName); cleanUpScheduled {
		return nil
	}

	// get pods linked to the volume
	var pods []v1.Pod
	pods, err = c.getPodsFromVolume(ctx, c.cachedClient, volumeName)
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

	var nodes []string
	nodes, err = c.getNodesForReplica(ctx, volumeName, pods, remainingReplicas)
	if err != nil {
		w.Logger().Errorf(err, "failed to get a list of nodes for replica attachment")
		return err
	}

	requiredReplicas := remainingReplicas
	for _, node := range nodes {
		if err = c.createReplicaAzVolumeAttachment(ctx, volumeID, node, volumeContext); err != nil {
			w.Logger().Errorf(err, "failed to create replica AzVolumeAttachment for volume %s", volumeName)
			//Push to queue the failed replica number
			request := ReplicaRequest{VolumeName: volumeName, Priority: remainingReplicas}
			c.priorityReplicaRequestsQueue.Push(ctx, &request)
			return err
		}
		remainingReplicas--
	}

	if remainingReplicas > 0 {
		//no failed replica attachments, but there are still more replicas to reach MaxShares
		request := ReplicaRequest{VolumeName: volumeName, Priority: remainingReplicas}
		c.priorityReplicaRequestsQueue.Push(ctx, &request)
		for _, pod := range pods {
			c.eventRecorder.Eventf(pod.DeepCopyObject(), v1.EventTypeWarning, consts.ReplicaAttachmentFailedEvent, "Not enough suitable nodes to attach %d of %d replica mount(s) for volume %s", remainingReplicas, requiredReplicas, volumeName)
		}
	}
	return nil
}

func (c *SharedState) getNodesForReplica(ctx context.Context, volumeName string, pods []v1.Pod, numReplica int) ([]string, error) {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	if len(pods) == 0 {
		pods, err = c.getPodsFromVolume(ctx, c.cachedClient, volumeName)
		if err != nil {
			return nil, err
		}
	}

	var volumes []string
	volumes, err = c.getVolumesForPodObjs(ctx, pods)
	if err != nil {
		return nil, err
	}

	var nodes []string
	nodes, err = c.getRankedNodesForReplicaAttachments(ctx, volumes, pods)
	if err != nil {
		return nil, err
	}

	var replicaNodes []string
	replicaNodes, err = c.getNodesWithReplica(ctx, volumeName)
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

func verifyObjectDeleted(obj interface{}, objectDeleted bool) (bool, error) {
	if obj == nil || objectDeleted {
		return true, nil
	}

	// otherwise, the volume detachment has either failed with error or pending
	azVolumeAttachmentInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
	if azVolumeAttachmentInstance.Status.Error != nil {
		return false, util.ErrorFromAzError(azVolumeAttachmentInstance.Status.Error)
	}
	return false, nil
}
