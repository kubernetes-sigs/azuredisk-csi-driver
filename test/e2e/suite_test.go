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
	"flag"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/reporters"
	"github.com/onsi/gomega"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/config"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	testtypes "sigs.k8s.io/azuredisk-csi-driver/test/types"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/azure"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/credentials"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
)

const (
	poll        = time.Duration(2) * time.Second
	pollTimeout = time.Duration(10) * time.Minute
)

var (
	azurediskDriver           azuredisk.CSIDriver
	isUsingInTreeVolumePlugin = os.Getenv(driver.AzureDriverNameVar) == inTreeStorageClass
	isTestingMigration        = os.Getenv(testMigrationEnvVar) != ""
	isWindowsCluster          = os.Getenv(testWindowsEnvVar) != ""
	isAzureStackCloud         = strings.EqualFold(os.Getenv(cloudNameEnvVar), "AZURESTACKCLOUD")
	location                  string
	supportsZRS               bool
	supportsDynamicResize     bool
)

var _ = ginkgo.BeforeSuite(func() {
	// k8s.io/kubernetes/test/e2e/framework requires env KUBECONFIG to be set
	// it does not fall back to defaults
	if os.Getenv(testconsts.KubeconfigEnvVar) == "" {
		kubeconfig := filepath.Join(os.Getenv("HOME"), ".kube", "config")
		os.Setenv(testconsts.KubeconfigEnvVar, kubeconfig)
	}
	handleFlags()
	framework.AfterReadingAllFlags(&framework.TestContext)

	// Default storage driver configuration is CSI. Freshly built
	// CSI driver is installed for that case.
	if testconsts.IsTestingMigration || !testconsts.IsUsingInTreeVolumePlugin {
		creds, err := credentials.CreateAzureCredentialFile()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		azureClient, err := azure.GetAzureClient(creds.Cloud, creds.SubscriptionID, creds.AADClientID, creds.TenantID, creds.AADClientSecret)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		_, err = azureClient.EnsureResourceGroup(context.Background(), creds.ResourceGroup, creds.Location, nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		location = creds.Location

		if location == "westus2" || location == "westeurope" || location == "northeurope" || location == "francecentral" {
			supportsZRS = true
		}

		dynamicResizeZones := []string{
			"westcentralus",
			"francesouth",
			"westindia",
			"norwaywest",
			"eastasia",
			"francecentral",
			"germanywestcentral",
			"japanwest",
			"southafricanorth",
			"jioindiawest",
			"canadacentral",
			"australiacentral",
			"japaneast",
			"northeurope",
			"centralindia",
			"uaecentral",
			"switzerlandwest",
			"brazilsouth",
			"uksouth"}

		supportsDynamicResize = false
		for _, zone := range dynamicResizeZones {
			if location == zone {
				supportsDynamicResize = true
				break
			}
		}

		// Install Azure Disk CSI Driver on cluster from project root
		e2eBootstrap := testtypes.TestCmd{
			Command:  "make",
			Args:     []string{"e2e-bootstrap"},
			StartLog: "Installing Azure Disk CSI Driver...",
			EndLog:   "Azure Disk CSI Driver installed",
		}

		createMetricsSVC := testtypes.TestCmd{
			Command:  "make",
			Args:     []string{"create-metrics-svc"},
			StartLog: "create metrics service ...",
			EndLog:   "metrics service created",
		}
		if !*skipClusterBootstrap {
			testutil.ExecTestCmd([]testtypes.TestCmd{e2eBootstrap, createMetricsSVC})
		}

		driverOptions := azuredisk.DriverOptions{
			NodeID:                 os.Getenv("nodeid"),
			DriverName:             consts.DefaultDriverName,
			VolumeAttachLimit:      16,
			EnablePerfOptimization: false,
		}
		os.Setenv("AZURE_CREDENTIAL_FILE", testconsts.TempAzureCredentialFilePath)
		kubeconfig := os.Getenv(testconsts.KubeconfigEnvVar)
		kubeclient, err := azureutils.GetKubeClient(kubeconfig)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		azureCloud, err = azureutils.GetCloudProviderFromClient(kubeclient, driverOptions.CloudConfigSecretName, driverOptions.CloudConfigSecretNamespace, azuredisk.GetUserAgent(driverOptions.DriverName, driverOptions.CustomUserAgent, driverOptions.UserAgentSuffix))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
})

var _ = ginkgo.AfterSuite(func() {
	// Default storage driver configuration is CSI. Freshly built
	// CSI driver is installed for that case.
	if testconsts.IsTestingMigration || testconsts.IsUsingInTreeVolumePlugin {
		cmLog := testtypes.TestCmd{
			Command:  "bash",
			Args:     []string{"test/utils/controller-manager-log.sh"},
			StartLog: "===================controller-manager log=======",
			EndLog:   "===================================================",
		}
		testutil.ExecTestCmd([]testtypes.TestCmd{cmLog})
	}

	if testconsts.IsTestingMigration || !testconsts.IsUsingInTreeVolumePlugin {
		checkPodsRestart := testtypes.TestCmd{
			Command:  "bash",
			Args:     []string{"test/utils/check_driver_pods_restart.sh", "log"},
			StartLog: "Check driver pods if restarts ...",
			EndLog:   "Check successfully",
		}
		testutil.ExecTestCmd([]testtypes.TestCmd{checkPodsRestart})

		os := "linux"
		cloud := "azurepubliccloud"
		if testconsts.IsWindowsCluster {
			os = "windows"
		}
		if testconsts.IsAzureStackCloud {
			cloud = "azurestackcloud"
		}
		createExampleDeployment := testtypes.TestCmd{
			Command:  "bash",
			Args:     []string{"hack/verify-examples.sh", os, cloud},
			StartLog: "create example deployments",
			EndLog:   "example deployments created",
		}
		testutil.ExecTestCmd([]testtypes.TestCmd{createExampleDeployment})

		azurediskLogArgs := []string{"test/utils/azuredisk_log.sh", "azuredisk"}
		if testconsts.IsUsingCSIDriverV2 {
			azurediskLogArgs = append(azurediskLogArgs, "v2")
		}

		azurediskLog := testtypes.TestCmd{
			Command:  "bash",
			Args:     azurediskLogArgs,
			StartLog: "===================azuredisk log===================",
			EndLog:   "===================================================",
		}

		deleteMetricsSVC := testtypes.TestCmd{
			Command:  "make",
			Args:     []string{"delete-metrics-svc"},
			StartLog: "delete metrics service...",
			EndLog:   "metrics service deleted",
		}

		e2eTeardown := testtypes.TestCmd{
			Command:  "make",
			Args:     []string{"e2e-teardown"},
			StartLog: "Uninstalling Azure Disk CSI Driver...",
			EndLog:   "Azure Disk CSI Driver uninstalled",
		}

		if *skipClusterBootstrap {
			testutil.ExecTestCmd([]testtypes.TestCmd{azurediskLog})
		} else {
			testutil.ExecTestCmd([]testtypes.TestCmd{azurediskLog, deleteMetricsSVC, e2eTeardown})
		}

		if !testconsts.IsTestingMigration && !testconsts.IsUsingCSIDriverV2 {

			// install Azure Disk CSI Driver deployment scripts test
			installDriver := testtypes.TestCmd{
				Command:  "bash",
				Args:     []string{"deploy/install-driver.sh", "master", "windows,snapshot,local"},
				StartLog: "===================install Azure Disk CSI Driver deployment scripts test===================",
				EndLog:   "===================================================",
			}
			testutil.ExecTestCmd([]testtypes.TestCmd{installDriver})

			// run example deployment again
			createExampleDeployment := testtypes.TestCmd{
				Command:  "bash",
				Args:     []string{"hack/verify-examples.sh", os, cloud},
				StartLog: "create example deployments#2",
				EndLog:   "example deployments#2 created",
			}
			testutil.ExecTestCmd([]testtypes.TestCmd{createExampleDeployment})

			// uninstall Azure Disk CSI Driver deployment scripts test
			uninstallDriver := testtypes.TestCmd{
				Command:  "bash",
				Args:     []string{"deploy/uninstall-driver.sh", "master", "windows,snapshot,local"},
				StartLog: "===================uninstall Azure Disk CSI Driver deployment scripts test===================",
				EndLog:   "===================================================",
			}
			testutil.ExecTestCmd([]testtypes.TestCmd{uninstallDriver})
		}

		err := credentials.DeleteAzureCredentialFile()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
})

func TestE2E(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	reportDir := os.Getenv(testconsts.ReportDirEnvVar)
	if reportDir == "" {
		reportDir = testconsts.DefaultReportDir
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

func skipIfOnAzureStackCloud() {
	if isAzureStackCloud {
		ginkgo.Skip("test case not supported on Azure Stack Cloud")
	}
}

func skipIfNotZRSSupported() {
	if !supportsZRS {
		ginkgo.Skip("test case not supported on regions without ZRS")
	}
}

func skipIfNotDynamicallyResizeSuported() {
	if !supportsDynamicResize {
		ginkgo.Skip("test case not supported on regions without dynamic resize support")
	}
}

func convertToPowershellorCmdCommandIfNecessary(command string) string {
	if !isWindowsCluster {
		return command
	}

	switch command {
	case "echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data":
		return "echo 'hello world' | Out-File -FilePath C:\\mnt\\test-1\\data.txt; Get-Content C:\\mnt\\test-1\\data.txt | findstr 'hello world'"
	case "touch /mnt/test-1/data":
		return "echo $null >> C:\\mnt\\test-1\\data"
	case "while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done":
		return "while (1) { Add-Content -Encoding Ascii C:\\mnt\\test-1\\data.txt $(Get-Date -Format u); sleep 3600 }"
	case "echo 'hello world' >> /mnt/test-1/data && while true; do sleep 3600; done":
		return "echo 'hello world' | Out-File -Append -FilePath C:\\mnt\\test-1\\data.txt; Get-Content C:\\mnt\\test-1\\data.txt | findstr 'hello world'; Start-Sleep 3600"
	case "echo 'hello world' > /mnt/test-1/data && echo 'hello world' > /mnt/test-2/data && echo 'hello world' > /mnt/test-3/data && grep 'hello world' /mnt/test-1/data && grep 'hello world' /mnt/test-2/data && grep 'hello world' /mnt/test-3/data":
		return "echo 'hello world' | Out-File -FilePath C:\\mnt\\test-1\\data.txt; Get-Content C:\\mnt\\test-1\\data.txt | findstr 'hello world'; echo 'hello world' | Out-File -FilePath C:\\mnt\\test-2\\data.txt; Get-Content C:\\mnt\\test-2\\data.txt | findstr 'hello world'; echo 'hello world' | Out-File -FilePath C:\\mnt\\test-3\\data.txt; Get-Content C:\\mnt\\test-3\\data.txt | findstr 'hello world'"
	case "while true; do sleep 5; done":
		return "while ($true) { Start-Sleep 5 }"
	}

	return command
}

// handleFlags sets up all flags and parses the command line.
func handleFlags() {
	config.CopyFlags(config.Flags, flag.CommandLine)
	framework.RegisterCommonFlags(flag.CommandLine)
	framework.RegisterClusterFlags(flag.CommandLine)
	flag.Parse()
}
