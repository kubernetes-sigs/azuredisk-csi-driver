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
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	crdClientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/kubernetes"
	cache "k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	csitranslator "k8s.io/csi-translation-lib/plugins"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/features"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azdiskinformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/watcher"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type SharedState struct {
	recoveryComplete              uint32
	config                        *azdiskv1beta2.AzDiskDriverConfiguration
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
	crdClient                     crdClientset.Interface
	conditionWatcher              *watcher.ConditionWatcher
	azureDiskCSITranslator        csitranslator.InTreePlugin
	availableAttachmentsMap       sync.Map
}

func NewSharedState(config *azdiskv1beta2.AzDiskDriverConfiguration, topologyKey string, eventRecorder record.EventRecorder, cachedClient client.Client, azClient azdisk.Interface, kubeClient kubernetes.Interface, crdClient crdClientset.Interface) *SharedState {
	newSharedState := &SharedState{
		config:        config,
		topologyKey:   topologyKey,
		eventRecorder: eventRecorder,
		cachedClient:  cachedClient,
		crdClient:     crdClient,
		azClient:      azClient,
		kubeClient:    kubeClient,
		conditionWatcher: watcher.New(
			context.Background(),
			azClient,
			azdiskinformers.NewSharedInformerFactory(azClient, consts.DefaultInformerResync),
			config.ObjectNamespace),
		azureDiskCSITranslator: csitranslator.NewAzureDiskCSITranslator(),
	}
	newSharedState.createReplicaRequestsQueue()

	return newSharedState
}

func (c *SharedState) isRecoveryComplete() bool {
	return atomic.LoadUint32(&c.recoveryComplete) == 1
}

func (c *SharedState) MarkRecoveryComplete() {
	atomic.StoreUint32(&c.recoveryComplete, 1)
}

func (c *SharedState) DeleteAPIVersion(ctx context.Context, deleteVersion string) error {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	crdNames := []string{consts.AzDriverNodeCRDName, consts.AzVolumeCRDName, consts.AzVolumeAttachmentCRDName}
	for _, crdName := range crdNames {
		err := retry.RetryOnConflict(retry.DefaultBackoff,
			func() error {
				crd, err := c.crdClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crdName, metav1.GetOptions{})
				if err != nil {
					if apiErrors.IsNotFound(err) {
						return err
					}
					return nil
				}

				updated := crd.DeepCopy()
				var storedVersions []string
				// remove version from status stored versions
				for _, version := range updated.Status.StoredVersions {
					if version == deleteVersion {
						continue
					}
					storedVersions = append(storedVersions, version)
				}
				updated.Status.StoredVersions = storedVersions
				crd, err = c.crdClient.ApiextensionsV1().CustomResourceDefinitions().UpdateStatus(ctx, updated, metav1.UpdateOptions{})
				if err != nil {
					// log the error and continue
					return err
				}
				return nil
			})

		if err != nil {
			w.Logger().Errorf(err, "failed to delete %s api version from CRD (%s)", deleteVersion, crdName)
		}

		// Uncomment when the all deployments have rolled over to v1beta1.
		// updated = crd.DeepCopy()
		// // remove version from spec versions
		// var specVersions []crdv1.CustomResourceDefinitionVersion
		// for _, version := range updated.Spec.Versions {
		// 	if version.Name == deleteVersion {
		// 		continue
		// 	}
		// 	specVersions = append(specVersions, version)
		// }
		// updated.Spec.Versions = specVersions

		// // update the crd
		// crd, err = c.crdClient.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, updated, metav1.UpdateOptions{})
		// if err != nil {
		// 	// log the error and continue
		// 	w.Logger().Errorf(err, "failed to remove %s spec version from CRD (%s)", deleteVersion, crd.Name)
		// 	continue
		// }
	}
	return nil
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
			defer lockable.Unlock()
			for {
				operationQueue := lockable.entry.(*operationQueue)
				// pop the first operation
				front := operationQueue.Front()
				operation := front.Value.(*replicaOperation)

				// only run the operation if the operation requester is not enlisted in blacklist
				if !operationQueue.gcExclusionList.has(operation.requester) {
					lockable.Unlock()
					err := operation.operationFunc(operation.ctx)
					lockable.Lock()
					if err != nil {
						if shouldRequeueReplicaOperation(operation.isReplicaGarbageCollection, err) {
							// if failed, push it to the end of the queue
							if operationQueue.isActive {
								operationQueue.PushBack(operation)
							}
						}
					}
				}

				operationQueue.remove(front)
				// there is no entry remaining, exit the loop
				if operationQueue.Front() == nil {
					break
				}
			}
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
	defer lockable.Unlock()
	lockable.entry.(*operationQueue).Init()
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
	defer lockable.Unlock()
	lockable.entry.(*operationQueue).gcExclusionList.add(target)
}

func (c *SharedState) removeFromExclusionList(volumeName string, target operationRequester) {
	v, ok := c.volumeOperationQueues.Load(volumeName)
	if !ok {
		return
	}
	lockable := v.(*lockableEntry)
	lockable.Lock()
	defer lockable.Unlock()
	delete(lockable.entry.(*operationQueue).gcExclusionList, target)
}

func (c *SharedState) dequeueGarbageCollection(volumeName string) {
	v, ok := c.volumeOperationQueues.Load(volumeName)
	if !ok {
		return
	}
	lockable := v.(*lockableEntry)
	lockable.Lock()
	defer lockable.Unlock()
	queue := lockable.entry.(*operationQueue)
	// look for garbage collection operation in the queue and remove from queue
	var next *list.Element
	for cur := queue.Front(); cur != nil; cur = next {
		next = cur.Next()
		if cur.Value.(*replicaOperation).isReplicaGarbageCollection {
			queue.remove(cur)
		}
	}
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
	var err error
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
			var pv *v1.PersistentVolume
			if pv, err = c.azureDiskCSITranslator.TranslateInTreeInlineVolumeToCSI(&volume, pod.Namespace); err != nil {
				w.Logger().V(5).Errorf(err, "failed to translate inline volume to csi")
				continue
			} else if pv == nil {
				w.Logger().V(5).Errorf(status.Errorf(codes.Internal, "unexpected failure in translating inline volume to csi"), "nil pv returned")
				continue
			}
			w.Logger().V(5).Infof("Creating AzVolume instance for inline volume %s.", volume.AzureDisk.DiskName)
			if err := c.createAzVolumeFromPv(ctx, *pv, map[string]string{consts.InlineVolumeAnnotation: volume.AzureDisk.DataDiskURI}); err != nil {
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
			// Log message if the Pod status is Running
			if pod.Status.Phase == v1.PodRunning {
				w.Logger().V(5).Infof("Skipping Pod %s. Volume %s not csi. Driver: %+v", pod.Name, volume.Name, volume.CSI)
			}
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
			if err := c.azClient.DiskV1beta2().AzVolumes(c.config.ObjectNamespace).Delete(ctx, inline, metav1.DeleteOptions{}); err != nil && !apiErrors.IsNotFound(err) {
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
		azVolume, err = azureutils.GetAzVolume(ctx, c.cachedClient, c.azClient, volume, c.config.ObjectNamespace, true)
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
			w.Logger().V(10).Infof("Filtered node list from filter plugin (%s): %+v", filterPlugin.name(), nodeStrs)
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
		&scoreByInterPodAffinity{},
		&scoreByInterPodAntiAffinity{},
		&scoreByPodNodeAffinity{},
	}

	for _, nodeScorerPlugin := range nodeScorerPlugins {
		nodeScorerPlugin.setup(nodes, pods, volumes, c)
		if updatedNodeScores, err := nodeScorerPlugin.score(ctx, nodeScores); err != nil {
			w.Logger().Errorf(err, "failed to score nodes by node scorer (%s)", nodeScorerPlugin.name())
		} else {
			// update node scores if scorer plugin returned success
			nodeScores = updatedNodeScores
		}
		var nodeScoreResult string
		for nodeName, score := range nodeScores {
			nodeScoreResult += fmt.Sprintf("<%s: %d> ", nodeName, score)
		}
		w.Logger().V(10).Infof("node score after node score plugin (%s): %s", nodeScorerPlugin.name(), nodeScoreResult)
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
		azVolume, err = azureutils.GetAzVolume(ctx, c.cachedClient, c.azClient, volume, c.config.ObjectNamespace, true)
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

	return selectedNodes, nil
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
			Namespace: c.config.ObjectNamespace,
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
	azureutils.AnnotateAPIVersion(&azVolumeAttachment)

	_, err = c.azClient.DiskV1beta2().AzVolumeAttachments(c.config.ObjectNamespace).Create(ctx, &azVolumeAttachment, metav1.CreateOptions{})
	if err != nil {
		err = status.Errorf(codes.Internal, "failed to create replica AzVolumeAttachment %s.", replicaName)
		return err
	}
	return nil
}

func (c *SharedState) cleanUpAzVolumeAttachmentByVolume(ctx context.Context, azVolumeName string, caller operationRequester, role azureutils.AttachmentRoleMode, deleteMode attachmentCleanUpMode) ([]azdiskv1beta2.AzVolumeAttachment, error) {
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

func (c *SharedState) cleanUpAzVolumeAttachmentByNode(ctx context.Context, azDriverNodeName string, caller operationRequester, role azureutils.AttachmentRoleMode, deleteMode attachmentCleanUpMode) ([]azdiskv1beta2.AzVolumeAttachment, error) {
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
	attachments, err = c.azClient.DiskV1beta2().AzVolumeAttachments(c.config.ObjectNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
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

func (c *SharedState) cleanUpAzVolumeAttachments(ctx context.Context, attachments []azdiskv1beta2.AzVolumeAttachment, cleanUp attachmentCleanUpMode, caller operationRequester) error {
	var err error

	for _, attachment := range attachments {
		var patchRequired bool
		patched := attachment.DeepCopy()

		// if caller is azdrivernode, don't append cleanup annotation
		if (caller != azdrivernode && !metav1.HasAnnotation(patched.ObjectMeta, consts.CleanUpAnnotation)) ||
			// replica attachments should always be detached regardless of the cleanup mode
			((cleanUp == cleanUpAttachment || patched.Spec.RequestedRole == azdiskv1beta2.ReplicaRole) && !metav1.HasAnnotation(patched.ObjectMeta, consts.VolumeDetachRequestAnnotation)) {
			patchRequired = true
			if caller != azdrivernode {
				patched.Status.Annotations = azureutils.AddToMap(patched.Status.Annotations, consts.CleanUpAnnotation, string(caller))
			}
			if cleanUp == cleanUpAttachment || patched.Spec.RequestedRole == azdiskv1beta2.ReplicaRole {
				patched.Status.Annotations = azureutils.AddToMap(patched.Status.Annotations, consts.VolumeDetachRequestAnnotation, string(caller))
			}
		}

		if patchRequired {
			if err = c.cachedClient.Status().Patch(ctx, patched, client.MergeFrom(&attachment)); err != nil && apiErrors.IsNotFound(err) {
				err = status.Errorf(codes.Internal, "failed to patch AzVolumeAttachment (%s)", attachment.Name)
				return err
			}
		}
	}
	return nil
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
			if _, err := c.cleanUpAzVolumeAttachmentByVolume(ctx, volumeName, requester, azureutils.ReplicaOnly, cleanUpAttachment); err != nil {
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
	azVolume, err = azureutils.GetAzVolume(ctx, c.cachedClient, c.azClient, volumeName, c.config.ObjectNamespace, true)
	if apiErrors.IsNotFound(err) {
		w.Logger().V(5).Info("Volume no longer exists. Aborting manage replica operation")
		return nil
	} else if err != nil {
		w.Logger().Error(err, "failed to get AzVolume")
		return err
	}

	// replica management should not be executed or retried if AzVolume is scheduled for a deletion or not created.
	deleteRequested, _ := objectDeletionRequested(azVolume)
	if !isCreated(azVolume) || deleteRequested {
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
			// continue to try attachment with next node
			continue
		}
		remainingReplicas--
		if remainingReplicas <= 0 {
			// no more remainingReplicas, don't need to create replica AzVolumeAttachment
			break
		}
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
	for _, node := range nodes {
		if skipSet[node] {
			continue
		}
		// if the node has no capacity for disk attachment, we should skip it
		remainingCapacity, nodeExists := c.availableAttachmentsMap.Load(node)
		if !nodeExists || remainingCapacity == nil || remainingCapacity.(*atomic.Int32).Load() <= int32(0) {
			w.Logger().V(5).Infof("skip node(%s) because it has no capacity for disk attachment", node)
			continue
		}
		filtered = append(filtered, node)
	}

	return filtered, nil
}

func (c *SharedState) createAzVolumeFromPv(ctx context.Context, pv v1.PersistentVolume, annotations map[string]string) error {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	var desiredAzVolume *azdiskv1beta2.AzVolume
	requiredBytes, _ := pv.Spec.Capacity.Storage().AsInt64()
	volumeCapability := getVolumeCapabilityFromPv(&pv)

	// translate intree pv to csi pv to convert them into AzVolume resource
	if utilfeature.DefaultFeatureGate.Enabled(features.CSIMigration) &&
		utilfeature.DefaultFeatureGate.Enabled(features.CSIMigrationAzureDisk) &&
		pv.Spec.AzureDisk != nil {
		var transPV *v1.PersistentVolume
		// if an error occurs while translating, it's unrecoverable, so return no error
		if transPV, err = c.translateInTreePVToCSI(&pv); err != nil {
			return err
		}
		pv = *transPV
	}

	// skip if PV is not managed by azuredisk driver
	if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != c.config.DriverName {
		return nil
	}

	// create AzVolume CRI for CSI Volume Source
	desiredAzVolume, err = c.createAzVolumeFromCSISource(pv.Spec.CSI)
	if err != nil {
		return err
	}

	if pv.Spec.NodeAffinity != nil && pv.Spec.NodeAffinity.Required != nil {
		desiredAzVolume.Status.Detail.AccessibleTopology = azureutils.GetTopologyFromNodeSelector(*pv.Spec.NodeAffinity.Required, c.topologyKey)
	}
	if azureutils.IsMultiNodePersistentVolume(pv) {
		desiredAzVolume.Spec.MaxMountReplicaCount = 0
	}

	// if it's an inline volume, no pv label or pvc label should be added
	if !azureutils.MapContains(annotations, consts.InlineVolumeAnnotation) {
		desiredAzVolume.Labels = azureutils.AddToMap(desiredAzVolume.Labels, consts.PvNameLabel, pv.Name)

		if pv.Spec.ClaimRef != nil {
			desiredAzVolume.Labels = azureutils.AddToMap(desiredAzVolume.Labels, consts.PvcNameLabel, pv.Spec.ClaimRef.Name)
			desiredAzVolume.Labels = azureutils.AddToMap(desiredAzVolume.Labels, consts.PvcNamespaceLabel, pv.Spec.ClaimRef.Namespace)
		}
	}

	desiredAzVolume.Spec.VolumeCapability = volumeCapability
	desiredAzVolume.Spec.PersistentVolume = pv.Name
	desiredAzVolume.Spec.CapacityRange = &azdiskv1beta2.CapacityRange{RequiredBytes: requiredBytes}

	desiredAzVolume.Status.Detail.CapacityBytes = requiredBytes

	for k, v := range annotations {
		desiredAzVolume.Status.Annotations = azureutils.AddToMap(desiredAzVolume.Status.Annotations, k, v)
	}

	w.AddDetailToLogger(consts.PvNameKey, pv.Name, consts.VolumeNameLabel, desiredAzVolume.Name)

	if err = c.createAzVolume(ctx, desiredAzVolume); err != nil {
		err = status.Errorf(codes.Internal, "failed to create AzVolume (%s) for PV (%s): %v", desiredAzVolume.Name, pv.Name, err)
		return err
	}
	return nil
}

func (c *SharedState) createAzVolumeFromCSISource(source *v1.CSIPersistentVolumeSource) (*azdiskv1beta2.AzVolume, error) {
	diskName, err := azureutils.GetDiskName(source.VolumeHandle)
	if err != nil {
		return nil, fmt.Errorf("failed to extract diskName from volume handle (%s): %v", source.VolumeHandle, err)
	}

	_, maxMountReplicaCount := azureutils.GetMaxSharesAndMaxMountReplicaCount(source.VolumeAttributes, false)

	var volumeParams map[string]string
	if source.VolumeAttributes == nil {
		volumeParams = make(map[string]string)
	} else {
		volumeParams = source.VolumeAttributes
	}

	azVolumeName := strings.ToLower(diskName)

	azVolume := azdiskv1beta2.AzVolume{
		ObjectMeta: metav1.ObjectMeta{
			Name:       azVolumeName,
			Finalizers: []string{consts.AzVolumeFinalizer},
		},
		Spec: azdiskv1beta2.AzVolumeSpec{
			MaxMountReplicaCount: maxMountReplicaCount,
			Parameters:           volumeParams,
			VolumeName:           diskName,
		},
		Status: azdiskv1beta2.AzVolumeStatus{
			Detail: &azdiskv1beta2.AzVolumeStatusDetail{
				VolumeID:      source.VolumeHandle,
				VolumeContext: source.VolumeAttributes,
			},
			State: azdiskv1beta2.VolumeCreated,
		},
	}
	azureutils.AnnotateAPIVersion(&azVolume)

	return &azVolume, nil
}

func (c *SharedState) createAzVolume(ctx context.Context, desiredAzVolume *azdiskv1beta2.AzVolume) error {
	w, _ := workflow.GetWorkflowFromContext(ctx)

	var err error
	var azVolume *azdiskv1beta2.AzVolume
	var updated *azdiskv1beta2.AzVolume

	if azVolume, err = c.azClient.DiskV1beta2().AzVolumes(c.config.ObjectNamespace).Get(ctx, desiredAzVolume.Name, metav1.GetOptions{}); err != nil {
		if apiErrors.IsNotFound(err) {
			if azVolume, err = c.azClient.DiskV1beta2().AzVolumes(c.config.ObjectNamespace).Create(ctx, desiredAzVolume, metav1.CreateOptions{}); err != nil {
				return err
			}
			updated = azVolume.DeepCopy()
			updated.Status = desiredAzVolume.Status
		} else {
			return err
		}
	}

	if apiVersion, ok := azureutils.GetFromMap(azVolume.Annotations, consts.APIVersion); !ok || apiVersion != azdiskv1beta2.APIVersion {
		w.Logger().Infof("Found AzVolume (%s) with older api version. Converting to apiVersion(%s)", azVolume.Name, azdiskv1beta2.APIVersion)

		azVolume.Spec.PersistentVolume = desiredAzVolume.Spec.PersistentVolume

		for k, v := range desiredAzVolume.Labels {
			azVolume.Labels = azureutils.AddToMap(azVolume.Labels, k, v)
		}

		for k, v := range azVolume.Annotations {
			azVolume.Status.Annotations = azureutils.AddToMap(azVolume.Annotations, k, v)
		}

		// for now, we don't empty the meta annotatinos after migrating them to status annotation for safety.
		// note that this will leave some remnant garbage entries in meta annotations

		for k, v := range desiredAzVolume.Annotations {
			azVolume.Annotations = azureutils.AddToMap(azVolume.Annotations, k, v)
		}
		updated = azVolume.DeepCopy()
	} else {
		return nil
	}

	if _, err := c.azClient.DiskV1beta2().AzVolumes(c.config.ObjectNamespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return err
	}
	// if AzVolume CRI successfully recreated, also recreate the operation queue for the volume
	c.createOperationQueue(desiredAzVolume.Name)
	return nil
}

func (c *SharedState) translateInTreePVToCSI(pv *v1.PersistentVolume) (*v1.PersistentVolume, error) {
	var err error
	// translate intree pv to csi pv to convert them into AzVolume resource
	if utilfeature.DefaultFeatureGate.Enabled(features.CSIMigration) &&
		utilfeature.DefaultFeatureGate.Enabled(features.CSIMigrationAzureDisk) &&
		pv.Spec.AzureDisk != nil {
		// if an error occurs while translating, it's unrecoverable, so return no error
		if pv, err = c.azureDiskCSITranslator.TranslateInTreePVToCSI(pv); err != nil {
		} else if pv == nil {
			err = status.Errorf(codes.Internal, "unexpected failure in translating inline volume to csi")
		}

	}
	return pv, err
}

// waitForVolumeAttachmentNAme waits for the VolumeAttachment name to be updated in the azVolumeAttachmentVaMap by the volumeattachment controller
func (c *SharedState) waitForVolumeAttachmentName(ctx context.Context, azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment) (string, error) {
	var vaName string
	err := wait.PollImmediateUntilWithContext(ctx, consts.DefaultPollingRate, func(ctx context.Context) (bool, error) {
		val, exists := c.azVolumeAttachmentToVaMap.Load(azVolumeAttachment.Name)
		if exists {
			vaName = val.(string)
		}
		return exists, nil
	})
	return vaName, err
}

// Returns set of node names that qualify pod affinity term and set of node names with qualifying replica attachments.
func (c *SharedState) getQualifiedNodesForPodAffinityTerm(ctx context.Context, nodes []v1.Node, podNamespace string, affinityTerm v1.PodAffinityTerm) (podNodes, replicaNodes set) {
	var err error
	w, _ := workflow.GetWorkflowFromContext(ctx)
	candidateNodes := set{}
	for _, node := range nodes {
		candidateNodes.add(node.Name)
	}
	podNodes = set{}
	replicaNodes = set{}

	var podSelector labels.Selector
	podSelector, err = metav1.LabelSelectorAsSelector(affinityTerm.LabelSelector)
	// if failed to convert pod affinity label selector to selector, log error and skip
	if err != nil {
		w.Logger().Errorf(err, "failed to convert pod affinity (%v) to selector", affinityTerm.LabelSelector)
	}

	nsList := &v1.NamespaceList{}
	if affinityTerm.NamespaceSelector != nil {
		nsSelector, err := metav1.LabelSelectorAsSelector(affinityTerm.NamespaceSelector)
		// if failed to convert pod affinity label selector to selector, log error and skip
		if err != nil {
			w.Logger().Errorf(err, "failed to convert pod affinity (%v) to selector", affinityTerm.LabelSelector)
		} else {
			if err = c.cachedClient.List(ctx, nsList, &client.ListOptions{LabelSelector: nsSelector}); err != nil {
				w.Logger().Errorf(err, "failed to list namespaces with selector (%v)", nsSelector)
				return
			}

		}
	}

	namespaces := affinityTerm.Namespaces
	for _, ns := range nsList.Items {
		namespaces = append(namespaces, ns.Name)
	}

	pods := []v1.Pod{}
	if len(namespaces) > 0 {
		for _, namespace := range namespaces {
			podList := &v1.PodList{}
			if err = c.cachedClient.List(ctx, podList, &client.ListOptions{LabelSelector: podSelector, Namespace: namespace}); err != nil {
				w.Logger().Errorf(err, "failed to retrieve pod list: %v", err)
				pods = append(pods, podList.Items...)
			}
		}
	} else {
		podList := &v1.PodList{}
		if err = c.cachedClient.List(ctx, podList, &client.ListOptions{LabelSelector: podSelector, Namespace: podNamespace}); err != nil {
			w.Logger().Errorf(err, "failed to retrieve pod list: %v", err)
		}
		pods = podList.Items
	}

	// get replica nodes for pods that satisfy pod label selector
	replicaNodes = c.getReplicaNodesForPods(ctx, pods)
	for replicaNode := range replicaNodes {
		if !candidateNodes.has(replicaNode) {
			candidateNodes.remove(replicaNode)
		}
	}

	// get nodes with pod that share the same topology as pods satisfying pod label selector
	for _, pod := range pods {
		podNodes.add(pod.Spec.NodeName)
	}

	var podNodeObjs []v1.Node
	for node := range podNodes {
		var nodeObj v1.Node
		if err = c.cachedClient.Get(ctx, types.NamespacedName{Name: node.(string)}, &nodeObj); err != nil {
			w.Logger().Errorf(err, "failed to get node (%s)", node.(string))
			continue
		}
		podNodeObjs = append(podNodeObjs, nodeObj)
	}

	topologyLabel := c.getNodesTopologySelector(ctx, podNodeObjs, affinityTerm.TopologyKey)
	for _, node := range nodes {
		if topologyLabel != nil && topologyLabel.Matches(labels.Set(node.Labels)) {
			podNodes.add(node.Name)
		}
	}
	return
}

// Returns set of node names where replica mounts of given pod can be found
func (c *SharedState) getReplicaNodesForPods(ctx context.Context, pods []v1.Pod) (replicaNodes set) {
	// add nodes, to which replica attachments of matching pods' volumes are attached, to replicaNodes
	replicaNodes = set{}
	if volumes, err := c.getVolumesForPodObjs(ctx, pods); err == nil {
		for _, volume := range volumes {
			attachments, err := azureutils.GetAzVolumeAttachmentsForVolume(ctx, c.cachedClient, volume, azureutils.ReplicaOnly)
			if err != nil {
				continue
			}
			for _, attachment := range attachments {
				node := attachment.Spec.NodeName
				replicaNodes.add(node)
			}
		}
	}

	return replicaNodes
}

// Returns a label selector corresponding to a list of nodes and a topology key (aka label key)
func (c *SharedState) getNodesTopologySelector(ctx context.Context, nodes []v1.Node, topologyKey string) labels.Selector {
	w, _ := workflow.GetWorkflowFromContext(ctx)
	if len(nodes) == 0 {
		return nil
	}

	topologyValues := set{}
	for _, node := range nodes {
		nodeLabels := node.GetLabels()
		if topologyValue, exists := nodeLabels[topologyKey]; exists {
			topologyValues.add(topologyValue)
		} else {
			w.Logger().V(5).Infof("node (%s) doesn't have label value for topologyKey (%s)", node.Name, topologyKey)
		}
	}

	topologySelector := labels.NewSelector()
	topologyRequirement, err := azureutils.CreateLabelRequirements(topologyKey, selection.In, topologyValues.toStringSlice()...)
	// if failed to create label requirement, log error and return empty selector
	if err != nil {
		w.Logger().Errorf(err, "failed to create label requirement for topologyKey (%s)", topologyKey)
	} else {
		topologySelector = topologySelector.Add(*topologyRequirement)
	}
	return topologySelector
}

func (c *SharedState) addNodeToAvailableAttachmentsMap(ctx context.Context, nodeName string, nodeLables map[string]string) {
	if _, ok := c.availableAttachmentsMap.Load(nodeName); !ok {
		capacity, err := azureutils.GetNodeRemainingDiskCount(ctx, c.cachedClient, nodeName)
		if err != nil {
			klog.Errorf("Failed to get node(%s) remaining disk count with error: %v", nodeName, err)
			// store the maximum capacity if an entry for the node doesn't exist.
			capacity, err = azureutils.GetNodeMaxDiskCountWithLabels(nodeLables)
			if err != nil {
				klog.Errorf("Failed to add node(%s) in availableAttachmentsMap, because get capacity of available attachments is failed with error: %v", nodeName, err)
				return
			}
		}
		var count atomic.Int32
		count.Store(int32(capacity))
		klog.Infof("Added node(%s) to availableAttachmentsMap with capacity: %d", nodeName, capacity)
		c.availableAttachmentsMap.LoadOrStore(nodeName, &count)
	}
}

func (c *SharedState) deleteNodeFromAvailableAttachmentsMap(ctx context.Context, node string) {
	klog.Infof("Deleted node(%s) from availableAttachmentsMap", node)
	c.availableAttachmentsMap.Delete(node)
}
func (c *SharedState) decrementAttachmentCount(ctx context.Context, node string) bool {
	remainingCapacity, nodeExists := c.availableAttachmentsMap.Load(node)
	if nodeExists && remainingCapacity != nil {
		currentCapacity := int32(0)
		for {
			currentCapacity = remainingCapacity.(*atomic.Int32).Load()
			if currentCapacity == int32(0) || remainingCapacity.(*atomic.Int32).CompareAndSwap(currentCapacity, currentCapacity-1) {
				if currentCapacity == int32(0) {
					klog.Errorf("Failed to decrement attachment count for node(%s), because no available attachment", node)
					return false
				}
				return true
			}
		}
	}

	klog.Errorf("Failed to decrement attachment count, because node(%s) not found", node)

	return false
}
func (c *SharedState) incrementAttachmentCount(ctx context.Context, node string) bool {
	remainingCapacity, nodeExists := c.availableAttachmentsMap.Load(node)
	if nodeExists && remainingCapacity != nil {
		remainingCapacity.(*atomic.Int32).Add(1)
		return true
	}

	klog.Errorf("Failed to increment attachment count, because node(%s) not found", node)

	return false
}
