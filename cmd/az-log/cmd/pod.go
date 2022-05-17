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
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	v1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// podCmd represents the pod command
var podCmd = &cobra.Command{
	Use:   "pod",
	Short: "A brief description of your command",
	Long: `A longer description that spans multiple lines and likely contains examples
and usage of using your command. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
	Run: func(cmd *cobra.Command, args []string) {
		names := strings.Split(args[0], "/")
		if len(names) < 2 {
			fmt.Println("pod and container are required arguments for \"pod\" command")
			os.Exit(0)
		}

		podName := names[0]
		container := names[1]
		volumes, nodes, requestIds, since, sinceTime, isFollow, isPrevious := GetFlags(cmd)

		config := getConfig()
		clientsetK8s := getKubernetesClientset(config)

		GetLogsByAzDriverPod(clientsetK8s, podName, container, volumes, nodes, requestIds, since, sinceTime, isFollow, isPrevious)
	},
}

func init() {
	getCmd.AddCommand(podCmd)
}

func GetLogsByAzDriverPod(clientsetK8s kubernetes.Interface, podName string, container string, volumes []string,
	nodes []string, requestIds []string, since string, sinceTime string, isFollow bool, isPrevious bool) {

	v1PodLogOptions := v1.PodLogOptions{
		Container: container,
		Follow:    isFollow,
	}

	// If since/sinceTime is specified, data type conversion and set up PodLogOptions
	if since != "" {
		d, err := time.ParseDuration(since)
		if err != nil {
			fmt.Println(err.Error())
			os.Exit(0)
		}
		timeDuration := int64(d.Seconds())
		v1PodLogOptions.SinceSeconds = &timeDuration
	} else if sinceTime != "" {
		t, err := TimestampFormatValidation(sinceTime)
		if err != nil {
			fmt.Println(err.Error())
			os.Exit(0)
		}
		timestamp := metav1.NewTime(t)
		v1PodLogOptions.SinceTime = &timestamp
	}

	podLogOptions := make([]v1.PodLogOptions, 0)

	// If logs from previous container is needed
	if isPrevious {
		v1PodLogOptions.Previous = true
		podLogOptions = append(podLogOptions, v1PodLogOptions)
		v1PodLogOptions.Previous = false
	}

	podLogOptions = append(podLogOptions, v1PodLogOptions)

	for i := 0; i < len(podLogOptions); i++ {
		req := clientsetK8s.CoreV1().Pods(consts.ReleaseNamespace).GetLogs(podName, &podLogOptions[i])
		podLogs, err := req.Stream(context.TODO())
		if err != nil {
			panic(err.Error())
		}

		defer podLogs.Close()

		buf := bufio.NewScanner(podLogs)
		LogFilter(buf, volumes, nodes, requestIds, "")
	}
}
