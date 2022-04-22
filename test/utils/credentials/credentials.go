/*
Copyright 2019 The Kubernetes Authors.

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
	"fmt"
	"html/template"
	"io/ioutil"
	"log"
	"os"

	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"

	"github.com/pborman/uuid"
	"github.com/pelletier/go-toml"
)

// Config is used in Prow to store Azure credentials
// https://github.com/kubernetes/test-infra/blob/master/kubetest/azure.go#L116-L118
type Config struct {
	Creds FromProw
}

// FromProw is used in Prow to store Azure credentials
// https://github.com/kubernetes/test-infra/blob/master/kubetest/azure.go#L107-L114
type FromProw struct {
	ClientID           string
	ClientSecret       string
	TenantID           string
	SubscriptionID     string
	StorageAccountName string
	StorageAccountKey  string
}

// Credentials is used in Azure Disk CSI driver to store Azure credentials
type Credentials struct {
	Cloud           string
	TenantID        string
	SubscriptionID  string
	AADClientID     string
	AADClientSecret string
	ResourceGroup   string
	Location        string
	VMType          string
}

// CreateAzureCredentialFile creates a temporary Azure credential file for
// Azure Disk CSI driver tests and returns the credentials
func CreateAzureCredentialFile() (*Credentials, error) {
	// Search credentials through env vars first
	var cloud, tenantID, subscriptionID, aadClientID, aadClientSecret, resourceGroup, location, vmType string
	cloud = os.Getenv(testconsts.CloudNameEnvVar)
	if cloud == "" {
		cloud = testconsts.AzurePublicCloud
	}
	tenantID = os.Getenv(testconsts.TenantIDEnvVar)
	subscriptionID = os.Getenv(testconsts.SubscriptionIDEnvVar)
	aadClientID = os.Getenv(testconsts.AadClientIDEnvVar)
	aadClientSecret = os.Getenv(testconsts.AadClientSecretEnvVar)
	resourceGroup = os.Getenv(testconsts.ResourceGroupEnvVar)
	location = os.Getenv(testconsts.LocationEnvVar)
	vmType = os.Getenv(testconsts.VMTypeEnvVar)

	if resourceGroup == "" {
		resourceGroup = testconsts.ResourceGroupPrefix + uuid.NewUUID().String()
	}

	if location == "" {
		location = testconsts.DefaultAzurePublicCloudLocation
	}

	if vmType == "" {
		vmType = testconsts.DefaultAzurePublicCloudVMType
	}

	// Running test locally
	if tenantID != "" && subscriptionID != "" && aadClientID != "" && aadClientSecret != "" {
		return parseAndExecuteTemplate(cloud, tenantID, subscriptionID, aadClientID, aadClientSecret, resourceGroup, location, vmType)
	}

	// If the tests are being run in Prow, credentials are not supplied through env vars. Instead, it is supplied
	// through env var AZURE_CREDENTIALS. We need to convert it to AZURE_CREDENTIAL_FILE for sanity, integration and E2E tests
	if testutil.IsRunningInProw() {
		log.Println("Running in Prow, converting AZURE_CREDENTIALS to AZURE_CREDENTIAL_FILE")
		c, err := getCredentialsFromAzureCredentials(os.Getenv("AZURE_CREDENTIALS"))
		if err != nil {
			return nil, err
		}
		return parseAndExecuteTemplate(cloud, c.TenantID, c.SubscriptionID, c.ClientID, c.ClientSecret, resourceGroup, location, vmType)
	}

	return nil, fmt.Errorf("If you are running tests locally, you will need to set the following env vars: $%s, $%s, $%s, $%s, $%s, $%s",
		testconsts.TenantIDEnvVar, testconsts.SubscriptionIDEnvVar, testconsts.AadClientIDEnvVar, testconsts.AadClientSecretEnvVar, testconsts.ResourceGroupEnvVar, testconsts.LocationEnvVar)
}

// DeleteAzureCredentialFile deletes the temporary Azure credential file
func DeleteAzureCredentialFile() error {
	if err := os.Remove(testconsts.TempAzureCredentialFilePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("error removing %s %v", testconsts.TempAzureCredentialFilePath, err)
	}

	return nil
}

// getCredentialsFromAzureCredentials parses the azure credentials toml (AZURE_CREDENTIALS)
// in Prow and returns the credential information usable to Azure Disk CSI driver
func getCredentialsFromAzureCredentials(azureCredentialsPath string) (*FromProw, error) {
	content, err := ioutil.ReadFile(azureCredentialsPath)
	log.Printf("Reading credentials file %v", azureCredentialsPath)
	if err != nil {
		return nil, fmt.Errorf("error reading credentials file %v %v", azureCredentialsPath, err)
	}

	c := Config{}
	if err := toml.Unmarshal(content, &c); err != nil {
		return nil, fmt.Errorf("error parsing credentials file %v %v", azureCredentialsPath, err)
	}

	return &c.Creds, nil
}

// parseAndExecuteTemplate replaces credential placeholders in azureCredentialFileTemplate with actual credentials
func parseAndExecuteTemplate(cloud, tenantID, subscriptionID, aadClientID, aadClientSecret, resourceGroup, location, vmType string) (*Credentials, error) {
	t := template.New("AzureCredentialFileTemplate")
	t, err := t.Parse(testconsts.AzureCredentialFileTemplate)
	if err != nil {
		return nil, fmt.Errorf("error parsing azureCredentialFileTemplate %v", err)
	}

	f, err := os.Create(testconsts.TempAzureCredentialFilePath)
	if err != nil {
		return nil, fmt.Errorf("error creating %s %v", testconsts.TempAzureCredentialFilePath, err)
	}
	defer f.Close()

	c := Credentials{
		cloud,
		tenantID,
		subscriptionID,
		aadClientID,
		aadClientSecret,
		resourceGroup,
		location,
		vmType,
	}
	err = t.Execute(f, c)
	if err != nil {
		return nil, fmt.Errorf("error executing parsed azure credential file template %v", err)
	}

	return &c, nil
}
