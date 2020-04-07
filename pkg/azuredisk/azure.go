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

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog"
	"k8s.io/legacy-cloud-providers/azure"
)

// GetCloudProvider get Azure Cloud Provider
func GetCloudProvider() (*azure.Cloud, error) {
	klog.V(2).Infof("reading cloud config from secret")
	kubeClient, err := getKubeClient()
	if err != nil {
		return nil, fmt.Errorf("failed to get KubeClient: %v", err)
	}
	az := &azure.Cloud{
		KubeClient: kubeClient,
	}
	az.InitializeCloudFromSecret()

	if az.SubscriptionID == "" {
		klog.V(2).Infof("could not read cloud config from secret")
		credFile, ok := os.LookupEnv("AZURE_CREDENTIAL_FILE")
		if ok {
			klog.V(2).Infof("AZURE_CREDENTIAL_FILE env var set as %v", credFile)
		} else {
			credFile = "/etc/kubernetes/azure.json"
			klog.V(2).Infof("use default AZURE_CREDENTIAL_FILE env var: %v", credFile)
		}

		f, err := os.Open(credFile)
		if err != nil {
			klog.Errorf("Failed to load config from file: %s", credFile)
			return nil, fmt.Errorf("Failed to load config from file: %s, cloud not get azure cloud provider", credFile)
		}
		defer f.Close()

		return azure.NewCloudWithoutFeatureGates(f)
	}

	klog.V(2).Infof("read cloud config from secret successfully")
	return az, nil
}

func getKubeClient() (*kubernetes.Clientset, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}
