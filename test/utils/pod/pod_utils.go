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

package podutil

import (
	"context"
	"fmt"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	clientset "k8s.io/client-go/kubernetes"
	deploymentutil "k8s.io/kubernetes/pkg/controller/deployment/util"
	"k8s.io/kubernetes/test/e2e/framework"
	e2elog "k8s.io/kubernetes/test/e2e/framework/log"
	e2epod "k8s.io/kubernetes/test/e2e/framework/pod"
)

func CleanupPodOrFail(client clientset.Interface, name, namespace string) {
	e2elog.Logf("deleting Pod %q/%q", namespace, name)
	body, err := PodLogs(client, name, namespace)
	if err != nil {
		e2elog.Logf("Error getting logs for pod %s: %v", name, err)
	} else {
		e2elog.Logf("Pod %s has the following logs: %s", name, body)
	}
	e2epod.DeletePodOrFail(client, namespace, name)
}

func PodLogs(client clientset.Interface, name, namespace string) ([]byte, error) {
	return client.CoreV1().Pods(namespace).GetLogs(name, &v1.PodLogOptions{}).Do(context.TODO()).Raw()
}

// GetNewReplicaSet returns a replica set that matches the intent of the given deployment; get ReplicaSetList from client interface.
// Returns nil if the new replica set doesn't exist yet.
func GetNewReplicaSet(deployment *apps.Deployment, c clientset.Interface) (*apps.ReplicaSet, error) {
	rsList, err := deploymentutil.ListReplicaSets(deployment, deploymentutil.RsListFromClient(c.AppsV1()))
	if err != nil {
		return nil, err
	}
	return deploymentutil.FindNewReplicaSet(deployment, rsList), nil
}

func GetPodsForDeployment(client clientset.Interface, deployment *apps.Deployment) (*v1.PodList, error) {
	replicaSet, err := GetNewReplicaSet(deployment, client)
	if err != nil {
		return nil, fmt.Errorf("failed to get new replica set for deployment %q: %v", deployment.Name, err)
	}
	if replicaSet == nil {
		return nil, fmt.Errorf("expected a new replica set for deployment %q, found none", deployment.Name)
	}
	podListFunc := func(namespace string, options metav1.ListOptions) (*v1.PodList, error) {
		return client.CoreV1().Pods(namespace).List(context.TODO(), options)
	}
	rsList := []*apps.ReplicaSet{replicaSet}
	podList, err := deploymentutil.ListPods(deployment, rsList, podListFunc)
	if err != nil {
		return nil, fmt.Errorf("failed to list Pods of Deployment %q: %v", deployment.Name, err)
	}
	return podList, nil
}

func DeleteAllPodsWithMatchingLabelWithSleep(cs clientset.Interface, ns *v1.Namespace, matchLabels map[string]string) {

	DeleteAllPodsWithMatchingLabel(cs, ns, matchLabels)
	//sleep ensure waitForPodready will not pass before old pod is deleted.
	time.Sleep(60 * time.Second)
}

func DeleteAllPodsWithMatchingLabel(cs clientset.Interface, ns *v1.Namespace, matchLabels map[string]string) {

	e2elog.Logf("Deleting all pods with %v labels in namespace %s", matchLabels, ns.Name)
	labelSelector := metav1.LabelSelector{MatchLabels: matchLabels}
	err := cs.CoreV1().Pods(ns.Name).DeleteCollection(context.TODO(), metav1.DeleteOptions{}, metav1.ListOptions{LabelSelector: labels.Set(labelSelector.MatchLabels).String()})
	if !errors.IsNotFound(err) {
		framework.ExpectNoError(err)
	}
}

func CountAllPodsWithMatchingLabel(cs clientset.Interface, ns *v1.Namespace, matchLabels map[string]string, state string) int {
	e2elog.Logf("Getting all pods with %v labels in namespace %s", matchLabels, ns.Name)
	var listOptions metav1.ListOptions
	labelSelector := metav1.LabelSelector{MatchLabels: matchLabels}
	if state != "" {
		fieldSelector := fields.SelectorFromSet(map[string]string{"status.phase": state})
		listOptions = metav1.ListOptions{LabelSelector: labels.Set(labelSelector.MatchLabels).String(), FieldSelector: fieldSelector.String()}
	} else {
		listOptions = metav1.ListOptions{LabelSelector: labels.Set(labelSelector.MatchLabels).String()}
	}
	pods, err := cs.CoreV1().Pods(ns.Name).List(context.TODO(), listOptions)
	if !errors.IsNotFound(err) {
		framework.ExpectNoError(err)
	}
	return len(pods.Items)
}

func WaitForPodsWithMatchingLabelToRun(testTimeout int, tickerDuration time.Duration, desiredNumberOfPods int, cs clientset.Interface, ns *v1.Namespace) (numberOfRunningPods int) {
	ticker := time.NewTicker(tickerDuration)
	tickerCount := 0
	timeout := time.After(time.Duration(testTimeout) * time.Minute)

	for {
		select {
		case <-timeout:
			return
		case <-ticker.C:
			tickerCount++
			numberOfRunningPods = CountAllPodsWithMatchingLabel(cs, ns, map[string]string{"group": "azuredisk-scale-tester"}, string(v1.PodRunning))
			e2elog.Logf("%d min: %d pods are ready", tickerCount, numberOfRunningPods)
			if numberOfRunningPods >= desiredNumberOfPods {
				return
			}
		}
	}
}
