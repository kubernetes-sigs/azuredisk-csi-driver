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

package integration

import (
	"context"
	"flag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"testing"

	"sigs.k8s.io/azuredisk-csi-driver/test/utils/azure"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/credentials"

	"github.com/stretchr/testify/assert"
)

const (
	driverV1 = "v1"
	driverV2 = "v2"
)

var (
	nodeids = []string{"integration-test-node-0", "integration-test-node-1", "integration-test-node-2"}
)

var testDriverVersion = flag.String("test-driver-version", driverV1, "The version of the driver to be tested. Valid values are \"v1\" or \"v2\".")
var imageTag = flag.String("image-tag", "", "A flag to get the docker image tag")

func TestIntegrationOnAzurePublicCloud(t *testing.T) {
	flag.Parse()
	// Test on AzurePublicCloud
	creds, err := credentials.CreateAzureCredentialFile()
	defer func() {
		err := credentials.DeleteAzureCredentialFile()
		azure.AssertNoError(t, err)
	}()
	azure.AssertNoError(t, err)
	azure.AssertNotNil(t, creds)

	// Set necessary env vars for sanity test
	os.Setenv("AZURE_CREDENTIAL_FILE", credentials.TempAzureCredentialFilePath)

	useDriverV2 := strings.EqualFold(*testDriverVersion, driverV2)

	if useDriverV2 {
		os.Setenv("nodeid_0", nodeids[0])
		os.Setenv("nodeid_1", nodeids[1])
		os.Setenv("nodeid_2", nodeids[2])
	}

	azureClient, err := azure.GetAzureClient(creds.Cloud, creds.SubscriptionID, creds.AADClientID, creds.TenantID, creds.AADClientSecret)
	azure.AssertNoError(t, err)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// Create an empty resource group for integration test
	log.Printf("Creating resource group %s in %s", creds.ResourceGroup, creds.Cloud)
	_, err = azureClient.EnsureResourceGroup(ctx, creds.ResourceGroup, creds.Location, nil)
	azure.AssertNoError(t, err)
	defer func() {
		// Only delete resource group the test created
		if strings.HasPrefix(creds.ResourceGroup, credentials.ResourceGroupPrefix) {
			log.Printf("Deleting resource group %s", creds.ResourceGroup)
			err := azureClient.DeleteResourceGroup(context.Background(), creds.ResourceGroup)
			azure.AssertNoError(t, err)
		}
	}()

	// for v2 driver testing, we need multiple VMs for testing AzVolumeAttachment where maxShares > 1
	numVM := 1
	if useDriverV2 {
		numVM = 3
	}
	for i := 0; i < numVM; i++ {
		log.Printf("Creating a VM in %s", creds.ResourceGroup)
		_, err = azureClient.EnsureVirtualMachine(ctx, creds.ResourceGroup, creds.Location, nodeids[i])
		azure.AssertNoError(t, err, 1)
	}

	// Execute the script from project root
	err = os.Chdir("../..")
	azure.AssertNoError(t, err)
	// Change directory back to test/integration in preparation for next test
	defer func() {
		err := os.Chdir("test/integration")
		azure.AssertNoError(t, err)
	}()

	cwd, err := os.Getwd()
	azure.AssertNoError(t, err)
	assert.True(t, strings.HasSuffix(cwd, "azuredisk-csi-driver"))

	args := []string{creds.Cloud}
	if useDriverV2 {
		args = append(args, "v2")
		args = append(args, *imageTag)
	}

	cmd := exec.CommandContext(ctx, "./test/integration/run-tests-all-clouds.sh", args...)
	cmd.Dir = cwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Integration test failed %v", err)
	}
}
