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
	"k8s.io/kubernetes/test/e2e/framework/skipper"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
	podutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/pod"
	volutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/volume"
)

type PodSchedulingWithPVScaleTest struct {
	CSIDriver              driver.DynamicPVTestDriver
	Pod                    resources.PodDetails
	Replicas               int
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
	totalNumberOfPods := t.Replicas
	var oneMinPodsRunning,
		twoMinPodsRunning,
		threeMinPodsRunning,
		fiveMinPodsRunning,
		tenMinPodsRunning = totalNumberOfPods, totalNumberOfPods, totalNumberOfPods, totalNumberOfPods, totalNumberOfPods
	var oneMinPodsDeleted,
		twoMinPodsDeleted,
		threeMinPodsDeleted,
		fiveMinPodsDeleted,
		tenMinPodsDeleted = totalNumberOfPods, totalNumberOfPods, totalNumberOfPods, totalNumberOfPods, totalNumberOfPods

	tStorageClass, storageCleanup := t.Pod.CreateStorageClass(client, namespace, t.CSIDriver, t.StorageClassParameters)
	defer storageCleanup()
	statefulSet, cleanupStatefulSet := t.Pod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName, t.Replicas, &tStorageClass, map[string]string{"group": "azuredisk-scale-tester"})
	defer cleanupStatefulSet(45 * time.Minute)

	startScaleOut := time.Now()
	statefulSet.CreateWithoutWaiting()
	numberOfRunningPods := 0
	ticker := time.NewTicker(time.Minute)
	tickerCount := 0
	timeout := time.After(time.Duration(*scaleTestTimeout) * time.Minute)

scaleOutTest:
	for {
		select {
		case <-timeout:
			break scaleOutTest
		case <-ticker.C:
			tickerCount = tickerCount + 1
			numberOfRunningPods = podutil.CountAllPodsWithMatchingLabel(client, namespace, map[string]string{"group": "azuredisk-scale-tester"}, string(v1.PodRunning))
			if numberOfRunningPods >= totalNumberOfPods {
				break scaleOutTest
			}

			if tickerCount == 1 {
				oneMinPodsRunning = numberOfRunningPods
			} else if tickerCount == 2 {
				twoMinPodsRunning = numberOfRunningPods
			} else if tickerCount == 3 {
				threeMinPodsRunning = numberOfRunningPods
			} else if tickerCount == 5 {
				fiveMinPodsRunning = numberOfRunningPods
			} else if tickerCount == 10 {
				tenMinPodsRunning = numberOfRunningPods
			}
		}
	}

	scaleOutCompleted := time.Since(startScaleOut).Minutes()

	startScaleIn := time.Now()
	newScale := &scale.Scale{
		ObjectMeta: metav1.ObjectMeta{
			Name:      statefulSet.Statefulset.Name,
			Namespace: statefulSet.Namespace.Name,
		},
		Spec: scale.ScaleSpec{
			Replicas: int32(0),
		},
	}
	_, _ = client.AppsV1().StatefulSets(statefulSet.Namespace.Name).UpdateScale(context.TODO(), statefulSet.Statefulset.Name, newScale, metav1.UpdateOptions{})

	numberOfRemainingPods := totalNumberOfPods
	ticker.Reset(time.Minute)
	tickerCount = 0
	timeout = time.After(time.Duration(*scaleTestTimeout) * time.Minute)
scaleInTest:
	for {
		select {
		case <-timeout:
			break scaleInTest
		case <-ticker.C:
			tickerCount = tickerCount + 1
			numberOfRemainingPods = podutil.CountAllPodsWithMatchingLabel(client, namespace, map[string]string{"group": "azuredisk-scale-tester"}, string(v1.PodRunning))
			if numberOfRemainingPods <= 0 {
				break scaleInTest
			}

			if tickerCount == 1 {
				oneMinPodsDeleted = totalNumberOfPods - numberOfRemainingPods
			} else if tickerCount == 2 {
				twoMinPodsDeleted = totalNumberOfPods - numberOfRemainingPods
			} else if tickerCount == 3 {
				threeMinPodsDeleted = totalNumberOfPods - numberOfRemainingPods
			} else if tickerCount == 5 {
				fiveMinPodsDeleted = totalNumberOfPods - numberOfRemainingPods
			} else if tickerCount == 10 {
				tenMinPodsDeleted = totalNumberOfPods - numberOfRemainingPods
			}
		}
	}

	scaleInPodsCompleted := time.Since(startScaleIn).Minutes()
	for {
		numberOfRemainingVolumeAtts := volutil.CountAllVolumeAttachments(client)
		if numberOfRemainingVolumeAtts <= 0 || time.Since(startScaleIn) > time.Duration(*scaleTestTimeout)*time.Minute {
			break
		}
		time.Sleep(30 * time.Second)
	}
	scaleInCompleted := time.Since(startScaleIn).Minutes()

	klog.Infof("Scaling out test completed in %f minutes.", scaleOutCompleted)
	klog.Infof("1 min: %d pods are ready", oneMinPodsRunning)
	klog.Infof("2 min: %d pods are ready", twoMinPodsRunning)
	klog.Infof("3 min: %d pods are ready", threeMinPodsRunning)
	klog.Infof("5 min: %d pods are ready", fiveMinPodsRunning)
	klog.Infof("10 min: %d pods are ready", tenMinPodsRunning)
	klog.Infof("Total number of pods in Running state: %d", numberOfRunningPods)

	klog.Infof("Scaling in test completed in %f minutes.", scaleInPodsCompleted)
	klog.Infof("1 min: %d pods are deleted", oneMinPodsDeleted)
	klog.Infof("2 min: %d pods are deleted", twoMinPodsDeleted)
	klog.Infof("3 min: %d pods are deleted", threeMinPodsDeleted)
	klog.Infof("5 min: %d pods are deleted", fiveMinPodsDeleted)
	klog.Infof("10 min: %d pods are deleted", tenMinPodsDeleted)
	klog.Infof("Total number of remaining pods: %d", numberOfRemainingPods)

	klog.Infof("Scaling in detach completed in %f minutes.", scaleInCompleted)

	if numberOfRunningPods != totalNumberOfPods || numberOfRemainingPods != 0 {
		ginkgo.Fail("Scale test failed to fully scale out/in. Number of pods that ran: %d, number of pods that were not deleted: %d", numberOfRunningPods, numberOfRemainingPods)
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
	numberOfRunningPods1 := podutil.WaitForPodsWithMatchingLabelToRun(*scaleTestTimeout, time.Minute, totalNumberOfPods, client, namespace)
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
	numberOfAttachedReplicaAtts := volutil.WaitForReplicaAttachmentsToAttach(*scaleTestTimeout, time.Minute, totalNumberOfReplicaAtts, t.AzDiskClient)
	createReplicaAttsCompleted := time.Since(startCreateReplicaAtts).Minutes()

	// delete all pods
	podutil.DeleteAllPodsWithMatchingLabel(client, namespace, map[string]string{"group": "azuredisk-scale-tester"})

	// start failover test
	startFailoverScale := time.Now()
	numberOfRunningPods2 := podutil.WaitForPodsWithMatchingLabelToRun(*scaleTestTimeout, time.Minute, totalNumberOfPods, client, namespace)
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
