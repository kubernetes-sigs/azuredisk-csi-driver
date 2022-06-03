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

package resources

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/test/e2e/framework"
	azdiskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
)

func DeleteTestAzDriverNode(azclient azdisk.Interface, namespace, nodeName string) {
	_ = azclient.DiskV1beta1().AzDriverNodes(namespace).Delete(context.Background(), nodeName, metav1.DeleteOptions{})
}

func NewTestAzDriverNode(azclient azdisk.Interface, namespace, nodeName string) *azdiskv1beta1.AzDriverNode {
	// Delete the leftover azDriverNode from previous runs
	azDriverNodes := azclient.DiskV1beta1().AzDriverNodes(namespace)
	if _, err := azDriverNodes.Get(context.Background(), nodeName, metav1.GetOptions{}); err == nil {
		err := azDriverNodes.Delete(context.Background(), nodeName, metav1.DeleteOptions{})
		framework.ExpectNoError(err)
	}

	newAzDriverNode, err := azDriverNodes.Create(context.Background(), &azdiskv1beta1.AzDriverNode{
		ObjectMeta: metav1.ObjectMeta{
			Name: nodeName,
		},
		Spec: azdiskv1beta1.AzDriverNodeSpec{
			NodeName: nodeName,
		},
	}, metav1.CreateOptions{})
	framework.ExpectNoError(err)

	return newAzDriverNode
}
