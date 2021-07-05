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
	"os"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/driver"
	"sigs.k8s.io/azuredisk-csi-driver/test/e2e/testsuites"

	"github.com/onsi/ginkgo"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	clientset "k8s.io/client-go/kubernetes"
	restclientset "k8s.io/client-go/rest"
	"k8s.io/kubernetes/test/e2e/framework"
)

var _ = ginkgo.Describe("Dynamic Provisioning", func() {
	t := dynamicProvisioningTestSuite{}
	schedulers := getListOfSchedulers()
	for _, scheduler := range schedulers {
		scheduler := scheduler
		ginkgo.Context("[single-az]", func() {
			t.defineTests(false, scheduler)
		})
	}

	//TODO add support for scheduler extender
	ginkgo.Context("[multi-az]", func() {
		t.defineTests(true, "default-scheduler")
	})
})

type dynamicProvisioningTestSuite struct {
	allowedTopologyValues []string
}

func (t *dynamicProvisioningTestSuite) defineTests(isMultiZone bool, schedulerName string) {
	f := framework.NewDefaultFramework("azuredisk")

	var (
		cs          clientset.Interface
		ns          *v1.Namespace
		snapshotrcs restclientset.Interface
		testDriver  driver.PVTestDriver
	)

	ginkgo.BeforeEach(func() {
		checkPodsRestart := testCmd{
			command:  "bash",
			args:     []string{"test/utils/check_driver_pods_restart.sh"},
			startLog: "Check driver pods if restarts ...",
			endLog:   "Check successfully",
		}
		execTestCmd([]testCmd{checkPodsRestart})

		cs = f.ClientSet
		ns = f.Namespace

		var err error
		snapshotrcs, err = restClient(testsuites.SnapshotAPIGroup, testsuites.APIVersionv1beta1)
		if err != nil {
			ginkgo.Fail(fmt.Sprintf("could not get rest clientset: %v", err))
		}

		// Populate allowedTopologyValues from node labels fior the first time
		if isMultiZone && len(t.allowedTopologyValues) == 0 {
			nodes, err := cs.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
			framework.ExpectNoError(err)
			allowedTopologyValuesMap := make(map[string]bool)
			for _, node := range nodes.Items {
				if zone, ok := node.Labels[driver.TopologyKey]; ok {
					allowedTopologyValuesMap[zone] = true
				}
			}
			for k := range allowedTopologyValuesMap {
				t.allowedTopologyValues = append(t.allowedTopologyValues, k)
			}
		}
	})

	testDriver = driver.InitAzureDiskDriver()
	ginkgo.It(fmt.Sprintf("should create a volume on demand with mount options [kubernetes.io/azure-disk] [disk.csi.azure.com] [Windows] [%s]", schedulerName), func() {
		pvcSize := "10Gi"
		if isMultiZone {
			pvcSize = "1000Gi"
		}
		pods := []testsuites.PodDetails{
			{
				Cmd: convertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
				Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
					{
						ClaimSize: pvcSize,
						MountOptions: []string{
							"barrier=1",
							"acl",
						},
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
				}, isMultiZone),
				IsWindows: isWindowsCluster,
			},
		}
		test := testsuites.DynamicallyProvisionedCmdVolumeTest{
			CSIDriver:              testDriver,
			Pods:                   pods,
			StorageClassParameters: map[string]string{"skuName": "Standard_LRS"},
		}

		if isMultiZone && !isUsingInTreeVolumePlugin {
			test.StorageClassParameters = map[string]string{
				"skuName":           "UltraSSD_LRS",
				"cachingmode":       "None",
				"diskIopsReadWrite": "2000",
				"diskMbpsReadWrite": "320",
				"logicalSectorSize": "512",
			}
		}
		if !isUsingInTreeVolumePlugin && supportsZRS {
			test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_ZRS"}
		}

		if isUsingInTreeVolumePlugin {
			// cover case: https://github.com/kubernetes/kubernetes/issues/103433
			test.StorageClassParameters = map[string]string{"Kind": "managed"}
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should create and attach a volume with basic perfProfile [disk.csi.azure.com] [Windows]", func() {
		skipIfOnAzureStackCloud()
		skipIfUsingInTreeVolumePlugin()
		pods := []testsuites.PodDetails{
			{
				Cmd: convertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
				Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
					{
						FSType:    "ext4",
						ClaimSize: "10Gi",
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
				}, isMultiZone),
				IsWindows: isWindowsCluster,
			},
		}
		test := testsuites.DynamicallyProvisionedCmdVolumeTest{
			CSIDriver: testDriver,
			Pods:      pods,
			StorageClassParameters: map[string]string{
				"skuName":     "Premium_LRS",
				"perfProfile": "Basic",
			},
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should receive FailedMount event with invalid mount options [kubernetes.io/azure-disk] [disk.csi.azure.com] [%s]", schedulerName), func() {
		skipIfTestingInWindowsCluster()

		pods := []testsuites.PodDetails{
			{
				Cmd: "echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data",
				Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
					{
						ClaimSize: "10Gi",
						MountOptions: []string{
							"invalid",
							"mount",
							"options",
						},
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
				}, isMultiZone),
			},
		}
		test := testsuites.DynamicallyProvisionedInvalidMountOptions{
			CSIDriver:              testDriver,
			Pods:                   pods,
			StorageClassParameters: map[string]string{"skuName": "StandardSSD_LRS"},
		}
		if !isUsingInTreeVolumePlugin && (location == "westus2" || location == "westeurope") {
			test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_ZRS"}
		}
		if isAzureStackCloud {
			test.StorageClassParameters = map[string]string{"skuName": "Standard_LRS"}
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should create a raw block volume on demand [kubernetes.io/azure-disk] [disk.csi.azure.com] [%s]", schedulerName), func() {
		skipIfTestingInWindowsCluster()

		pods := []testsuites.PodDetails{
			{
				Cmd: "ls /dev | grep e2e-test",
				Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
					{
						ClaimSize:  "10Gi",
						VolumeMode: testsuites.Block,
						VolumeDevice: testsuites.VolumeDeviceDetails{
							NameGenerate: "test-volume-",
							DevicePath:   "/dev/e2e-test",
						},
					},
				}, isMultiZone),
			},
		}
		test := testsuites.DynamicallyProvisionedCmdVolumeTest{
			CSIDriver:              testDriver,
			Pods:                   pods,
			StorageClassParameters: map[string]string{"skuName": "Premium_LRS"},
		}
		if !isUsingInTreeVolumePlugin && supportsZRS {
			test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_ZRS"}
		}

		test.Run(cs, ns, schedulerName)
	})

	// Track issue https://github.com/kubernetes/kubernetes/issues/70505
	ginkgo.It(fmt.Sprintf("should create a volume on demand and mount it as readOnly in a pod [kubernetes.io/azure-disk] [disk.csi.azure.com] [Windows] [%s]", schedulerName), func() {
		pods := []testsuites.PodDetails{
			{
				Cmd: convertToPowershellorCmdCommandIfNecessary("touch /mnt/test-1/data"),
				Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
					{
						FSType:    "ext4",
						ClaimSize: "10Gi",
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
							ReadOnly:          true,
						},
					},
				}, isMultiZone),
				IsWindows: isWindowsCluster,
			},
		}
		test := testsuites.DynamicallyProvisionedReadOnlyVolumeTest{
			CSIDriver:              testDriver,
			Pods:                   pods,
			StorageClassParameters: map[string]string{"skuName": "StandardSSD_LRS"},
		}
		if !isUsingInTreeVolumePlugin && (location == "westus2" || location == "westeurope") {
			test.StorageClassParameters = map[string]string{"skuName": "Premium_ZRS"}
		}
		if isAzureStackCloud {
			test.StorageClassParameters = map[string]string{"skuName": "Standard_LRS"}
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should create multiple PV objects, bind to PVCs and attach all to different pods on the same node [kubernetes.io/azure-disk] [disk.csi.azure.com] [Windows] [%s]", schedulerName), func() {
		pods := []testsuites.PodDetails{
			{
				Cmd: convertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
				Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
					{
						FSType:    "ext3",
						ClaimSize: "10Gi",
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
				}, isMultiZone),
				IsWindows: isWindowsCluster,
			},
			{
				Cmd: convertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
				Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
					{
						FSType:    "ext4",
						ClaimSize: "10Gi",
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
				}, isMultiZone),
				IsWindows: isWindowsCluster,
			},
			{
				Cmd: convertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
				Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
					{
						FSType:    "xfs",
						ClaimSize: "10Gi",
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
				}, isMultiZone),
				IsWindows: isWindowsCluster,
			},
		}
		test := testsuites.DynamicallyProvisionedCollocatedPodTest{
			CSIDriver:              testDriver,
			Pods:                   pods,
			ColocatePods:           true,
			StorageClassParameters: map[string]string{"skuName": "Premium_LRS"},
		}
		if !isUsingInTreeVolumePlugin && supportsZRS {
			test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_ZRS"}
		}

		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should create a deployment object, write and read to it, delete the pod and write and read to it again [kubernetes.io/azure-disk] [disk.csi.azure.com] [Windows] [%s]", schedulerName), func() {
		pod := testsuites.PodDetails{
			Cmd: convertToPowershellorCmdCommandIfNecessary("echo 'hello world' >> /mnt/test-1/data && while true; do sleep 3600; done"),
			Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
				{
					FSType:    "ext3",
					ClaimSize: "10Gi",
					VolumeMount: testsuites.VolumeMountDetails{
						NameGenerate:      "test-volume-",
						MountPathGenerate: "/mnt/test-",
					},
				},
			}, isMultiZone),
			IsWindows: isWindowsCluster,
			UseCMD:    false,
		}

		podCheckCmd := []string{"cat", "/mnt/test-1/data"}
		expectedString := "hello world\n"
		if isWindowsCluster {
			podCheckCmd = []string{"cmd", "/c", "type C:\\mnt\\test-1\\data.txt"}
			expectedString = "hello world\r\n"
		}
		test := testsuites.DynamicallyProvisionedDeletePodTest{
			CSIDriver: testDriver,
			Pod:       pod,
			PodCheck: &testsuites.PodExecCheck{
				Cmd:            podCheckCmd,
				ExpectedString: expectedString, // pod will be restarted so expect to see 2 instances of string
			},
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should delete PV with reclaimPolicy %q [kubernetes.io/azure-disk] [disk.csi.azure.com] [Windows]", v1.PersistentVolumeReclaimDelete), func() {
		reclaimPolicy := v1.PersistentVolumeReclaimDelete
		volumes := t.normalizeVolumes([]testsuites.VolumeDetails{
			{
				FSType:        "ext4",
				ClaimSize:     "10Gi",
				ReclaimPolicy: &reclaimPolicy,
			},
		}, isMultiZone)
		test := testsuites.DynamicallyProvisionedReclaimPolicyTest{
			CSIDriver: testDriver,
			Volumes:   volumes,
		}
		test.Run(cs, ns)
	})

	ginkgo.It(fmt.Sprintf("should retain PV with reclaimPolicy %q [disk.csi.azure.com]", v1.PersistentVolumeReclaimRetain), func() {
		// This tests uses the CSI driver to delete the PV.
		// TODO: Go via the k8s interfaces and also make it more reliable for in-tree and then
		//       test can be enabled.
		skipIfUsingInTreeVolumePlugin()

		reclaimPolicy := v1.PersistentVolumeReclaimRetain
		volumes := t.normalizeVolumes([]testsuites.VolumeDetails{
			{
				FSType:        "ext4",
				ClaimSize:     "10Gi",
				ReclaimPolicy: &reclaimPolicy,
			},
		}, isMultiZone)
		test := testsuites.DynamicallyProvisionedReclaimPolicyTest{
			CSIDriver: testDriver,
			Volumes:   volumes,
			Azuredisk: azurediskDriver,
		}
		test.Run(cs, ns)
	})

	ginkgo.It(fmt.Sprintf("should clone a volume from an existing volume and read from it [disk.csi.azure.com] [%s]", schedulerName), func() {
		skipIfTestingInWindowsCluster()
		skipIfUsingInTreeVolumePlugin()

		pod := testsuites.PodDetails{
			Cmd: "echo 'hello world' > /mnt/test-1/data",
			Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
				{
					FSType:    "ext4",
					ClaimSize: "10Gi",
					VolumeMount: testsuites.VolumeMountDetails{
						NameGenerate:      "test-volume-",
						MountPathGenerate: "/mnt/test-",
					},
				},
			}, isMultiZone),
		}
		podWithClonedVolume := testsuites.PodDetails{
			Cmd: "grep 'hello world' /mnt/test-1/data",
		}
		test := testsuites.DynamicallyProvisionedVolumeCloningTest{
			CSIDriver:           testDriver,
			Pod:                 pod,
			PodWithClonedVolume: podWithClonedVolume,
			StorageClassParameters: map[string]string{
				"skuName": "Standard_LRS",
				"fsType":  "xfs",
			},
		}
		if !isUsingInTreeVolumePlugin && supportsZRS {
			test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_ZRS"}
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should clone a volume of larger size than the source volume and make sure the filesystem is appropriately adjusted [disk.csi.azure.com] [%s]", schedulerName), func() {
		skipIfTestingInWindowsCluster()
		skipIfUsingInTreeVolumePlugin()

		pod := testsuites.PodDetails{
			Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
				{
					FSType:    "ext4",
					ClaimSize: "10Gi",
					VolumeMount: testsuites.VolumeMountDetails{
						NameGenerate:      "test-volume-",
						MountPathGenerate: "/mnt/test-",
					},
				},
			}, isMultiZone),
		}
		clonedVolumeSize := "20Gi"

		podWithClonedVolume := testsuites.PodDetails{
			Cmd: "df -h | grep /mnt/test- | awk '{print $2}' | grep 20.0G",
		}

		test := testsuites.DynamicallyProvisionedVolumeCloningTest{
			CSIDriver:           testDriver,
			Pod:                 pod,
			PodWithClonedVolume: podWithClonedVolume,
			ClonedVolumeSize:    clonedVolumeSize,
			StorageClassParameters: map[string]string{
				"skuName": "Standard_LRS",
				"fsType":  "xfs",
			},
		}
		if !isUsingInTreeVolumePlugin && supportsZRS {
			test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_ZRS", "fsType": "xfs"}
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should create multiple PV objects, bind to PVCs and attach all to a single pod [kubernetes.io/azure-disk] [disk.csi.azure.com] [Windows] [%s]", schedulerName), func() {
		pods := []testsuites.PodDetails{
			{
				Cmd: convertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && echo 'hello world' > /mnt/test-2/data && echo 'hello world' > /mnt/test-3/data && grep 'hello world' /mnt/test-1/data && grep 'hello world' /mnt/test-2/data && grep 'hello world' /mnt/test-3/data"),
				Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
					{
						FSType:    "ext3",
						ClaimSize: "10Gi",
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
					{
						FSType:    "ext4",
						ClaimSize: "10Gi",
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
					{
						FSType:    "xfs",
						ClaimSize: "10Gi",
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
				}, isMultiZone),
				IsWindows: isWindowsCluster,
			},
		}
		test := testsuites.DynamicallyProvisionedCmdVolumeTest{
			CSIDriver:              testDriver,
			Pods:                   pods,
			StorageClassParameters: map[string]string{"skuName": "StandardSSD_LRS"},
		}
		if isAzureStackCloud {
			test.StorageClassParameters = map[string]string{"skuName": "Standard_LRS"}
		}
		if !isUsingInTreeVolumePlugin && supportsZRS {
			test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_ZRS"}
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should create a raw block volume and a filesystem volume on demand and bind to the same pod [kubernetes.io/azure-disk] [disk.csi.azure.com] [%s]", schedulerName), func() {
		skipIfTestingInWindowsCluster()

		pods := []testsuites.PodDetails{
			{
				Cmd: "dd if=/dev/zero of=/dev/xvda bs=1024k count=100 && echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data",
				Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
					{
						FSType:    "ext4",
						ClaimSize: "10Gi",
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
					{
						FSType:       "ext4",
						MountOptions: []string{"rw"},
						ClaimSize:    "10Gi",
						VolumeMode:   testsuites.Block,
						VolumeDevice: testsuites.VolumeDeviceDetails{
							NameGenerate: "test-block-volume-",
							DevicePath:   "/dev/xvda",
						},
					},
				}, isMultiZone),
			},
		}
		test := testsuites.DynamicallyProvisionedCmdVolumeTest{
			CSIDriver:              testDriver,
			Pods:                   pods,
			StorageClassParameters: map[string]string{"skuName": "Premium_LRS"},
		}
		if !isUsingInTreeVolumePlugin && supportsZRS {
			test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_ZRS"}
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should create a pod, write and read to it, take a volume snapshot, and create another pod from the snapshot [disk.csi.azure.com] [%s]", schedulerName), func() {
		skipIfTestingInWindowsCluster()
		skipIfUsingInTreeVolumePlugin()

		pod := testsuites.PodDetails{
			Cmd: "echo 'hello world' > /mnt/test-1/data",
			Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
				{
					FSType:    "ext4",
					ClaimSize: "10Gi",
					VolumeMount: testsuites.VolumeMountDetails{
						NameGenerate:      "test-volume-",
						MountPathGenerate: "/mnt/test-",
					},
				},
			}, isMultiZone),
		}
		podWithSnapshot := testsuites.PodDetails{
			Cmd: "grep 'hello world' /mnt/test-1/data",
		}
		test := testsuites.DynamicallyProvisionedVolumeSnapshotTest{
			CSIDriver:              testDriver,
			Pod:                    pod,
			ShouldOverwrite:        false,
			PodWithSnapshot:        podWithSnapshot,
			StorageClassParameters: map[string]string{"skuName": "StandardSSD_LRS"},
		}
		if isAzureStackCloud {
			test.StorageClassParameters = map[string]string{"skuName": "Standard_LRS"}
		}
		if !isUsingInTreeVolumePlugin && supportsZRS {
			test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_ZRS"}
		}
		test.Run(cs, snapshotrcs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should create a pod, write to its pv, take a volume snapshot, overwrite data in original pv, create another pod from the snapshot, and read unaltered original data from original pv[disk.csi.azure.com] [%s]", schedulerName), func() {
		skipIfTestingInWindowsCluster()
		skipIfUsingInTreeVolumePlugin()

		pod := testsuites.PodDetails{
			Cmd: "echo 'hello world' > /mnt/test-1/data",
			Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
				{
					FSType:    "ext4",
					ClaimSize: "10Gi",
					VolumeMount: testsuites.VolumeMountDetails{
						NameGenerate:      "test-volume-",
						MountPathGenerate: "/mnt/test-",
					},
				},
			}, isMultiZone),
		}

		podOverwrite := testsuites.PodDetails{
			Cmd: "echo 'overwrite' > /mnt/test-1/data",
		}

		podWithSnapshot := testsuites.PodDetails{
			Cmd: "grep 'hello world' /mnt/test-1/data",
		}

		test := testsuites.DynamicallyProvisionedVolumeSnapshotTest{
			CSIDriver:              testDriver,
			Pod:                    pod,
			ShouldOverwrite:        true,
			PodOverwrite:           podOverwrite,
			PodWithSnapshot:        podWithSnapshot,
			StorageClassParameters: map[string]string{"skuName": "StandardSSD_LRS"},
		}
		if isAzureStackCloud {
			test.StorageClassParameters = map[string]string{"skuName": "Standard_LRS"}
		}
		if !isUsingInTreeVolumePlugin && supportsZRS {
			test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_ZRS"}
		}
		test.Run(cs, snapshotrcs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should create a pod with multiple volumes [kubernetes.io/azure-disk] [disk.csi.azure.com] [Windows] [%s]", schedulerName), func() {
		volumes := []testsuites.VolumeDetails{}
		for i := 1; i <= 3; i++ {
			volume := testsuites.VolumeDetails{
				ClaimSize: "10Gi",
				VolumeMount: testsuites.VolumeMountDetails{
					NameGenerate:      "test-volume-",
					MountPathGenerate: "/mnt/test-",
				},
			}
			volumes = append(volumes, volume)
		}

		pods := []testsuites.PodDetails{
			{
				Cmd:       convertToPowershellorCmdCommandIfNecessary("echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data"),
				Volumes:   t.normalizeVolumes(volumes, isMultiZone),
				IsWindows: isWindowsCluster,
			},
		}
		test := testsuites.DynamicallyProvisionedPodWithMultiplePVsTest{
			CSIDriver: testDriver,
			Pods:      pods,
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should create a volume on demand and resize it [disk.csi.azure.com] [Windows] [%s]", schedulerName), func() {
		skipIfUsingInTreeVolumePlugin()
		volume := testsuites.VolumeDetails{
			ClaimSize: "10Gi",
			VolumeMount: testsuites.VolumeMountDetails{
				NameGenerate:      "test-volume-",
				MountPathGenerate: "/mnt/test-",
			},
		}
		pod := testsuites.PodDetails{
			Cmd: convertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
			Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
				{
					ClaimSize: volume.ClaimSize,
					MountOptions: []string{
						"barrier=1",
						"acl",
					},
					VolumeMount: volume.VolumeMount,
				},
			}, isMultiZone),
			IsWindows: isWindowsCluster,
			UseCMD:    false,
		}

		test := testsuites.DynamicallyProvisionedResizeVolumeTest{
			CSIDriver:              testDriver,
			Volume:                 volume,
			Pod:                    pod,
			StorageClassParameters: map[string]string{"skuName": "Standard_LRS"},
		}
		if !isUsingInTreeVolumePlugin && supportsZRS {
			test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_ZRS"}
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should create a volume azuredisk with tag [disk.csi.azure.com] [Windows] [%s]", schedulerName), func() {
		skipIfUsingInTreeVolumePlugin()
		pods := []testsuites.PodDetails{
			{
				Cmd: convertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
				Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
					{
						ClaimSize: "10Gi",
						MountOptions: []string{
							"barrier=1",
							"acl",
						},
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
				}, isMultiZone),
				IsWindows: isWindowsCluster,
			},
		}
		tags := "disk=test"
		test := testsuites.DynamicallyProvisionedAzureDiskWithTag{
			CSIDriver:              testDriver,
			Pods:                   pods,
			StorageClassParameters: map[string]string{"skuName": "Standard_LRS", "tags": tags},
			Tags:                   tags,
		}
		if !isUsingInTreeVolumePlugin && supportsZRS {
			test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_ZRS", "tags": tags}
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It(fmt.Sprintf("should detach disk after pod deleted [disk.csi.azure.com] [Windows] [%s]", schedulerName), func() {
		pods := []testsuites.PodDetails{
			{
				Cmd: convertToPowershellorCmdCommandIfNecessary("while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done"),
				Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
					{
						ClaimSize: "10Gi",
						MountOptions: []string{
							"barrier=1",
							"acl",
						},
						VolumeMount: testsuites.VolumeMountDetails{
							NameGenerate:      "test-volume-",
							MountPathGenerate: "/mnt/test-",
						},
					},
				}, isMultiZone),
				IsWindows: isWindowsCluster,
			},
		}
		test := testsuites.DynamicallyProvisionedAzureDiskDetach{
			CSIDriver:              testDriver,
			Pods:                   pods,
			StorageClassParameters: map[string]string{"skuName": "Standard_LRS"},
		}
		if !isUsingInTreeVolumePlugin && supportsZRS {
			test.StorageClassParameters = map[string]string{"skuName": "StandardSSD_ZRS"}
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("should create a statefulset object, write and read to it, delete the pod and write and read to it again [kubernetes.io/azure-disk] [disk.csi.azure.com] [Windows]", func() {
		pod := testsuites.PodDetails{
			Cmd: convertToPowershellorCmdCommandIfNecessary("echo 'hello world' >> /mnt/test-1/data && while true; do sleep 3600; done"),
			Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
				{
					FSType:    "ext3",
					ClaimSize: "10Gi",
					VolumeMount: testsuites.VolumeMountDetails{
						NameGenerate:      "pvc",
						MountPathGenerate: "/mnt/test-",
					},
				},
			}, isMultiZone),
			IsWindows: isWindowsCluster,
			UseCMD:    false,
		}

		podCheckCmd := []string{"cat", "/mnt/test-1/data"}
		expectedString := "hello world\n"
		if isWindowsCluster {
			podCheckCmd = []string{"cmd", "/c", "type C:\\mnt\\test-1\\data.txt"}
			expectedString = "hello world\r\n"
		}
		test := testsuites.DynamicallyProvisionedStatefulSetTest{
			CSIDriver: testDriver,
			Pod:       pod,
			PodCheck: &testsuites.PodExecCheck{
				Cmd:            podCheckCmd,
				ExpectedString: expectedString, // pod will be restarted so expect to see 2 instances of string
			},
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should test pod failover with cordoning a node", func() {
		skipIfUsingInTreeVolumePlugin()
		if isMultiZone {
			ginkgo.Skip("test case does not apply to multi az case")
		}

		volume := testsuites.VolumeDetails{
			ClaimSize: "10Gi",
			VolumeMount: testsuites.VolumeMountDetails{
				NameGenerate:      "test-volume-",
				MountPathGenerate: "/mnt/test-",
			},
		}
		pod := testsuites.PodDetails{
			Cmd: convertToPowershellorCmdCommandIfNecessary("echo 'hello world' >> /mnt/test-1/data && while true; do sleep 3600; done"),
			Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
				{
					ClaimSize: volume.ClaimSize,
					MountOptions: []string{
						"barrier=1",
						"acl",
					},
					VolumeMount: volume.VolumeMount,
				},
			}, false),
			IsWindows: isWindowsCluster,
			UseCMD:    false,
		}
		podCheckCmd := []string{"cat", "/mnt/test-1/data"}
		expectedString := "hello world\n"
		if isWindowsCluster {
			podCheckCmd = []string{"cmd", "/c", "type C:\\mnt\\test-1\\data.txt"}
			expectedString = "hello world\r\n"
		}

		storageClassParameters := map[string]string{"skuName": "StandardSSD_LRS"}

		test := testsuites.PodFailover{
			CSIDriver: testDriver,
			Pod:       pod,
			Volume:    volume,
			PodCheck: &testsuites.PodExecCheck{
				Cmd:            podCheckCmd,
				ExpectedString: expectedString, // pod will be restarted so expect to see 2 instances of string
			},
			StorageClassParameters: storageClassParameters,
		}
		test.Run(cs, ns, schedulerName)
	})

	ginkgo.It("Should test pod failover with cordoning a node using ZRS", func() {
		skipIfUsingInTreeVolumePlugin()
		skipIfNotZRSSupported()

		volume := testsuites.VolumeDetails{
			ClaimSize: "10Gi",
			VolumeMount: testsuites.VolumeMountDetails{
				NameGenerate:      "test-volume-",
				MountPathGenerate: "/mnt/test-",
			},
		}
		pod := testsuites.PodDetails{
			Cmd: convertToPowershellorCmdCommandIfNecessary("echo 'hello world' >> /mnt/test-1/data && while true; do sleep 3600; done"),
			Volumes: t.normalizeVolumes([]testsuites.VolumeDetails{
				{
					ClaimSize: volume.ClaimSize,
					MountOptions: []string{
						"barrier=1",
						"acl",
					},
					VolumeMount: volume.VolumeMount,
				},
			}, false),
			IsWindows: isWindowsCluster,
			UseCMD:    false,
		}
		podCheckCmd := []string{"cat", "/mnt/test-1/data"}
		expectedString := "hello world\n"
		if isWindowsCluster {
			podCheckCmd = []string{"cmd", "/c", "type C:\\mnt\\test-1\\data.txt"}
			expectedString = "hello world\r\n"
		}

		storageClassParameters := map[string]string{"skuName": "StandardSSD_ZRS"}

		test := testsuites.PodFailover{
			CSIDriver: testDriver,
			Pod:       pod,
			Volume:    volume,
			PodCheck: &testsuites.PodExecCheck{
				Cmd:            podCheckCmd,
				ExpectedString: expectedString, // pod will be restarted so expect to see 2 instances of string
			},
			StorageClassParameters: storageClassParameters,
		}
		test.Run(cs, ns, schedulerName)
	})
}

// Normalize volumes by adding allowed topology values and WaitForFirstConsumer binding mode if we are testing in a multi-az cluster
func (t *dynamicProvisioningTestSuite) normalizeVolumes(volumes []testsuites.VolumeDetails, isMultiZone bool) []testsuites.VolumeDetails {
	for i := range volumes {
		volumes[i] = t.normalizeVolume(volumes[i], isMultiZone)
	}
	return volumes
}

func (t *dynamicProvisioningTestSuite) normalizeVolume(volume testsuites.VolumeDetails, isMultiZone bool) testsuites.VolumeDetails {
	driverName := os.Getenv(driver.AzureDriverNameVar)
	switch driverName {
	case "kubernetes.io/azure-disk":
		volumeBindingMode := storagev1.VolumeBindingWaitForFirstConsumer
		volume.VolumeBindingMode = &volumeBindingMode
	case "", azuredisk.DefaultDriverName:
		if !isMultiZone {
			return volume
		}
		volume.AllowedTopologyValues = t.allowedTopologyValues
		volumeBindingMode := storagev1.VolumeBindingWaitForFirstConsumer
		volume.VolumeBindingMode = &volumeBindingMode
	}

	return volume
}

func restClient(group string, version string) (restclientset.Interface, error) {
	config, err := framework.LoadConfig()
	if err != nil {
		ginkgo.Fail(fmt.Sprintf("could not load config: %v", err))
	}
	gv := schema.GroupVersion{Group: group, Version: version}
	config.GroupVersion = &gv
	config.APIPath = "/apis"
	config.NegotiatedSerializer = serializer.WithoutConversionCodecFactory{CodecFactory: serializer.NewCodecFactory(runtime.NewScheme())}
	return restclientset.RESTClientFor(config)
}
