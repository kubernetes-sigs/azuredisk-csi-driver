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
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/kubernetes/test/e2e/framework"
	"k8s.io/kubernetes/test/e2e/framework/config"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/azure"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/credentials"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
	"sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

const (
	poll        = time.Duration(2) * time.Second
	pollTimeout = time.Duration(10) * time.Minute
)

var (
	skipClusterBootstrap = flag.Bool("skip-cluster-bootstrap", false, "flag to indicate that we can skip cluster bootstrap.")
	azureCloud           *provider.Cloud
	location             string
)

var _ = ginkgo.BeforeSuite(func() {
	log.Println(testconsts.AzureDriverNameVar, os.Getenv(testconsts.AzureDriverNameVar), fmt.Sprintf("%v", testconsts.IsUsingInTreeVolumePlugin))
	log.Println(testconsts.TestMigrationEnvVar, os.Getenv(testconsts.TestMigrationEnvVar), fmt.Sprintf("%v", testconsts.IsTestingMigration))
	log.Println(testconsts.TestWindowsEnvVar, os.Getenv(testconsts.TestWindowsEnvVar), fmt.Sprintf("%v", testconsts.IsWindowsCluster))
	log.Println(testconsts.TestWinServerVerEnvVar, os.Getenv(testconsts.TestWinServerVerEnvVar), fmt.Sprintf("%v", testconsts.WinServerVer))

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

		// Install Azure Disk CSI Driver on cluster from project root
		e2eBootstrap := testutil.TestCmd{
			Command:  "make",
			Args:     []string{"e2e-bootstrap"},
			StartLog: "Installing Azure Disk CSI Driver...",
			EndLog:   "Azure Disk CSI Driver installed",
		}

		createMetricsSVC := testutil.TestCmd{
			Command:  "make",
			Args:     []string{"create-metrics-svc"},
			StartLog: "create metrics service ...",
			EndLog:   "metrics service created",
		}
		if !*skipClusterBootstrap {
			testutil.ExecTestCmd([]testutil.TestCmd{e2eBootstrap, createMetricsSVC})
		}

		os.Setenv("AZURE_CREDENTIAL_FILE", testconsts.TempAzureCredentialFilePath)
		kubeconfig := os.Getenv(testconsts.KubeconfigEnvVar)
		kubeclient, err := azureutils.GetKubeClient(kubeconfig)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cloudConfig := azdiskv1beta2.CloudConfiguration{
			SecretName:                               "",
			SecretNamespace:                          "",
			AllowEmptyCloudConfig:                    false,
			EnableAzureClientAttachDetachRateLimiter: consts.DefaultEnableAzureClientAttachDetachRateLimiter,
			AzureClientAttachDetachRateLimiterQPS:    consts.DefaultAzureClientAttachDetachRateLimiterQPS,
			AzureClientAttachDetachRateLimiterBucket: consts.DefaultAzureClientAttachDetachRateLimiterBucket,
		}
		azureCloud, err = azureutils.GetCloudProviderFromClient(
			kubeclient,
			cloudConfig,
			azuredisk.GetUserAgent(consts.DefaultDriverName, "E2E", ""),
		)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
})

var _ = ginkgo.AfterSuite(func() {
	// Default storage driver configuration is CSI. Freshly built
	// CSI driver is installed for that case.
	if testconsts.IsTestingMigration || testconsts.IsUsingInTreeVolumePlugin {
		cmLog := testutil.TestCmd{
			Command:  "bash",
			Args:     []string{"test/utils/controller-manager-log.sh"},
			StartLog: "===================controller-manager log=======",
			EndLog:   "===================================================",
		}
		testutil.ExecTestCmd([]testutil.TestCmd{cmLog})
	}

	if testconsts.IsTestingMigration || !testconsts.IsUsingInTreeVolumePlugin {
		checkPodsRestart := testutil.TestCmd{
			Command:  "bash",
			Args:     []string{"test/utils/check_driver_pods_restart.sh", "log"},
			StartLog: "Check driver pods if restarts ...",
			EndLog:   "Check successfully",
		}
		testutil.ExecTestCmd([]testutil.TestCmd{checkPodsRestart})

		os := "linux"
		cloud := "azurepubliccloud"
		if testconsts.IsWindowsCluster {
			os = "windows"
			if testconsts.WinServerVer == "windows-2022" {
				os = testconsts.WinServerVer
			}
		}
		if testconsts.IsAzureStackCloud {
			cloud = "azurestackcloud"
		}
		createExampleDeployment := testutil.TestCmd{
			Command:  "bash",
			Args:     []string{"hack/verify-examples.sh", os, cloud},
			StartLog: "create example deployments",
			EndLog:   "example deployments created",
		}
		testutil.ExecTestCmd([]testutil.TestCmd{createExampleDeployment})

		azurediskLogArgs := []string{"test/utils/azuredisk_log.sh", "azuredisk"}
		if testconsts.IsUsingCSIDriverV2 {
			azurediskLogArgs = append(azurediskLogArgs, "v2")
		}

		azurediskLog := testutil.TestCmd{
			Command:  "bash",
			Args:     azurediskLogArgs,
			StartLog: "===================azuredisk log===================",
			EndLog:   "===================================================",
		}

		deleteMetricsSVC := testutil.TestCmd{
			Command:  "make",
			Args:     []string{"delete-metrics-svc"},
			StartLog: "delete metrics service...",
			EndLog:   "metrics service deleted",
		}

		e2eTeardown := testutil.TestCmd{
			Command:  "make",
			Args:     []string{"e2e-teardown"},
			StartLog: "Uninstalling Azure Disk CSI Driver...",
			EndLog:   "Azure Disk CSI Driver uninstalled",
		}

		if *skipClusterBootstrap {
			testutil.ExecTestCmd([]testutil.TestCmd{azurediskLog})
		} else {
			testutil.ExecTestCmd([]testutil.TestCmd{azurediskLog, deleteMetricsSVC, e2eTeardown})
		}

		if !testconsts.IsTestingMigration && !testconsts.IsUsingCSIDriverV2 {

			// install Azure Disk CSI Driver deployment scripts test
			installDriver := testutil.TestCmd{
				Command:  "bash",
				Args:     []string{"deploy/install-driver.sh", "master", "windows,snapshot,local"},
				StartLog: "===================install Azure Disk CSI Driver deployment scripts test===================",
				EndLog:   "===================================================",
			}
			testutil.ExecTestCmd([]testutil.TestCmd{installDriver})

			// run example deployment again
			createExampleDeployment := testutil.TestCmd{
				Command:  "bash",
				Args:     []string{"hack/verify-examples.sh", os, cloud},
				StartLog: "create example deployments#2",
				EndLog:   "example deployments#2 created",
			}
			testutil.ExecTestCmd([]testutil.TestCmd{createExampleDeployment})

			// uninstall Azure Disk CSI Driver deployment scripts test
			uninstallDriver := testutil.TestCmd{
				Command:  "bash",
				Args:     []string{"deploy/uninstall-driver.sh", "master", "windows,snapshot,local"},
				StartLog: "===================uninstall Azure Disk CSI Driver deployment scripts test===================",
				EndLog:   "===================================================",
			}
			testutil.ExecTestCmd([]testutil.TestCmd{uninstallDriver})
		}

		err := credentials.DeleteAzureCredentialFile()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
})

func TestE2E(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "AzureDisk CSI Driver End-to-End Tests")
}

// handleFlags sets up all flags and parses the command line.
func handleFlags() {
	config.CopyFlags(config.Flags, flag.CommandLine)
	framework.RegisterCommonFlags(flag.CommandLine)
	framework.RegisterClusterFlags(flag.CommandLine)
	flag.Parse()
}
