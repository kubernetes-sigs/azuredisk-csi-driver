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
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

const (
	AzureDiskContainer = "azuredisk"
	RFC3339Format      = `^\d{4}-(\d{2})-(\d{2})T(\d{2}:\d{2}:\d{2}(.\d+)?)`
	KlogTimeFormat     = `(\d{4}) (\d{2}:\d{2}:\d{2}(.\d+)?)`
)

func GetFlags(cmd *cobra.Command) ([]string, []string, []string, string, string, bool, bool) {
	volumes, _ := cmd.Flags().GetStringSlice("volume")
	nodes, _ := cmd.Flags().GetStringSlice("node")
	requestIds, _ := cmd.Flags().GetStringSlice("request-id")
	since, _ := cmd.Flags().GetString("since")
	sinceTime, _ := cmd.Flags().GetString("since-time")
	isFollow, _ := cmd.Flags().GetBool("follow")
	isPrevious, _ := cmd.Flags().GetBool("previous")

	if since != "" && sinceTime != "" {
		fmt.Println("error: only one of --since/--since-time may be specified")
		os.Exit(0)
	}

	return volumes, nodes, requestIds, since, sinceTime, isFollow, isPrevious
}

func getConfig() *rest.Config {
	kubeconfig := viper.GetString("kubeconfig")

	if kubeconfig == "" {
		if home, _ := homedir.Dir(); home != "" {
			kubeconfig = filepath.Join(home, ".kube", "config")
		}
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

func TimestampFormatValidation(sinceTime string) (time.Time, error) {
	var t time.Time
	var err error

	// sinceTime input validation and convert it to time.Time from string
	if isMatch, _ := regexp.MatchString(RFC3339Format, sinceTime); isMatch {
		t, err = time.Parse(time.RFC3339, sinceTime)
		if err != nil {
			return t, fmt.Errorf("error: %v", err)
		}
	} else if isMatch, _ := regexp.MatchString(KlogTimeFormat, sinceTime); isMatch {
		if isUTC, _ := regexp.MatchString(KlogTimeFormat+`$`, sinceTime); isUTC {
			sinceTime += "Z"
		}

		t, err = time.Parse("20060102 15:04:05Z07:00", fmt.Sprint(time.Now().Year())+sinceTime)
		if err != nil {
			return t, fmt.Errorf("error: %v", err)
		}

	} else {
		return t, fmt.Errorf("\"%v\" is not a valid timestamp format", sinceTime)
	}

	return t, nil
}

func LogTimeFilter(log *string, sinceTime string) bool {
	isMatch, _ := regexp.MatchString(KlogTimeFormat, *log)
	return isMatch && len(*log) > 21 && (*log)[1:21] >= sinceTime
}

func LogFilter(buf *bufio.Scanner, volumes []string, nodes []string, requestIds []string, sinceTime string) {
	var isAfterTime bool
	if sinceTime == "" {
		isAfterTime = true
	} else {
		isAfterTime = false
	}

	for buf.Scan() {
		log := buf.Text()

		if !isAfterTime {
			isAfterTime = LogTimeFilter(&log, sinceTime)
		}

		if isAfterTime {
			if ContainsAny(&log, volumes) && ContainsAny(&log, nodes) && ContainsAny(&log, requestIds) {
				fmt.Println(log)
			}
		}
	}

	err := buf.Err()
	if err != nil {
		log.Fatal(err)
	}
}

func ContainsAny(log *string, objects []string) bool {
	if len(objects) == 0 {
		return true
	}

	for _, obj := range objects {
		if strings.Contains(*log, obj) {
			return true
		}
	}

	return false
}

func GetReleaseNamespace() string {
	releaseNamespace := viper.GetString("releaseNamespace")
	if releaseNamespace == "" {
		releaseNamespace = consts.ReleaseNamespace
	}
	return releaseNamespace
}

func GetDriverInstallationType() string {
	installedSideBySide := viper.GetString("v2InstalledSideBySide")
	if installedSideBySide == "" || installedSideBySide == "false" {
		return "csi-azuredisk"
	}
	return "csi-azuredisk2"
}
