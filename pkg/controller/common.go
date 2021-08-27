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

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
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
	azClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	maxRetry = 10
	// DefaultTimeUntilDeletion = time.Duration(5) * time.Minute
	DefaultTimeUntilDeletion = time.Duration(30) * time.Second
)

type cleanUpMode int

const (
	primaryOnly cleanUpMode = iota
	replicaOnly
	all
)

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
	getNamespace() string
}

type SharedState struct {
	podToClaimsMap   *sync.Map
	claimToPodsMap   *sync.Map
	volumeToClaimMap *sync.Map
	claimToVolumeMap *sync.Map
	podLocks         *sync.Map
}

func NewSharedState() *SharedState {
	return &SharedState{
		podToClaimsMap:   &sync.Map{},
		claimToPodsMap:   &sync.Map{},
		volumeToClaimMap: &sync.Map{},
		claimToVolumeMap: &sync.Map{},
		podLocks:         &sync.Map{},
	}
}

func (c *SharedState) getVolumesFromPod(podName string) ([]string, error) {
	var claims []string
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
			continue
		}
		volume, ok := value.(string)
		if !ok {
			return nil, status.Errorf(codes.Internal, "wrong output type: expected string")
		}
		volumes = append(volumes, volume)
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
	pods, ok := value.([]string)
	if !ok {
		return nil, status.Errorf(codes.Internal, "claimToPodsMap should hold string slices")
	}
	return pods, nil
}

func (c *SharedState) getNodesForReplica(ctx context.Context, azr azReconciler, volumes []string, pods ...string) ([]string, error) {
	if volumes == nil {
		volumes = []string{}
		for _, pod := range pods {
			podVolumes, err := c.getVolumesFromPod(pod)
			if err != nil {
				return nil, err
			}
			volumes = append(volumes, podVolumes...)
		}
	}

	numReplica := 0
	for _, volume := range volumes {
		var azVolume v1alpha1.AzVolume
		if err := azr.getClient().Get(ctx, types.NamespacedName{Namespace: azr.getNamespace(), Name: volume}, &azVolume); err != nil {
			return nil, err
		}
		numReplica = max(numReplica, azVolume.Spec.MaxMountReplicaCount)
	}

	nodes := []string{}
	nodeScores := map[string]int{}
	primaryNode := ""
	for _, volume := range volumes {
		volReq, err := createLabelRequirements(azureutils.VolumeNameLabel, volume)
		if err != nil {
			return nil, err
		}
		labelSelector := labels.NewSelector().Add(*volReq)
		azVolumeAttachments := &v1alpha1.AzVolumeAttachmentList{}
		err = azr.getClient().List(ctx, azVolumeAttachments, &client.ListOptions{LabelSelector: labelSelector})
		if err != nil {
			return nil, err
		}

		for _, azVolumeAttachment := range azVolumeAttachments.Items {
			if azVolumeAttachment.Spec.RequestedRole == v1alpha1.PrimaryRole {
				if primaryNode == "" {
					primaryNode = azVolumeAttachment.Spec.NodeName
				}
				continue
			}
			if _, ok := nodeScores[azVolumeAttachment.Spec.NodeName]; !ok {
				nodeScores[azVolumeAttachment.Spec.NodeName] = 0
				nodes = append(nodes, azVolumeAttachment.Spec.NodeName)
			}
			nodeScores[azVolumeAttachment.Spec.NodeName]++
		}
	}
	sort.Slice(nodes[:], func(i, j int) bool {
		return nodeScores[nodes[i]] > nodeScores[nodes[j]]
	})

	if len(nodes) < numReplica {
		selected, err := selectNodes(ctx, azr, numReplica-len(nodes), primaryNode, nodeScores)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, selected...)
	}
	return nodes[:min(len(nodes), numReplica)], nil
}

func (c *SharedState) addPod(pod *corev1.Pod, updateOption updateWithLock) (podLock *sync.Mutex) {
	podKey := getQualifiedName(pod.Namespace, pod.Name)

	v, _ := c.podLocks.LoadOrStore(podKey, &sync.Mutex{})
	podLock = v.(*sync.Mutex)
	if updateOption == acquireLock {
		podLock.Lock()
	}

	claims := []string{}
	for _, volume := range pod.Spec.Volumes {
		if volume.CSI == nil || volume.CSI.Driver != azureutils.DriverName {
			continue
		}
		namespacedClaimName := getQualifiedName(pod.Namespace, volume.PersistentVolumeClaim.ClaimName)
		claims = append(claims, namespacedClaimName)
		v, _ := c.claimToPodsMap.LoadOrStore(namespacedClaimName, []string{})

		pods := v.([]string)
		podExist := false
		for _, pod := range pods {
			if pod == podKey {
				podExist = true
				break
			}
		}
		if !podExist {
			pods = append(pods, podKey)
		}
		c.claimToPodsMap.Store(namespacedClaimName, pods)
	}
	c.podToClaimsMap.Store(podKey, claims)
	return
}

func (c *SharedState) deletePod(podKey string) {
	value, exists := c.podToClaimsMap.LoadAndDelete(podKey)
	if !exists {
		return
	}
	claims := value.([]string)

	for _, claim := range claims {
		c.claimToPodsMap.Delete(claim)
	}
	c.podLocks.Delete(podKey)
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
		klog.Errorf("failed to retrieve azDriverNode List for namespace %s: %v", azr.getNamespace(), err)
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
			nodeRequirement, err := createLabelRequirements(azureutils.NodeNameLabel, node.Name)
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
	// get all replica attachments for the given volume
	volReq, err := createLabelRequirements(azureutils.VolumeNameLabel, volumeName)
	if err != nil {
		return nil, err
	}
	roleReq, err := createLabelRequirements(azureutils.RoleLabel, string(v1alpha1.ReplicaRole))
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector().Add(*volReq, *roleReq)
	azVolumeAttachments := &v1alpha1.AzVolumeAttachmentList{}
	err = azr.getClient().List(ctx, azVolumeAttachments, &client.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return nil, err
	}

	nodes := []string{}
	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		nodes = append(nodes, azVolumeAttachment.Spec.NodeName)
	}

	return nodes, nil
}

func createReplica(ctx context.Context, azr azReconciler, volumeID, node string) error {
	diskName, err := azureutils.GetDiskNameFromAzureManagedDiskURI(volumeID)
	if err != nil {
		return err
	}
	volumeName := strings.ToLower(diskName)
	replicaName := azureutils.GetAzVolumeAttachmentName(volumeName, node)

	_, err = azr.getAzClient().DiskV1alpha1().AzVolumeAttachments(azr.getNamespace()).Create(ctx, &v1alpha1.AzVolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      replicaName,
			Namespace: azr.getNamespace(),
			Labels: map[string]string{
				azureutils.NodeNameLabel:   node,
				azureutils.VolumeNameLabel: volumeName,
				azureutils.RoleLabel:       string(v1alpha1.ReplicaRole),
			},
		},
		Spec: v1alpha1.AzVolumeAttachmentSpec{
			NodeName:         node,
			VolumeID:         volumeID,
			UnderlyingVolume: volumeName,
			RequestedRole:    v1alpha1.ReplicaRole,
			VolumeContext:    map[string]string{},
		},
		Status: v1alpha1.AzVolumeAttachmentStatus{
			State: v1alpha1.AttachmentPending,
		},
	}, metav1.CreateOptions{})

	if err != nil {
		klog.Infof("Replica AzVolumeAttachment (%s) has been successfully created.", replicaName)
	}
	return err
}

func cleanUpAzVolumeAttachmentByVolume(ctx context.Context, azr azReconciler, azVolumeName string, mode cleanUpMode) (*v1alpha1.AzVolumeAttachmentList, error) {
	volRequirement, err := createLabelRequirements(azureutils.VolumeNameLabel, azVolumeName)
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector().Add(*volRequirement)

	attachments, err := azr.getAzClient().DiskV1alpha1().AzVolumeAttachments(azr.getNamespace()).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		klog.Errorf("failed to get AzVolumeAttachments: %v", err)
		return nil, err
	}

	cleanUps := []v1alpha1.AzVolumeAttachment{}
	for _, attachment := range attachments.Items {
		if shouldCleanUp(attachment, mode) {
			cleanUps = append(cleanUps, attachment)
		}
	}

	if err := cleanUpAzVolumeAttachments(ctx, azr, cleanUps); err != nil {
		return attachments, err
	}
	klog.Infof("successfully requested deletion of AzVolumeAttachments for AzVolume (%s)", azVolumeName)
	return attachments, nil
}

func cleanUpAzVolumeAttachmentByNode(ctx context.Context, azr azReconciler, azDriverNodeName string, mode cleanUpMode) (*v1alpha1.AzVolumeAttachmentList, error) {
	nodeRequirement, err := createLabelRequirements(azureutils.NodeNameLabel, azDriverNodeName)
	if err != nil {
		return nil, err
	}
	labelSelector := labels.NewSelector().Add(*nodeRequirement)

	attachments, err := azr.getAzClient().DiskV1alpha1().AzVolumeAttachments(azr.getNamespace()).List(ctx, metav1.ListOptions{LabelSelector: labelSelector.String()})
	if err != nil {
		if errors.IsNotFound(err) {
			return attachments, nil
		}
		klog.Errorf("failed to get AzVolumeAttachments: %v", err)
		return nil, err
	}

	cleanUps := []v1alpha1.AzVolumeAttachment{}
	for _, attachment := range attachments.Items {
		if shouldCleanUp(attachment, mode) {
			cleanUps = append(cleanUps, attachment)
		}
	}

	if err := cleanUpAzVolumeAttachments(ctx, azr, cleanUps); err != nil {
		return attachments, err
	}
	klog.Infof("successfully requested deletion of AzVolumeAttachments for AzDriverNode (%s)", azDriverNodeName)
	return attachments, nil
}

func cleanUpAzVolumeAttachments(ctx context.Context, azr azReconciler, attachments []v1alpha1.AzVolumeAttachment) error {
	for _, attachment := range attachments {
		patched := attachment.DeepCopy()
		if patched.Annotations == nil {
			patched.Annotations = map[string]string{}
		}
		patched.Annotations[azureutils.CleanUpAnnotation] = "true"
		if err := azr.getClient().Patch(ctx, patched, client.MergeFrom(&attachment)); err != nil {
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", attachment.Name, err)
			return err
		}
		if err := azr.getAzClient().DiskV1alpha1().AzVolumeAttachments(azr.getNamespace()).Delete(ctx, attachment.Name, metav1.DeleteOptions{}); err != nil {
			klog.Errorf("failed to delete AzVolumeAttachment (%s): %v", attachment.Name, err)
			return err
		}
		klog.V(5).Infof("Set deletion timestamp for AzVolumeAttachment (%s)", attachment.Name)
	}
	return nil
}

func shouldCleanUp(attachment v1alpha1.AzVolumeAttachment, mode cleanUpMode) bool {
	return mode == all || (attachment.Spec.RequestedRole == v1alpha1.PrimaryRole && mode == primaryOnly) || (attachment.Spec.RequestedRole == v1alpha1.ReplicaRole && mode == replicaOnly)
}

func isAttached(attachment *v1alpha1.AzVolumeAttachment) bool {
	return attachment != nil && attachment.Status.Detail != nil && attachment.Status.Detail.PublishContext != nil
}

func isCreated(volume *v1alpha1.AzVolume) bool {
	return volume != nil && volume.Status.Detail != nil && volume.Status.Detail.ResponseObject != nil
}

func deletionRequested(objectMeta *metav1.ObjectMeta) bool {
	if objectMeta == nil {
		return false
	}
	return !objectMeta.DeletionTimestamp.IsZero() && objectMeta.DeletionTimestamp.Time.Before(time.Now())
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

func formatUpdateStateError(objectType, fromState string, possibleStates ...string) string {
	length := len(possibleStates)
	if length > 1 {
		possibleStates[length-1] = fmt.Sprintf("or %s", possibleStates[length-1])
	}
	return fmt.Sprintf("%s's state '%s' can only be updated to %s", objectType, fromState, strings.Join(possibleStates, ", "))
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
