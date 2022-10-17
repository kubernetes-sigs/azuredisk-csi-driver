/*
Copyright 2020 The Kubernetes Authors.

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

package scale

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/onsi/ginkgo"
	scale "k8s.io/api/autoscaling/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	"k8s.io/kubernetes/test/e2e/framework/skipper"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
	podutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/pod"
	volutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/volume"
)

type PodSchedulingWithPVScaleTest struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    resources.PodDetails
	AzDiskClient           *azdisk.Clientset
	Replicas               int
	MaxShares              int
	StorageClassParameters map[string]string
}

type PodSchedulingOnFailoverScaleTest struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    resources.PodDetails
	AzDiskClient           *azdisk.Clientset
	Replicas               int
	StorageClassParameters map[string]string
}

func (t *PodSchedulingWithPVScaleTest) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	nodes := nodeutil.ListAgentNodeNames(client, t.Pod.IsWindows)
	skipper.SkipUnlessAtLeast(len(nodes), t.MaxShares, fmt.Sprintf("Need at least %d agent nodes to verify the test case.", t.MaxShares))

	totalNumberOfPods := t.Replicas
	testWithReplicas := t.MaxShares > 1
	totalNumberOfReplicaAtts := totalNumberOfPods * (t.MaxShares - 1)

	tStorageClass, storageCleanup := t.Pod.CreateStorageClass(client, namespace, t.CSIDriver, t.StorageClassParameters)
	defer storageCleanup()
	statefulSet, cleanupStatefulSet := t.Pod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName, t.Replicas, &tStorageClass, map[string]string{"group": "azuredisk-scale-tester"})
	defer cleanupStatefulSet(45 * time.Minute)

	// start scale out test
	startScaleOut := time.Now()
	statefulSet.CreateWithoutWaiting()

	ticker := time.NewTicker(time.Minute)
	timeout := time.After(time.Duration(*scaleTestTimeout) * time.Minute)
	var (
		numberOfRunningPods, numberOfAttachedReplicaAtts, tickerCount int     = 0, 0, 0
		scaleOutPodsCompleted, scaleOutReplicaAttsCompleted           float64 = 0, 0
		allPodsRunning, allReplicasAttached                           bool    = false, false
	)
scaleOutTest:
	for {
		select {
		case <-timeout:
			if !allReplicasAttached {
				scaleOutReplicaAttsCompleted = time.Since(startScaleOut).Minutes()
			}
			if !allPodsRunning {
				scaleOutPodsCompleted = time.Since(startScaleOut).Minutes()
			}
			break scaleOutTest
		case <-ticker.C:
			tickerCount++

			if !testWithReplicas {
				allReplicasAttached = true
			}

			if !allPodsRunning {
				numberOfRunningPods = podutil.CountAllPodsWithMatchingLabel(client, namespace, map[string]string{"group": "azuredisk-scale-tester"}, string(v1.PodRunning))
				e2elog.Logf("%d min: %d pods are running", tickerCount, numberOfRunningPods)
				if numberOfRunningPods >= totalNumberOfPods {
					scaleOutPodsCompleted = time.Since(startScaleOut).Minutes()
					allPodsRunning = true
				}
			}

			if !allReplicasAttached {
				numberOfAttachedReplicaAtts = volutil.CountAllAttachedReplicaAttachments(t.AzDiskClient)
				e2elog.Logf("%d min: %d replica attchements are attached", tickerCount, numberOfAttachedReplicaAtts)
				if numberOfAttachedReplicaAtts >= totalNumberOfReplicaAtts {
					scaleOutReplicaAttsCompleted = time.Since(startScaleOut).Minutes()
					allReplicasAttached = true
				}
			}

			if allPodsRunning && allReplicasAttached {
				break scaleOutTest
			}
		}
	}

	// start scale in test
	newScale := &scale.Scale{
		ObjectMeta: metav1.ObjectMeta{
			Name:      statefulSet.Statefulset.Name,
			Namespace: statefulSet.Namespace.Name,
		},
		Spec: scale.ScaleSpec{
			Replicas: int32(0),
		},
	}

	startScaleIn := time.Now()
	_, _ = client.AppsV1().StatefulSets(statefulSet.Namespace.Name).UpdateScale(context.TODO(), statefulSet.Statefulset.Name, newScale, metav1.UpdateOptions{})
	numberOfRemainingPods := podutil.WaitForAllPodsWithMatchingLabelToDelete(*scaleTestTimeout, time.Minute, totalNumberOfPods, client, namespace)
	scaleInPodsCompleted := time.Since(startScaleIn).Minutes()

	numberOfRemainingVolumeAtts := volutil.WaitForAllVolumeAttachmentsToDetach(*scaleTestTimeout, 30*time.Second, totalNumberOfPods**volMountedToPod, client)
	scaleInVolsDetached := time.Since(startScaleIn).Minutes()

	numberOfRemainingReplicaAtts := 0
	scaleInAttsCompleted := float64(0)
	if testWithReplicas {
		time.Sleep(controller.DefaultTimeUntilGarbageCollection)
		startScaleInAtts := time.Now()
		numberOfRemainingReplicaAtts = volutil.WaitForAllReplicaAttachmentsToDetach(*scaleTestTimeout, time.Minute, numberOfRunningPods+numberOfAttachedReplicaAtts, t.AzDiskClient)
		scaleInAttsCompleted = time.Since(startScaleInAtts).Minutes()
	}

	klog.Infof("Scaling out test of pods completed in %f minutes.", scaleOutPodsCompleted)
	klog.Infof("Total number of pods in Running state: %d", numberOfRunningPods)

	klog.Infof("Scaling in test of pods completed in %f minutes.", scaleInPodsCompleted)
	klog.Infof("Total number of remaining pods: %d", numberOfRemainingPods)

	klog.Infof("Scaling in detach completed in %f minutes.", scaleInVolsDetached)

	if testWithReplicas {
		klog.Infof("Scaling out test of replica attachments completed in %f minutes.", scaleOutReplicaAttsCompleted)
		klog.Infof("Total number of replica attachments are attached: %d", numberOfAttachedReplicaAtts)

		klog.Infof("Scaling in test of replica attachments completed in %f minutes.", scaleInAttsCompleted)
		klog.Infof("Total number of remaining replica attachments: %d", numberOfRemainingReplicaAtts)
	}

	if numberOfRunningPods != totalNumberOfPods || numberOfAttachedReplicaAtts != totalNumberOfReplicaAtts || numberOfRemainingPods != 0 || numberOfRemainingReplicaAtts != 0 || numberOfRemainingVolumeAtts != 0 {
		podTestResult := fmt.Sprintf("Scale test failed to fully scale out/in. Number of pods that ran: %d, number of pods that were not deleted: %d, number of volumes that were not detached: %d", numberOfRunningPods, numberOfRemainingPods, numberOfRemainingReplicaAtts)

		replicaTestResult := ""
		if testWithReplicas {
			replicaTestResult = fmt.Sprintf(", number of replica attachments that were attached: %d, number of replicas that were not detached: %d", numberOfAttachedReplicaAtts, numberOfRemainingReplicaAtts)
		}
		ginkgo.Fail(podTestResult + replicaTestResult)
	}
}

func (t *PodSchedulingOnFailoverScaleTest) Run(client clientset.Interface, namespace *v1.Namespace, schedulerName string) {
	totalNumberOfPods := t.Replicas
	nodes := nodeutil.ListAgentNodeNames(client, t.Pod.IsWindows)
	skipper.SkipUnlessAtLeast(len(nodes), 2, "Need at least 2 agent nodes to verify the test case.")
	midNode := len(nodes) / 2

	// uncordon the first half nodes and cordon the second half nodes
	for _, node := range nodes[:midNode] {
		node := node
		framework.Logf("Uncordoning %s.", node)
		nodeutil.SetNodeUnschedulable(client, node, false)
	}

	for _, node := range nodes[midNode:] {
		node := node
		framework.Logf("Cordoning %s.", node)
		nodeutil.SetNodeUnschedulable(client, node, true)
	}

	tStorageClass, storageCleanup := t.Pod.CreateStorageClass(client, namespace, t.CSIDriver, t.StorageClassParameters)
	defer storageCleanup()
	statefulSet, cleanupStatefulSet := t.Pod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName, t.Replicas, &tStorageClass, map[string]string{"group": "azuredisk-scale-tester"})
	defer cleanupStatefulSet(45 * time.Minute)

	// start scale out test
	startScaleOut := time.Now()
	statefulSet.CreateWithoutWaiting()
	numberOfRunningPods1 := podutil.WaitForAllPodsWithMatchingLabelToRun(*scaleTestTimeout, time.Minute, totalNumberOfPods, client, namespace)
	scaleOutCompleted := time.Since(startScaleOut).Minutes()

	// cordon the first half nodes where the running pods sit on
	for _, node := range nodes[:midNode] {
		node := node
		framework.Logf("Cordoning %s.", node)
		nodeutil.SetNodeUnschedulable(client, node, true)
		defer func() {
			framework.Logf("Uncordoning %s", node)
			nodeutil.SetNodeUnschedulable(client, node, false)
		}()
	}

	// uncordon the second nodes and measure the amount of time to create replica attachments
	maxShares, _ := azureutils.GetMaxSharesAndMaxMountReplicaCount(t.StorageClassParameters, false)
	totalNumberOfReplicaAtts := totalNumberOfPods * (maxShares - 1)

	var wg sync.WaitGroup
	for _, node := range nodes[midNode:] {
		node := node
		wg.Add(1)
		go func() {
			defer wg.Done()
			framework.Logf("Uncordoning %s.", node)
			nodeutil.SetNodeUnschedulable(client, node, false)
		}()
	}
	wg.Wait()

	startCreateReplicaAtts := time.Now()
	numberOfAttachedReplicaAtts := volutil.WaitForAllReplicaAttachmentsToAttach(*scaleTestTimeout, time.Minute, totalNumberOfReplicaAtts, t.AzDiskClient)
	createReplicaAttsCompleted := time.Since(startCreateReplicaAtts).Minutes()

	// delete all pods
	podutil.DeleteAllPodsWithMatchingLabel(client, namespace, map[string]string{"group": "azuredisk-scale-tester"})

	// start failover test
	startFailoverScale := time.Now()
	numberOfRunningPods2 := podutil.WaitForAllPodsWithMatchingLabelToRun(*scaleTestTimeout, time.Minute, totalNumberOfPods, client, namespace)
	failoverScaleCompleted := time.Since(startFailoverScale).Minutes()

	klog.Infof("%d pods are in running state in test completed in %f minutes.", numberOfRunningPods1, scaleOutCompleted)
	klog.Infof("%d replica attachments are attached in test completed in %f minutes.", numberOfAttachedReplicaAtts, createReplicaAttsCompleted)
	klog.Infof("%d pods are in running state again after deletion in test completed in %f minutes.", numberOfRunningPods2, failoverScaleCompleted)

	if numberOfRunningPods1 != totalNumberOfPods || numberOfRunningPods2 != totalNumberOfPods || numberOfAttachedReplicaAtts != totalNumberOfReplicaAtts {
		ginkgo.Fail(fmt.Sprintf("Failover Scale test failed to fully creating running pods and/or attached replica attachments. "+
			"Number of pods that ran before deletion: %d, Number of pods that ran after deletion: %d, Number of attached replica attachments: %d",
			numberOfRunningPods1, numberOfRunningPods2, numberOfAttachedReplicaAtts))
	}
}
