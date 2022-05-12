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
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

const (
	AzureDiskContainer = "azuredisk"
	RFC3339Format = `^\d{4}-(\d{2})-(\d{2})T(\d{2}:\d{2}:\d{2}(.\d+)?)`
	KlogTimeFormat = `^(\d{4}) (\d{2}:\d{2}:\d{2}(.\d+)?)`
)

func GetFlags(cmd *cobra.Command) ([]string, []string, []string, string, bool, bool){
	volumes, _ := cmd.Flags().GetStringSlice("volume")
	nodes, _ := cmd.Flags().GetStringSlice("node")
	requestIds, _ := cmd.Flags().GetStringSlice("request-id")
	afterTime, _ := cmd.Flags().GetString("since-time")
	isFollow, _ := cmd.Flags().GetBool("follow")
	isPrevious, _ := cmd.Flags().GetBool("previous")

	return volumes, nodes, requestIds, afterTime, isFollow, isPrevious
}

func getConfig() *rest.Config {
	var kubeconfig string
	if home, _ := homedir.Dir(); home != "" {
		kubeconfig = filepath.Join(home, ".kube", "config")
	}

	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	return config
}

func getKubernetesClientset(config *rest.Config) *kubernetes.Clientset {
	clientsetK8s, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	return clientsetK8s
}

func GetLogsByAzDriverPod(clientsetK8s kubernetes.Interface, podName string, container string, volumes []string,
	nodes []string, requestIds []string, sinceTime string, isFollow bool, isPrevious bool) {

	var tt time.Time
	// sinceTime input validation and convert it to time.Time from string
	if sinceTime != "" {
		if isMatch, _ := regexp.MatchString(RFC3339Format, sinceTime); isMatch {
			t, err := time.Parse(time.RFC3339, sinceTime)
			if err != nil {
				fmt.Printf("error: %v\n", err)
				return
			}
			tt = t

		} else if isMatch, _ := regexp.MatchString(KlogTimeFormat, sinceTime); isMatch{
			t, err := time.Parse("20060102 15:04:05", fmt.Sprint(time.Now().Year()) + sinceTime)
			if err != nil {
				fmt.Printf("error: %v\n", err)
				return
			}
			tt = t

		} else {
			fmt.Printf("\"%v\" is not a valid timestamp format\n", sinceTime)
			return
		}
	}

	timestamp := metav1.NewTime(tt)
	podLogOptions := make([]v1.PodLogOptions, 0)

	// If logs from previous container is needed
	if isPrevious {
		podLogOptions = append(podLogOptions, v1.PodLogOptions {
			Container: container,
			Previous: isPrevious,
			Follow: false,
			SinceTime: &timestamp,
		})
	}

	podLogOptions = append(podLogOptions, v1.PodLogOptions {
		Container: container,
		Previous: false,
		Follow: isFollow,
		SinceTime: &timestamp,
	})

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

func LogFilter(buf *bufio.Scanner, volumes []string, nodes []string, requestIds []string, sinceTime string) {
	for buf.Scan() {
        log := buf.Text()

		if sinceTime == "" || log[1:21] >= sinceTime {
			isPrint := true
			if len(volumes) > 0 {
				isPrint = false
				for _, v := range volumes {
					isPrint = strings.Contains(log, v)
					if isPrint {
						break
					}
				}
			}

			if !isPrint {
				fmt.Println("No logs are queried")
				return
			}

			if len(nodes) > 0 {
				isPrint = false
				for _, n := range nodes{
					isPrint = strings.Contains(log, n)
					if isPrint {
						break
					}
				}
			}

			if !isPrint {
				fmt.Println("No logs are queried")
				return
			}

			if len(requestIds) > 0 {
				isPrint = false
				for _, rid := range requestIds{
					isPrint = strings.Contains(log, rid)
					if isPrint {
						break
					}
				}
			}

			if !isPrint {
				fmt.Println("No logs are queried")
				return
			}

			if isPrint {
				fmt.Println(log)
			}
		}
    }

    err := buf.Err()
    if err != nil {
        log.Fatal(err)
    }
}
