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
	homedir "github.com/mitchellh/go-homedir"
	"github.com/spf13/viper"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"path/filepath"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
)

// access to config
func getConfig() *rest.Config {
	kubeconfig := viper.GetString("kubeconfig")

	// if kubeconfig isn't provided in az-analyze.yaml, using default path "$HOME/.kube/config"
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

func getDriverNamesapce() string{
	return viper.GetString("driverNamespace")
}
