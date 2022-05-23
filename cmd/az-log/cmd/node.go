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
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

// nodeCmd represents the node command
var nodeCmd = &cobra.Command{
	Use:   "node",
	Short: "csi-azuredisk2-node",
	Long:  `csi-azuredisk2-node`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) == 0 {
			fmt.Println("node name is a required argument for \"node\" command")
			os.Exit(0)
		}

		config := getConfig()
		clientsetK8s := getKubernetesClientset(config)
		volumes, nodes, requestIds, since, sinceTime, isFollow, isPrevious := GetFlags(cmd)
		nodeName := args[0]

		pod := GetAzurediskPodFromNode(clientsetK8s, nodeName)
		for {
			currPodName := pod.Name
			GetLogsByAzDriverPod(clientsetK8s, currPodName, AzureDiskContainer, volumes, nodes, requestIds, since, sinceTime, isFollow, isPrevious)
			// If in watch mode (--follow) and the pod failover and restarts, keep watching logs from newly created pos in the same node
			if !isFollow {
				break
			} else {
				time.Sleep(10 * time.Second)
				pod = GetAzurediskPodFromNode(clientsetK8s, nodeName)
				if pod.Name == currPodName {
					break
				}
			}
		}
	},
}

func init() {
	getCmd.AddCommand(nodeCmd)
}

func GetAzurediskPodFromNode(clientsetK8s kubernetes.Interface, node string) *v1.Pod {
	// Get pod name of the azuredisk2-node running on given node
	pods, err := clientsetK8s.CoreV1().Pods(consts.ReleaseNamespace).List(context.Background(), metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + node,
		LabelSelector: "app=csi-azuredisk2-node",
	})
	if err != nil {
		panic(err.Error())
	}
	if len(pods.Items) > 1 {
		panic(errors.New("more than one node pods were found"))
	}

	return &pods.Items[0]
}
