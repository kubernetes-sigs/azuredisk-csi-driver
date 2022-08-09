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
	"strings"
	"sync"
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
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azdiskinformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
	azdiskinformertypes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions/azuredisk/v1beta2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

var (
	kubeconfig = flag.String("kubeconfig", "", "Absolute path to the kubeconfig file. Required only when running out of cluster.")

	azVolumeAttachmentInformer azdiskinformertypes.AzVolumeAttachmentInformer
	azDriverNodeInformer       azdiskinformertypes.AzDriverNodeInformer
	pvcInformer                kubeInformerTypes.PersistentVolumeClaimInformer
	pvInformer                 kubeInformerTypes.PersistentVolumeInformer
	kubeClientset              kubernetes.Interface
	kubeExtensionClientset     azdisk.Interface
	criNamespace               string
	azDiskVolumes              sync.Map
	pvcToPvMap                 sync.Map
	pvToDiskNameMap            sync.Map
)

type azDriverNodesMeta struct {
	nodes []*azdiskv1beta2.AzDriverNode
	err   error
}

type azVolumeAttachmentsMeta struct {
	volumeAttachments []*azdiskv1beta2.AzVolumeAttachment
	err               error
}

func init() {
	flag.StringVar(&criNamespace, "driver-object-namespace", consts.DefaultAzureDiskCrdNamespace, "The namespace where driver related custom resources are created.")
}

func initSchedulerExtender(ctx context.Context) {
	pvcToPvMap = sync.Map{}
	pvToDiskNameMap = sync.Map{}
	azDiskVolumes = sync.Map{}
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

	azurediskInformerFactory := azdiskinformers.NewSharedInformerFactory(kubeExtensionClientset, time.Second*30)
	coreInformerFactory := informers.NewSharedInformerFactory(kubeClientset, time.Second*30)

	azVolumeAttachmentInformer = azurediskInformerFactory.Disk().V1beta2().AzVolumeAttachments()
	azDriverNodeInformer = azurediskInformerFactory.Disk().V1beta2().AzDriverNodes()
	pvcInformer = coreInformerFactory.Core().V1().PersistentVolumeClaims()
	pvInformer = coreInformerFactory.Core().V1().PersistentVolumes()

	pvcInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: onPvcUpdate, // Using Update instead of Add because pvc is not bound on Add
		DeleteFunc: onPvcDelete,
	})

	pvInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: onPvUpdate, // Using Update instead of Add because pvc is not bound on Add
		DeleteFunc: onPvDelete,
	})

	azVolumeAttachmentInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: onAzVolAttAdd,
	})

	klog.V(2).Infof("Starting informers.")
	go azurediskInformerFactory.Start(ctx.Done())
	go coreInformerFactory.Start(ctx.Done())

	if !cache.WaitForCacheSync(
		ctx.Done(),
		azDriverNodeInformer.Informer().HasSynced,
		azVolumeAttachmentInformer.Informer().HasSynced,
		pvcInformer.Informer().HasSynced,
		pvInformer.Informer().HasSynced) {
		klog.Fatalf("Failed to sync and populate the cache for informers %s ...", err)
		os.Exit(1)
	}
}

func filter(context context.Context, schedulerExtenderArgs schedulerapi.ExtenderArgs) (*schedulerapi.ExtenderFilterResult, error) {
	var (
		filteredNodeNames      *[]string
		storeFilteredNodeNames []string
		failedNodes            map[string]string
	)

	startTime := time.Now()
	defer func() {
		FilterNodesDuration.WithLabelValues("Filter").Observe(DurationSince("Filter", startTime))
	}()

	requestedVolumes := schedulerExtenderArgs.Pod.Spec.Volumes
	ns := schedulerExtenderArgs.Pod.Namespace

	// get requested disks created by Azure Disk V2 driver and return early if none are found
	diskRequestedByPod := getQualifyingDisksRequestedByPod(ns, requestedVolumes)
	if len(diskRequestedByPod) == 0 {
		return formatFilterResult(nil, schedulerExtenderArgs.NodeNames, failedNodes, ""), nil
	}

	nodesChan := make(chan azDriverNodesMeta)
	allNodes := schedulerExtenderArgs.NodeNames // all node candidates
	if allNodes == nil || len(*allNodes) == 0 {
		return formatFilterResult(nil, schedulerExtenderArgs.NodeNames, failedNodes, ""), nil
	}

	go getAzDriverNodes(context, nodesChan)
	azDriverNodesMeta := <-nodesChan
	if azDriverNodesMeta.err != nil {
		klog.Warningf("failed to get the list of azDriverNodes: %v. Further filtering is skipped", azDriverNodesMeta.err)
		return formatFilterResult(nil, schedulerExtenderArgs.NodeNames, failedNodes, ""), nil
	}

	// map node name to azDriverNode state
	nodeNameToAzDriverNodeStatusMap := make(map[string]bool)
	for _, azDriverNode := range azDriverNodesMeta.nodes {
		if azDriverNode.Status != nil && azDriverNode.Status.ReadyForVolumeAllocation != nil {
			nodeNameToAzDriverNodeStatusMap[azDriverNode.Spec.NodeName] = *azDriverNode.Status.ReadyForVolumeAllocation
		} else {
			// add entry to map if AzDriverNode exists, even if AzDriverNode is not ready
			nodeNameToAzDriverNodeStatusMap[azDriverNode.Spec.NodeName] = false
		}
	}

	// filter the nodes based on AzDiverNode status and number of disk attachments
	failedNodes = make(map[string]string)
	for _, node := range *allNodes {
		if ready, found := nodeNameToAzDriverNodeStatusMap[node]; found {
			if ready {
				storeFilteredNodeNames = append(storeFilteredNodeNames, node)
			} else {
				failedNodes[node] = fmt.Sprintf("AzDriverNode for %s is not ready.", node)
			}
		} else {
			// if AzDriverNode cannot be found for the node, either 1) AzDriverNode cache is stale or 2) the node plugin has not started and needs to be scheduled.
			// so add the node to filtered node list.
			storeFilteredNodeNames = append(storeFilteredNodeNames, node)
		}
	}
	filteredNodeNames = &storeFilteredNodeNames

	klog.V(2).Infof("request to Filter completed with the following filter and failed node lists. Filtered nodes: %+v. Failed nodes: %+v.", storeFilteredNodeNames, failedNodes)

	return formatFilterResult(nil, filteredNodeNames, failedNodes, ""), nil
}

func prioritize(context context.Context, schedulerExtenderArgs schedulerapi.ExtenderArgs) (priorityList schedulerapi.HostPriorityList, err error) {
	startTime := time.Now()
	defer func() {
		PrioritizeNodesDuration.WithLabelValues("Prioritize").Observe(DurationSince("Prioritize", startTime))
	}()

	requestedVolumes := schedulerExtenderArgs.Pod.Spec.Volumes
	ns := schedulerExtenderArgs.Pod.Namespace
	availableNodes := schedulerExtenderArgs.NodeNames
	if availableNodes == nil || len(*availableNodes) == 0 {
		return
	}

	// get requested disks created by Azure Disk V2 driver and return early if none are found
	azDisksRequestedByPod := getQualifyingDisksRequestedByPod(ns, requestedVolumes, v1.ReadOnlyMany, v1.ReadWriteMany)
	if len(azDisksRequestedByPod) == 0 {
		return setNodeScoresToZero(availableNodes), nil
	}

	nodeNameToNumCreatedAttachmentsForPodMap := make(map[string]int)
	nodeNameToNumAttachedAttachmentsForPodsMap := make(map[string]int)
	nodeNameToNumAllAttachmentsMap := make(map[string]int)
	nodeNameToHeartbeatMap := make(map[string]metav1.Time)
	nodesChan, volumesChan := make(chan azDriverNodesMeta), make(chan azVolumeAttachmentsMeta)

	go getAzDriverNodes(context, nodesChan)
	go getAzVolumeAttachments(context, volumesChan)

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

	// for every volume pod needs, append its azVolumeAttachment name to the node name
	for _, azVolumeAttachment := range azVolumeAttachmentsMeta.volumeAttachments {
		nodeNameToNumAllAttachmentsMap[azVolumeAttachment.Spec.NodeName] += 1

		if !azVolumeAttachment.DeletionTimestamp.IsZero() || azVolumeAttachment.Status.Error != nil {
			klog.V(4).Infof("AzVolumeAttachment excluded because it is to be deleted or its attach operation failed: Name %s, Volume: %s", azVolumeAttachment.Name, azVolumeAttachment.Spec.VolumeName)
			continue
		}

		_, requestedByPod := azDisksRequestedByPod[azVolumeAttachment.Spec.VolumeName]
		if requestedByPod {
			klog.V(4).Infof("Volume attachment is needed: Name: %s, Volume: %s.", azVolumeAttachment.Name, azVolumeAttachment.Spec.VolumeName)
			nodeNameToNumCreatedAttachmentsForPodMap[azVolumeAttachment.Spec.NodeName] += 1
			if azVolumeAttachment.Status.State == azdiskv1beta2.Attached {
				nodeNameToNumAttachedAttachmentsForPodsMap[azVolumeAttachment.Spec.NodeName] += 1
			}
		}
	}

	// score nodes based in how many azVolumeAttchments are appended to its name
	klog.V(4).Infof("scoring nodes for pod %+v.", schedulerExtenderArgs.Pod.Name)
	for _, nodeName := range *availableNodes {
		klog.V(4).Infof("node %+v has %d/%d of requested volumeAttachments created", nodeName, nodeNameToNumCreatedAttachmentsForPodMap[nodeName], len(requestedVolumes))
		score := getNodeScore(nodeNameToNumCreatedAttachmentsForPodMap[nodeName],
			nodeNameToNumAttachedAttachmentsForPodsMap[nodeName],
			nodeNameToNumAllAttachmentsMap[nodeName],
			nodeNameToHeartbeatMap[nodeName],
			nodeName)
		hostPriority := schedulerapi.HostPriority{Host: nodeName, Score: score}
		priorityList = append(priorityList, hostPriority)
	}

	klog.V(2).Infof("request to Prioritize completed with the following priority list: %+v", priorityList)
	return
}

func getKubeConfig() (config *rest.Config, err error) {
	if len(*kubeconfig) != 0 {
		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
	}
	return
}

func getKubernetesExtensionClientsets() (azKubeExtensionClientset azdisk.Interface, err error) {
	// getKubeConfig gets config object from config file
	config, err := getKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get the kubernetes config: %v", err)
	}

	// generate the clientset extension based off of the config
	azKubeExtensionClientset, err = azdisk.NewForConfig(config)
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

func getNodeScore(createdAttachmentsForPodCount, attachedAttachmentsForPodCount, allAttachmentsCount int, latestHeartbeat metav1.Time, nodeName string) int64 {
	now := time.Now()
	latestHeartbeatWas := latestHeartbeat.Time
	latestHeartbeatCanBe := now.Add(-2 * time.Minute)

	if latestHeartbeatWas.Before(latestHeartbeatCanBe) {
		klog.Warningf(
			"node is unresponsive. Latest node heartbeat for %s was: %v. Latest accepted heartbeat is: %s",
			nodeName,
			latestHeartbeatWas.Format(time.UnixDate),
			latestHeartbeatCanBe.Format(time.UnixDate))
		return 0
	}

	// TODO: add nodeMaxCapacity to AzDriver nodes to be used in scoring
	// we prioritze in the following order: 1) number of created requested attachments, 2) number of attached requested attachments, and 3) node capacity
	score := int64(createdAttachmentsForPodCount*100) + int64(attachedAttachmentsForPodCount*10) - int64(allAttachmentsCount-createdAttachmentsForPodCount)
	if score < 0 {
		score = 0
	}
	return score
}

func getAzDriverNodes(context context.Context, out chan azDriverNodesMeta) {
	Goroutines.WithLabelValues("getAzDriverNodes").Inc()
	defer Goroutines.WithLabelValues("getAzDriverNodes").Dec()
	// get all nodes that have azDriverNode running
	var activeDriverNodes azDriverNodesMeta
	activeDriverNodes.nodes, activeDriverNodes.err = azDriverNodeInformer.Lister().AzDriverNodes(criNamespace).List(labels.Everything())
	out <- activeDriverNodes
}

func getAzVolumeAttachments(context context.Context, out chan azVolumeAttachmentsMeta) {
	Goroutines.WithLabelValues("getAzVolumeAttachments").Inc()
	defer Goroutines.WithLabelValues("getAzVolumeAttachments").Dec()
	// get all azVolumeAttachments running in the cluster
	var activeVolumeAttachments azVolumeAttachmentsMeta
	activeVolumeAttachments.volumeAttachments, activeVolumeAttachments.err = azVolumeAttachmentInformer.Lister().AzVolumeAttachments(criNamespace).List(labels.Everything())
	out <- activeVolumeAttachments
}

func getAdditionalAzVolumeAttachmentsOnNode(nodeName string, volumeNames []string) ([]*azdiskv1beta2.AzVolumeAttachment, error) {
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

func formatFilterResult(filteredNodes *v1.NodeList, nodeNames *[]string, failedNodes map[string]string, errorMessage string) *schedulerapi.ExtenderFilterResult {
	return &schedulerapi.ExtenderFilterResult{
		Nodes:       filteredNodes,
		NodeNames:   nodeNames,
		FailedNodes: failedNodes,
		Error:       errorMessage,
	}
}

func setNodeScoresToZero(nodeNames *[]string) (priorityList schedulerapi.HostPriorityList) {
	if nodeNames == nil {
		return
	}

	for _, node := range *nodeNames {
		hostPriority := schedulerapi.HostPriority{Host: node, Score: 0}
		priorityList = append(priorityList, hostPriority)
	}
	return
}

// diskRequestedByPod == total number of v2 disks requested by pod
func getQualifyingDisksRequestedByPod(ns string, requestedVolumes []v1.Volume, filterOutAccessModes ...v1.PersistentVolumeAccessMode) (disksRequestedByPod map[string]struct{}) {
	// create a set of all volumes needed
	disksRequestedByPod = map[string]struct{}{}
	for _, volume := range requestedVolumes {
		if volume.PersistentVolumeClaim != nil {
			if value, ok := pvcToPvMap.Load(getQualifiedName(ns, volume.PersistentVolumeClaim.ClaimName)); ok {
				pvEntry := value.(*pvcToPVEntry)
				hasCorrectAccessMode := true
				for _, filterOutAccessMode := range filterOutAccessModes {
					if strings.EqualFold(pvEntry.AccessMode, string(filterOutAccessMode)) {
						hasCorrectAccessMode = false
					}
				}
				if hasCorrectAccessMode {
					if value, ok := pvToDiskNameMap.Load(pvEntry.VolumeName); ok {
						diskName := value.(string)
						if _, ok := azDiskVolumes.Load(diskName); ok {
							disksRequestedByPod[diskName] = struct{}{}
						}
					}
				}
			}
		}
	}
	return
}

func onPvcUpdate(obj interface{}, newObj interface{}) {
	pvc := newObj.(*v1.PersistentVolumeClaim)
	addPvcEntry(pvc)
}

func addPvcEntry(pvc *v1.PersistentVolumeClaim) {
	qualifiedName := getQualifiedName(pvc.Namespace, pvc.Name)
	klog.V(4).Infof("Updating pvcToPv cache. PVC: %s PV: %s, AccessMode: %v", qualifiedName, pvc.Spec.VolumeName, pvc.Spec.AccessModes)
	cacheValue := pvcToPVEntry{VolumeName: pvc.Spec.VolumeName, AccessMode: fmt.Sprintf("%v", pvc.Spec.AccessModes[0])}
	pvcToPvMap.Store(qualifiedName, &cacheValue)
}

func onPvcDelete(obj interface{}) {
	pvc := obj.(*v1.PersistentVolumeClaim)
	qualifiedName := getQualifiedName(pvc.Namespace, pvc.Name)
	klog.V(4).Infof("Deleting %s from pvcToPv cache entry.", qualifiedName)
	pvcToPvMap.Delete(qualifiedName)
}

func onPvUpdate(obj interface{}, newObj interface{}) {
	pv := newObj.(*v1.PersistentVolume)
	addPvEntry(pv)
}

func addPvEntry(pv *v1.PersistentVolume) {
	// only add v2 disks to the pv map
	if pv.Spec.CSI != nil && pv.Spec.CSI.Driver == *driverName {
		if diskName, err := azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle); err == nil {
			klog.V(4).Infof("Updating pvToVolume cache. PV: %s, DiskName: %v", pv.Name, diskName)
			pvToDiskNameMap.Store(pv.Name, strings.ToLower(diskName))
		}
	}
}

func onPvDelete(obj interface{}) {
	pv := obj.(*v1.PersistentVolume)
	klog.V(4).Infof("Deleting %s from pvToVolume cache.", pv.Name)
	diskName, loaded := pvToDiskNameMap.LoadAndDelete(pv.Name)
	if loaded {
		klog.V(4).Infof("Deleting %s from azDiskVolumes cache.", diskName)
		azDiskVolumes.Delete(diskName)
	}
}

func onAzVolAttAdd(obj interface{}) {
	azVolAtt := obj.(*azdiskv1beta2.AzVolumeAttachment)
	qualifiedName := azVolAtt.Spec.VolumeName
	klog.V(4).Infof("Updating azDiskVolumes cache. DiskName: %s AzVolAtt: %s", qualifiedName, azVolAtt.Name)
	azDiskVolumes.LoadOrStore(qualifiedName, struct{}{})
}

func getQualifiedName(namespace, name string) string {
	return fmt.Sprintf("%s/%s", namespace, name)
}
