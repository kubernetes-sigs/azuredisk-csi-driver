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
	"strconv"
	"time"

	apps "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
	deploymentutil "k8s.io/kubernetes/pkg/controller/deployment/util"
)

const (
	podFailoverNamespace = "pod-failover-ns"
	scheduleExtenderName = "csi-azuredisk-scheduler-extender"
)

var (
	driverVersion       = flag.String("driver-version", "v2", "Specify whether the azuredisk csi driver being tested is v1 or v2")
	maxShares           = flag.Int("maxshares", 3, "Specify the maxshares value for the storage class")
	duration            = flag.Int("duration", 60, "Duration for which the test should run in minutes")
	workloadImage       = flag.String("workload-image", "nearora4/workloadpod:latest", "Image of the workload pod that will be deployed by the controller")
	podCount            = flag.Int("pod-count", 1, "The number of pods that should be created for a deployment")
	pvcPerPod           = flag.Int("pvc-per-pod", 3, "Number of pvcs that should be created per pod")
	metricsEndpoint     = flag.String("metrics-endpoint", "", "Target where prometheus metrics shouls be published")
	delayBeforeFailover = flag.Int("delay-before-failover", 0, "Time in seconds for which the controller should wait before failing the pod")
)

func main() {

	flag.Parse()
	config, err := rest.InClusterConfig()
	if err != nil {
		kubeConfigPath := os.Getenv("KUBECONFIG")
		if len(kubeConfigPath) == 0 {
			kubeConfigPath = filepath.Join(os.Getenv("HOME"), ".kube", "config")
		}
		config, _ = clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	}

	clientset, _ := kubernetes.NewForConfig(config)

	ctx := context.Background()
	if err := createTestNamespace(ctx, clientset); err != nil {
		klog.Errorf("Error occurred while creating namespace %s, err: %v", podFailoverNamespace, err)
		return
	}
	defer deleteTestNamespace(ctx, clientset)

	scName, err := createStorageClass(ctx, clientset, *maxShares)
	if err != nil {
		klog.Errorf("Error occurred while creating storageClass: %v", err)
		return
	}
	defer deleteStorageClass(ctx, clientset, scName)

	numPods := *podCount
	numPvcsPerPod := *pvcPerPod
	totalPvcCount := numPods * numPvcsPerPod

	var pvcCreatedList []string
	for count := 0; count < totalPvcCount; count++ {
		pvcName, err := createPVC(ctx, clientset, &scName)

		if err != nil {
			klog.Errorf("Error occurred while creating pvc: %v", err)
			return
		}

		pvcCreatedList = append(pvcCreatedList, pvcName)
	}

	var deployments []*apps.Deployment
	nextPVC := 0
	for count := 0; count < numPods; count++ {

		deployment, err := createDeployment(ctx, clientset, pvcCreatedList, nextPVC, numPvcsPerPod)

		if err != nil {
			klog.Errorf("Error occurred while creating deployment: %v", err)
			return
		}
		deployments = append(deployments, deployment)
		nextPVC = nextPVC + numPvcsPerPod

	}

	// Run workload pod for a given duration
	timer := time.NewTimer(time.Duration(*duration) * time.Minute)
	stopCh := make(chan struct{})

	go func() {
		<-timer.C
		close(stopCh)
	}()

	RunWorkloadPods(ctx, clientset, deployments, stopCh)

}

func createTestNamespace(ctx context.Context, clientset *kubernetes.Clientset) error {
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: podFailoverNamespace,
		},
	}
	namespace, err := clientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		return err
	}

	if err := wait.PollImmediate(5*time.Second, 2*time.Minute, func() (bool, error) {
		var err error
		namespace, err = clientset.CoreV1().Namespaces().Get(ctx, podFailoverNamespace, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if namespace.Status.Phase == v1.NamespaceActive {
			return true, nil
		}
		return false, nil
	}); err != nil {
		return err
	}

	return nil
}

func deleteTestNamespace(ctx context.Context, clientset *kubernetes.Clientset) {
	err := clientset.CoreV1().Namespaces().Delete(ctx, podFailoverNamespace, metav1.DeleteOptions{})
	if err != nil {
		klog.Errorf("Error occurred while deleting namespace %s: %v", podFailoverNamespace, err)
	}

}

func createStorageClass(ctx context.Context, clientset *kubernetes.Clientset, maxShares int) (string, error) {

	allowVolumeExpansion := true
	reclaimPolicy := v1.PersistentVolumeReclaimDelete
	storageClass := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "pod-failover-sc-",
		},
		Provisioner:          "disk.csi.azure.com",
		Parameters:           map[string]string{"skuname": "Premium_LRS", "maxShares": strconv.Itoa(maxShares), "cachingMode": "None"},
		ReclaimPolicy:        &reclaimPolicy,
		AllowVolumeExpansion: &allowVolumeExpansion,
	}

	scCreated, err := clientset.StorageV1().StorageClasses().Create(ctx, storageClass, metav1.CreateOptions{})
	return scCreated.Name, err
}

func deleteStorageClass(ctx context.Context, clientset *kubernetes.Clientset, name string) {
	err := clientset.StorageV1().StorageClasses().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		klog.Errorf("Error occurred while deleting the storage class %s : %v", name, err)
	}
}

func createPVC(ctx context.Context, clientset *kubernetes.Clientset, scName *string) (string, error) {
	pvc := &v1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "pvc-",
			Namespace:    podFailoverNamespace,
		},
		Spec: v1.PersistentVolumeClaimSpec{
			StorageClassName: scName,
			AccessModes: []v1.PersistentVolumeAccessMode{
				v1.ReadWriteOnce,
			},
			Resources: v1.ResourceRequirements{
				Requests: v1.ResourceList{
					v1.ResourceName(v1.ResourceStorage): resource.MustParse("10Gi"),
				},
			},
		},
	}

	pvcCreated, err := clientset.CoreV1().PersistentVolumeClaims(podFailoverNamespace).Create(context.TODO(), pvc, metav1.CreateOptions{})
	return pvcCreated.Name, err
}

func createDeployment(ctx context.Context, clientset *kubernetes.Clientset, pvcList []string, start int, countOfPvc int) (*apps.Deployment, error) {
	var podReplicas int32 = 1

	var volumes []v1.Volume
	var volumeMounts []v1.VolumeMount
	for index := 0; index < countOfPvc; index++ {
		pvcListIndex := strconv.Itoa(start + index)
		volume := v1.Volume{
			Name: "azuredisk-" + pvcListIndex,
			VolumeSource: v1.VolumeSource{
				PersistentVolumeClaim: &v1.PersistentVolumeClaimVolumeSource{
					ClaimName: pvcList[start+index],
				},
			},
		}

		volumeMount := v1.VolumeMount{
			Name:      "azuredisk-" + pvcListIndex,
			MountPath: "/mnt/azuredisk-" + pvcListIndex,
			ReadOnly:  false,
		}

		volumes = append(volumes, volume)
		volumeMounts = append(volumeMounts, volumeMount)
	}

	mountPath := volumeMounts[0].MountPath
	deploymentName := "azuredisk-pod-failover-tester-" + strconv.Itoa(start/countOfPvc)

	deployment := &apps.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name: deploymentName,
		},
		Spec: apps.DeploymentSpec{
			Replicas: &podReplicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "nginx"},
			},
			Template: v1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "nginx"},
				},
				Spec: v1.PodSpec{
					NodeSelector: map[string]string{"kubernetes.io/os": "linux"},
					Containers: []v1.Container{
						{
							Name:            "volume-tester",
							Image:           *workloadImage,
							VolumeMounts:    volumeMounts,
							ImagePullPolicy: v1.PullAlways,
							Lifecycle: &v1.Lifecycle{
								PreStop: &v1.Handler{
									HTTPGet: &v1.HTTPGetAction{
										Path: "/cleanup",
										Port: intstr.IntOrString{IntVal: 9091},
									},
								},
							},
							Args: []string{"--mount-path=" + mountPath, "--metrics-endpoint=" + *metricsEndpoint},
						},
					},
					RestartPolicy: v1.RestartPolicyAlways,
					Volumes:       volumes,
				},
			},
		}}

	if *driverVersion == "v2" {
		deployment.Spec.Template.Spec.SchedulerName = scheduleExtenderName
	}

	deploymentCreated, err := clientset.AppsV1().Deployments(podFailoverNamespace).Create(context.TODO(), deployment, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	err = waitForDeploymentToComplete(ctx, podFailoverNamespace, clientset, deploymentCreated)

	return deploymentCreated, err
}

func waitForDeploymentToComplete(ctx context.Context, namespace string, clientset *kubernetes.Clientset, deployment *apps.Deployment) error {
	if err := wait.PollImmediate(30*time.Second, 10*time.Minute, func() (bool, error) {
		var err error
		deployment, err = clientset.AppsV1().Deployments(namespace).Get(context.TODO(), deployment.Name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if deployment.Status.ReadyReplicas == *deployment.Spec.Replicas {
			return true, nil
		}
		return false, nil
	}); err != nil {
		return err
	}
	return nil
}

func RunWorkloadPods(ctx context.Context, clientset *kubernetes.Clientset, deployments []*apps.Deployment, stopCh <-chan struct{}) {
	for {
		select {
		case <-stopCh:
			return
		default:
			n := rand.Intn(len(deployments))
			selectedDeployment := deployments[n]
			podList, err := getPodsForDeployment(clientset, selectedDeployment)
			if err != nil {
				klog.Errorf("Error occurred while getting pods for the deployment  %s: %v", selectedDeployment.Name, err)
				return
			}

			for _, pod := range podList.Items {
				nodeName := pod.Spec.NodeName
				makeNodeUnschedulable(nodeName, true, clientset)
				err = clientset.CoreV1().Pods(podFailoverNamespace).Delete(context.TODO(), pod.Name, metav1.DeleteOptions{})
				if err != nil {
					klog.Errorf("Error occurred while deleting the pod  %s: %v", pod.Name, err)
					return
				}

				// wait for the pod to come back up
				err = waitForDeploymentToComplete(ctx, podFailoverNamespace, clientset, selectedDeployment)
				if err != nil {
					klog.Errorf("Error occurred while waiting for the deployment to complete  %s: %v", selectedDeployment.Name, err)
					return
				}
				// Wait for the given time delay
				time.Sleep(time.Duration(*delayBeforeFailover) * time.Second)
				makeNodeUnschedulable(nodeName, false, clientset)
			}
		}
	}
}

func makeNodeUnschedulable(nodeName string, unschedulable bool, clientset *kubernetes.Clientset) {
	node, _ := clientset.CoreV1().Nodes().Get(context.TODO(), nodeName, metav1.GetOptions{})
	nodeTobeCordoned := node.DeepCopy()
	nodeTobeCordoned.Spec.Unschedulable = unschedulable
	// Cordon off the node
	_, err := clientset.CoreV1().Nodes().Update(context.TODO(), nodeTobeCordoned, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("Error occurred in makeNodeUnschedulable; unschedulable: %t, err: %v", unschedulable, err)
	}
}

func getPodsForDeployment(client *kubernetes.Clientset, deployment *apps.Deployment) (*v1.PodList, error) {
	replicaSet, err := deploymentutil.GetNewReplicaSet(deployment, client.AppsV1())
	if err != nil {
		return nil, fmt.Errorf("Failed to get new replica set for deployment %q: %v", deployment.Name, err)
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
		return nil, fmt.Errorf("Failed to list Pods of Deployment %q: %v", deployment.Name, err)
	}
	return podList, nil
}
