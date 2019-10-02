package integration

import (
	"context"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/kubernetes-sigs/azuredisk-csi-driver/test/utils/azure"
	"github.com/kubernetes-sigs/azuredisk-csi-driver/test/utils/credentials"
	"github.com/kubernetes-sigs/azuredisk-csi-driver/test/utils/testutil"
	"github.com/stretchr/testify/assert"
)

const (
	nodeid = "integration-test-node"
)

func TestIntegrationOnAzurePublicCloud(t *testing.T) {
	// Test on AzurePublicCloud
	creds, err := credentials.CreateAzureCredentialFile(false)
	defer func() {
		err := credentials.DeleteAzureCredentialFile()
		assert.NoError(t, err)
	}()
	assert.NoError(t, err)
	assert.NotNil(t, creds)

	testIntegration(t, creds)
}

func TestIntegrationOnAzureChinaCloud(t *testing.T) {
	if testutil.IsRunningInProw() {
		t.Skipf("Skipping integration test on Azure China Cloud because Prow only tests on Azure Public Cloud at the moment")
	}

	// Test on AzureChinaCloud
	creds, err := credentials.CreateAzureCredentialFile(true)
	defer func() {
		err := credentials.DeleteAzureCredentialFile()
		assert.NoError(t, err)
	}()

	if err != nil {
		// Skip the test if Azure China Cloud credentials are not supplied
		t.Skipf("Skipping integration test on Azure China Cloud due to the following error %v", err)
	}
	assert.NotNil(t, creds)
	testIntegration(t, creds)
}

func testIntegration(t *testing.T, creds *credentials.Credentials) {
	// Set necessary env vars for sanity test
	os.Setenv("AZURE_CREDENTIAL_FILE", credentials.TempAzureCredentialFilePath)
	os.Setenv("nodeid", nodeid)

	azureClient, err := azure.GetAzureClient(creds.Cloud, creds.SubscriptionID, creds.AADClientID, creds.TenantID, creds.AADClientSecret)
	assert.NoError(t, err)

	ctx := context.Background()
	// Create an empty resource group for integration test
	log.Printf("Creating resource group %s in %s", creds.ResourceGroup, creds.Cloud)
	_, err = azureClient.EnsureResourceGroup(ctx, creds.ResourceGroup, creds.Location, nil)
	assert.NoError(t, err)
	defer func() {
		// Only delete resource group the test created
		if strings.HasPrefix(creds.ResourceGroup, credentials.ResourceGroupPrefix) {
			log.Printf("Deleting resource group %s", creds.ResourceGroup)
			err := azureClient.DeleteResourceGroup(ctx, creds.ResourceGroup)
			assert.NoError(t, err)
		}
	}()

	log.Printf("Creating a VM in %s", creds.ResourceGroup)
	_, err = azureClient.EnsureVirtualMachine(ctx, creds.ResourceGroup, creds.Location, nodeid)
	assert.NoError(t, err)

	// Execute the script from project root
	err = os.Chdir("../..")
	assert.NoError(t, err)
	// Change directory back to test/integration in preparation for next test
	defer func() {
		err := os.Chdir("test/integration")
		assert.NoError(t, err)
	}()

	cwd, err := os.Getwd()
	assert.NoError(t, err)
	assert.True(t, strings.HasSuffix(cwd, "azuredisk-csi-driver"))

	cmd := exec.Command("./test/integration/run-tests-all-clouds.sh", creds.Cloud)
	cmd.Dir = cwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Integration test failed %v", err)
	}
}
