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
	"sync"
	"time"

	"github.com/onsi/ginkgo"
	scale "k8s.io/api/autoscaling/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
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
	PodCount               int
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

	startScaleOut := time.Now()
	tStorageClass, storageCleanup := t.Pod.CreateStorageClass(client, namespace, t.CSIDriver, t.StorageClassParameters)
	defer storageCleanup()
	statefulSet, cleanupStatefulSet := t.Pod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName, t.Replicas, t.StorageClassParameters, &tStorageClass)
	for i := range cleanupStatefulSet {
		i := i
		defer cleanupStatefulSet[i]()
	}
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
			numberOfRunningPods = podutil.CountAllPodsWithMatchingLabel(client, namespace, map[string]string{"app": "azuredisk-volume-tester"}, string(v1.PodRunning))
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
			numberOfRemainingPods = podutil.CountAllPodsWithMatchingLabel(client, namespace, map[string]string{"app": "azuredisk-volume-tester"}, string(v1.PodRunning))
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
	statefulSets := []*resources.TestStatefulset{}

	ginkgo.By("Initiating statefulset setup")

	tStorageClass, storageCleanup := t.Pod.CreateStorageClass(client, namespace, t.CSIDriver, t.StorageClassParameters)
	defer storageCleanup()

	for i := 0; i < t.PodCount; i++ {
		ss, cleanup := t.Pod.SetupStatefulset(client, namespace, t.CSIDriver, schedulerName, t.Replicas, t.StorageClassParameters, &tStorageClass)
		statefulSets = append(statefulSets, ss)

		for j := range cleanup {
			defer cleanup[j]()
		}
	}

	ginkgo.By("Completed statefulset setup")

	ginkgo.By("Initiating statefulset deployment")
	var wg sync.WaitGroup
	start := time.Now()
	for i := range statefulSets {
		wg.Add(1)
		go func(statefulset *resources.TestStatefulset) {
			defer wg.Done()
			defer ginkgo.GinkgoRecover()

			statefulset.Create()
		}(statefulSets[i])
	}

	ginkgo.By("Waiting for the pod for all the statefulsets to succeed")
	wg.Wait()
	framework.Logf("Time taken to complete deployment of statefulsets : %d", time.Since(start).Milliseconds())

	ginkgo.By("All pods for the statefulsets are ready")
	ginkgo.By("Initiating scaling down all the statefulsets")

	for i := range statefulSets {
		newScale := &scale.Scale{
			ObjectMeta: metav1.ObjectMeta{
				Name:      statefulSets[i].Statefulset.Name,
				Namespace: statefulSets[i].Namespace.Name,
			},
			Spec: scale.ScaleSpec{
				Replicas: int32(0),
			},
		}

		go func(ss *resources.TestStatefulset) {
			defer wg.Done()
			defer ginkgo.GinkgoRecover()

			_, err := client.AppsV1().StatefulSets(ss.Namespace.Name).UpdateScale(context.TODO(), ss.Statefulset.Name, newScale, metav1.UpdateOptions{})
			framework.ExpectNoError(err)
			time.Sleep(30 * time.Second)
			ss.WaitForPodReady()
		}(statefulSets[i])
	}

	ginkgo.By("Sleep 30mins waiting for statefulset update to complete")
	time.Sleep(30 * time.Minute)

	ginkgo.By("Resetting the scale for the statefulsets to 1")
	start = time.Now()

	for i := range statefulSets {
		newScale := &scale.Scale{
			ObjectMeta: metav1.ObjectMeta{
				Name:      statefulSets[i].Statefulset.Name,
				Namespace: statefulSets[i].Namespace.Name,
			},
			Spec: scale.ScaleSpec{
				Replicas: int32(1),
			},
		}

		wg.Add(1)
		go func(ss *resources.TestStatefulset) {
			defer wg.Done()
			defer ginkgo.GinkgoRecover()

			_, err := client.AppsV1().StatefulSets(ss.Namespace.Name).UpdateScale(context.TODO(), ss.Statefulset.Name, newScale, metav1.UpdateOptions{})
			framework.ExpectNoError(err)
			time.Sleep(30 * time.Second)
			ss.WaitForPodReady()
		}(statefulSets[i])
	}

	wg.Wait()
	framework.Logf("Time taken to rescale the statefulsets : %d", time.Since(start).Milliseconds())
}
