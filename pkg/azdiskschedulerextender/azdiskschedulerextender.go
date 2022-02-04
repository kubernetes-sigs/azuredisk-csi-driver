//go:build azurediskv2
// +build azurediskv2

/*
Copyright 2017 The Kubernetes Authors.

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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/informers"
	kubeInformerTypes "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	schedulerapi "k8s.io/kube-scheduler/extender/v1"
	diskv1alpha2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azurediskInformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
	azurediskInformerTypes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions/azuredisk/v1alpha2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

var (
	azVolumeAttachmentInformer azurediskInformerTypes.AzVolumeAttachmentInformer
	azDriverNodeInformer       azurediskInformerTypes.AzDriverNodeInformer
	pvcInformer                kubeInformerTypes.PersistentVolumeClaimInformer
	kubeClientset              *kubernetes.Clientset
	kubeExtensionClientset     versioned.Interface
	criNamespace               string
	pvcToPvMapCache            cachedMapping
)

type azDriverNodesMeta struct {
	nodes []*diskv1alpha2.AzDriverNode
	err   error
}

type azVolumeAttachmentsMeta struct {
	volumes []*diskv1alpha2.AzVolumeAttachment
	err     error
}

func init() {
	flag.StringVar(&criNamespace, "driver-object-namespace", consts.DefaultAzureDiskCrdNamespace, "The namespace where driver related custom resources are created.")
}

func initSchedulerExtender(ctx context.Context) {
	pvcToPvMapCache = cachedMapping{memo: make(map[string]*cacheEntry)}
	var err error
	kubeExtensionClientset, err = getKubernetesExtensionClientsets()
	if err != nil {
		klog.Fatalf("Failed to create kubernetes extension clientset %s ...", err)
		os.Exit(1)
	}

	kubeClientset, err = getKubernetesClientset()
	if err != nil {
		klog.Fatalf("Failed to create kubernetes clientset %s ...", err)
		os.Exit(1)
	}

	RegisterMetrics(metricsList...)

	azurediskInformerFactory := azurediskInformers.NewSharedInformerFactory(kubeExtensionClientset, time.Second*30)
	pvcInformerFactory := informers.NewSharedInformerFactory(kubeClientset, time.Second*30)

	azVolumeAttachmentInformer = azurediskInformerFactory.Disk().V1alpha2().AzVolumeAttachments()
	azDriverNodeInformer = azurediskInformerFactory.Disk().V1alpha2().AzDriverNodes()
	pvcInformer = pvcInformerFactory.Core().V1().PersistentVolumeClaims()

	pvcInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: onPvcUpdate, // Using Update instead of Add because pvc is not bound on Add
		DeleteFunc: onPvcDelete,
	})

	klog.V(2).Infof("Starting informers.")
	go azurediskInformerFactory.Start(ctx.Done())
	go pvcInformerFactory.Start(ctx.Done())

	if !cache.WaitForCacheSync(
		ctx.Done(),
		azDriverNodeInformer.Informer().HasSynced,
		azVolumeAttachmentInformer.Informer().HasSynced,
		pvcInformer.Informer().HasSynced) {
		klog.Fatalf("Failed to sync and populate the cache for informers %s ...", err)
		os.Exit(1)
	}
}

func filter(context context.Context, schedulerExtenderArgs schedulerapi.ExtenderArgs) (*schedulerapi.ExtenderFilterResult, error) {
	var (
		filteredNodes          []v1.Node
		filteredNodeNames      *[]string
		storeFilteredNodeNames []string
		failedNodes            map[string]string
	)

	startTime := time.Now()
	defer func() {
		FilterNodesDuration.WithLabelValues("Filter").Observe(DurationSince("Filter", startTime))
	}()

	requestedVolumes := schedulerExtenderArgs.Pod.Spec.Volumes
	klog.V(2).Infof("pod %s has requested %d volume/s", schedulerExtenderArgs.Pod.Name, len(requestedVolumes))

	// if no volumes are requested, don't filter the nodes
	if len(requestedVolumes) == 0 {
		klog.V(2).Infof("no volumes are requested by pod %s, no further filtering is required", schedulerExtenderArgs.Pod.Name, len(requestedVolumes))
		return formatFilterResult(schedulerExtenderArgs.Nodes.Items, schedulerExtenderArgs.NodeNames, failedNodes, ""), nil
	}

	nodesChan := make(chan azDriverNodesMeta)
	// all node candidates
	allNodes := schedulerExtenderArgs.Nodes.Items

	go getAzDriverNodes(context, nodesChan)
	azDriverNodesMeta := <-nodesChan
	if azDriverNodesMeta.err != nil {
		klog.Warningf("failed to get the list of azDriverNodes: %v. Further filtering is skipped", azDriverNodesMeta.err)
		return formatFilterResult(schedulerExtenderArgs.Nodes.Items, schedulerExtenderArgs.NodeNames, failedNodes, ""), nil
	}

	// map node name to azDriverNode state
	nodeNameToAzDriverNodeStatusMap := make(map[string]bool)
	for _, azDriverNode := range azDriverNodesMeta.nodes {
		if azDriverNode.Status != nil && azDriverNode.Status.ReadyForVolumeAllocation != nil {
			nodeNameToAzDriverNodeStatusMap[azDriverNode.Spec.NodeName] = *azDriverNode.Status.ReadyForVolumeAllocation
		}
	}

	// get PV names for requested PVCs
	var pvNamesForRequestedVolumes []string
	numberOfRequestedAttachments := 0
	for _, volume := range requestedVolumes {
		if volume.PersistentVolumeClaim != nil {
			// not the same as len(pvNamesForRequestedVolumes). PV-s for a new pod might not be cached yet.
			numberOfRequestedAttachments++
			if entry, ok := pvcToPvMapCache.Get(volume.PersistentVolumeClaim.ClaimName); ok {
				klog.V(2).Infof(
					"pvc(%s) to pv(%s) mapping for pod %s exists.",
					volume.PersistentVolumeClaim.ClaimName,
					entry.VolumeName,
					schedulerExtenderArgs.Pod.Name,
				)
				pvNamesForRequestedVolumes = append(pvNamesForRequestedVolumes, entry.VolumeName)
			}
		}
	}

	if numberOfRequestedAttachments == 0 {
		return formatFilterResult(schedulerExtenderArgs.Nodes.Items, schedulerExtenderArgs.NodeNames, failedNodes, ""), nil
	}

	// filter the nodes based on AzDiverNode status and number of disk attachments
	failedNodes = make(map[string]string)
	for _, node := range allNodes {
		ready, found := nodeNameToAzDriverNodeStatusMap[node.Name]
		excludeNode := false
		if found && ready {
			// get AzVolumeAttachments for the node
			azVolumeAttachments, err := getAdditionalAzVolumeAttachmentsOnNode(node.Name, pvNamesForRequestedVolumes)
			klog.V(2).Infof(
				"number of additional volume attachments for node %s and pod %s is %d. Number of requested volumes is %d. Err: %v",
				node.Name, schedulerExtenderArgs.Pod.Name, len(azVolumeAttachments), numberOfRequestedAttachments, err)
			if err == nil {
				// get the max disk count for the node type
				maxDiskCount, err := azureutils.GetNodeMaxDiskCount(node.Labels)
				if err == nil && len(azVolumeAttachments)+numberOfRequestedAttachments > maxDiskCount {
					klog.Warningf("max number of azVolumeAttachments for node %s has been reached. Node will be skipped.", node.Name)
					excludeNode = true
					failedNodes[node.Name] = "node(s) reached the maximum number of disk attachments"
				}
			}
			if !excludeNode {
				filteredNodes = append(filteredNodes, node)
				storeFilteredNodeNames = append(storeFilteredNodeNames, node.Name)
			}
		} else {
			failedNodes[node.Name] = fmt.Sprintf("AzDriverNode for %s is not ready.", node.Name)
		}
	}
	filteredNodeNames = &storeFilteredNodeNames

	klog.V(2).Infof("request to Filter completed with the following filter and failed node lists. Filtered nodes: %+v. Failed nodes: %+v.", storeFilteredNodeNames, failedNodes)

	return formatFilterResult(filteredNodes, filteredNodeNames, failedNodes, ""), nil
}

func prioritize(context context.Context, schedulerExtenderArgs schedulerapi.ExtenderArgs) (priorityList schedulerapi.HostPriorityList, err error) {
	startTime := time.Now()
	defer func() {
		PrioritizeNodesDuration.WithLabelValues("Prioritize").Observe(DurationSince("Prioritize", startTime))
	}()
	availableNodes := schedulerExtenderArgs.Nodes.Items
	requestedVolumes := schedulerExtenderArgs.Pod.Spec.Volumes

	// if no volumes are requested, return assigning 0 score to all nodes
	if len(requestedVolumes) == 0 {
		priorityList = setNodeScoresToZero(availableNodes)
	} else {
		volumesPodNeeds := make(map[string]struct{})
		nodeNameToRequestedVolumeMap := make(map[string][]string)
		nodeNameToVolumeMap := make(map[string][]string)
		nodeNameToHeartbeatMap := make(map[string]metav1.Time)
		nodesChan, volumesChan := make(chan azDriverNodesMeta), make(chan azVolumeAttachmentsMeta)

		go getAzDriverNodes(context, nodesChan)
		go getAzVolumeAttachments(context, volumesChan)

		// create a lookup map of all the volumes the pod needs
		for _, volume := range requestedVolumes {
			if volume.PersistentVolumeClaim != nil {
				if entry, ok := pvcToPvMapCache.Get(volume.PersistentVolumeClaim.ClaimName); ok &&
					!strings.EqualFold(entry.AccessMode, string(v1.ReadWriteMany)) &&
					!strings.EqualFold(entry.AccessMode, string(v1.ReadOnlyMany)) {
					klog.V(2).Infof(
						"pvc(%s) to pv(%s) mapping for pod %s exists.",
						volume.PersistentVolumeClaim.ClaimName,
						entry.VolumeName,
						schedulerExtenderArgs.Pod.Name,
					)
					volumesPodNeeds[entry.VolumeName] = struct{}{}
				}
			}
		}

		// get all nodes that have azDriverNode running
		azDriverNodesMeta := <-nodesChan
		if azDriverNodesMeta.err != nil {
			priorityList = setNodeScoresToZero(availableNodes)
			klog.Warningf("failed to get the list of azDriverNodes: %v", azDriverNodesMeta.err)
			return
		}

		// map azDriverNode name to its heartbeat
		for _, azDriverNode := range azDriverNodesMeta.nodes {
			if azDriverNode.Status != nil {
				nodeNameToHeartbeatMap[azDriverNode.Spec.NodeName] = *azDriverNode.Status.LastHeartbeatTime
			}
		}

		// get all azVolumeAttachments running in the cluster
		azVolumeAttachmentsMeta := <-volumesChan
		if azVolumeAttachmentsMeta.err != nil {
			priorityList = setNodeScoresToZero(availableNodes)
			klog.Warningf("failed to get the list of azVolumeAttachments: %v", azVolumeAttachmentsMeta.err)
			return
		}

		// for every volume the pod needs, append its azVolumeAttachment name to the node name
		for _, attachedVolume := range azVolumeAttachmentsMeta.volumes {
			klog.V(2).Infof(
				"volume attachment in consideration: Name: %s, Volume: %s.",
				attachedVolume.Name,
				attachedVolume.Spec.VolumeName,
			)

			nodeNameToVolumeMap[attachedVolume.Spec.NodeName] = append(nodeNameToVolumeMap[attachedVolume.Spec.NodeName], attachedVolume.Spec.VolumeName)

			if !attachedVolume.DeletionTimestamp.IsZero() || attachedVolume.Status.State != diskv1alpha2.Attached {
				klog.V(2).Infof("volume attachment excluded because it is to be deleted or not in the Attached state: Name %s, Volume: %s", attachedVolume.Name, attachedVolume.Spec.VolumeName)
				continue
			}

			_, requestedByPod := volumesPodNeeds[attachedVolume.Spec.VolumeName]
			if requestedByPod {
				klog.V(2).Infof("volume attachment is needed: Name: %s, Volume: %s.", attachedVolume.Name, attachedVolume.Spec.VolumeName)
				nodeNameToRequestedVolumeMap[attachedVolume.Spec.NodeName] = append(nodeNameToRequestedVolumeMap[attachedVolume.Spec.NodeName], attachedVolume.Spec.VolumeName)
			}
		}

		// score nodes based in how many azVolumeAttchments are appended to its name
		klog.V(2).Infof("scoring nodes for pod %+v.", schedulerExtenderArgs.Pod.Name)
		for _, node := range availableNodes {
			klog.V(2).Infof("node %+v has %d/%d of requested volumes volumes", node.Name, len(nodeNameToRequestedVolumeMap[node.Name]), len(requestedVolumes))
			score := getNodeScore(len(nodeNameToRequestedVolumeMap[node.Name]),
				nodeNameToHeartbeatMap[node.Name],
				len(availableNodes),
				len(nodeNameToVolumeMap[node.Name]),
				node.Name)
			hostPriority := schedulerapi.HostPriority{Host: node.Name, Score: score}
			priorityList = append(priorityList, hostPriority)
		}
	}
	klog.V(2).Infof("request to Prioritize completed with the following priority list: %+v", priorityList)
	return
}

func getKubeConfig() (config *rest.Config, err error) {
	config, err = rest.InClusterConfig()
	if err != nil {
		klog.Warning("failed getting the in cluster config: %v", err)
		// fallback to kubeconfig
		kubeConfigPath := os.Getenv("KUBECONFIG")
		if len(kubeConfigPath) == 0 {
			kubeConfigPath = filepath.Join(os.Getenv("HOME"), ".kube", "config")
		}

		// create the config from the path
		config, err = clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	}
	return
}

func getKubernetesExtensionClientsets() (azKubeExtensionClientset versioned.Interface, err error) {
	// getKubeConfig gets config object from config file
	config, err := getKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get the kubernetes config: %v", err)
	}

	// generate the clientset extension based off of the config
	azKubeExtensionClientset, err = versioned.NewForConfig(config)
	if err != nil {
		return azKubeExtensionClientset, fmt.Errorf("cannot create the clientset: %v", err)
	}

	klog.V(2).Info("successfully constructed kubernetes client and extension clientset")
	return azKubeExtensionClientset, nil
}

func getKubernetesClientset() (*kubernetes.Clientset, error) {
	// getKubeConfig gets config object from config file
	config, err := getKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get the kubernetes config: %v", err)
	}

	// generate the clientset extension based off of the config
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("cannot create the kubernetes client: %v", err)
	}
	return kubeClient, nil
}

func getNodeScore(volumeAttachments int, latestHeartbeat metav1.Time, availableNodeCount int, totalnumberOfVolumeAttachments int, nodeName string) int64 {
	now := time.Now()
	latestHeartbeatWas := latestHeartbeat.Time
	latestHeartbeatCanBe := now.Add(-2 * time.Minute)

	if latestHeartbeatWas.Before(latestHeartbeatCanBe) {
		klog.Warningf(
			"node is unresponsive. Latest node heartbeat for %v was: %v. Latest accepted heartbeat is: %s",
			nodeName,
			latestHeartbeatWas.Format(time.UnixDate),
			latestHeartbeatCanBe.Format(time.UnixDate))
		return 0
	}

	score := int64(volumeAttachments*100) - int64((totalnumberOfVolumeAttachments-volumeAttachments)*10)
	return score
}

func getAzDriverNodes(context context.Context, out chan azDriverNodesMeta) {
	Goroutines.WithLabelValues("getAzDriverNodes").Inc()
	defer Goroutines.WithLabelValues("getAzDriverNodes").Dec()
	// get all nodes that have azDriverNode running
	var activeDriverNodes azDriverNodesMeta
	activeDriverNodes.nodes, activeDriverNodes.err = azDriverNodeInformer.Lister().AzDriverNodes(criNamespace).List(labels.Everything())
	klog.V(2).Infof("AzDriverNodes: %d", len(activeDriverNodes.nodes))
	out <- activeDriverNodes
}

func getAzVolumeAttachments(context context.Context, out chan azVolumeAttachmentsMeta) {
	Goroutines.WithLabelValues("getAzVolumeAttachments").Inc()
	defer Goroutines.WithLabelValues("getAzVolumeAttachments").Dec()
	// get all azVolumeAttachments running in the cluster
	var activeVolumeAttachments azVolumeAttachmentsMeta
	activeVolumeAttachments.volumes, activeVolumeAttachments.err = azVolumeAttachmentInformer.Lister().AzVolumeAttachments(criNamespace).List(labels.Everything())
	klog.V(2).Infof("AzVolumeAttachments: %d", len(activeVolumeAttachments.volumes))
	out <- activeVolumeAttachments
}

func getAdditionalAzVolumeAttachmentsOnNode(nodeName string, volumeNames []string) ([]*diskv1alpha2.AzVolumeAttachment, error) {
	nodeNameFilter, err := azureutils.CreateLabelRequirements(consts.NodeNameLabel, selection.Equals, nodeName)
	if err != nil {
		return nil, fmt.Errorf("failed to create a filter label for listing AzVolumeAttachments for node %s: %v", nodeName, err)
	}
	labelSelector := labels.NewSelector().Add(*nodeNameFilter)
	if len(volumeNames) > 0 {
		volumeFilter, err := azureutils.CreateLabelRequirements(consts.VolumeNameLabel, selection.NotIn, volumeNames...)
		if err != nil {
			return nil, fmt.Errorf("failed to create a filter label for filtering out replica AzVolumeAttachments for volumes %v: %v", volumeNames, err)
		}
		labelSelector = labelSelector.Add(*volumeFilter)
	}

	attachments, err := azVolumeAttachmentInformer.Lister().AzVolumeAttachments(criNamespace).List(labelSelector)
	if err != nil {
		return nil, fmt.Errorf("failed listing AzVolumeAttachments for node %s: %v", nodeName, err)
	}

	return attachments, nil
}

func formatFilterResult(filteredNodes []v1.Node, nodeNames *[]string, failedNodes map[string]string, errorMessage string) *schedulerapi.ExtenderFilterResult {
	return &schedulerapi.ExtenderFilterResult{
		Nodes: &v1.NodeList{
			Items: filteredNodes,
		},
		NodeNames:   nodeNames,
		FailedNodes: failedNodes,
		Error:       errorMessage,
	}
}

func setNodeScoresToZero(nodes []v1.Node) (priorityList schedulerapi.HostPriorityList) {
	for _, node := range nodes {
		hostPriority := schedulerapi.HostPriority{Host: node.Name, Score: 0}
		priorityList = append(priorityList, hostPriority)
	}
	return
}

func onPvcUpdate(obj interface{}, newObj interface{}) {
	// Cast the obj to pvc
	pvc := newObj.(*v1.PersistentVolumeClaim)
	klog.V(4).Infof("updating pvcToPv mapping cache. PVC: %s PV: %s, AccessMode: %v", pvc.Name, pvc.Spec.VolumeName, pvc.Spec.AccessModes)
	cacheValue := cacheEntry{VolumeName: pvc.Spec.VolumeName, AccessMode: fmt.Sprintf("%v", pvc.Spec.AccessModes[0])}
	pvcToPvMapCache.AddOrUpdate(pvc.Name, &cacheValue)
}

func onPvcDelete(obj interface{}) {
	// Cast the obj to pvc
	pvc := obj.(*v1.PersistentVolumeClaim)
	klog.V(4).Infof("deleting from pvcToPv mapping cache. PVC: %s PV: %s", pvc.Name, pvc.Spec.VolumeName)
	pvcToPvMapCache.Delete(pvc.Name)
}
