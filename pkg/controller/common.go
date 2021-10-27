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
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/util/wait"
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
	lock  *sync.RWMutex
	entry interface{}
}

func newLockableEntry(entry interface{}) lockableEntry {
	return lockableEntry{
		lock:  &sync.RWMutex{},
		entry: entry,
	}
}

type SharedState struct {
	podToClaimsMap   *sync.Map
	claimToPodsMap   *sync.Map
	volumeToClaimMap *sync.Map
	claimToVolumeMap *sync.Map
	podLocks         *sync.Map
	visitedVolumes   *sync.Map
}

func NewSharedState() *SharedState {
	return &SharedState{
		podToClaimsMap:   &sync.Map{},
		claimToPodsMap:   &sync.Map{},
		volumeToClaimMap: &sync.Map{},
		claimToVolumeMap: &sync.Map{},
		podLocks:         &sync.Map{},
		visitedVolumes:   &sync.Map{},
	}
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
	lockable, ok := value.(lockableEntry)
	if !ok {
		return nil, status.Errorf(codes.Internal, "claimToPodsMap should hold lockable entry")
	}

	lockable.lock.RLock()
	defer lockable.lock.RUnlock()

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

func getRankedNodesForReplicaAttachments(ctx context.Context, azr azReconciler, volumes []string) ([]string, error) {
	klog.V(5).Info("Getting ranked list of nodes for creating AzVolumeAttachments")
	numReplica := 0
	nodes := []string{}
	nodeScores := map[string]int{}
	primaryNode := ""

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
	}

	// remove the primary node from the nodeScores map as it can get added if all the volume attachments aren't promoted at the same time
	delete(nodeScores, primaryNode)

	// Remove the primary node from the node list as it can get added if all the volume attachments aren't promoted at the same time
	if len(nodes) > 0 {
		indexToRemove := -1
		for i, node := range nodes {
			if node == primaryNode {
				indexToRemove = i
				break
			}
		}
		if indexToRemove != -1 {
			nodes[indexToRemove] = nodes[len(nodes)-1]
			nodes = nodes[:len(nodes)-1]
		}
	}

	// sorting nodes array per their score in nodeScore array
	sort.Slice(nodes[:], func(i, j int) bool {
		return nodeScores[nodes[i]] > nodeScores[nodes[j]]
	})

	// select at least as many nodes as requested replicas
	if len(nodes) < numReplica {
		selected, err := selectNodes(ctx, azr, numReplica-len(nodes), primaryNode, nodeScores)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, selected...)
	}
	return nodes[:min(len(nodes), numReplica)], nil
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

		lockable := v.(lockableEntry)
		lockable.lock.Lock()
		pods := lockable.entry.(set)
		if !pods.has(podKey) {
			pods.add(podKey)
		}
		// No need to restore the amended set to claimToPodsMap because set is a reference type
		lockable.lock.Unlock()

		klog.V(5).Infof("Storing pod %s and claim %s to claimToPodsMap map.", pod.Name, namespacedClaimName)
		c.claimToPodsMap.Store(namespacedClaimName, lockable)
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
			lockable, ok := value.(lockableEntry)
			if !ok {
				klog.Errorf("claimToPodsMap should hold lockable entry")
				return
			}

			lockable.lock.Lock()
			defer lockable.lock.Unlock()

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

func selectNodes(ctx context.Context, azr azReconciler, numNodes int, primaryNode string, exceptionNodes map[string]int) ([]string, error) {
	// TODO: fill with node selection logic (based on node capacity and node failure domain)
	/*
		Below is temporary node selection logic
	*/
	filteredNodes := []string{}
	var nodes *v1alpha1.AzDriverNodeList
	var err error
	// List all AzDriverNodes
	nodes = &v1alpha1.AzDriverNodeList{}
	if err = azr.getClient().List(ctx, nodes, &client.ListOptions{}); err != nil {
		klog.Errorf("failed to retrieve azDriverNode List for namespace %s: %v", consts.AzureDiskCrdNamespace, err)
		return filteredNodes, err
	}

	nodeScores := map[string]int{}
	if nodes != nil {
		for _, node := range nodes.Items {
			if _, ok := exceptionNodes[node.Name]; ok || node.Name == primaryNode {
				continue
			}

			// filter out attachments labeled with specified node and volume
			var attachmentList v1alpha1.AzVolumeAttachmentList
			nodeRequirement, err := createLabelRequirements(consts.NodeNameLabel, node.Name)
			if err != nil {
				klog.Errorf("Encountered error while creating Requirement: %+v", err)
				continue
			}

			labelSelector := labels.NewSelector().Add(*nodeRequirement)
			if err := azr.getClient().List(ctx, &attachmentList, &client.ListOptions{LabelSelector: labelSelector}); err != nil {
				klog.Warningf("failed to get AzVolumeAttachmentList labeled with node (%s): %v", node.Name, err)
				continue
			}

			nodeScores[node.Name] = len(attachmentList.Items)
			filteredNodes = append(filteredNodes, node.Name)
			klog.Infof("node (%s) has %d attachments", node.Name, len(attachmentList.Items))
		}
	}

	// sort the filteredNodes by their number of attachments (low to high) and return a slice
	sort.Slice(filteredNodes[:], func(i, j int) bool {
		return nodeScores[filteredNodes[i]] < nodeScores[filteredNodes[j]]
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

func cleanUpAzVolumeAttachmentByVolume(ctx context.Context, azr azReconciler, azVolumeName, caller string, role roleMode, deleteMode cleanUpMode) (*v1alpha1.AzVolumeAttachmentList, error) {
	klog.Infof("AzVolumeAttachment clean up requested by %s for AzVolume (%s)", caller, azVolumeName)
	volRequirement, err := createLabelRequirements(consts.VolumeNameLabel, azVolumeName)
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector().Add(*volRequirement)

	attachments, err := azr.getAzClient().DiskV1alpha1().AzVolumeAttachments(consts.AzureDiskCrdNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		if errors.IsNotFound(err) {
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
	klog.Infof("successfully requested deletion of AzVolumeAttachments for AzVolume (%s)", azVolumeName)
	return attachments, nil
}

func cleanUpAzVolumeAttachmentByNode(ctx context.Context, azr azReconciler, azDriverNodeName, caller string, role roleMode, deleteMode cleanUpMode) (*v1alpha1.AzVolumeAttachmentList, error) {
	klog.Infof("AzVolumeAttachment clean up requested by %s for AzDriverNode (%s)", caller, azDriverNodeName)
	nodeRequirement, err := createLabelRequirements(consts.NodeNameLabel, azDriverNodeName)
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector().Add(*nodeRequirement)

	attachments, err := azr.getAzClient().DiskV1alpha1().AzVolumeAttachments(consts.AzureDiskCrdNamespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		if errors.IsNotFound(err) {
			return attachments, nil
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
	klog.Infof("successfully requested deletion of AzVolumeAttachments for AzDriverNode (%s)", azDriverNodeName)
	return attachments, nil
}

func cleanUpAzVolumeAttachments(ctx context.Context, azr azReconciler, attachments []v1alpha1.AzVolumeAttachment, cleanUp cleanUpMode, caller string) error {
	for _, attachment := range attachments {
		patched := attachment.DeepCopy()
		if patched.Annotations == nil {
			patched.Annotations = map[string]string{}
		}
		patched.Annotations[consts.CleanUpAnnotation] = "true"
		// replica attachments should always be detached regardless of the cleanup mode
		if cleanUp == detachAndDeleteCRI || patched.Spec.RequestedRole == v1alpha1.ReplicaRole {
			patched.Annotations[consts.VolumeDetachRequestAnnotation] = caller
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
	labelSelector := labels.NewSelector()
	volReq, err := createLabelRequirements(consts.VolumeNameLabel, volumeName)
	if err != nil {
		klog.V(5).Infof("Failed to create volume based label for listing AzVolumeAttachments for %s. Error: %v", volumeName, err)
		return
	}
	// filter by role if a role is specified
	if azVolumeAttachmentRole != all {
		roleReq, err := createLabelRequirements(consts.RoleLabel, roles[azVolumeAttachmentRole])
		if err != nil {
			klog.V(5).Infof("Failed to create role based label for listing AzVolumeAttachments for %s. Error: %v", volumeName, err)
			return nil, err
		}
		labelSelector = labelSelector.Add(*roleReq)
	}
	labelSelector = labelSelector.Add(*volReq)
	klog.V(5).Infof("Label selector for AzVolumeAttachments for volume %s is: %v.", volumeName, labelSelector)
	azVolumeAttachments := &v1alpha1.AzVolumeAttachmentList{}
	err = azclient.List(ctx, azVolumeAttachments, &client.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		klog.V(5).Infof("Error retrieving AzVolumeAttachments azvolume %s. Error: %v", volumeName, err)
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

func createLabelRequirements(label, value string) (*labels.Requirement, error) {
	req, err := labels.NewRequirement(label, selection.Equals, []string{value})
	if err != nil {
		return nil, err
	}
	if req == nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("Unable to create Requirement to for label key : (%s) and label value: (%s)", label, value))
	}
	return req, nil
}

func getQualifiedName(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
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
