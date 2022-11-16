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

	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/testsuites"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/kubernetes/test/e2e/framework"
)

const (
	defaultDiskSize = int64(10)
)

var _ = ginkgo.Describe("Pre-Provisioned", func() {
	f := framework.NewDefaultFramework("azuredisk")

	var (
		cs         clientset.Interface
		ns         *v1.Namespace
		testDriver driver.PreProvisionedVolumeTestDriver
		volumeID   string
		// Set to true if the volume should be not deleted automatically after test
		skipVolumeDeletion bool
	)

	ginkgo.BeforeEach(func() {
		cs = f.ClientSet
		ns = f.Namespace
		testDriver = driver.InitAzureDiskDriver()
		// reset value to false to default to volume clean up after test unless specified otherwise
		skipVolumeDeletion = false
	})

	ginkgo.AfterEach(func() {
		if !skipVolumeDeletion {
			req := &csi.DeleteVolumeRequest{
				VolumeId: volumeID,
			}
			_, err := azurediskDriver.DeleteVolume(context.Background(), req)
			if err != nil {
				ginkgo.Fail(fmt.Sprintf("create volume %q error: %v", volumeID, err))
			}
		}
	})

	ginkgo.Context("[single-az]", func() {
		ginkgo.It("should use a pre-provisioned volume and mount it as readOnly in a pod [disk.csi.azure.com][windows]", func() {
			// Az tests need to be changed to pass the right parameters for in-tree driver.
			// Skip these tests until above is fixed.
			skipIfUsingInTreeVolumePlugin()

			req := makeCreateVolumeReq("pre-provisioned-readOnly", defaultDiskSize)
			resp, err := azurediskDriver.CreateVolume(context.Background(), req)
			if err != nil {
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			volumeID = resp.Volume.VolumeId
			ginkgo.By(fmt.Sprintf("Successfully provisioned AzureDisk volume: %q\n", volumeID))

			diskSize := fmt.Sprintf("%dGi", defaultDiskSize)
			pods := []testsuites.PodDetails{
				{
					Cmd: convertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
					Volumes: []testsuites.VolumeDetails{
						{
							VolumeID:  volumeID,
							FSType:    "ext4",
							ClaimSize: diskSize,
							VolumeMount: testsuites.VolumeMountDetails{
								NameGenerate:      "test-volume-",
								MountPathGenerate: "/mnt/test-",
								ReadOnly:          true,
							},
							VolumeAccessMode: v1.ReadWriteOnce,
						},
					},
					IsWindows:    isWindowsCluster,
					WinServerVer: winServerVer,
				},
			}
			test := testsuites.PreProvisionedReadOnlyVolumeTest{
				CSIDriver: testDriver,
				Pods:      pods,
			}
			test.Run(cs, ns)
		})

		ginkgo.It(fmt.Sprintf("should use a pre-provisioned volume and retain PV with reclaimPolicy %q [disk.csi.azure.com][windows]", v1.PersistentVolumeReclaimRetain), func() {
			// Az tests need to be changed to pass the right parameters for in-tree driver.
			// Skip these tests until above is fixed.
			skipIfUsingInTreeVolumePlugin()

			req := makeCreateVolumeReq("pre-provisioned-retain-reclaimPolicy", defaultDiskSize)
			resp, err := azurediskDriver.CreateVolume(context.Background(), req)
			if err != nil {
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			volumeID = resp.Volume.VolumeId
			ginkgo.By(fmt.Sprintf("Successfully provisioned AzureDisk volume: %q\n", volumeID))

			diskSize := fmt.Sprintf("%dGi", defaultDiskSize)
			reclaimPolicy := v1.PersistentVolumeReclaimRetain
			volumes := []testsuites.VolumeDetails{
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
				VolumeContext: resp.Volume.VolumeContext,
			}
			test.Run(cs, ns)
		})

		ginkgo.It("should succeed when creating a shared disk [disk.csi.azure.com][windows]", func() {
			skipIfUsingInTreeVolumePlugin()
			skipIfOnAzureStackCloud()
			req := makeCreateVolumeReq("single-shared-disk", 512)
			req.Parameters = map[string]string{
				"skuName":     "Premium_LRS",
				"maxShares":   "2",
				"cachingMode": "None",
				"location":    "eastus2",
			}
			req.VolumeCapabilities[0].AccessType = &csi.VolumeCapability_Block{
				Block: &csi.VolumeCapability_BlockVolume{},
			}
			resp, err := azurediskDriver.CreateVolume(context.Background(), req)
			if err != nil {
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			volumeID = resp.Volume.VolumeId
			ginkgo.By(fmt.Sprintf("Successfully provisioned a shared disk volume: %q\n", volumeID))
		})

		ginkgo.It("should fail when maxShares is invalid [disk.csi.azure.com][windows]", func() {
			// Az tests need to be changed to pass the right parameters for in-tree driver.
			// Skip these tests until above is fixed.
			skipIfUsingInTreeVolumePlugin()

			skipVolumeDeletion = true
			req := makeCreateVolumeReq("invalid-maxShares", 256)
			req.Parameters = map[string]string{"maxShares": "0"}
			_, err := azurediskDriver.CreateVolume(context.Background(), req)
			framework.ExpectError(err)
		})

		ginkgo.It("should succeed when attaching a shared block volume to multiple pods [disk.csi.azure.com][shared disk]", func() {
			skipIfUsingInTreeVolumePlugin()
			skipIfOnAzureStackCloud()
			skipIfTestingInWindowsCluster()

			sharedDiskSize := int64(10)
			req := makeCreateVolumeReq("shared-disk-multiple-pods", sharedDiskSize)
			diskSize := fmt.Sprintf("%dGi", sharedDiskSize)
			req.Parameters = map[string]string{
				"skuName":     "Premium_LRS",
				"maxshares":   "2",
				"cachingMode": "None",
				"perfProfile": "None",
			}
			req.VolumeCapabilities[0].AccessType = &csi.VolumeCapability_Block{
				Block: &csi.VolumeCapability_BlockVolume{},
			}
			req.VolumeCapabilities[0].AccessMode = &csi.VolumeCapability_AccessMode{
				Mode: csi.VolumeCapability_AccessMode_MULTI_NODE_MULTI_WRITER,
			}
			resp, err := azurediskDriver.CreateVolume(context.Background(), req)
			if err != nil {
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			volumeID = resp.Volume.VolumeId
			ginkgo.By(fmt.Sprintf("Successfully provisioned a shared disk volume: %q\n", volumeID))

			pod := testsuites.PodDetails{
				Cmd: convertToPowershellorCmdCommandIfNecessary("while true; do sleep 5; done"),
				Volumes: []testsuites.VolumeDetails{
					{
						VolumeID:  volumeID,
						ClaimSize: diskSize,
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-shared-volume-",
							MountPathGenerate: "/dev/shared-",
						},
						VolumeMode:       testsuites.Block,
						VolumeAccessMode: v1.ReadWriteMany,
					},
				},
				UseCMD:       false,
				IsWindows:    isWindowsCluster,
				WinServerVer: winServerVer,
				ReplicaCount: 2,
			}

			podCheck := &testsuites.PodExecCheck{
				ExpectedString: "VOLUME ATTACHED",
			}
			if !isWindowsCluster {
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
				VolumeContext: resp.Volume.VolumeContext,
			}
			test.Run(cs, ns)
		})

		ginkgo.It("should succeed when creating a PremiumV2_LRS disk [disk.csi.azure.com][windows]", func() {
			skipIfUsingInTreeVolumePlugin()
			skipIfOnAzureStackCloud()
			req := makeCreateVolumeReq("premium-v2-disk", 100)
			req.Parameters = map[string]string{
				"skuName":           "PremiumV2_LRS",
				"location":          "eastus", // eastus2euap, swedencentral, westeurope
				"DiskIOPSReadWrite": "3000",
				"DiskMBpsReadWrite": "200",
			}
			req.VolumeCapabilities[0].AccessType = &csi.VolumeCapability_Block{
				Block: &csi.VolumeCapability_BlockVolume{},
			}
			resp, err := azurediskDriver.CreateVolume(context.Background(), req)
			if err != nil {
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			volumeID = resp.Volume.VolumeId
			ginkgo.By(fmt.Sprintf("Successfully provisioned a PremiumV2_LRS disk volume: %q\n", volumeID))
		})

		ginkgo.It("should succeed when reattaching a disk to a new node on DanglingAttachError [disk.csi.azure.com]", func() {
			skipIfUsingInTreeVolumePlugin()
			skipIfOnAzureStackCloud()
			req := makeCreateVolumeReq("reattach-disk-multiple-nodes", defaultDiskSize)
			req.Parameters = map[string]string{
				"skuName":     "Premium_LRS",
				"cachingMode": "None",
			}
			req.VolumeCapabilities[0].AccessType = &csi.VolumeCapability_Block{
				Block: &csi.VolumeCapability_BlockVolume{},
			}
			resp, err := azurediskDriver.CreateVolume(context.Background(), req)
			if err != nil {
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			volumeID = resp.Volume.VolumeId
			ginkgo.By(fmt.Sprintf("Successfully provisioned a shared disk volume: %q\n", volumeID))

			diskSize := fmt.Sprintf("%dGi", defaultDiskSize)

			pod := testsuites.PodDetails{
				Cmd: convertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
				Volumes: []testsuites.VolumeDetails{
					{
						VolumeID:  volumeID,
						ClaimSize: diskSize,
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
						VolumeAccessMode: v1.ReadWriteOnce,
					},
				},
				IsWindows:    isWindowsCluster,
				WinServerVer: winServerVer,
			}

			test := testsuites.PreProvisionedDanglingAttachVolumeTest{
				CSIDriver:       testDriver,
				AzureDiskDriver: azurediskDriver,
				Pod:             pod,
				VolumeContext:   resp.Volume.VolumeContext,
			}
			test.Run(cs, ns)
		})

		ginkgo.It("should create an inline volume by in-tree driver [kubernetes.io/azure-disk]", func() {
			if !isUsingInTreeVolumePlugin {
				ginkgo.Skip("test case is only available for csi driver")
			}
			if !isTestingMigration {
				ginkgo.Skip("test case is only available for migration test")
			}

			skipVolumeDeletion = true
			req := makeCreateVolumeReq("pre-provisioned-inline-volume", defaultDiskSize)
			resp, err := azurediskDriver.CreateVolume(context.Background(), req)
			if err != nil {
				ginkgo.Fail(fmt.Sprintf("create volume error: %v", err))
			}
			volumeID = resp.Volume.VolumeId
			ginkgo.By(fmt.Sprintf("Successfully provisioned AzureDisk volume: %q\n", volumeID))

			diskSize := fmt.Sprintf("%dGi", defaultDiskSize)
			pods := []testsuites.PodDetails{
				{
					Cmd: convertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
					Volumes: []testsuites.VolumeDetails{
						{
							VolumeID:  volumeID,
							ClaimSize: diskSize,
							VolumeMount: testsuites.VolumeMountDetails{
								NameGenerate:      "test-volume-",
								MountPathGenerate: "/mnt/test-",
							},
							VolumeAccessMode: v1.ReadWriteOnce,
						},
					},
					IsWindows:    isWindowsCluster,
					WinServerVer: winServerVer,
				},
			}

			test := testsuites.PreProvisionedInlineVolumeTest{
				CSIDriver: testDriver,
				Pods:      pods,
				DiskURI:   volumeID,
				ReadOnly:  false,
			}
			test.Run(cs, ns)
		})
	})
})

func makeCreateVolumeReq(volumeName string, sizeGiB int64) *csi.CreateVolumeRequest {
	req := &csi.CreateVolumeRequest{
		Name: volumeName,
		VolumeCapabilities: []*csi.VolumeCapability{
			{
				AccessType: &csi.VolumeCapability_Mount{
					Mount: &csi.VolumeCapability_MountVolume{},
				},
				AccessMode: &csi.VolumeCapability_AccessMode{
					Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
				},
			},
		},
		CapacityRange: &csi.CapacityRange{
			RequiredBytes: sizeGiB * 1024 * 1024 * 1024,
			LimitBytes:    sizeGiB * 1024 * 1024 * 1024,
		},
	}

	return req
}
