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

package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"k8s.io/klog/v2"
	podfailure "sigs.k8s.io/azuredisk-csi-driver/test/podFailover/apis/client/clientset/versioned"
)

var kubeconfig = flag.String("kubeconfig", "", "kube config path for cluster")

const (
	podFailoverLabelKey    = "app"
	podFailoverLabelValue  = "pod-failover"
	sameNodeFailoverConst  = "same-node-failover"
	deletePodFailoverConst = "delete-pod"
)

func main() {
	flag.Parse()
	var config *rest.Config
	var err error
	// get config from flag first, then env variable, then inclusterconfig, then default
	if *kubeconfig != "" {
		config, _ = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		kubeConfigPath := os.Getenv("KUBECONFIG")
		if len(kubeConfigPath) == 0 {
			config, err = rest.InClusterConfig()
			if err != nil {
				defaultKubeConfigPath := filepath.Join(os.Getenv("HOME"), ".kube", "config")
				config, _ = clientcmd.BuildConfigFromFlags("", defaultKubeConfigPath)
			}
		} else {
			config, _ = clientcmd.BuildConfigFromFlags("", kubeConfigPath)
		}
	}

	clientset, _ := kubernetes.NewForConfig(config)

	podFailoverClient, _ := podfailure.NewForConfig(config)

	ctx := context.Background()

	for {
		podList, err := getPods(ctx, clientset)
		if err != nil {
			klog.Errorf("Error occurred while getting pod list: %v", err)
		}
		pods := podList.Items
		if len(pods) > 0 {
			n := rand.Intn(len(pods))
			selectedPod := pods[n]
			failureType, err := getFailureType(selectedPod)
			if err != nil {
				klog.Errorf("Error occurred while getting failure type for pod %s: %v", selectedPod.Name, err)
			}
			switch failureType {
			case sameNodeFailoverConst:
				klog.Info("Facilitating same node failover.")
				sameNodePods, err := getSameNodePods(selectedPod, pods)
				if err != nil {
					klog.Errorf("Error occurred while getting same node pods %s: %v", selectedPod.Name, err)
				}
				sameNodeFailover(ctx, clientset, podFailoverClient, sameNodePods, failureType)
			case deletePodFailoverConst:
				klog.Info("Facilitating delete-pod failover.")
				deleteAndRestartPods(ctx, clientset, podFailoverClient, selectedPod, true, failureType)
			default:
				klog.Info("Facilitating delete-pod failover.")
				deleteAndRestartPods(ctx, clientset, podFailoverClient, selectedPod, true, failureType)
			}
			time.Sleep(10 * time.Second)
		}
	}
}

func deleteAndRestartPods(ctx context.Context, clientset *kubernetes.Clientset, podFailoverClient *podfailure.Clientset, pod v1.Pod, unscheduleNode bool, failureType string) {

	var nodeName string

	if unscheduleNode {
		nodeName = pod.Spec.NodeName
		makeNodeUnschedulable(ctx, nodeName, true, clientset)
	}

	klog.Infof("Deleting pod %s", pod.Name)
	err := deletePod(ctx, pod.Name, pod.Namespace, clientset)
	if err != nil {
		klog.Errorf("Error occurred while deleting the pod %s: %v", pod.Name, err)
	}

	klog.Infof("Report pod failure to pod failure CRD for pod: %s", pod.Name)
	err = getCrdAndReportFailure(ctx, pod.Name, pod.Namespace, failureType, podFailoverClient)
	if err != nil {
		klog.Errorf("Error occurred while deleting the pod %s: %v", pod.Name, err)
	}

	// wait for the pod to come back up
	klog.Infof("Waiting for pod %s to be ready.", pod.Name)
	err = waitForPodReady(ctx, clientset, pod, pod.Namespace)
	if err != nil {
		klog.Errorf("Error occurred while waiting for the pod to be ready %s: %v", pod.Name, err)
	}

	klog.Infof("Pod %s ready.", pod.Name)

	if unscheduleNode {
		makeNodeUnschedulable(ctx, nodeName, false, clientset)
	}
}

func sameNodeFailover(ctx context.Context, clientset *kubernetes.Clientset, podFailoverClient *podfailure.Clientset, pods []v1.Pod, failureType string) {
	var nodeName string
	if len(pods) > 0 {
		nodeName = pods[0].Spec.NodeName
		makeNodeUnschedulable(ctx, nodeName, true, clientset)
	}

	// Reschedule the 1st deployment as it doesn't have any associated pod affinity rules
	deleteAndRestartPods(ctx, clientset, podFailoverClient, pods[0], false, failureType)

	// After that restart all the pods that have associated affinity rules
	var wg sync.WaitGroup

	for i := 1; i < len(pods); i++ {

		wg.Add(1)

		pod := pods[i]
		klog.Infof("Failing over node: %s in same node failover", pod.Name)
		go func() {
			defer wg.Done()
			deleteAndRestartPods(ctx, clientset, podFailoverClient, pod, false, failureType)
		}()

	}
	wg.Wait()

	makeNodeUnschedulable(ctx, nodeName, false, clientset)
}

func makeNodeUnschedulable(ctx context.Context, nodeName string, unschedulable bool, clientset *kubernetes.Clientset) {
	backoff := wait.Backoff{Duration: 1 * time.Second, Factor: 2.0, Steps: 5}
	err := retry.RetryOnConflict(backoff, func() error {
		node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return err
		}

		nodeTobeCordoned := node.DeepCopy()
		nodeTobeCordoned.Spec.Unschedulable = unschedulable

		// Cordon off the node
		_, err = clientset.CoreV1().Nodes().Update(ctx, nodeTobeCordoned, metav1.UpdateOptions{})
		if err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		klog.Errorf("Error occurred setting schedulability of node %s; unschedulable: %t, err: %v", nodeName, unschedulable, err)
	}
}

func deletePod(ctx context.Context, podName string, namespace string, clientset *kubernetes.Clientset) error {
	err := clientset.CoreV1().Pods(namespace).Delete(ctx, podName, metav1.DeleteOptions{})
	if err != nil {
		return err
	}
	return waitForPodToBeDeleted(ctx, podName, namespace, clientset)
}

func waitForPodToBeDeleted(ctx context.Context, podName string, namespace string, clientset *kubernetes.Clientset) error {
	return wait.PollImmediate(1*time.Second, 10*time.Minute, func() (done bool, err error) {
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if pod.Status.Phase != v1.PodRunning {
			return true, nil
		}
		return false, err
	})
}

func waitForPodReady(ctx context.Context, clientset *kubernetes.Clientset, pod v1.Pod, namespace string) error {
	return wait.PollImmediate(1*time.Second, 10*time.Minute, func() (bool, error) {
		newPod, err := clientset.CoreV1().Pods(namespace).Get(context.TODO(), pod.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if newPod.Status.Phase == v1.PodRunning {
			return true, nil
		}
		return false, nil
	})
}

func getPods(ctx context.Context, client *kubernetes.Clientset) (*v1.PodList, error) {
	labelSelector := metav1.LabelSelector{
		MatchLabels: map[string]string{podFailoverLabelKey: podFailoverLabelValue},
	}
	options := metav1.ListOptions{
		LabelSelector: labels.Set(labelSelector.MatchLabels).String(),
		FieldSelector: "status.phase=Running",
	}

	podList, err := client.CoreV1().Pods("").List(ctx, options)
	if err != nil {
		return nil, fmt.Errorf("failed to list pods for namespace: %v", err)
	}
	return podList, nil
}

func getSameNodePods(selectedPod v1.Pod, pods []v1.Pod) ([]v1.Pod, error) {
	var samePods []v1.Pod
	for _, pod := range pods {
		podLabels := pod.GetObjectMeta().GetLabels()
		app, exists := podLabels["app"]
		if exists && app == "pod-failover" {
			samePods = append(samePods, pod)
		}
	}
	return samePods, nil
}

func getFailureType(pod v1.Pod) (string, error) {
	podLabels := pod.GetObjectMeta().GetLabels()
	failureType, exists := podLabels["failureType"]
	klog.Infof("Failure type: %s", failureType)
	if !exists {
		return "", fmt.Errorf("failed to get failureType for Pod %q", pod.Name)
	}
	return failureType, nil
}

func getCrdAndReportFailure(ctx context.Context, podName string, namespace string, failureType string, clientset *podfailure.Clientset) error {
	podfailureobj, err := clientset.PodfailureV1beta1().PodFailures(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get crd for pod %q: %v", podName, err)
	}

	updatedPodFailure := podfailureobj.DeepCopy()

	updatedPodFailure.Status.FailureType = failureType

	_, err = clientset.PodfailureV1beta1().PodFailures(namespace).Update(ctx, updatedPodFailure, metav1.UpdateOptions{})
	if err != nil {
		return fmt.Errorf("failed to update crd for pod %q: %v", podName, err)
	}

	return nil
}
