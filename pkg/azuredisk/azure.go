/*
Copyright 2017 The Kubernetes Authors.

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

package azuredisk

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	clientSet "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

var (
	// DefaultAzureCredentialFileEnv is the default azure credentials file env variable
	DefaultAzureCredentialFileEnv = "AZURE_CREDENTIAL_FILE"
	// DefaultCredFilePathLinux is default creds file for linux machine
	DefaultCredFilePathLinux = "/etc/kubernetes/azure.json"
	// DefaultCredFilePathWindows is default creds file for windows machine
	DefaultCredFilePathWindows = "C:\\k\\azure.json"
)

// IsAzureStackCloud decides whether the driver is running on Azure Stack Cloud.
func IsAzureStackCloud(cloud string, disableAzureStackCloud bool) bool {
	return !disableAzureStackCloud && strings.EqualFold(cloud, azureStackCloud)
}

// GetCloudProvider get Azure Cloud Provider
func GetCloudProvider(kubeconfig, secretName, secretNamespace, userAgent string) (*azure.Cloud, error) {
	az := &azure.Cloud{
		InitSecretConfig: azure.InitSecretConfig{
			SecretName:      secretName,
			SecretNamespace: secretNamespace,
			CloudConfigKey:  "cloud-config",
		},
	}

	kubeClient, err := getKubeClient(kubeconfig)
	if err != nil {
		klog.Warningf("get kubeconfig(%s) failed with error: %v", kubeconfig, err)
		if !os.IsNotExist(err) && err != rest.ErrNotInCluster {
			return az, fmt.Errorf("failed to get KubeClient: %v", err)
		}
	}
	var (
		config     *azure.Config
		fromSecret bool
	)

	if kubeClient != nil {
		klog.V(2).Infof("reading cloud config from secret %s/%s", az.SecretNamespace, az.SecretName)
		az.KubeClient = kubeClient
		config, err = az.GetConfigFromSecret()
		if err == nil && config != nil {
			fromSecret = true
		}
		if err != nil {
			klog.Warningf("InitializeCloudFromSecret: failed to get cloud config from secret %s/%s: %v", az.SecretNamespace, az.SecretName, err)
		}
	}

	if config == nil {
		klog.V(2).Infof("could not read cloud config from secret %s/%s", az.SecretNamespace, az.SecretName)
		credFile, ok := os.LookupEnv(DefaultAzureCredentialFileEnv)
		if ok && strings.TrimSpace(credFile) != "" {
			klog.V(2).Infof("%s env var set as %v", DefaultAzureCredentialFileEnv, credFile)
		} else {
			if util.IsWindowsOS() {
				credFile = DefaultCredFilePathWindows
			} else {
				credFile = DefaultCredFilePathLinux
			}
			klog.V(2).Infof("use default %s env var: %v", DefaultAzureCredentialFileEnv, credFile)
		}

		credFileConfig, err := os.Open(credFile)
		if err != nil {
			klog.Warningf("load azure config from file(%s) failed with %v", credFile, err)
		} else {
			defer credFileConfig.Close()
			klog.V(2).Infof("read cloud config from file: %s successfully", credFile)
			if config, err = azure.ParseConfig(credFileConfig); err != nil {
				klog.Warningf("parse config file(%s) failed with error: %v", credFile, err)
			}
		}
	}

	if config == nil {
		klog.V(2).Infof("no cloud config provided, error: %v, driver will run without cloud config", err)
	} else {
		config.UserAgent = userAgent
		if err = az.InitializeCloudFromConfig(config, fromSecret, false); err != nil {
			klog.Warningf("InitializeCloudFromConfig failed with error: %v", err)
		}
	}

	// reassign kubeClient
	if kubeClient != nil && az.KubeClient == nil {
		az.KubeClient = kubeClient
	}
	return az, nil
}

// GetKubeConfig gets config object from config file
func GetKubeConfig(kubeconfig string) (config *rest.Config, err error) {

	if kubeconfig == "" {
		// if kubeconfig path is not passed
		// read the incluster config
		config, err = rest.InClusterConfig()

		// if we couldn't get the in-cluster config
		// get kubeconfig path from environment variable
		if err != nil {
			kubeconfig = os.Getenv("KUBECONFIG")
			if kubeconfig == "" {
				kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
			}
		} else {
			return config, err
		}
	}

	config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)

	return config, err
}

func getKubeClient(kubeconfig string) (*clientSet.Clientset, error) {
	config, err := GetKubeConfig(kubeconfig)
	if err != nil {
		return nil, err
	}

	return clientSet.NewForConfig(config)
}
