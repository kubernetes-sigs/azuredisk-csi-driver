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

package scale

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo"
	"github.com/onsi/ginkgo/reporters"
	"github.com/onsi/gomega"
	"k8s.io/kubernetes/test/e2e/framework"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"

	"sigs.k8s.io/azuredisk-csi-driver/test/utils/azure"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/credentials"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
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

		err := credentials.DeleteAzureCredentialFile()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
})

func TestScale(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	reportDir := os.Getenv(testconsts.ReportDirEnvVar)
	if reportDir == "" {
		reportDir = testconsts.DefaultReportDir
	}
	r := []ginkgo.Reporter{reporters.NewJUnitReporter(path.Join(reportDir, "junit_01.xml"))}
	ginkgo.RunSpecsWithDefaultAndCustomReporters(t, "AzureDisk CSI Driver Scale Tests", r)
}
