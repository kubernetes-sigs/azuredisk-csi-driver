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
	"bytes"
	"fmt"
	"path/filepath"
	"time"

	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/viper"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

// access to config
func getConfig() *rest.Config {
	kubeconfig := viper.GetString("kubeconfig")

	// if kubeconfig isn't provided in config file, using default path "$HOME/.kube/config"
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
func getAzDiskClientset(config *rest.Config) *azDiskClientSet.Clientset {
	clientsetAzDisk, err := azDiskClientSet.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	return clientsetAzDisk
}

// if driverNamespace isn't provided in config file, using default one
func getDriverNamesapce() string {
	driverNamespace := viper.GetString("driverNamespace")
	if driverNamespace == "" {
		driverNamespace = consts.DefaultAzureDiskCrdNamespace
	}
	return driverNamespace
}

func timeFmt(t time.Duration) string {
	day := t / (24 * time.Hour)
	t = t % (24 * time.Hour)
	hour := t / time.Hour
	t = t % time.Hour
	minute := t / time.Minute
	t = t % time.Minute
	second := int(t / time.Second)

	var buffer bytes.Buffer
	if day > 0 {
		buffer.WriteString(fmt.Sprintf("%dd", day))
	}
	if hour > 0 {
		buffer.WriteString(fmt.Sprintf("%dh", hour))
		return buffer.String()
	}
	if minute > 0 {
		buffer.WriteString(fmt.Sprintf("%dm", minute))
	}
	if second > 0 {
		buffer.WriteString(fmt.Sprintf("%ds", second))
	}
	return buffer.String()
}
