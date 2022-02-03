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

package e2e

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/testsuites"
	testtypes "sigs.k8s.io/azuredisk-csi-driver/test/types"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2021-07-01/compute"
	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/test/utils/testutil"
	azure "sigs.k8s.io/cloud-provider-azure/pkg/provider"
)

const (
	defaultDiskSize = 10
)

var _ = ginkgo.Describe("Pre-Provisioned", func() {
	f := framework.NewDefaultFramework("azuredisk")
	scheduler := testutil.GetSchedulerForE2E()

	var (
		cs           clientset.Interface
		ns           *v1.Namespace
		azDiskClient azDiskClientSet.Interface
		testDriver   driver.PreProvisionedVolumeTestDriver
		volumeID     string
		// Set to true if the volume should not be deleted automatically after test
		skipVolumeDeletion bool
	)

	ginkgo.BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
		var err error
		if testconsts.IsUsingCSIDriverV2 {
			azDiskClient, err = azDiskClientSet.NewForConfig(f.ClientConfig())
		}
		framework.ExpectNoError(err, fmt.Sprintf("Failed to create disk client. Error: %v", err))
		testDriver = driver.InitAzureDiskDriver()
		// reset value to false to default to volume clean up after test unless specified otherwise
		skipVolumeDeletion = false
	})

	ginkgo.AfterEach(func() {
		if !skipVolumeDeletion {
			EnsureDiskExists(volumeID)

			if testconsts.IsUsingCSIDriverV2 {
				// wait for the AzVolumeAttachments to be deleted
				if azDiskClient == nil {
					framework.Fail("azure disk client is nil")
				}
				diskName, err := azureutils.GetDiskName(volumeID)
				framework.ExpectNoError(err)
				diskName = strings.ToLower(diskName)

				labelSelector := metav1.LabelSelector{MatchLabels: map[string]string{consts.VolumeNameLabel: diskName}}
				err = wait.PollImmediate(poll, pollTimeout,
					func() (bool, error) {
						azVolumeAttachments, listErr := azDiskClient.DiskV1alpha2().AzVolumeAttachments(consts.DefaultAzureDiskCrdNamespace).List(context.Background(), metav1.ListOptions{LabelSelector: labels.Set(labelSelector.MatchLabels).String()})
						if listErr != nil {
							if errors.IsNotFound(listErr) {
								return true, nil
							}
							return false, listErr
						}
						return azVolumeAttachments == nil || len(azVolumeAttachments.Items) == 0, nil
					})
				framework.ExpectNoError(err)
			}

			err := azureCloud.DeleteManagedDisk(context.Background(), volumeID)
			framework.ExpectNoError(err, fmt.Sprintf("delete volume %q error: %v", volumeID, err))
		}
	})

	ginkgo.Context("[single-az]", func() {
		//dynamically generate the following tests for all available schedulers
		ginkgo.It(fmt.Sprintf("should use a pre-provisioned volume and mount it as readOnly in a pod [disk.csi.azure.com][windows][%s]", scheduler), func() {
			// Az tests need to be changed to pass the right parameters for in-tree driver.
			// Skip these tests until above is fixed.
			testutil.SkipIfUsingInTreeVolumePlugin()
			var err error
			volumeID, err = CreateVolume("pre-provisioned-read-only", defaultDiskSize, make(map[string]string))
			if err != nil {
				skipVolumeDeletion = true
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			ginkgo.By(fmt.Sprintf("Successfully provisioned AzureDisk volume: %q\n", volumeID))

			diskSize := fmt.Sprintf("%dGi", defaultDiskSize)
			pods := []testtypes.PodDetails{
				{
					Cmd: testutil.ConvertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
					Volumes: []testtypes.VolumeDetails{
						{
							VolumeID:  volumeID,
							FSType:    "ext4",
							ClaimSize: diskSize,
							VolumeMount: testtypes.VolumeMountDetails{
								NameGenerate:      "test-volume-",
								MountPathGenerate: "/mnt/test-",
								ReadOnly:          true,
							},
							VolumeAccessMode: v1.ReadWriteOnce,
						},
					},
					IsWindows: testconsts.IsWindowsCluster,
				},
			}
			test := testsuites.PreProvisionedReadOnlyVolumeTest{
				CSIDriver: testDriver,
				Pods:      pods,
			}
			test.Run(cs, ns, schedulerName)
		})

		ginkgo.It(fmt.Sprintf("should succeed when reattaching a disk to a new node on DanglingAttachError [disk.csi.azure.com][%s]", scheduler), func() {
			testutil.SkipIfUsingInTreeVolumePlugin()
			testutil.SkipIfOnAzureStackCloud()
			var err error
			volumeContext := map[string]string{
				consts.SkuNameField:     "Premium_LRS",
				consts.CachingModeField: "None",
			}
			volumeID, err = CreateVolume("reattach-disk-multiple-nodes", defaultDiskSize, volumeContext)
			if err != nil {
				skipVolumeDeletion = true
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			ginkgo.By(fmt.Sprintf("Successfully provisioned a shared disk volume: %q\n", volumeID))

			diskSize := fmt.Sprintf("%dGi", defaultDiskSize)

			pod := testtypes.PodDetails{
				Cmd: testutil.ConvertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
				Volumes: []testtypes.VolumeDetails{
					{
						VolumeID:  volumeID,
						ClaimSize: diskSize,
						VolumeMount: testtypes.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
						VolumeAccessMode: v1.ReadWriteOnce,
					},
				},
				IsWindows: testconsts.IsWindowsCluster,
			}

			test := testsuites.PreProvisionedDanglingAttachVolumeTest{
				CSIDriver:     testDriver,
				AzureCloud:    azureCloud,
				Pod:           pod,
				VolumeContext: volumeContext,
			}
			test.Run(cs, ns, schedulerName)
		})

		ginkgo.It(fmt.Sprintf("should use a pre-provisioned volume and retain PV with reclaimPolicy %q [disk.csi.azure.com][windows]", v1.PersistentVolumeReclaimRetain), func() {
			// Az tests need to be changed to pass the right parameters for in-tree driver.
			// Skip these tests until above is fixed.
			testutil.SkipIfUsingInTreeVolumePlugin()

			var err error
			volumeID, err = CreateVolume("pre-provisioned-retain-reclaim-policy", defaultDiskSize, make(map[string]string))
			if err != nil {
				skipVolumeDeletion = true
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			ginkgo.By(fmt.Sprintf("Successfully provisioned AzureDisk volume: %q\n", volumeID))

			diskSize := fmt.Sprintf("%dGi", defaultDiskSize)
			reclaimPolicy := v1.PersistentVolumeReclaimRetain
			volumes := []testtypes.VolumeDetails{
				{
					VolumeID:         volumeID,
					FSType:           "ext4",
					ClaimSize:        diskSize,
					ReclaimPolicy:    &reclaimPolicy,
					VolumeAccessMode: v1.ReadWriteOnce,
				},
			}
			test := testsuites.PreProvisionedReclaimPolicyTest{
				CSIDriver:     testDriver,
				Volumes:       volumes,
				VolumeContext: make(map[string]string),
			}
			test.Run(cs, ns)
		})

		ginkgo.It("should succeed when creating a shared disk [disk.csi.azure.com][windows]", func() {

			testutil.SkipIfUsingInTreeVolumePlugin()
			testutil.SkipIfOnAzureStackCloud()
			var err error
			volumeContext := map[string]string{
				consts.SkuNameField:     "Premium_LRS",
				consts.MaxSharesField:   "2",
				consts.CachingModeField: "None",
			}
			volumeID, err = CreateVolume("single-shared-disk", 512, volumeContext)
			if err != nil {
				skipVolumeDeletion = true
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			ginkgo.By(fmt.Sprintf("Successfully provisioned a shared disk volume: %q\n", volumeID))
		})

		ginkgo.It("should succeed when attaching a shared block volume to multiple pods [disk.csi.azure.com][shared disk]", func() {
			testutil.SkipIfUsingInTreeVolumePlugin()
			testutil.SkipIfOnAzureStackCloud()
			testutil.SkipIfTestingInWindowsCluster()

			var err error

			sharedDiskSize := 10
			volumeContext := map[string]string{
				"skuName":     "Premium_LRS",
				"maxshares":   "2",
				"cachingMode": "None",
			}
			volumeID, err = CreateVolume("shared-disk-multiple-pods", sharedDiskSize, volumeContext)
			if err != nil {
				skipVolumeDeletion = true
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			ginkgo.By(fmt.Sprintf("Successfully provisioned a shared disk volume: %q\n", volumeID))

			diskSize := fmt.Sprintf("%dGi", sharedDiskSize)
			pod := testtypes.PodDetails{
				Cmd: testutil.ConvertToPowershellorCmdCommandIfNecessary("while true; do sleep 5; done"),
				Volumes: []testtypes.VolumeDetails{
					{
						VolumeID:  volumeID,
						ClaimSize: diskSize,
						VolumeMount: testtypes.VolumeMountDetails{
							NameGenerate:      "test-shared-volume-",
							MountPathGenerate: "/dev/shared-",
						},
						VolumeMode:       testtypes.Block,
						VolumeAccessMode: v1.ReadWriteMany,
					},
				},
				UseCMD:       false,
				IsWindows:    testconsts.IsWindowsCluster,
				ReplicaCount: 2,
			}

			podCheck := &testsuites.PodExecCheck{
				ExpectedString: "VOLUME ATTACHED",
			}
			if !testconsts.IsWindowsCluster {
				podCheck.Cmd = []string{
					"sh",
					"-c",
					"(stat /dev/shared-1 > /dev/null) && echo \"VOLUME ATTACHED\"",
				}
			} else {
				podCheck.Cmd = []string{
					"powershell",
					"-NoLogo",
					"-Command",
					"if (Test-Path c:\\dev\\shared-1) { \"VOLUME ATTACHED\" | Out-Host }",
				}
			}

			test := testsuites.PreProvisionedSharedDiskTester{
				CSIDriver:     testDriver,
				Pod:           pod,
				PodCheck:      podCheck,
				VolumeContext: volumeContext,
			}
			test.Run(cs, ns, schedulerName)
		})

		ginkgo.It(fmt.Sprintf("should succeed when creating a single pod and with replica attachments [disk.csi.azure.com][%s]", scheduler), func() {
			testutil.SkipIfUsingInTreeVolumePlugin()
			testutil.SkipIfOnAzureStackCloud()
			testutil.SkipIfNotUsingCSIDriverV2()

			sharedDiskSize := 256
			volumeName := "shared-disk-replicas"
			volumeContext := map[string]string{
				consts.SkuNameField:     "Premium_LRS",
				consts.MaxSharesField:   "3",
				consts.CachingModeField: "None",
				consts.PerfProfileField: "None",
			}

			var err error
			volumeID, err = CreateVolume(volumeName, sharedDiskSize, volumeContext)
			if err != nil {
				skipVolumeDeletion = true
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			diskSize := fmt.Sprintf("%dGi", sharedDiskSize)

			ginkgo.By(fmt.Sprintf("Successfully provisioned a shared disk volume: %q\n", volumeID))
			pods := []testtypes.PodDetails{}
			for i := 1; i <= 1; i++ {
				pod := testtypes.PodDetails{
					Cmd: testutil.ConvertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
					Volumes: []testtypes.VolumeDetails{
						{
							VolumeID:  volumeID,
							ClaimSize: diskSize,
							VolumeMount: testtypes.VolumeMountDetails{
								NameGenerate:      "test-volume-",
								MountPathGenerate: "/mnt/test-",
							},
							VolumeAccessMode: v1.ReadWriteOnce,
						},
					},
					IsWindows: testconsts.IsWindowsCluster,
				}
				pods = append(pods, pod)
			}

			test := testsuites.PreProvisionedCheckForReplicasTest{
				CSIDriver:     testDriver,
				Pods:          pods,
				VolumeName:    volumeName,
				AzDiskClient:  azDiskClient,
				VolumeContext: volumeContext,
			}
			test.Run(cs, ns, schedulerName)
		})

		ginkgo.It("should create an inline volume by in-tree driver [kubernetes.io/azure-disk]", func() {
			if !testconsts.IsUsingInTreeVolumePlugin {
				ginkgo.Skip("test case is only available for csi driver")
			}
			if !testconsts.IsTestingMigration {
				ginkgo.Skip("test case is only available for migration test")
			}
			var err error
			skipVolumeDeletion = true
			volumeID, err = CreateVolume("pre-provisioned-inline-volume", defaultDiskSize, make(map[string]string))
			if err != nil {
				skipVolumeDeletion = true
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			ginkgo.By(fmt.Sprintf("Successfully provisioned AzureDisk volume: %q\n", volumeID))

			diskSize := fmt.Sprintf("%dGi", defaultDiskSize)
			pods := []testtypes.PodDetails{
				{
					Cmd: testutil.ConvertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
					Volumes: []testtypes.VolumeDetails{
						{
							VolumeID:  volumeID,
							ClaimSize: diskSize,
							VolumeMount: testtypes.VolumeMountDetails{
								NameGenerate:      "test-volume-",
								MountPathGenerate: "/mnt/test-",
							},
							VolumeAccessMode: v1.ReadWriteOnce,
						},
					},
					IsWindows: testconsts.IsWindowsCluster,
				},
			}

			test := testsuites.PreProvisionedInlineVolumeTest{
				CSIDriver: testDriver,
				Pods:      pods,
				DiskURI:   volumeID,
				ReadOnly:  false,
			}
			test.Run(cs, ns, schedulerName)
		})
	})
})

func CreateVolume(diskName string, sizeGiB int, volumeContext map[string]string) (string, error) {
	var (
		err                error
		maxShares          int
		storageAccountType string
	)

	for k, v := range volumeContext {
		switch strings.ToLower(k) {
		case consts.SkuNameField:
			storageAccountType = v
		case consts.MaxSharesField:
			maxShares, err = strconv.Atoi(v)
			if err != nil || maxShares < 1 {
				maxShares = 1
			}
		}
	}

	accoutType, err := azureutils.NormalizeStorageAccountType(storageAccountType, azureCloud.Config.Cloud, azureCloud.Config.DisableAzureStackCloud)
	if err != nil {
		accoutType = compute.DiskStorageAccountTypes(compute.DiskStorageAccountTypesPremiumLRS)
	}
	volumeOptions := &azure.ManagedDiskOptions{
		DiskName:           diskName,
		StorageAccountType: accoutType,
		ResourceGroup:      azureCloud.ResourceGroup,
		SizeGB:             sizeGiB,
		MaxShares:          int32(maxShares),
	}

	diskURI, err := azureCloud.CreateManagedDisk(volumeOptions)
	if err != nil {
		return "", err
	}

	return diskURI, nil
}

func EnsureDiskExists(diskURI string) {
	diskMeta := strings.Split(diskURI, "/")
	diskName := diskMeta[len(diskMeta)-1]
	// pre-prov disk should not be deleted when pod, pvc and pv are deleted
	_, _, err := azureCloud.GetDisk(azureCloud.ResourceGroup, diskName)
	if err != nil {
		ginkgo.Fail(fmt.Sprintf("pre-provisioning disk got deleted with the deletion of pv: %v", err))
	}
}
