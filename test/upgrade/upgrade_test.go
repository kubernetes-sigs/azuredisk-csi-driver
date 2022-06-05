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

package upgrade

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/test/e2e/framework"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/resources"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/azure"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/credentials"
	nodeutil "sigs.k8s.io/azuredisk-csi-driver/test/utils/node"
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

	creds, err := credentials.CreateAzureCredentialFile()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	azureClient, err := azure.GetAzureClient(creds.Cloud, creds.SubscriptionID, creds.AADClientID, creds.TenantID, creds.AADClientSecret)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	_, err = azureClient.EnsureResourceGroup(context.Background(), creds.ResourceGroup, creds.Location, nil)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Uninstall Azure Disk CSI Driver on cluster if there is one running
	testCmds := []testutil.TestCmd{}
	if *driverExists {
		e2eTeardown := testutil.TestCmd{
			Command:  "make",
			Args:     []string{"e2e-teardown"},
			StartLog: "Uninstalling Any Existing Azure Disk CSI Driver...",
			EndLog:   "Azure Disk CSI Driver Uninstalled",
		}
		testCmds = append(testCmds, e2eTeardown)
	}

	// Install Azure Disk CSI Driver on cluster from project root
	os.Unsetenv(testconsts.BuildV2Driver)
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
	testCmds = append(testCmds, e2eBootstrap, createMetricsSVC)
	testutil.ExecTestCmd(testCmds)
})

var _ = ginkgo.AfterSuite(func() {
	// Default storage driver configuration is CSI. Freshly built
	// CSI driver is installed for that case.
	cmLog := testutil.TestCmd{
		Command:  "bash",
		Args:     []string{"test/utils/controller-manager-log.sh"},
		StartLog: "===================controller-manager log=======",
		EndLog:   "===================================================",
	}
	testutil.ExecTestCmd([]testutil.TestCmd{cmLog})

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

	testutil.ExecTestCmd([]testutil.TestCmd{azurediskLog, deleteMetricsSVC, e2eTeardown})

	err := credentials.DeleteAzureCredentialFile()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
})

func TestUpgrade(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Upgrade Test")
}

var _ = ginkgo.Describe("Upgrade testing", func() {

	ginkgo.Context("[single-az]", func() {
		upgradeTest(false)
	})

})

func upgradeTest(isMultiZone bool) {
	f := framework.NewDefaultFramework("azuredisk")

	var (
		cs         clientset.Interface
		ns         *v1.Namespace
		testDriver driver.DynamicPVTestDriver
		scheduler  string
	)

	ginkgo.BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
		testDriver = driver.InitAzureDiskDriver()
		scheduler = testutil.GetSchedulerForE2E()
	})

	ginkgo.It("Should create a pod with 3 volumes with v1 Azure Disk CSI Driver and should successfully create necessary CRIs upon upgrade to v2 Azure Disk CSI Driver.", func() {
		ctx := context.Background()
		nodes := nodeutil.ListNodeNames(cs)
		volumes := []resources.VolumeDetails{}
		for j := 1; j <= 3; j++ {
			volume := resources.VolumeDetails{
				FSType:    "ext4",
				ClaimSize: "256Gi",
				VolumeMount: resources.VolumeMountDetails{
					NameGenerate:      "test-volume-",
					MountPathGenerate: "/mnt/test-",
				},
			}
			volumes = append(volumes, volume)
		}

		pod := resources.PodDetails{
			Cmd:          testutil.ConvertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
			Volumes:      resources.NormalizeVolumes(volumes, []string{}, isMultiZone),
			IsWindows:    testconsts.IsWindowsCluster,
			WinServerVer: testconsts.WinServerVer,
		}

		storageClassParameters := map[string]string{consts.SkuNameField: "Premium_LRS", "maxShares": "1", "cachingmode": "None"}

		tpod, cleanup := pod.SetupWithDynamicVolumes(cs, ns, testDriver, storageClassParameters, scheduler)
		// defer must be called here for resources not get removed before using them
		for i := range cleanup {
			defer cleanup[i]()
		}

		podObj := tpod.Pod.DeepCopy()

		ginkgo.By("deploying pod")
		tpod.Create()
		defer tpod.Cleanup()
		ginkgo.By("checking that the pod is running")
		tpod.WaitForRunning()

		e2eUpgrade := testutil.TestCmd{
			Command:  "make",
			Args:     []string{"e2e-upgrade-v2"},
			StartLog: "upgrading to v2 driver ...",
			EndLog:   "upgraded to v2 driver",
		}
		testutil.ExecTestCmd([]testutil.TestCmd{e2eUpgrade})

		ginkgo.By("checking whether necessary CRIs are created.")
		azDiskClient, err := azdisk.NewForConfig(f.ClientConfig())
		framework.ExpectNoError(err)

		diskNames := make([]string, len(podObj.Spec.Volumes))
		for j, volume := range podObj.Spec.Volumes {
			if volume.PersistentVolumeClaim == nil {
				framework.Failf("volume (%s) does not hold PersistentVolumeClaim field", volume.Name)
			}
			pvc, err := cs.CoreV1().PersistentVolumeClaims(tpod.Namespace.Name).Get(ctx, volume.PersistentVolumeClaim.ClaimName, metav1.GetOptions{})
			framework.ExpectNoError(err)

			pv, err := cs.CoreV1().PersistentVolumes().Get(ctx, pvc.Spec.VolumeName, metav1.GetOptions{})
			framework.ExpectNoError(err)
			framework.ExpectNotEqual(pv.Spec.CSI, nil)
			framework.ExpectEqual(pv.Spec.CSI.Driver, consts.DefaultDriverName)

			diskName, err := azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
			framework.ExpectNoError(err)
			diskNames[j] = strings.ToLower(diskName)
		}

		verifyLoop := func(verifyFuncs ...func() (bool, error)) {
			for _, verifyFunc := range verifyFuncs {
				err := wait.PollImmediate(testconsts.Poll, testconsts.PollTimeout, verifyFunc)
				framework.ExpectNoError(err)
			}
		}

		checkAzDriverNodes := func() (bool, error) {
			azDriverNodes, err := azDiskClient.DiskV1beta2().AzDriverNodes(consts.DefaultAzureDiskCrdNamespace).List(ctx, metav1.ListOptions{})
			if err != nil && !errors.IsNotFound(err) {
				return false, err
			}
			return len(nodes) == len(azDriverNodes.Items), nil
		}

		checkAzVolumes := func() (bool, error) {
			azVolumes, err := azDiskClient.DiskV1beta2().AzVolumes(consts.DefaultAzureDiskCrdNamespace).List(ctx, metav1.ListOptions{})
			if err != nil && !errors.IsNotFound(err) {
				return false, err
			}
			if len(diskNames) != len(azVolumes.Items) {
				return false, nil
			}
			azVolumeSet := map[string]struct{}{}
			for _, azVolume := range azVolumes.Items {
				if azVolume.Status.Detail == nil {
					return false, nil
				}
				klog.Infof("AzVolume CRI (%s) successfully created and populated", azVolume.Name)
				azVolumeSet[azVolume.Name] = struct{}{}
			}
			for _, diskName := range diskNames {
				if _, exists := azVolumeSet[diskName]; !exists {
					ginkgo.Fail(fmt.Sprintf("unable to find AzVolume CRI for disk (%s)", diskName))
				}
			}
			return true, nil
		}
		volumeAttachments, err := cs.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
		framework.ExpectNoError(err, fmt.Sprintf("failed to list AzVolumeAttachment CRIs under namespace (%s): %v", consts.DefaultAzureDiskCrdNamespace, err))

		var attachmentNames []string
		for _, volumeAttachment := range volumeAttachments.Items {
			volumeName := volumeAttachment.Spec.Source.PersistentVolumeName
			if volumeName == nil {
				continue
			}
			// get PV and retrieve diskName
			pv, err := cs.CoreV1().PersistentVolumes().Get(ctx, *volumeName, metav1.GetOptions{})
			framework.ExpectNoError(err)

			if pv.Spec.CSI == nil || pv.Spec.CSI.Driver != consts.DefaultDriverName {
				continue
			}

			diskName, err := azureutils.GetDiskName(pv.Spec.CSI.VolumeHandle)
			framework.ExpectNoError(err)

			attachmentName := azureutils.GetAzVolumeAttachmentName(strings.ToLower(diskName), volumeAttachment.Spec.NodeName)
			attachmentNames = append(attachmentNames, attachmentName)
		}

		checkAzVolumeAttachments := func() (bool, error) {
			azVolumeAttachments, err := azDiskClient.DiskV1beta2().AzVolumeAttachments(consts.DefaultAzureDiskCrdNamespace).List(ctx, metav1.ListOptions{})
			if err != nil && !errors.IsNotFound(err) {
				return false, err
			}
			if len(attachmentNames) != len(azVolumeAttachments.Items) {
				return false, nil
			}
			azVolumeAttachmentSet := map[string]struct{}{}
			for _, azVolumeAttachment := range azVolumeAttachments.Items {
				if azVolumeAttachment.Status.Detail == nil {
					return false, nil
				}
				klog.Infof("AzVolumeAttachment CRI (%s) successfully created and populated", azVolumeAttachment.Name)
				azVolumeAttachmentSet[azVolumeAttachment.Name] = struct{}{}
			}

			for _, attachmentName := range attachmentNames {
				if _, exists := azVolumeAttachmentSet[attachmentName]; !exists {
					ginkgo.Fail(fmt.Sprintf("unable to find AzVolumeAttachment CRI for disk (%s)", attachmentName))
				}
			}
			return true, nil
		}

		verifyLoop(checkAzDriverNodes, checkAzVolumes, checkAzVolumeAttachments)
	})

}
