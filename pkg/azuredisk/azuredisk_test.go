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
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"reflect"
	"testing"
	"time"

	clientset "k8s.io/client-go/kubernetes"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

func TestRun(t *testing.T) {
	fakeCredFile := "fake-cred-file.json"
	fakeCredContent := `{
    "tenantId": "1234",
    "subscriptionId": "12345",
    "aadClientId": "123456",
    "aadClientSecret": "1234567",
    "resourceGroup": "rg1",
    "location": "loc"
}`

	validKubeConfigPath := "valid-Kube-Config-Path"
	validKubeConfigContent := `
    apiVersion: v1
    clusters:
    - cluster:
        server: https://foo-cluster-dns-57e0bda1.hcp.westus2.azmk8s.io:443
      name: foo-cluster
    contexts:
    - context:
        cluster: foo-cluster
        user: clusterUser_abhib-resources_foo-cluster
      name: foo-cluster
    current-context: foo-cluster
    kind: Config
    preferences: {}
    users:
    - name: clusterUser_abhib-resources_foo-cluster
      user:
`

	testCases := []struct {
		name     string
		testFunc func(t *testing.T)
	}{
		{
			name: "Successful run",
			testFunc: func(t *testing.T) {
				if err := ioutil.WriteFile(fakeCredFile, []byte(fakeCredContent), 0666); err != nil {
					t.Error(err)
				}

				defer func() {
					if err := os.Remove(fakeCredFile); err != nil {
						t.Error(err)
					}
				}()

				originalCredFile, ok := os.LookupEnv(consts.DefaultAzureCredentialFileEnv)
				if ok {
					defer os.Setenv(consts.DefaultAzureCredentialFileEnv, originalCredFile)
				} else {
					defer os.Unsetenv(consts.DefaultAzureCredentialFileEnv)
				}
				os.Setenv(consts.DefaultAzureCredentialFileEnv, fakeCredFile)

				err := createConfigFile(validKubeConfigPath, validKubeConfigContent)
				defer deleteConfig(validKubeConfigPath)
				if err != nil {
					t.Error(err)
				}

				d, _ := NewFakeDriver(t)
				go d.Run("tcp://127.0.0.1:0", validKubeConfigPath, true, true)
				select {
				case <-d.Ready():
				case <-time.After(30 * time.Second):
					t.Error("Driver failed to ready within timeout")
				}
			},
		},
		{
			name: "Successful run with node ID missing",
			testFunc: func(t *testing.T) {
				if err := ioutil.WriteFile(fakeCredFile, []byte(fakeCredContent), 0666); err != nil {
					t.Error(err)
				}

				defer func() {
					if err := os.Remove(fakeCredFile); err != nil {
						t.Error(err)
					}
				}()

				originalCredFile, ok := os.LookupEnv(consts.DefaultAzureCredentialFileEnv)
				if ok {
					defer os.Setenv(consts.DefaultAzureCredentialFileEnv, originalCredFile)
				} else {
					defer os.Unsetenv(consts.DefaultAzureCredentialFileEnv)
				}
				os.Setenv(consts.DefaultAzureCredentialFileEnv, fakeCredFile)

				err := createConfigFile(validKubeConfigPath, validKubeConfigContent)
				defer deleteConfig(validKubeConfigPath)

				if err != nil {
					t.Error(err)
				}

				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				d.setNodeID("")
				go d.Run("tcp://127.0.0.1:0", validKubeConfigPath, true, true)
				select {
				case <-d.Ready():
				case <-time.After(30 * time.Second):
					t.Error("Driver failed to ready within timeout")
				}
			},
		},
		{
			name: "Successful run with vmss VMType",
			testFunc: func(t *testing.T) {
				if err := ioutil.WriteFile(fakeCredFile, []byte(fakeCredContent), 0666); err != nil {
					t.Error(err)
				}

				defer func() {
					if err := os.Remove(fakeCredFile); err != nil {
						t.Error(err)
					}
				}()

				originalCredFile, ok := os.LookupEnv(consts.DefaultAzureCredentialFileEnv)
				if ok {
					defer os.Setenv(consts.DefaultAzureCredentialFileEnv, originalCredFile)
				} else {
					defer os.Unsetenv(consts.DefaultAzureCredentialFileEnv)
				}
				os.Setenv(consts.DefaultAzureCredentialFileEnv, fakeCredFile)

				d := newDriverV1(&azdiskv1beta2.AzDiskDriverConfiguration{
					NodeConfig: azdiskv1beta2.NodeConfiguration{
						NodeID:                 "",
						EnablePerfOptimization: true,
					},
					DriverName: consts.DefaultDriverName,
					ControllerConfig: azdiskv1beta2.ControllerConfiguration{
						EnableListVolumes:   true,
						EnableListSnapshots: true,
						VMType:              "vmss",
					},
					CloudConfig: azdiskv1beta2.CloudConfiguration{
						VMSSCacheTTLInSeconds: 10,
					},
				})
				d.Run("tcp://127.0.0.1:0", "", true, true)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func createConfigFile(path string, content string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := ioutil.WriteFile(path, []byte(content), 0666); err != nil {
		return err
	}
	return nil
}

func deleteConfig(path string) {
	os.Remove(path)
}

func TestGetNodeInfoFromLabels(t *testing.T) {
	tests := []struct {
		nodeName      string
		kubeClient    clientset.Interface
		expectedError error
	}{
		{
			nodeName:      "",
			kubeClient:    nil,
			expectedError: fmt.Errorf("kubeClient is nil"),
		},
	}

	for _, test := range tests {
		_, _, err := getNodeInfoFromLabels(context.TODO(), test.nodeName, test.kubeClient)
		if !reflect.DeepEqual(err, test.expectedError) {
			t.Errorf("Unexpected result: %v, expected result: %v", err, test.expectedError)
		}
	}
}
