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
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2022-03-01/compute"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/provisioner"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"

	cache "k8s.io/client-go/tools/cache"

	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const (
	DefaultTimeUntilGarbageCollection = time.Duration(5) * time.Minute

	maxRetry             = 10
	defaultRetryDuration = time.Duration(1) * time.Second
	defaultRetryFactor   = 5.0
	defaultRetrySteps    = 5

	cloudTimeout = time.Duration(5) * time.Minute

	defaultMaxPodAffinityWeight = 100
)

type noOpReconciler struct{}

func (n *noOpReconciler) Reconcile(_ context.Context, _ reconcile.Request) (reconcile.Result, error) {
	return reconcile.Result{}, nil
}

type operationRequester string

const (
	azdrivernode     operationRequester = "azdrivernode-controller"
	azvolume         operationRequester = "azvolume-controller"
	pv               operationRequester = "pv-controller"
	replica          operationRequester = "replica-controller"
	nodeavailability operationRequester = "nodeavailability-controller"
	pod              operationRequester = "pod-controller"
)

type attachmentCleanUpMode int

const (
	cleanUpAttachmentForUninstall attachmentCleanUpMode = iota
	cleanUpAttachment
)

type deleteMode int

const (
	deleteOnly deleteMode = iota
	deleteAndWait
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

func shouldRequeueReplicaOperation(isReplicaGarbageCollection bool, err error) bool {
	return !isReplicaGarbageCollection || !errors.Is(err, context.Canceled)
}

type filterPlugin interface {
	name() string
	setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, state *SharedState)
	filter(ctx context.Context, nodes []v1.Node) ([]v1.Node, error)
}

// interPodAffinityFilter selects nodes that either meets inter-pod affinity rules or has replica mounts of volumes of pods with matching labels
type interPodAffinityFilter struct {
	pods  []v1.Pod
	state *SharedState
}

func (p *interPodAffinityFilter) name() string {
	return "inter-pod affinity filter"
}

func (p *interPodAffinityFilter) setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, state *SharedState) {
	p.pods = pods
	p.state = state
}

func (p *interPodAffinityFilter) filter(ctx context.Context, nodes []v1.Node) ([]v1.Node, error) {
	ctx, w := workflow.New(ctx, workflow.WithDetails("filter-plugin", p.name()))
	defer w.Finish(nil)
	nodeMap := map[string]int{}
	qualifyingNodes := set{}

	for i, node := range nodes {
		nodeMap[node.Name] = i
		qualifyingNodes.add(node.Name)
	}

	isFirst := true
	for _, pod := range p.pods {
		if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAffinity == nil {
			continue
		}
		for _, affinityTerm := range pod.Spec.Affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			podNodes, replicaNodes := p.state.getQualifiedNodesForPodAffinityTerm(ctx, nodes, pod.Namespace, affinityTerm)
			if isFirst {
				qualifyingNodes = set{}
				for podNode := range podNodes {
					qualifyingNodes.add(podNode)
				}
				for replicaNode := range replicaNodes {
					qualifyingNodes.add(replicaNode)
				}
				isFirst = false
			} else {
				for qualifyingNode := range qualifyingNodes {
					if !podNodes.has(qualifyingNode) && !replicaNodes.has(qualifyingNode) {
						qualifyingNodes.remove(qualifyingNode)
					}
				}
			}
		}
	}

	var filteredNodes []v1.Node
	for qualifyingNode := range qualifyingNodes {
		if i, exists := nodeMap[qualifyingNode.(string)]; exists {
			filteredNodes = append(filteredNodes, nodes[i])
		}
	}
	// Logging purpose
	evictedNodes := make([]string, len(nodes)-len(filteredNodes))
	i := 0
	for _, node := range nodes {
		if !qualifyingNodes.has(node.Name) {
			evictedNodes[i] = node.Name
			i++
		}
	}
	w.Logger().V(10).Infof("nodes (%+v) filtered out by %s", evictedNodes, p.name())

	return filteredNodes, nil
}

type interPodAntiAffinityFilter struct {
	pods  []v1.Pod
	state *SharedState
}

func (p *interPodAntiAffinityFilter) name() string {
	return "inter-pod anti-affinity filter"
}

func (p *interPodAntiAffinityFilter) setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, state *SharedState) {
	p.pods = pods
	p.state = state
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

	qualifyingNodes := set{}
	isFirst := true
	for _, pod := range p.pods {
		if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAntiAffinity == nil {
			continue
		}
		for _, affinityTerm := range pod.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
			podNodes, _ := p.state.getQualifiedNodesForPodAffinityTerm(ctx, nodes, pod.Namespace, affinityTerm)
			if isFirst {
				for podNode := range podNodes {
					qualifyingNodes.add(podNode)
				}
				isFirst = false
			} else {
				for qualifyingNode := range qualifyingNodes {
					if !podNodes.has(qualifyingNode) {
						qualifyingNodes.remove(qualifyingNode)
					}
				}
			}
		}
	}

	var filteredNodes []v1.Node
	for candidateNode := range candidateNodes {
		if !qualifyingNodes.has(candidateNode) {
			if i, exists := nodeMap[candidateNode.(string)]; exists {
				filteredNodes = append(filteredNodes, nodes[i])
			}
		}
	}

	// Logging purpose
	evictedNodes := make([]string, len(nodes)-len(filteredNodes))
	i := 0
	for _, node := range nodes {
		if qualifyingNodes.has(node.Name) {
			evictedNodes[i] = node.Name
			i++
		}
	}
	w.Logger().V(10).Infof("nodes (%+v) filtered out by %s", evictedNodes, p.name())

	return filteredNodes, nil
}

type podTolerationFilter struct {
	pods []v1.Pod
}

func (p *podTolerationFilter) name() string {
	return "pod toleration filter"
}

func (p *podTolerationFilter) setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, state *SharedState) {
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

func (p *podNodeAffinityFilter) setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, state *SharedState) {
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
	pods  []v1.Pod
	state *SharedState
}

func (p *podNodeSelectorFilter) name() string {
	return "pod node-selector filter"
}

func (p *podNodeSelectorFilter) setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, state *SharedState) {
	p.pods = pods
	p.state = state
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

func (v *volumeNodeSelectorFilter) setup(pods []v1.Pod, persistentVolumes []*v1.PersistentVolume, state *SharedState) {
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
	setup(nodes []v1.Node, pods []v1.Pod, volumes []string, state *SharedState)
	priority() float64 // returns the score plugin's priority in a scale of 1 ~ 5 (5 being the highest priority)
	score(ctx context.Context, nodeScores map[string]int) (map[string]int, error)
}

type scoreByNodeCapacity struct {
	nodes   []v1.Node
	volumes []string
	state   *SharedState
}

func (s *scoreByNodeCapacity) name() string {
	return "score by node capacity"
}

func (s *scoreByNodeCapacity) priority() float64 {
	return 1
}

func (s *scoreByNodeCapacity) setup(nodes []v1.Node, pods []v1.Pod, volumes []string, state *SharedState) {
	s.nodes = nodes
	s.volumes = volumes
	s.state = state
}

func (s *scoreByNodeCapacity) score(ctx context.Context, nodeScores map[string]int) (map[string]int, error) {
	ctx, w := workflow.New(ctx, workflow.WithDetails("score-plugin", s.name()))
	defer w.Finish(nil)
	for _, node := range s.nodes {
		if _, ok := nodeScores[node.Name]; !ok {
			continue
		}
		maxCapacity, err := azureutils.GetNodeMaxDiskCountWithLabels(node.Labels)
		if err != nil {
			w.Logger().Errorf(err, "failed to get max capacity of node (%s)", node.Name)
			delete(nodeScores, node.Name)
			continue
		}
		remainingCapacity, err := azureutils.GetNodeRemainingDiskCount(ctx, s.state.cachedClient, node.Name)
		if err != nil {
			// if failed to get node's remaining capacity, remove the node from the candidate list and proceed
			w.Logger().Errorf(err, "failed to get remaining capacity of node (%s)", node.Name)
			delete(nodeScores, node.Name)
			continue
		}

		nodeScores[node.Name] += int((float64(remainingCapacity) / float64(maxCapacity)) * math.Pow(10, s.priority()))

		if remainingCapacity-len(s.volumes) < 0 {
			delete(nodeScores, node.Name)
		}

		w.Logger().V(10).Infof("node (%s) can accept %d more attachments", node.Name, remainingCapacity)
	}
	return nodeScores, nil
}

type scoreByReplicaCount struct {
	volumes []string
	state   *SharedState
}

func (s *scoreByReplicaCount) name() string {
	return "score by replica count"
}

func (s *scoreByReplicaCount) priority() float64 {
	return 3
}

func (s *scoreByReplicaCount) setup(nodes []v1.Node, pods []v1.Pod, volumes []string, state *SharedState) {
	s.volumes = volumes
	s.state = state
}

func (s *scoreByReplicaCount) score(ctx context.Context, nodeScores map[string]int) (map[string]int, error) {
	ctx, w := workflow.New(ctx, workflow.WithDetails("score-plugin", s.name()))
	defer w.Finish(nil)

	var requestedReplicaCount int

	nodeReplicaCounts := map[string]int{}

	for _, volume := range s.volumes {
		azVolume, err := azureutils.GetAzVolume(ctx, s.state.cachedClient, nil, volume, s.state.config.ObjectNamespace, true)
		if err != nil {
			w.Logger().Errorf(err, "failed to get AzVolume (%s)", volume)
			continue
		}
		requestedReplicaCount += azVolume.Spec.MaxMountReplicaCount
		azVolumeAttachments, err := azureutils.GetAzVolumeAttachmentsForVolume(ctx, s.state.cachedClient, volume, azureutils.AllRoles)
		if err != nil {
			w.Logger().V(5).Errorf(err, "failed listing AzVolumeAttachments for azvolume %s", volume)
			continue
		}

		for _, azVolumeAttachment := range azVolumeAttachments {
			if _, exists := nodeScores[azVolumeAttachment.Spec.NodeName]; exists {
				if azVolumeAttachment.Spec.RequestedRole == azdiskv1beta2.PrimaryRole {
					delete(nodeScores, azVolumeAttachment.Spec.NodeName)
				} else {
					nodeReplicaCounts[azVolumeAttachment.Spec.NodeName]++
				}
			}
		}

		if requestedReplicaCount > 0 {
			for nodeName, replicaCount := range nodeReplicaCounts {
				if _, ok := nodeScores[nodeName]; !ok {
					continue
				}
				nodeScores[nodeName] += int((float64(replicaCount) / float64(requestedReplicaCount)) * math.Pow(10, s.priority()))
			}
		}
	}
	return nodeScores, nil
}

type scoreByInterPodAffinity struct {
	nodes   []v1.Node
	pods    []v1.Pod
	volumes []string
	state   *SharedState
}

func (s *scoreByInterPodAffinity) name() string {
	return "score by inter pod affinity"
}

func (s *scoreByInterPodAffinity) priority() float64 {
	return 2
}

func (s *scoreByInterPodAffinity) setup(nodes []v1.Node, pods []v1.Pod, volumes []string, state *SharedState) {
	s.nodes = nodes
	s.pods = pods
	s.volumes = volumes
	s.state = state
}

func (s *scoreByInterPodAffinity) score(ctx context.Context, nodeScores map[string]int) (map[string]int, error) {
	ctx, w := workflow.New(ctx, workflow.WithDetails("score-plugin", s.name()))
	defer w.Finish(nil)

	nodeAffinityScores := map[string]int{}
	var maxAffinityScore int

	for _, pod := range s.pods {
		if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAffinity == nil {
			continue
		}

		// pod affinity weight range from 1-100
		// and as a node can both be a part of 1) nodes that satisfy pod affinity rule and 2) nodes with qualifying replica attachments (for which we give half of the score), we need to 1.5X the max weight
		maxAffinityScore += 1.5 * defaultMaxPodAffinityWeight * len(pod.Spec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution)

		for _, weightedAffinityTerm := range pod.Spec.Affinity.PodAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
			podNodes, replicaNodes := s.state.getQualifiedNodesForPodAffinityTerm(ctx, s.nodes, pod.Namespace, weightedAffinityTerm.PodAffinityTerm)
			for podNode := range podNodes {
				w.Logger().Infof("podNode: %s", podNode.(string))
				nodeAffinityScores[podNode.(string)] += int(weightedAffinityTerm.Weight)
			}
			for replicaNode := range replicaNodes {
				nodeAffinityScores[replicaNode.(string)] += int(weightedAffinityTerm.Weight / 2)
			}
		}
	}

	for node, affinityScore := range nodeAffinityScores {
		if _, ok := nodeScores[node]; !ok {
			continue
		}
		nodeScores[node] += int((float64(affinityScore) / float64(maxAffinityScore)) * math.Pow(10, s.priority()))
	}

	return nodeScores, nil
}

type scoreByInterPodAntiAffinity struct {
	nodes   []v1.Node
	pods    []v1.Pod
	volumes []string
	state   *SharedState
}

func (s *scoreByInterPodAntiAffinity) name() string {
	return "score by inter pod anti affinity"
}

func (s *scoreByInterPodAntiAffinity) priority() float64 {
	return 2
}

func (s *scoreByInterPodAntiAffinity) setup(nodes []v1.Node, pods []v1.Pod, volumes []string, state *SharedState) {
	s.nodes = nodes
	s.pods = pods
	s.volumes = volumes
	s.state = state
}

func (s *scoreByInterPodAntiAffinity) score(ctx context.Context, nodeScores map[string]int) (map[string]int, error) {
	ctx, w := workflow.New(ctx, workflow.WithDetails("score-plugin", s.name()))
	defer w.Finish(nil)

	nodeAffinityScores := map[string]int{}
	var maxAffinityScore int

	for _, pod := range s.pods {
		if pod.Spec.Affinity == nil || pod.Spec.Affinity.PodAntiAffinity == nil {
			continue
		}

		// pod affinity weight range from 1-100
		maxAffinityScore += defaultMaxPodAffinityWeight * len(pod.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution)

		for _, weightedAffinityTerm := range pod.Spec.Affinity.PodAntiAffinity.PreferredDuringSchedulingIgnoredDuringExecution {
			podNodes, _ := s.state.getQualifiedNodesForPodAffinityTerm(ctx, s.nodes, pod.Namespace, weightedAffinityTerm.PodAffinityTerm)

			for node := range nodeScores {
				if !podNodes.has(node) {
					nodeAffinityScores[node] += int(weightedAffinityTerm.Weight)
				}
			}
		}
	}

	for node, affinityScore := range nodeAffinityScores {
		if _, ok := nodeScores[node]; !ok {
			continue
		}
		nodeScores[node] += int((float64(affinityScore) / float64(maxAffinityScore)) * math.Pow(10, s.priority()))
	}

	return nodeScores, nil
}

type scoreByPodNodeAffinity struct {
	nodes   []v1.Node
	pods    []v1.Pod
	volumes []string
	state   *SharedState
}

func (s *scoreByPodNodeAffinity) name() string {
	return "score by pod node affinity"
}

func (s *scoreByPodNodeAffinity) priority() float64 {
	return 2
}

func (s *scoreByPodNodeAffinity) setup(nodes []v1.Node, pods []v1.Pod, volumes []string, state *SharedState) {
	s.nodes = nodes
	s.pods = pods
	s.volumes = volumes
	s.state = state
}

func (s *scoreByPodNodeAffinity) score(ctx context.Context, nodeScores map[string]int) (map[string]int, error) {
	_, w := workflow.New(ctx, workflow.WithDetails("filter-plugin", s.name()))
	defer w.Finish(nil)

	var preferredSchedulingTerms []v1.PreferredSchedulingTerm

	for _, pod := range s.pods {
		if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil {
			continue
		}
		preferredSchedulingTerms = append(preferredSchedulingTerms, pod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution...)
	}

	preferredAffinity, err := nodeaffinity.NewPreferredSchedulingTerms(preferredSchedulingTerms)
	if err != nil {
		return nodeScores, err
	}

	for _, node := range s.nodes {
		nodeScore := preferredAffinity.Score(&node)
		if _, ok := nodeScores[node.Name]; !ok {
			continue
		}
		nodeScores[node.Name] += int(nodeScore)
	}
	return nodeScores, nil
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

func markDetachRequest(attachment *azdiskv1beta2.AzVolumeAttachment, caller operationRequester) {
	attachment.Status.Annotations = azureutils.AddToMap(attachment.Status.Annotations, consts.VolumeDetachRequestAnnotation, string(caller))
}

func markCleanUp(attachment *azdiskv1beta2.AzVolumeAttachment, caller operationRequester) {
	attachment.Status.Annotations = azureutils.AddToMap(attachment.Status.Annotations, consts.CleanUpAnnotation, string(caller))
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

// objectDeletionRequested returns whether deletion of the specified object has been requested.
// If so, it will return true and a time.Duration after which to delete the object. If the
// duration is less than or equal to 0, the object should be deleted immediately.
func objectDeletionRequested(obj runtime.Object) (bool, time.Duration) {
	meta, _ := meta.Accessor(obj)
	if meta == nil {
		return false, time.Duration(0)
	}
	deletionTime := meta.GetDeletionTimestamp()

	if deletionTime.IsZero() {
		return false, time.Duration(0)
	}

	return true, time.Until(deletionTime.Time)
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

// reconcileReturnOnSuccess returns a reconciler result on successful reconciliation.
func reconcileReturnOnSuccess(objectName string, retryInfo *retryInfo) (reconcile.Result, error) {
	retryInfo.deleteEntry(objectName)
	return reconcile.Result{}, nil
}

// reconcileReturnOnError returns a reconciler result on error that requeues the object for later processing if the error is retriable.
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

// reconcileAfter returns a reconciler result that requeues the current object for processing after the specified time.
func reconcileAfter(after time.Duration, objectName string, retryInfo *retryInfo) (reconcile.Result, error) {
	retryInfo.deleteEntry(objectName)
	return reconcile.Result{Requeue: true, RequeueAfter: after}, nil
}

func isOperationInProcess(obj interface{}) bool {
	switch target := obj.(type) {
	case *azdiskv1beta2.AzVolume:
		return target.Status.State == azdiskv1beta2.VolumeCreating || target.Status.State == azdiskv1beta2.VolumeDeleting || target.Status.State == azdiskv1beta2.VolumeUpdating
	case *azdiskv1beta2.AzVolumeAttachment:
		deleteRequested, _ := objectDeletionRequested(target)
		return target.Status.State == azdiskv1beta2.Attaching || (target.Status.State == azdiskv1beta2.Detaching && !deleteRequested)
	}
	return false
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

func verifyObjectDeleted(obj interface{}, objectDeleted bool) (bool, error) {
	if obj == nil || objectDeleted {
		return true, nil
	}
	return false, nil
}

func verifyObjectFailedOrDeleted(obj interface{}, objectDeleted bool) (bool, error) {
	if obj == nil || objectDeleted {
		return true, nil
	}

	switch target := obj.(type) {
	case azdiskv1beta2.AzVolumeAttachment:
		if target.Status.Error != nil {
			return false, util.ErrorFromAzError(target.Status.Error)
		}
	case azdiskv1beta2.AzVolume:
		// otherwise, the volume detachment has either failed with error or pending
		if target.Status.Error != nil {
			return false, util.ErrorFromAzError(target.Status.Error)
		}
	}

	return false, nil
}

// WatchObject creates a noop controller to set up a watch and an informer for an object in controller-runtime manager
// Use this function if you want to set up a watch for an object without a configuring a separate informer factory.
func WatchObject(mgr manager.Manager, objKind source.Kind) error {
	objType := objKind.Type.GetName()
	c, err := controller.New(fmt.Sprintf("watch %s", objType), mgr, controller.Options{
		Reconciler: &noOpReconciler{},
	})
	if err != nil {
		return err
	}

	c.GetLogger().Info("Starting to watch %s", objType)

	// Watch for CRUD events on objects
	err = c.Watch(&objKind, &handler.EnqueueRequestForObject{}, predicate.Funcs{
		CreateFunc: func(_ event.CreateEvent) bool {
			return false
		},
		UpdateFunc: func(_ event.UpdateEvent) bool {
			return true
		},
		GenericFunc: func(_ event.GenericEvent) bool {
			return false
		},
		DeleteFunc: func(_ event.DeleteEvent) bool {
			return false
		},
	})
	return err
}
