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
	"io/ioutil"
	"os"
	"testing"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

var (
	KubeConfigFileEnvVar = "KUBECONFIG"
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

				existingConfigPath, err := createConfigFileAndSetEnv(validKubeConfigPath, validKubeConfigContent, KubeConfigFileEnvVar)
				if len(existingConfigPath) > 0 {
					defer cleanConfigAndRestoreEnv(validKubeConfigPath, KubeConfigFileEnvVar, existingConfigPath)
				}
				if err != nil {
					t.Error(err)
				}

				d, _ := NewFakeDriver(t)
				d.Run("tcp://127.0.0.1:0", "", true, true)
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

				existingConfigPath, err := createConfigFileAndSetEnv(validKubeConfigPath, validKubeConfigContent, KubeConfigFileEnvVar)
				if len(existingConfigPath) > 0 {
					defer cleanConfigAndRestoreEnv(validKubeConfigPath, KubeConfigFileEnvVar, existingConfigPath)
				}
				if err != nil {
					t.Error(err)
				}

				d, _ := NewFakeDriver(t)
				d.setCloud(&azure.Cloud{})
				d.setNodeID("")
				d.Run("tcp://127.0.0.1:0", "", true, true)
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, tc.testFunc)
	}
}

func createConfigFileAndSetEnv(path string, content string, envVariableName string) (string, error) {
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if err != nil {
		return "", err
	}

	if err := ioutil.WriteFile(path, []byte(content), 0666); err != nil {
		return "", err
	}

	envValue, _ := os.LookupEnv(envVariableName)
	err = os.Setenv(envVariableName, path)
	if err != nil {
		return "", fmt.Errorf("Failed to set env variable")
	}

	return envValue, err
}

func cleanConfigAndRestoreEnv(path string, envVariableName string, envValue string) {
	defer os.Setenv(envVariableName, envValue)
	os.Remove(path)
}
