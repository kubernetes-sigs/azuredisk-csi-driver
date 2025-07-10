/*
Copyright 2020 The Kubernetes Authors.

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

package credentials

import (
	"bytes"
	"os"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
)

const (
	fakeAzureCredentials = `
	[Creds]
	ClientID = "df7269f2-xxxx-xxxx-xxxx-0f12a7d97404"
	ClientSecret = "8c416dc5-xxxx-xxxx-xxxx-d77069e2a255"
	TenantID = "72f988bf-xxxx-xxxx-xxxx-2d7cd011db47"
	SubscriptionID = "b9d2281e-xxxx-xxxx-xxxx-0d50377cdf76"
	StorageAccountName = "TestStorageAccountName"
	StorageAccountKey = "TestStorageAccountKey"
	`
	testTenantID              = "test-tenant-id"
	testSubscriptionID        = "test-subscription-id"
	testAadClientID           = "test-aad-client-id"
	testAadClientSecret       = "test-aad-client-secret"
	testResourceGroup         = "test-resource-group"
	testLocation              = "test-location"
	testVMType                = "standard"
	testAADFederatedTokenFile = "/tmp/federated-token-file"
)

func TestCreateAzureCredentialFileOnAzurePublicCloud(t *testing.T) {
	t.Run("WithEnvironmentVariables", func(t *testing.T) {
		t.Setenv(tenantIDEnvVar, testTenantID)
		t.Setenv(subscriptionIDEnvVar, testSubscriptionID)
		t.Setenv(aadClientIDEnvVar, testAadClientID)
		t.Setenv(aadClientSecretEnvVar, testAadClientSecret)
		t.Setenv(resourceGroupEnvVar, testResourceGroup)
		t.Setenv(locationEnvVar, testLocation)
		t.Setenv(vmTypeEnvVar, testVMType)
		t.Setenv(federatedTokenFileVar, testAADFederatedTokenFile)
		withEnvironmentVariables(t)
	})
}

func TestCreateAzureCredentialFileOnAzureStackCloud(t *testing.T) {
	t.Run("WithEnvironmentVariables", func(t *testing.T) {
		t.Setenv(cloudNameEnvVar, "AzureStackCloud")
		t.Setenv(tenantIDEnvVar, testTenantID)
		t.Setenv(subscriptionIDEnvVar, testSubscriptionID)
		t.Setenv(aadClientIDEnvVar, testAadClientID)
		t.Setenv(aadClientSecretEnvVar, testAadClientSecret)
		t.Setenv(resourceGroupEnvVar, testResourceGroup)
		t.Setenv(locationEnvVar, testLocation)
		t.Setenv(vmTypeEnvVar, testVMType)
		t.Setenv(federatedTokenFileVar, testAADFederatedTokenFile)
		withEnvironmentVariables(t)
	})
}

func withEnvironmentVariables(t *testing.T) {
	creds, err := CreateAzureCredentialFile()
	defer func() {
		err := DeleteAzureCredentialFile()
		assert.NoError(t, err)
	}()
	assert.NoError(t, err)

	var cloud string
	cloud = os.Getenv(cloudNameEnvVar)
	if cloud == "" {
		cloud = AzurePublicCloud
	}

	assert.Equal(t, cloud, creds.Cloud)
	assert.Equal(t, testTenantID, creds.TenantID)
	assert.Equal(t, testSubscriptionID, creds.SubscriptionID)
	assert.Equal(t, testAadClientID, creds.AADClientID)
	assert.Equal(t, testAadClientSecret, creds.AADClientSecret)
	assert.Equal(t, testResourceGroup, creds.ResourceGroup)
	assert.Equal(t, testLocation, creds.Location)
	assert.Equal(t, testVMType, creds.VMType)
	assert.Equal(t, testAADFederatedTokenFile, creds.AADFederatedTokenFile)

	azureCredentialFileContent, err := os.ReadFile(TempAzureCredentialFilePath)
	assert.NoError(t, err)

	const expectedAzureCredentialFileContent = `
	{
		"cloud": "{{.Cloud}}",
		"tenantId": "test-tenant-id",
		"subscriptionId": "test-subscription-id",
		"aadClientId": "test-aad-client-id",
		"aadClientSecret": "test-aad-client-secret",
		"resourceGroup": "test-resource-group",
		"location": "test-location",
		"vmType": "standard",
		"aadFederatedTokenFile": "/tmp/federated-token-file"
	}
	`
	tmpl := template.New("expectedAzureCredentialFileContent")
	tmpl, err = tmpl.Parse(expectedAzureCredentialFileContent)
	assert.NoError(t, err)

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, struct {
		Cloud string
	}{
		cloud,
	})
	assert.NoError(t, err)
	assert.JSONEq(t, buf.String(), string(azureCredentialFileContent))
}

func TestDeleteAzureCredentialFile(t *testing.T) {
	tests := []struct {
		name            string
		setupFile       bool
		expectedError   bool
	}{
		{
			name:          "delete existing file",
			setupFile:     true,
			expectedError: false,
		},
		{
			name:          "delete non-existing file (should not error)",
			setupFile:     false,
			expectedError: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Clean up any existing file first
			os.Remove(TempAzureCredentialFilePath)

			if test.setupFile {
				// Create a temporary file
				file, err := os.Create(TempAzureCredentialFilePath)
				assert.NoError(t, err)
				file.WriteString("test content")
				file.Close()
			}

			err := DeleteAzureCredentialFile()
			
			if test.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// File should not exist after deletion
			_, err = os.Stat(TempAzureCredentialFilePath)
			assert.True(t, os.IsNotExist(err))
		})
	}
}
