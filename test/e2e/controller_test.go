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

package e2e

import (
	"context"
	"fmt"
	"strings"

	"github.com/onsi/ginkgo"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"

	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
)

const (
	partitionKey = "azdrivernodes.disk.csi.azure.com/partition"
)

var _ = ginkgo.Describe("Controller", func() {
	f := framework.NewDefaultFramework("azuredisk")

	var (
		cs           clientset.Interface
		azDiskClient *azDiskClientSet.Clientset
		err          error
	)

	ginkgo.BeforeEach(func() {
		cs = f.ClientSet
		azDiskClient, err = azDiskClientSet.NewForConfig(f.ClientConfig())
		if err != nil {
			ginkgo.Fail(fmt.Sprintf("Failed to create disk client. Error: %v", err))
		}
	})

	ginkgo.Context("AzDriverNode", func() {
		ginkgo.It("Should create AzDriverNode resource and report heartbeat.", func() {
			skipIfUsingInTreeVolumePlugin()
			skipIfNotUsingCSIDriverV2()

			pods, err := cs.CoreV1().Pods("kube-system").List(context.TODO(), metav1.ListOptions{})
			framework.ExpectNoError(err)

			for _, pod := range pods.Items {
				if strings.Contains(pod.Spec.NodeName, "csi-azuredisk-node") {
					azN := azDiskClient.DiskV1alpha1().AzDriverNodes("azure-disk-csi")
					dNode, err := azN.Get(context.Background(), pod.Spec.NodeName, metav1.GetOptions{})
					framework.ExpectNoError(err)
					ginkgo.By("Checking AzDriverNode/Staus")
					if dNode.Status == nil {
						ginkgo.Fail("Driver status is not updated")
					}
					ginkgo.By("Checking to see if node is ReadyForVolumeAllocation")
					if dNode.Status.ReadyForVolumeAllocation == nil || *dNode.Status.ReadyForVolumeAllocation != true {
						ginkgo.Fail("Driver found not ready for allocation")
					}
					ginkgo.By("Checking to see if node reported heartbeat")
					if dNode.Status.LastHeartbeatTime == nil || *dNode.Status.LastHeartbeatTime <= 0 {
						ginkgo.Fail("Driver heartbeat not reported")
					}

					ginkgo.By("Checking to see if node has partition key label.")
					partition, ok := dNode.Labels[partitionKey]
					if ok == false || partition == "" {
						ginkgo.Fail("Driver node parition label was not applied correctly.")
					}
					break
				}
			}
		})
	})
})
