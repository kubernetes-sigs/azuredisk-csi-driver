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

package e2e

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/reporters"
	"github.com/onsi/gomega"
	"github.com/pborman/uuid"
	"k8s.io/kubernetes/test/e2e/framework"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/azure"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/credentials"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
)

const (
	kubeconfigEnvVar    = "KUBECONFIG"
	reportDirEnvVar     = "ARTIFACTS"
	testMigrationEnvVar = "TEST_MIGRATION"
	testWindowsEnvVar   = "TEST_WINDOWS"
	defaultReportDir    = "/workspace/_artifacts"
	inTreeStorageClass  = "kubernetes.io/azure-disk"
)

var (
	azurediskDriver           *azuredisk.Driver
	isUsingInTreeVolumePlugin = os.Getenv(driver.AzureDriverNameVar) == inTreeStorageClass
	isTestingMigration        = os.Getenv(testMigrationEnvVar) != ""
	isWindowsCluster          = os.Getenv(testWindowsEnvVar) != ""
)

type testCmd struct {
	command  string
	args     []string
	startLog string
	endLog   string
}

var _ = ginkgo.BeforeSuite(func() {
	// k8s.io/kubernetes/test/e2e/framework requires env KUBECONFIG to be set
	// it does not fall back to defaults
	if os.Getenv(kubeconfigEnvVar) == "" {
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		os.Setenv(kubeconfigEnvVar, kubeconfig)
	}
	framework.HandleFlags()
	framework.AfterReadingAllFlags(&framework.TestContext)

	// Default storage driver configuration is CSI. Freshly built
	// CSI driver is installed for that case.
	if testutil.IsRunningInProw() && (isTestingMigration || !isUsingInTreeVolumePlugin) {
		creds, err := credentials.CreateAzureCredentialFile(false)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		azureClient, err := azure.GetAzureClient(creds.Cloud, creds.SubscriptionID, creds.AADClientID, creds.TenantID, creds.AADClientSecret)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = azureClient.EnsureResourceGroup(context.Background(), creds.ResourceGroup, creds.Location, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Install Azure Disk CSI Driver on cluster from project root
		e2eBootstrap := testCmd{
			command:  "make",
			args:     []string{"e2e-bootstrap"},
			startLog: "Installing Azure Disk CSI Driver...",
			endLog:   "Azure Disk CSI Driver installed",
		}
		execTestCmd([]testCmd{e2eBootstrap})

		nodeid := os.Getenv("nodeid")
		azurediskDriver = azuredisk.NewDriver(nodeid)
		kubeconfig := os.Getenv(kubeconfigEnvVar)
		go func() {
			os.Setenv("AZURE_CREDENTIAL_FILE", credentials.TempAzureCredentialFilePath)
			azurediskDriver.Run(fmt.Sprintf("unix:///tmp/csi-%s.sock", uuid.NewUUID().String()), kubeconfig)
		}()
	}
})

var _ = ginkgo.AfterSuite(func() {
	// Default storage driver configuration is CSI. Freshly built
	// CSI driver is installed for that case.
	if testutil.IsRunningInProw() {
		azurediskLog := testCmd{
			command:  "bash",
			args:     []string{"test/utils/azuredisk_log.sh"},
			startLog: "===================azuredisk log===================",
			endLog:   "===================================================",
		}
		if isTestingMigration || !isUsingInTreeVolumePlugin {
			e2eTeardown := testCmd{
				command:  "make",
				args:     []string{"e2e-teardown"},
				startLog: "Uninstalling Azure Disk CSI Driver...",
				endLog:   "Azure Disk CSI Driver uninstalled",
			}
			execTestCmd([]testCmd{azurediskLog, e2eTeardown})

			// install/uninstall Azure Disk CSI Driver deployment scripts test
			installDriver := testCmd{
				command:  "bash",
				args:     []string{"deploy/install-driver.sh", "master", "windows,snapshot,local"},
				startLog: "===================install Azure Disk CSI Driver deployment scripts test===================",
				endLog:   "===================================================",
			}
			uninstallDriver := testCmd{
				command:  "bash",
				args:     []string{"deploy/uninstall-driver.sh", "master", "windows,snapshot,local"},
				startLog: "===================uninstall Azure Disk CSI Driver deployment scripts test===================",
				endLog:   "===================================================",
			}
			execTestCmd([]testCmd{installDriver, uninstallDriver})

			err := credentials.DeleteAzureCredentialFile()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	}
})

func TestE2E(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	reportDir := os.Getenv(reportDirEnvVar)
	if reportDir == "" {
		reportDir = defaultReportDir
	}
	r := []ginkgo.Reporter{reporters.NewJUnitReporter(path.Join(reportDir, "junit_01.xml"))}
	ginkgo.RunSpecsWithDefaultAndCustomReporters(t, "AzureDisk CSI Driver End-to-End Tests", r)
}

func execTestCmd(cmds []testCmd) {
	err := os.Chdir("../..")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer func() {
		err := os.Chdir("test/e2e")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}()

	projectRoot, err := os.Getwd()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(strings.HasSuffix(projectRoot, "azuredisk-csi-driver")).To(gomega.Equal(true))

	for _, cmd := range cmds {
		log.Println(cmd.startLog)
		cmdSh := exec.Command(cmd.command, cmd.args...)
		cmdSh.Dir = projectRoot
		cmdSh.Stdout = os.Stdout
		cmdSh.Stderr = os.Stderr
		err = cmdSh.Run()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		log.Println(cmd.endLog)
	}
}

func skipIfTestingInWindowsCluster() {
	if isWindowsCluster {
		ginkgo.Skip("test case not supported by Windows clusters")
	}
}

func skipIfUsingInTreeVolumePlugin() {
	if isUsingInTreeVolumePlugin {
		ginkgo.Skip("test case is only available for CSI drivers")
	}
}

func convertToPowershellCommandIfNecessary(command string) string {
	if !isWindowsCluster {
		return command
	}

	switch command {
	case "echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data":
		return "echo 'hello world' | Out-File -FilePath C:\\mnt\\test-1\\data.txt; Get-Content C:\\mnt\\test-1\\data.txt | findstr 'hello world'"
	case "touch /mnt/test-1/data":
		return "echo $null >> C:\\mnt\\test-1\\data"
	case "while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done":
		return "while (1) { Add-Content -Encoding Unicode C:\\mnt\\test-1\\data.txt $(Get-Date -Format u); sleep 3600 }"
	case "echo 'hello world' >> /mnt/test-1/data && while true; do sleep 3600; done":
		return "Add-Content -Encoding Unicode C:\\mnt\\test-1\\data.txt 'hello world'; while (1) { sleep 3600 }"
	case "echo 'hello world' > /mnt/test-1/data && echo 'hello world' > /mnt/test-2/data && echo 'hello world' > /mnt/test-3/data && grep 'hello world' /mnt/test-1/data && grep 'hello world' /mnt/test-2/data && grep 'hello world' /mnt/test-3/data":
		return "echo 'hello world' | Out-File -FilePath C:\\mnt\\test-1\\data.txt; Get-Content C:\\mnt\\test-1\\data.txt | findstr 'hello world'; echo 'hello world' | Out-File -FilePath C:\\mnt\\test-2\\data.txt; Get-Content C:\\mnt\\test-2\\data.txt | findstr 'hello world'; echo 'hello world' | Out-File -FilePath C:\\mnt\\test-3\\data.txt; Get-Content C:\\mnt\\test-3\\data.txt | findstr 'hello world'"
	}

	return command
}
