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

package testsuites

import (
	"context"
	"fmt"
	"sync"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"

	"github.com/onsi/ginkgo/v2"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
)

// DynamicallyProvisionedAttachBatchTest will provision required StorageClass and Job(s)
// provisioning over the maximum number of disks that can be attached to the node(s) in the cluster
// Testing if MaximumDataDisksExceeded issue is seen during attaching
type DynamicallyProvisionedAttachBatchTest struct {
	CSIDriver driver.DynamicPVTestDriver
	Pods      []PodDetails
}

func (t *DynamicallyProvisionedAttachBatchTest) Run(ctx context.Context, client clientset.Interface, namespace *v1.Namespace) {
	for _, pod := range t.Pods {
		var numOfJobs int64
		nodes, err := client.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
		framework.ExpectNoError(err)
		for _, node := range nodes.Items {
			noSchedule := false
			for _, taint := range node.Spec.Taints {
				if taint.Effect == v1.TaintEffectNoSchedule {
					noSchedule = true
					break
				}
			}

			if !noSchedule && !node.Spec.Unschedulable {
				_, instanceType, err := azuredisk.GetNodeInfoFromLabels(ctx, string(node.Name), client)
				framework.ExpectNoError(err)
				if instanceType != "" {
					maxNumDisks, instanceExists := azuredisk.GetMaxDataDiskCount(instanceType)
					if instanceExists {
						numOfJobs += maxNumDisks
					}
				} else {
					// assuming the default instance type Standard_DS2_v2
					numOfJobs += 8
				}
			}
		}
		framework.Logf("maximum number of disks: %d", numOfJobs)
		if numOfJobs == 0 {
			ginkgo.Skip("no schedulable nodes found")
		}
		numOfJobs += 5
		framework.Logf("number of jobs to run: %d", numOfJobs)

		ch := make(chan error, numOfJobs)
		var wg sync.WaitGroup

		ginkgo.By("setting up the StorageClass")
		volume := pod.Volumes[0]
		storageClass := t.CSIDriver.GetDynamicProvisionStorageClass(driver.GetParameters(), volume.MountOptions, volume.ReclaimPolicy, volume.VolumeBindingMode, volume.AllowedTopologyValues, namespace.Name)
		tsc := NewTestStorageClass(client, namespace, storageClass)
		createdStorageClass := tsc.Create(ctx)
		defer tsc.Cleanup(ctx)

		for range numOfJobs {
			wg.Add(1)
			go func() {
				defer ginkgo.GinkgoRecover()
				defer wg.Done()
				tjob, cleanup := pod.SetupJob(ctx, client, namespace, &createdStorageClass)
				// defer must be called here for resources not get removed before using them
				for i := range cleanup {
					defer cleanup[i](ctx)
				}

				ginkgo.By("deploying the job")
				tjob.Create(ctx)

				ginkgo.By("waiting for the attach check")
				ch <- tjob.WaitForAttachBatchCheck(ctx)
			}()
		}
		wg.Wait()
		close(ch)
		ginkgo.By("all attach checks are done")
		for err := range ch {
			framework.ExpectNoError(err)
		}

		ginkgo.By("checking if there are any jobs left")
		tjobs, err := client.BatchV1().Jobs(namespace.Name).List(context.TODO(), metav1.ListOptions{})
		framework.ExpectNoError(err)
		if len(tjobs.Items) > 0 {
			tpods, err := client.CoreV1().Pods(namespace.Name).List(context.TODO(), metav1.ListOptions{})
			framework.ExpectNoError(err)
			if len(tpods.Items) > 0 {
				for _, pod := range tpods.Items {
					events, err := client.CoreV1().Events(pod.Namespace).List(
						context.TODO(),
						metav1.ListOptions{
							FieldSelector: fmt.Sprintf(
								"involvedObject.kind=Pod,involvedObject.name=%s,involvedObject.namespace=%s",
								pod.Name,
								pod.Namespace,
							),
						},
					)
					framework.ExpectNoError(err)
					for _, e := range events.Items {
						framework.Logf("Event on pod %s: %s %s %s", pod.Name, e.Reason, e.Type, e.Message)
					}
				}
			}
			framework.Failf("There are still jobs left in the namespace %s", namespace.Name)
		}
	}
}
