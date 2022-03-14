/*
Copyright 2019 The Kubernetes Authors.

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

package nodeutil

import (
	"context"
	"fmt"

	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
)

func ListNodeNames(c clientset.Interface) []string {
	var nodeNames []string
	nodes, err := c.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	framework.ExpectNoError(err)
	for _, item := range nodes.Items {
		nodeNames = append(nodeNames, item.Name)
	}
	return nodeNames
}

func MakeNodeSchedulable(c clientset.Interface, node *v1.Node, isMultiZone bool) (newNode *v1.Node, cleanup func()) {
	topologyValue := ""
	if isMultiZone {
		region := node.Labels[consts.TopologyRegionKey]
		zone := node.Labels[consts.WellKnownTopologyKey]
		topologyValue = fmt.Sprintf("%s-%s", region, zone)
	}
	var roleLabelCleanup func()
	node, roleLabelCleanup, err := SetNodeLabels(c, node, map[string]string{testconsts.TopologyKey: topologyValue})
	framework.ExpectNoError(err)

	csiNodeCleanup, err := SetCSINodeDriver(c, node.Name)
	framework.ExpectNoError(err)

	cleanup = func() {
		// annotationCleanup()
		roleLabelCleanup()
		csiNodeCleanup()
	}
	return
}

func UpdateNodeWithRetry(c clientset.Interface, nodeName string, updateFunc func(*v1.Node)) (nodeObj *v1.Node, err error) {
	ctx, cancelFunc := context.WithTimeout(context.Background(), testconsts.PollTimeout)
	defer cancelFunc()

	err = wait.PollImmediateUntil(testconsts.Poll, func() (bool, error) {
		nodeObj, err = c.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		updateFunc(nodeObj)

		nodeObj, err = c.CoreV1().Nodes().Update(ctx, nodeObj, metav1.UpdateOptions{})
		if errors.IsConflict(err) {
			return false, nil
		}
		return true, err
	}, ctx.Done())

	return
}

func SetNodeLabels(c clientset.Interface, node *v1.Node, newLabels map[string]string) (newNode *v1.Node, cleanup func(), err error) {
	originalLabels := node.Labels
	nodeName := node.Name
	cleanup = func() {
		updateFunc := func(nodeObj *v1.Node) {
			nodeObj.Labels = originalLabels
		}
		_, cleanUpErr := UpdateNodeWithRetry(c, nodeName, updateFunc)
		framework.ExpectNoError(cleanUpErr)
	}

	updateFunc := func(nodeObj *v1.Node) {
		if nodeObj.Labels == nil {
			nodeObj.Labels = newLabels
		} else {
			for key, value := range newLabels {
				nodeObj.Labels[key] = value
			}
		}
	}

	newNode, err = UpdateNodeWithRetry(c, node.Name, updateFunc)
	return
}

func SetNodeTaints(c clientset.Interface, node *v1.Node, taints ...v1.Taint) (newNode *v1.Node, cleanup func(), err error) {
	originalTaints := node.Spec.Taints
	nodeName := node.Name
	cleanup = func() {
		updateFunc := func(nodeObj *v1.Node) {
			node.Spec.Taints = originalTaints
		}
		_, cleanUpErr := UpdateNodeWithRetry(c, nodeName, updateFunc)
		framework.ExpectNoError(cleanUpErr)
	}

	updateFunc := func(nodeObj *v1.Node) {
		if node.Spec.Taints == nil {
			node.Spec.Taints = taints
		} else {
			// acknowledge that this can lead to duplicate taints
			node.Spec.Taints = append(node.Spec.Taints, taints...)
		}
	}

	newNode, err = UpdateNodeWithRetry(c, node.Name, updateFunc)
	return
}

func AnnotateNode(c clientset.Interface, node *v1.Node, newAnnotations map[string]string) (newNode *v1.Node, cleanup func(), err error) {
	originalAnnotations := node.Annotations
	nodeName := node.Name
	cleanup = func() {
		node, err := c.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		framework.ExpectNoError(err)
		node.Annotations = originalAnnotations
		_, _ = c.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
	}

	if node.Annotations == nil {
		node.Annotations = newAnnotations
	} else {
		for key, value := range newAnnotations {
			node.Annotations[key] = value
		}
	}

	newNode, err = c.CoreV1().Nodes().Update(context.Background(), node, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("failed to add annotations (%+v) to node (%s): %v", newAnnotations, node.Name, err)
	} else {
		klog.Infof("successfully added annotations (%+v) to node (%s)", newAnnotations, node.Name)
	}
	return
}

func SetCSINodeDriver(c clientset.Interface, nodeName string) (cleanup func(), err error) {
	csiNode, err := c.StorageV1().CSINodes().Get(context.Background(), nodeName, metav1.GetOptions{})
	framework.ExpectNoError(err)
	originalSpec := csiNode.Spec
	cleanup = func() {
		csiNode, err := c.StorageV1().CSINodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		framework.ExpectNoError(err)
		csiNode.Spec = originalSpec
		_, _ = c.StorageV1().CSINodes().Update(context.Background(), csiNode, metav1.UpdateOptions{})
	}

	if csiNode.Spec.Drivers == nil || len(csiNode.Spec.Drivers) == 0 {
		csiNode.Spec.Drivers = []storagev1.CSINodeDriver{
			{
				Name:         "secrets-store.csi.k8s.io",
				NodeID:       nodeName,
				TopologyKeys: nil,
			},
		}
	}

	_, err = c.StorageV1().CSINodes().Update(context.Background(), csiNode, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("failed to add driver spec to csi node (%s): %v", nodeName, err)
	}
	return
}

func ListAzDriverNodeNames(azDiskClient azDiskClientSet.Interface) []string {
	var nodeNames []string
	nodes, err := azDiskClient.DiskV1beta1().AzDriverNodes(azureconstants.DefaultAzureDiskCrdNamespace).List(context.TODO(), metav1.ListOptions{})
	framework.ExpectNoError(err)
	for _, item := range nodes.Items {
		nodeNames = append(nodeNames, item.Name)
	}
	return nodeNames
}
