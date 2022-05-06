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
	"path/filepath"
	"time"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

func GetFlags(cmd *cobra.Command) ([]string, []string, []string, string, bool, bool){
	volumes, _ := cmd.Flags().GetStringSlice("volume")
	nodes, _ := cmd.Flags().GetStringSlice("node")
	requestIds, _ := cmd.Flags().GetStringSlice("request-id")
	afterTime, _ := cmd.Flags().GetString("after-time")
	isWatch, _ := cmd.Flags().GetBool("volume")
	isPrevious, _ := cmd.Flags().GetBool("volume")

	return volumes, nodes, requestIds, afterTime, isWatch, isPrevious
}

func GetLogsByAzDriverPod(podName string, container string, volumes []string, nodes []string, requestIds []string, afterTime string, isWatch bool, isPrevious bool) {
	var kubeconfig string
	if home, _ := homedir.Dir(); home != "" {
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	clientsetK8s, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}

	t, _ := time.Parse(time.RFC3339, afterTime)
	sinceTime := metav1.NewTime(t)

	podLogOptions := v1.PodLogOptions{
        Container: container,
		Previous: isPrevious,
		SinceTime: &sinceTime,
    }

	req := clientsetK8s.CoreV1().Pods(consts.ReleaseNamespace).GetLogs(podName, &podLogOptions)
	podLogs, err := req.Stream(context.TODO())
	if err != nil {
		panic(err.Error())
	}

	defer podLogs.Close()

	// buf := new(bytes.Buffer)
    // _, err = io.Copy(buf, podLogs)
    // if err != nil {
    //     panic(err.Error())
    // }
    // str := buf.String()

	buf := bufio.NewScanner(podLogs)
	for buf.Scan() {
        fmt.Println(buf.Text()) // TODO: filter volumes, nodes, requestIds
    }
}

