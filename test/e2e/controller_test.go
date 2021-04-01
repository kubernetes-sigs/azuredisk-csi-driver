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
	"time"

	"github.com/onsi/ginkgo"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/testsuites"
)

const (
	partitionKey = "azdrivernodes.disk.csi.azure.com/partition"
	namespace    = "azure-disk-csi"
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

	ginkgo.Context("AzVolumeAttachment", func() {
		ginkgo.It("Should initialize AzVolumeAttachment object's status and append finalizer and create labels", func() {
			skipIfUsingInTreeVolumePlugin()
			skipIfNotUsingCSIDriverV2()
			testAzAtt := testsuites.NewTestAzVolumeAttachment(azDiskClient.DiskV1alpha1(), namespace, "test-volume", "test-node", nil, 0)
			att := testAzAtt.Create()

			err = testsuites.WaitForAttach(azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace), att.Namespace, att.Name, time.Duration(5)*time.Minute)
			framework.ExpectNoError(err)

			err = testsuites.WaitForFinalizer(azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace), att.Namespace, att.Name, time.Duration(5)*time.Minute)
			framework.ExpectNoError(err)

			err = testsuites.WaitForLabels(azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace), att.Namespace, att.Name, time.Duration(5)*time.Minute)
			framework.ExpectNoError(err)

			testAzAtt.Cleanup()
		})

		ginkgo.It("Should delete AzVolumeAttachment object properly", func() {
			skipIfUsingInTreeVolumePlugin()
			skipIfNotUsingCSIDriverV2()
			testAzAtt := testsuites.NewTestAzVolumeAttachment(azDiskClient.DiskV1alpha1(), namespace, "test-volume", "test-node", nil, 0)
			att := testAzAtt.Create()

			err = testsuites.WaitForFinalizer(azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace), att.Namespace, att.Name, time.Duration(5)*time.Minute)
			framework.ExpectNoError(err)

			err = azDiskClient.DiskV1alpha1().AzDriverNodes(namespace).Delete(context.Background(), "test-node", metav1.DeleteOptions{})
			framework.ExpectNoError(err)

			err = azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace).Delete(context.Background(), att.Name, metav1.DeleteOptions{})
			framework.ExpectNoError(err)

			err = testsuites.WaitForDelete(azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace), att.Namespace, att.Name, time.Duration(5)*time.Minute)
			framework.ExpectNoError(err)

			testAzAtt.Cleanup()
		})

		ginkgo.It("Should create replica azVolumeAttachment object when maxShares > 1", func() {
			skipIfUsingInTreeVolumePlugin()
			skipIfNotUsingCSIDriverV2()
			testAzAtt := testsuites.NewTestAzVolumeAttachment(azDiskClient.DiskV1alpha1(), namespace, "test-volume", "test-node", []string{"test-node-2"}, 1)
			att := testAzAtt.Create()

			// check if the second attachment object was created and marked attached.
			err = testsuites.WaitForReplicas(azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace), att.Namespace, "test-volume", 1, time.Duration(5)*time.Minute)
			framework.ExpectNoError(err)

			// clean-up
			testAzAtt.Cleanup()
		})

		ginkgo.It("If failover happens, should turn replica to primary and create an additional replica for replacment", func() {
			skipIfUsingInTreeVolumePlugin()
			skipIfNotUsingCSIDriverV2()
			volName := "test-volume"
			testAzAtt := testsuites.NewTestAzVolumeAttachment(azDiskClient.DiskV1alpha1(), namespace, volName, "test-node", []string{"test-node-2", "test-node-3"}, 1)
			att := testAzAtt.Create()
			err := testsuites.WaitForReplicas(azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace), att.Namespace, volName, 1, time.Duration(5)*time.Minute)
			framework.ExpectNoError(err)
			attachments, err := azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace).List(context.Background(), metav1.ListOptions{})
			framework.ExpectNoError(err)

			// fail primary attachment
			err = azDiskClient.DiskV1alpha1().AzDriverNodes(namespace).Delete(context.Background(), "test-node", metav1.DeleteOptions{})
			framework.ExpectNoError(err)
			err = azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace).Delete(context.Background(), testsuites.GetAzVolumeAttachmentName(volName, "test-node"), metav1.DeleteOptions{})
			framework.ExpectNoError(err)
			err = testsuites.WaitForDelete(azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace), att.Namespace, att.Name, time.Duration(5)*time.Minute)
			framework.ExpectNoError(err)

			// failover to one of replicas
			var replica *v1alpha1.AzVolumeAttachment
			for _, attachment := range attachments.Items {
				if attachment.Status != nil && attachment.Status.Role == v1alpha1.ReplicaRole {
					replica = &attachment
					break
				}
			}
			promoted := replica.DeepCopy()
			promoted.Spec.RequestedRole = v1alpha1.PrimaryRole
			_, err = azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace).Update(context.Background(), promoted, metav1.UpdateOptions{})
			framework.ExpectNoError(err)

			// check if a new primary has been created
			err = testsuites.WaitForPrimary(azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace), att.Namespace, volName, time.Duration(5)*time.Minute)
			framework.ExpectNoError(err)
			// check if the second attachment object was created and marked attached.
			err = testsuites.WaitForReplicas(azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace), att.Namespace, volName, 1, time.Duration(5)*time.Minute)
			framework.ExpectNoError(err)

			// clean-up
			testAzAtt.Cleanup()
		})

		// this test can fail occasionally  as it waits for the deletion of replica attachment if the test attachment is made on non-test nodes
		ginkgo.It("If a replica is deleted, should create another replica to replace the previous one", func() {
			skipIfUsingInTreeVolumePlugin()
			skipIfNotUsingCSIDriverV2()
			volName := "test-volume"
			testAzAtt := testsuites.NewTestAzVolumeAttachment(azDiskClient.DiskV1alpha1(), namespace, volName, "test-node", []string{"test-node-2", "test-node-3"}, 1)
			att := testAzAtt.Create()
			err := testsuites.WaitForReplicas(azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace), att.Namespace, volName, 1, time.Duration(5)*time.Minute)
			framework.ExpectNoError(err)
			attachments, err := azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace).List(context.Background(), metav1.ListOptions{})
			framework.ExpectNoError(err)

			// fail replica attachment
			var replica *v1alpha1.AzVolumeAttachment
			for _, attachment := range attachments.Items {
				if attachment.Status != nil && attachment.Status.Role == v1alpha1.ReplicaRole {
					replica = &attachment
					break
				}
			}
			err = azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace).Delete(context.Background(), replica.Name, metav1.DeleteOptions{})
			framework.ExpectNoError(err)
			// below will be commented out until the controller test uses AzureDiskDriver_v2 running on a separate dedicated namespace for testing
			// err = testsuites.WaitForDelete(azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace), att.Namespace, replica.Name, time.Duration(5)*time.Minute)
			// framework.ExpectNoError(err)
			time.Sleep(time.Duration(30) * time.Second)

			// check if a new replica has been created
			err = testsuites.WaitForReplicas(azDiskClient.DiskV1alpha1().AzVolumeAttachments(namespace), att.Namespace, volName, 1, time.Duration(5)*time.Minute)
			framework.ExpectNoError(err)

			// clean-up
			testAzAtt.Cleanup()
		})
	})

})
