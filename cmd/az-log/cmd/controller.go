/*
Copyright 2022 The Kubernetes Authors.

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

package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// controllerCmd represents the controller command
var controllerCmd = &cobra.Command{
	Use:   "controller",
	Short: "csi-azuredisk2-controller",
	Long:  `csi-azuredisk2-controller`,
	Run: func(cmd *cobra.Command, args []string) {
		volumes, nodes, requestIds, since, sinceTime, isFollow, isPrevious := GetFlags(cmd)
		config := getConfig()
		clientsetK8s := getKubernetesClientset(config)

		pod := GetLeaderControllerPod(clientsetK8s)
		for {
			currPodName := pod.Name
			GetLogsByAzDriverPod(clientsetK8s, currPodName, AzureDiskContainer, volumes, nodes, requestIds, since, sinceTime, isFollow, isPrevious)
			// If in watch mode (--follow) and the pod failover and restarts, keep watching logs from newly created pos in the same node
			if !isFollow {
				break
			} else {
				sinceTime = time.Now().Format(time.RFC3339)
				time.Sleep(20 * time.Second)
				pod = GetLeaderControllerPod(clientsetK8s)
				if pod.Name == currPodName {
					break
				}
			}
		}
	},
}

func init() {
	getCmd.AddCommand(controllerCmd)
}

func GetLeaderControllerPod(clientsetK8s kubernetes.Interface) *v1.Pod {
	// get LeaderElectionNamespace and PartitionName
	leaderElectionNamespace, leaseName := GetLeaderElectionNamespaceAndLeaseName()

	//get node that leader controller pod is running on
	lease, err := clientsetK8s.CoordinationV1().Leases(leaderElectionNamespace).Get(context.Background(), leaseName, metav1.GetOptions{})
	if err != nil {
		panic(err.Error())
	}
	holder := *lease.Spec.HolderIdentity
	node := strings.Split(holder, "_")[0]

	// get pod name of the leader controller
	fieldSelector := fields.SelectorFromSet(map[string]string{"spec.nodeName": node})
	labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app": GetDriverInstallationType() + "-controller"}}
	pods, err := clientsetK8s.CoreV1().Pods(GetReleaseNamespace()).List(context.Background(), metav1.ListOptions{
		FieldSelector: fieldSelector.String(),
		LabelSelector: labels.Set(labelSelector.MatchLabels).String(),
	})
	if err != nil {
		panic(err.Error())
	}

	if len(pods.Items) != 1 {
		fmt.Println("zero or more than one controller plugins were found")
		os.Exit(0)
	}
	return &pods.Items[0]
}
