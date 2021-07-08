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

package azdiskschedulerextender

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	kubeInformerTypes "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	schedulerapi "k8s.io/kube-scheduler/extender/v1"
	v1alpha1Meta "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azurediskInformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
	azurediskInformerTypes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions/azuredisk/v1alpha1"
)

var (
	azVolumeAttachmentInformer azurediskInformerTypes.AzVolumeAttachmentInformer
	azDriverNodeInformer       azurediskInformerTypes.AzDriverNodeInformer
	pvcInformer                kubeInformerTypes.PersistentVolumeClaimInformer
	kubeClientset              *kubernetes.Clientset
	kubeExtensionClientset     versioned.Interface
	ns                         = "azure-disk-csi"
	pvcToPvMapCache            cachedMapping
)

type azDriverNodesMeta struct {
	nodes []*v1alpha1Meta.AzDriverNode
	err   error
}

type azVolumeAttachmentsMeta struct {
	volumes []*v1alpha1Meta.AzVolumeAttachment
	err     error
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

	azVolumeAttachmentInformer = azurediskInformerFactory.Disk().V1alpha1().AzVolumeAttachments()
	azDriverNodeInformer = azurediskInformerFactory.Disk().V1alpha1().AzDriverNodes()
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
	klog.V(2).Infof("Pod %s has requested %d volume/s.", schedulerExtenderArgs.Pod.Name, len(requestedVolumes))

	// if no volumes are requested, return assigning 0 score to all nodes
	if len(requestedVolumes) != 0 {
		nodesChan := make(chan azDriverNodesMeta)
		// all available cluster nodes
		allNodes := schedulerExtenderArgs.Nodes.Items

		go getAzDriverNodes(context, nodesChan)
		azDriverNodesMeta := <-nodesChan
		if azDriverNodesMeta.err != nil {
			filteredNodes = schedulerExtenderArgs.Nodes.Items
			filteredNodeNames = schedulerExtenderArgs.NodeNames
			klog.Warningf("Failed to get the list of azDriverNodes: %v", azDriverNodesMeta.err)

			return formatFilterResult(filteredNodes, filteredNodeNames, failedNodes, ""), nil
		}

		// map node name to azDriverNode state
		nodeNameToStatusMap := make(map[string]bool)
		for _, azDriverNode := range azDriverNodesMeta.nodes {
			if azDriverNode.Status != nil {
				nodeNameToStatusMap[azDriverNode.Spec.NodeName] = *azDriverNode.Status.ReadyForVolumeAllocation
			}
		}

		// Filter the nodes based on AzDiverNode status
		failedNodes = make(map[string]string)
		for _, node := range allNodes {
			ready, ok := nodeNameToStatusMap[node.Name]
			if ok && ready {
				filteredNodes = append(filteredNodes, node)
				storeFilteredNodeNames = append(storeFilteredNodeNames, node.Name)
			} else {
				failedNodes[node.Name] = fmt.Sprintf("AzDriverNode for %s is not ready.", node.Name)
			}
		}
		filteredNodeNames = &storeFilteredNodeNames
	}

	klog.V(2).Infof("Request to Filter completed with the following filter and failed node lists. Filtered nodes: %+v. Failed nodes: %+v.", storeFilteredNodeNames, failedNodes)

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
		nodeNameToVolumeMap := make(map[string][]string)
		completeNodeNameToVolumeMap := make(map[string][]string)
		nodeNameToHeartbeatMap := make(map[string]int64)
		nodesChan, volumesChan := make(chan azDriverNodesMeta), make(chan azVolumeAttachmentsMeta)

		go getAzDriverNodes(context, nodesChan)
		go getAzVolumeAttachments(context, volumesChan)

		// create a lookup map of all the volumes the pod needs
		for _, volume := range requestedVolumes {
			klog.V(2).Infof(
				"Volume pod %s needs: %s. PVC: %v. PVC source: %v.",
				schedulerExtenderArgs.Pod.Name,
				volume.Name,
				volume.PersistentVolumeClaim,
				volume.VolumeSource.PersistentVolumeClaim,
			)
			if volume.PersistentVolumeClaim != nil {
				if entry, ok := pvcToPvMapCache.Get(volume.PersistentVolumeClaim.ClaimName); ok && entry.AccessMode != fmt.Sprintf("%v", v1.ReadWriteMany) {
					klog.V(2).Infof(
						"Mapping volume %s for pod %s. Claim: %v",
						volume.Name,
						schedulerExtenderArgs.Pod.Name,
						volume.PersistentVolumeClaim,
					)
					volumesPodNeeds[entry.VolumeName] = struct{}{}
				}
			}
		}

		// get all nodes that have azDriverNode running
		azDriverNodesMeta := <-nodesChan
		if azDriverNodesMeta.err != nil {
			priorityList = setNodeScoresToZero(availableNodes)
			klog.Warningf("Failed to get the list of azDriverNodes: %v", azDriverNodesMeta.err)
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
			klog.Warningf("Failed to get the list of azVolumeAttachments: %v", azVolumeAttachmentsMeta.err)
			return
		}

		// for every volume the pod needs, append its azVolumeAttachment name to the node name
		for _, attachedVolume := range azVolumeAttachmentsMeta.volumes {
			klog.V(2).Infof(
				"Volume attachment in consideration: Name: %s, Volume: %s. AzVolAtt: %s.",
				attachedVolume.Name,
				attachedVolume.Spec.UnderlyingVolume,
				attachedVolume.Spec.VolumeContext["csi.storage.k8s.io/pvc/name"],
			)
			completeNodeNameToVolumeMap[attachedVolume.Spec.NodeName] = append(completeNodeNameToVolumeMap[attachedVolume.Spec.NodeName], attachedVolume.Spec.UnderlyingVolume)
			_, needs := volumesPodNeeds[attachedVolume.Spec.UnderlyingVolume]
			if needs {
				klog.V(2).Infof("Volume attachment is needed: Name: %s, Volume: %s.", attachedVolume.Name, attachedVolume.Spec.UnderlyingVolume)
				nodeNameToVolumeMap[attachedVolume.Spec.NodeName] = append(nodeNameToVolumeMap[attachedVolume.Spec.NodeName], attachedVolume.Spec.UnderlyingVolume)
			}
		}

		// score nodes based in how many azVolumeAttchments are appended to its name
		klog.V(2).Infof("Scoring nodes for pod %+v.", schedulerExtenderArgs.Pod.Name)
		for _, node := range availableNodes {
			klog.V(2).Infof("Node %+v has %d/%d of requested volumes volumes", node.Name, len(nodeNameToVolumeMap[node.Name]), len(requestedVolumes))
			score := getNodeScore(len(nodeNameToVolumeMap[node.Name]),
				nodeNameToHeartbeatMap[node.Name],
				len(availableNodes),
				len(completeNodeNameToVolumeMap[node.Name]),
				node.Name)
			hostPriority := schedulerapi.HostPriority{Host: node.Name, Score: score}
			priorityList = append(priorityList, hostPriority)
		}
	}
	klog.V(2).Infof("Request to Prioritize completed with the following priority list: %+v", priorityList)
	return
}

func getKubeConfig() (config *rest.Config, err error) {
	config, err = rest.InClusterConfig()
	if err != nil {
		klog.Warning("Failed getting the in cluster config: %v", err)
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
		return nil, fmt.Errorf("Failed to get the kubernetes config: %v", err)
	}

	// generate the clientset extension based off of the config
	azKubeExtensionClientset, err = versioned.NewForConfig(config)
	if err != nil {
		return azKubeExtensionClientset, fmt.Errorf("Cannot create the clientset: %v", err)
	}

	klog.Info("Successfully constructed kubernetes client and extension clientset")
	return azKubeExtensionClientset, nil
}

func getKubernetesClientset() (*kubernetes.Clientset, error) {
	// getKubeConfig gets config object from config file
	config, err := getKubeConfig()
	if err != nil {
		return nil, fmt.Errorf("Failed to get the kubernetes config: %v", err)
	}

	// generate the clientset extension based off of the config
	kubeClient, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("Cannot create the kubernetes client: %v", err)
	}
	return kubeClient, nil
}

func getNodeScore(volumeAttachments int, heartbeat int64, availableNodeCount int, totalnumberOfVolumeAttachments int, nodeName string) int64 {
	now := time.Now()
	latestHeartbeatWas := time.Unix(0, heartbeat)
	latestHeartbeatCanBe := now.Add(-2 * time.Minute)

	if latestHeartbeatWas.Before(latestHeartbeatCanBe) {
		klog.Warningf(
			"Node is unresponsive. Latest node heartbeat for %v was: %v. Latest accepted heartbeat could be: %s",
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
	activeDriverNodes.nodes, activeDriverNodes.err = azDriverNodeInformer.Lister().AzDriverNodes(ns).List(labels.Everything())
	klog.Info("AzDriverNodes: %v", activeDriverNodes.nodes)
	out <- activeDriverNodes
}

func getAzVolumeAttachments(context context.Context, out chan azVolumeAttachmentsMeta) {
	Goroutines.WithLabelValues("getAzVolumeAttachments").Inc()
	defer Goroutines.WithLabelValues("getAzVolumeAttachments").Dec()
	// get all azVolumeAttachments running in the cluster
	var activeVolumeAttachments azVolumeAttachmentsMeta
	activeVolumeAttachments.volumes, activeVolumeAttachments.err = azVolumeAttachmentInformer.Lister().AzVolumeAttachments(ns).List(labels.Everything())
	klog.Info("AzVolumeAttachments: %v", len(activeVolumeAttachments.volumes))
	out <- activeVolumeAttachments
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
	klog.Info("Updating pvcToPv map cache.PVC: %s PV: %s, AccessMode: %v", pvc.Name, pvc.Spec.VolumeName, pvc.Spec.AccessModes)
	cacheValue := cacheEntry{VolumeName: pvc.Spec.VolumeName, AccessMode: fmt.Sprintf("%v", pvc.Spec.AccessModes[0])}
	pvcToPvMapCache.AddOrUpdate(pvc.Name, &cacheValue)
}

func onPvcDelete(obj interface{}) {
	// Cast the obj to pvc
	pvc := obj.(*v1.PersistentVolumeClaim)
	klog.Info("Deleting pvcToPv map cache entry. PVC: %s PV: %s", pvc.Name, pvc.Spec.VolumeName)
	pvcToPvMapCache.Delete(pvc.Name)
}
