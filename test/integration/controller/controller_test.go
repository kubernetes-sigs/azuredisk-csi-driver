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

package controller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	diskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	azVolumeClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/provisioner"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	namespace = consts.DefaultAzureDiskCrdNamespace
	interval  = time.Duration(15) * time.Second
	timeout   = time.Duration(15) * time.Minute
	maxRetry  = 5
)

var (
	crdProvisioner *provisioner.CrdProvisioner
	config         *rest.Config
	azVolumeClient azVolumeClientSet.Interface
)

func setUpConfig() error {
	var err error
	config, err = rest.InClusterConfig()
	if err != nil {
		kubeconfig := os.Getenv("KUBECONFIG")
		if kubeconfig == "" {
			kubeconfig = filepath.Join(os.Getenv("HOME"), ".kube", "config")
		}
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)

		if err != nil {
			klog.Errorf("failed to build config from %s", kubeconfig)
			return err
		}
	}
	return nil
}

func setUpClient() error {
	var err error
	if config == nil {
		if err = setUpConfig(); err != nil {
			return err
		}
	}
	azVolumeClient, err = azureutils.GetAzDiskClient(config)
	return err
}

func setUpProvisioner() error {
	var err error
	if config == nil {
		if err = setUpConfig(); err != nil {
			return err
		}
	}
	crdProvisioner, err = provisioner.NewCrdProvisioner(config, namespace)
	return err
}

func testBootstrap() error {
	if azVolumeClient == nil {
		if err := setUpClient(); err != nil {
			return err
		}
	}
	if crdProvisioner == nil {
		return setUpProvisioner()
	}
	return nil
}

func getVolumeID(volumeName string) (string, error) {
	azVolume, err := azVolumeClient.DiskV1beta1().AzVolumes(namespace).Get(context.Background(), volumeName, metav1.GetOptions{})
	if errors.IsNotFound(err) {
		return "", err
	}
	if err != nil || azVolume.Status.Detail == nil {
		return "", status.Error(codes.Internal, "volume creation seems to have failed.")
	}

	return azVolume.Status.Detail.VolumeID, nil
}

func checkReplicaCount(volumeName string, desiredNumReplica int) (bool, error) {
	volumeID, err := getVolumeID(volumeName)
	if len(volumeID) == 0 || err != nil {
		return false, status.Errorf(codes.Internal, "failed to get volumeID with volumeName (%s)", volumeName)
	}

	diskName, err := azureutils.GetDiskName(volumeID)
	if len(diskName) == 0 || err != nil {
		return false, status.Errorf(codes.Internal, "failed to extract diskName from diskURI (%s)", volumeID)
	}

	labelSelector := fmt.Sprintf("%s=%s", consts.VolumeNameLabel, diskName)
	azVAs, err := azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return false, status.Errorf(codes.Internal, "failed to get AzVolumeAttachment List for volumeName (%s): %v", diskName, err)
	}

	numReplicas := 0
	for _, azVA := range azVAs.Items {
		if azVA.Spec.RequestedRole == diskv1beta1.ReplicaRole &&
			azVA.Status.Detail != nil &&
			azVA.Status.Detail.PublishContext != nil &&
			azVA.Status.Detail.Role == diskv1beta1.ReplicaRole {
			numReplicas++
		}
	}
	return numReplicas == desiredNumReplica, nil
}

func TestAzVolume(t *testing.T) {
	err := testBootstrap()
	require.NoError(t, err)

	tests := []struct {
		testName            string
		volumeName          string
		capacityRange       *diskv1beta1.CapacityRange
		volumeCapabilities  []diskv1beta1.VolumeCapability
		parameters          map[string]string
		secrets             map[string]string
		volumeContentSource *diskv1beta1.ContentVolumeSource
		accessibilityReq    *diskv1beta1.TopologyRequirement
		testFunc            func(*testing.T, string)
		cleanUpFunc         func(*testing.T, string)
	}{
		{
			testName:   "create volume and check if finalizers have been properly populated.",
			volumeName: "test-volume",
			capacityRange: &diskv1beta1.CapacityRange{
				RequiredBytes: 1073741824,
			},
			volumeCapabilities: []diskv1beta1.VolumeCapability{},
			parameters: map[string]string{
				"kind":      "managed",
				"maxShares": "1",
				"skuName":   "StandardSSD_LRS",
			},
			secrets:             map[string]string{},
			volumeContentSource: nil,
			accessibilityReq:    nil,
			testFunc: func(t *testing.T, volumeName string) {
				azVolume, err := azVolumeClient.DiskV1beta1().AzVolumes(namespace).Get(context.Background(), volumeName, metav1.GetOptions{})
				require.NoError(t, err)
				require.NotNil(t, azVolume.Finalizers)

				// check if finalizer has been properly populated
				finalizerFound := false
				for _, finalizer := range azVolume.Finalizers {
					finalizerFound = finalizerFound || finalizer == consts.AzVolumeFinalizer
				}
				require.True(t, finalizerFound)
			},
			cleanUpFunc: func(t *testing.T, volumeName string) {
				// delete volume
				volumeID, err := getVolumeID(volumeName)
				require.NotEmpty(t, volumeID)
				require.NoError(t, err)

				conditionFunc := func() (bool, error) {
					err = crdProvisioner.DeleteVolume(context.Background(), volumeID, map[string]string{})
					if err != nil {
						return false, nil
					}
					return true, nil
				}

				err = wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)
			},
		},
		{
			testName:   "delete volume and check if volume and all its attachments get deleted",
			volumeName: "test-volume",
			capacityRange: &diskv1beta1.CapacityRange{
				RequiredBytes: 274877906944,
			},
			volumeCapabilities: []diskv1beta1.VolumeCapability{},
			parameters: map[string]string{
				"kind":      "managed",
				"maxShares": "2",
				"skuName":   "Premium_LRS",
			},
			secrets:             map[string]string{},
			volumeContentSource: nil,
			accessibilityReq:    nil,
			testFunc: func(t *testing.T, volumeName string) {
				volumeID, err := getVolumeID(volumeName)
				require.NotEmpty(t, volumeID)
				require.NoError(t, err)

				diskName, err := azureutils.GetDiskName(volumeID)
				require.NotEmpty(t, diskName)
				require.NoError(t, err)

				_, err = crdProvisioner.PublishVolume(context.Background(), volumeID, "integration-test-node-0", nil, false, nil, nil)
				require.NoError(t, err)

				klog.Infof("Waiting for replica AzVolumeAttachment to be created for AzVolume (%s)", volumeName)
				conditionFunc := func() (bool, error) {
					labelSelector := fmt.Sprintf("%s=%s", consts.VolumeNameLabel, diskName)
					azVolumeAttachments, err := azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
					if err != nil {
						return false, nil
					}

					for _, azVolumeAttachment := range azVolumeAttachments.Items {
						if azVolumeAttachment.Spec.RequestedRole == diskv1beta1.ReplicaRole && azVolumeAttachment.Status.Detail != nil {
							klog.Infof("Replica AzVolumeAttachment (%s) successfully created and attached.", azVolumeAttachment.Name)
							return true, nil
						}
					}
					return false, nil
				}

				err = wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)

				// delete volume
				klog.Infof("Waiting for all AzVolumeAttachments attached to AzVolume (%s) to be detached and deleted", volumeName)
				conditionFunc = func() (bool, error) {
					err = crdProvisioner.DeleteVolume(context.Background(), volumeID, map[string]string{})
					if err != nil {
						return false, nil
					}
					return true, nil
				}

				err = wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)

				// check if all azVolumeAttachments attached have been deleted
				conditionFunc = func() (bool, error) {
					labelSelector := fmt.Sprintf("%s=%s", consts.VolumeNameLabel, diskName)
					azVolumeAttachments, err := azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
					klog.Infof("Number of AzVolumeAttachment CRI attached to %s: %d", volumeName, len(azVolumeAttachments.Items))
					if err != nil && !errors.IsNotFound(err) {
						return false, err
					}
					return len(azVolumeAttachments.Items) == 0, nil
				}

				err = wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)
			},
			cleanUpFunc: func(t *testing.T, volumeName string) {
				// detach volume
				azVolume, err := azVolumeClient.DiskV1beta1().AzVolumes(namespace).Get(context.Background(), volumeName, metav1.GetOptions{})
				if errors.IsNotFound(err) {
					klog.Infof("AzVolume has been successfully deleted.")
					return
				}
				require.NoError(t, err)
				require.NotNil(t, azVolume.Status.Detail)

				volumeID := azVolume.Status.Detail.VolumeID
				diskName, err := azureutils.GetDiskName(volumeID)
				require.NotEmpty(t, diskName)
				require.NoError(t, err)

				labelSelector := fmt.Sprintf("%s=%s", consts.VolumeNameLabel, diskName)
				azVolumeAttachments, err := azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
				for _, azVolumeAttachment := range azVolumeAttachments.Items {
					err = azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).Delete(context.Background(), azVolumeAttachment.Name, metav1.DeleteOptions{})
					assert.NoError(t, err)

					conditionFunc := func() (bool, error) {
						_, err := azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).Get(context.Background(), azVolumeAttachment.Name, metav1.GetOptions{})
						if errors.IsNotFound(err) {
							return true, nil
						}
						return false, err
					}
					err = wait.PollImmediate(interval, timeout, conditionFunc)
					assert.NoError(t, err)
				}

				// delete volume
				conditionFunc := func() (bool, error) {
					err = crdProvisioner.DeleteVolume(context.Background(), volumeID, map[string]string{})
					if err != nil {
						return false, nil
					}
					return true, nil
				}

				err = wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)
			},
		},
	}

	for i, test := range tests {
		t.Run(test.testName, func(t *testing.T) {
			klog.Infof("AzVolume Test %d: %s", i, test.testName)

			var err error
			for retry := 0; retry < maxRetry; retry++ {
				_, err = crdProvisioner.CreateVolume(context.Background(), test.volumeName, test.capacityRange, test.volumeCapabilities, test.parameters, test.secrets, test.volumeContentSource, test.accessibilityReq)
				if err == nil {
					break
				}
			}
			require.NoError(t, err)

			if test.cleanUpFunc != nil {
				defer test.cleanUpFunc(t, test.volumeName)
			}

			if test.testFunc != nil {
				test.testFunc(t, test.volumeName)
			}
		})
	}
}

func TestAzVolumeAttachment(t *testing.T) {
	err := testBootstrap()
	require.NoError(t, err)

	defaultCleanUpFunc := func(t *testing.T, volumeName, nodeName string) {
		// unpublishVolume
		volumeID, err := getVolumeID(volumeName)
		require.NotEmpty(t, volumeID)
		require.NoError(t, err)

		diskName, err := azureutils.GetDiskName(volumeID)
		require.NotEmpty(t, diskName)
		require.NoError(t, err)

		conditionFunc := func() (bool, error) {
			err = crdProvisioner.DeleteVolume(context.Background(), volumeID, nil)
			if err != nil {
				return false, nil
			}
			return true, nil
		}

		err = wait.PollImmediate(interval, timeout, conditionFunc)
		require.NoError(t, err)

		// check if all azVolumeAttachments attached have been deleted
		conditionFunc = func() (bool, error) {
			labelSelector := fmt.Sprintf("%s=%s", consts.VolumeNameLabel, diskName)
			azVolumeAttachments, err := azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
			klog.Infof("Current number of AzVolumeAttachment CRI attached to %s: %d", volumeName, len(azVolumeAttachments.Items))
			if err != nil && !errors.IsNotFound(err) {
				return false, err
			}
			return len(azVolumeAttachments.Items) == 0, nil
		}

		err = wait.PollImmediate(interval, timeout, conditionFunc)
		require.NoError(t, err)
	}

	tests := []struct {
		testName         string
		volumeName       string
		primaryNode      string
		volumeCapability *diskv1beta1.VolumeCapability
		readOnly         bool
		secrets          map[string]string
		volumeContext    map[string]string
		setUpFunc        func(*testing.T, string)
		testFunc         func(*testing.T, string, string)
		cleanUpFunc      func(*testing.T, string, string)
	}{
		{
			testName:         "Create AzVolumeAttachment CRI and check if metadata gets properly populated.",
			volumeName:       "test-volume-0",
			primaryNode:      "integration-test-node-0",
			volumeCapability: nil,
			readOnly:         false,
			secrets:          nil,
			volumeContext:    nil,
			setUpFunc: func(t *testing.T, volumeName string) {
				// create volume
				_, err := crdProvisioner.CreateVolume(context.Background(), volumeName, &diskv1beta1.CapacityRange{RequiredBytes: 274877906944}, []diskv1beta1.VolumeCapability{}, map[string]string{"kind": "managed", "maxShares": "1", "skuName": "StandardSSD_LRS"}, nil, nil, nil)
				require.NoError(t, err)
			},
			testFunc: func(t *testing.T, volumeName, nodeName string) {
				volumeID, err := getVolumeID(volumeName)
				require.NotEmpty(t, volumeID)
				require.NoError(t, err)

				diskName, err := azureutils.GetDiskName(volumeID)
				require.NotEmpty(t, diskName)
				require.NoError(t, err)
				azVAName := azureutils.GetAzVolumeAttachmentName(diskName, nodeName)
				azVA, err := azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).Get(context.Background(), azVAName, metav1.GetOptions{})
				require.NoError(t, err)
				require.NotNil(t, azVA.Finalizers)
				require.NotNil(t, azVA.Labels)

				// check if finalizer has been properly populated
				finalizerFound := false
				for _, finalizer := range azVA.Finalizers {
					finalizerFound = finalizerFound || finalizer == consts.AzVolumeAttachmentFinalizer
				}
				require.True(t, finalizerFound)
				klog.Infof("Found finalizers added to AzVolumeAttachment (%s)", azVAName)

				// check if labels have been properly populated
				labelsFound := true
				for _, label := range []string{consts.RoleLabel, consts.NodeNameLabel, consts.VolumeNameLabel} {
					_, labelFound := azVA.Labels[label]
					labelsFound = labelsFound && labelFound
				}
				require.True(t, labelsFound)
				klog.Infof("Found labels added to AzVolumeAttachment (%s)", azVAName)
			},
			cleanUpFunc: defaultCleanUpFunc,
		},
		{
			testName:         "Create AzVolume with maxMountReplicaCount >= 1 and check replica AzVolumeAttachment creation",
			volumeName:       "test-volume-0",
			primaryNode:      "integration-test-node-0",
			volumeCapability: nil,
			readOnly:         false,
			secrets:          nil,
			volumeContext:    nil,
			setUpFunc: func(t *testing.T, volumeName string) {
				// create volume
				_, err := crdProvisioner.CreateVolume(context.Background(), volumeName, &diskv1beta1.CapacityRange{RequiredBytes: 274877906944}, []diskv1beta1.VolumeCapability{}, map[string]string{"kind": "managed", "maxShares": "2", "skuName": "Premium_LRS"}, nil, nil, nil)
				require.NoError(t, err)
			},
			testFunc: func(t *testing.T, volumeName, nodeName string) {
				conditionFunc := func() (bool, error) {
					return checkReplicaCount(volumeName, 1)
				}
				err := wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)
			},
			cleanUpFunc: defaultCleanUpFunc,
		},
		{
			testName:         "Delete a replica AzVolumeAttachment and new replica should replace the deleted.",
			volumeName:       "test-volume-0",
			primaryNode:      "integration-test-node-0",
			volumeCapability: nil,
			readOnly:         false,
			secrets:          nil,
			volumeContext:    nil,
			setUpFunc: func(t *testing.T, volumeName string) {
				// create volume
				_, err := crdProvisioner.CreateVolume(context.Background(), volumeName, &diskv1beta1.CapacityRange{RequiredBytes: 274877906944}, []diskv1beta1.VolumeCapability{}, map[string]string{"kind": "managed", "maxShares": "2", "skuName": "Premium_LRS"}, nil, nil, nil)
				require.NoError(t, err)
			},
			testFunc: func(t *testing.T, volumeName, nodeName string) {
				conditionFunc := func() (bool, error) {
					return checkReplicaCount(volumeName, 1)
				}
				err := wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)

				// delete a replica AzVolumeAttachment
				volumeID, err := getVolumeID(volumeName)
				require.NotEmpty(t, volumeID)
				require.NoError(t, err)

				diskName, err := azureutils.GetDiskName(volumeID)
				require.NotEmpty(t, diskName)
				require.NoError(t, err)
				labelSelector := fmt.Sprintf("%s=%s", consts.VolumeNameLabel, diskName)
				azVAs, err := azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
				require.NoError(t, err)
				var deletedReplica *diskv1beta1.AzVolumeAttachment
				for _, azVA := range azVAs.Items {
					if azVA.Spec.RequestedRole == diskv1beta1.ReplicaRole {
						err = azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).Delete(context.Background(), azVA.Name, metav1.DeleteOptions{})
						require.NoError(t, err)
						klog.Infof("Deleting replica AzVolumeAttachment (%s)", azVA.Name)

						// delete azdrivernode for the attached node to prevent immediate reattachment to the same node
						err = azVolumeClient.DiskV1beta1().AzDriverNodes(namespace).Delete(context.Background(), azVA.Spec.NodeName, metav1.DeleteOptions{})
						require.NoError(t, err)
						klog.Infof("Temporarily failing AzDriverNode (%s)", azVA.Spec.NodeName)

						azVA := azVA
						deletedReplica = &azVA
						break
					}
				}

				require.NotNil(t, deletedReplica, "expected a replica AzVolumeAttachment to have been deleted but no replica has been deleted.")

				// check if the replica has been properly deleted
				conditionFunc = func() (bool, error) {
					_, err := azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).Get(context.Background(), deletedReplica.Name, metav1.GetOptions{})
					if errors.IsNotFound(err) {
						klog.Infof("Successfully deleted replica AzVolumeAttachment (%s)", deletedReplica.Name)
						return true, nil
					}
					return false, nil
				}
				err = wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)

				// restore azdrivernode
				_, err = azVolumeClient.DiskV1beta1().AzDriverNodes(namespace).Create(context.Background(), &diskv1beta1.AzDriverNode{
					ObjectMeta: metav1.ObjectMeta{
						Name: deletedReplica.Spec.NodeName,
					},
					Spec: diskv1beta1.AzDriverNodeSpec{
						NodeName: deletedReplica.Spec.NodeName,
					},
				}, metav1.CreateOptions{})
				klog.Infof("Restoring failed AzDriverNode (%s)", deletedReplica.Spec.NodeName)
				require.NoError(t, err)
				klog.Infof("Successfully restored failed AzDriverNode (%s)", deletedReplica.Spec.NodeName)

				// check if a new replica has been created
				conditionFunc = func() (bool, error) {
					azVAs, err := azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
					require.NoError(t, err)
					require.NotEmpty(t, azVAs.Items)

					for _, azVA := range azVAs.Items {
						if azVA.Spec.RequestedRole == diskv1beta1.ReplicaRole && azVA.Status.Detail != nil && azVA.Status.Detail.Role == diskv1beta1.ReplicaRole && azVA.Status.Detail.PublishContext != nil {
							klog.Infof("A new replica AzVolumeAttachment (%s) found successfully attached to node (%s)", azVA.Name, azVA.Spec.NodeName)
							return true, nil
						}
					}
					return false, nil
				}

				err = wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)
			},
			cleanUpFunc: defaultCleanUpFunc,
		},
		{
			testName:         "Delete 2 replica AzVolumeAttachments and check if the number of replicas converge again to the right number",
			volumeName:       "test-volume-0",
			primaryNode:      "integration-test-node-0",
			volumeCapability: nil,
			readOnly:         false,
			secrets:          nil,
			volumeContext:    nil,
			setUpFunc: func(t *testing.T, volumeName string) {
				// create volume
				_, err := crdProvisioner.CreateVolume(context.Background(), volumeName, &diskv1beta1.CapacityRange{RequiredBytes: 1099511627776}, []diskv1beta1.VolumeCapability{}, map[string]string{"kind": "managed", "maxShares": "3", "skuName": "Premium_LRS"}, nil, nil, nil)
				require.NoError(t, err)
			},
			testFunc: func(t *testing.T, volumeName, nodeName string) {
				conditionFunc := func() (bool, error) {
					return checkReplicaCount(volumeName, 2)
				}
				err := wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)

				// delete two replicas
				volumeID, err := getVolumeID(volumeName)
				require.NotEmpty(t, volumeID)
				require.NoError(t, err)

				diskName, err := azureutils.GetDiskName(volumeID)
				require.NotEmpty(t, diskName)
				require.NoError(t, err)
				labelSelector := fmt.Sprintf("%s=%s", consts.VolumeNameLabel, diskName)
				azVAs, err := azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
				require.NoError(t, err)

				numReplicaDeleted := 0
				for _, azVA := range azVAs.Items {
					if azVA.Spec.RequestedRole == diskv1beta1.ReplicaRole {
						err = azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).Delete(context.Background(), azVA.Name, metav1.DeleteOptions{})
						require.NoError(t, err)
						klog.Infof("Deleting two replica AzVolumeAttachment (%s)", azVA.Name)
						numReplicaDeleted++
					}
					if numReplicaDeleted == 2 {
						break
					}
				}
				require.Equal(t, numReplicaDeleted, 2, fmt.Sprintf("Expected 2 replica deletions but instead got %d", numReplicaDeleted))

				klog.Infof("Sleeping for 1 minutes waiting for replica deletion to complete")
				time.Sleep(time.Duration(1) * time.Minute)

				// check if the number of converge to two
				klog.Infof("Check if the number of replica AzVolumeAttachments have converged to 2...")
				conditionFunc = func() (bool, error) {
					return checkReplicaCount(volumeName, 2)
				}

				err = wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)
			},
			cleanUpFunc: defaultCleanUpFunc,
		},
		{
			testName:         "Check if replica AzVolumeAttachment gets garbage collected if no PublishVolume call is made after primary AzVolumeAttachment is deleted",
			volumeName:       "test-volume-0",
			primaryNode:      "integration-test-node-0",
			volumeCapability: nil,
			readOnly:         false,
			secrets:          nil,
			volumeContext:    nil,
			setUpFunc: func(t *testing.T, volumeName string) {
				// create volume
				_, err := crdProvisioner.CreateVolume(context.Background(), volumeName, &diskv1beta1.CapacityRange{RequiredBytes: 274877906944}, []diskv1beta1.VolumeCapability{}, map[string]string{"kind": "managed", "maxShares": "2", "skuName": "Premium_LRS"}, nil, nil, nil)
				require.NoError(t, err)
			},
			testFunc: func(t *testing.T, volumeName, nodeName string) {
				conditionFunc := func() (bool, error) {
					return checkReplicaCount(volumeName, 1)
				}
				err := wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)

				volumeID, err := getVolumeID(volumeName)
				require.NotEmpty(t, volumeID)
				require.NoError(t, err)

				diskName, err := azureutils.GetDiskName(volumeID)
				require.NotEmpty(t, diskName)
				require.NoError(t, err)

				klog.Infof("Unpublishing volume (%s) from node (%s): expecting primary AzVolumeAttachment (%s) to be deleted.", volumeID, nodeName, azureutils.GetAzVolumeAttachmentName(diskName, nodeName))
				err = crdProvisioner.UnpublishVolume(context.Background(), volumeID, nodeName, nil)
				require.NoError(t, err)

				// sleep and wait for the replica garbage collection to happen
				klog.Infof("Sleeping for %.2f seconds allowing AzVolumeAttachment garbage collection to happen for AzVolume (%s)", controller.DefaultTimeUntilGarbageCollection.Seconds(), volumeName)
				time.Sleep(controller.DefaultTimeUntilGarbageCollection)

				labelSelector := fmt.Sprintf("%s=%s", consts.VolumeNameLabel, diskName)
				conditionFunc = func() (bool, error) {
					azVAs, err := azVolumeClient.DiskV1beta1().AzVolumeAttachments(namespace).List(context.Background(), metav1.ListOptions{LabelSelector: labelSelector})
					if errors.IsNotFound(err) || len(azVAs.Items) == 0 {
						klog.Infof("Garbage collection completed for AzVolume (%s)", volumeName)
						return true, nil
					}
					return false, err
				}

				err = wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)
			},
			cleanUpFunc: defaultCleanUpFunc,
		},
		{
			testName:         "if a new PublishVolume call is made after primary AzVolumeAttachment is deleted, replica AzVolumeAttachments should not be garbage collected.",
			volumeName:       "test-volume-0",
			primaryNode:      "integration-test-node-0",
			volumeCapability: nil,
			readOnly:         false,
			secrets:          nil,
			volumeContext:    nil,
			setUpFunc: func(t *testing.T, volumeName string) {
				// create volume
				_, err := crdProvisioner.CreateVolume(context.Background(), volumeName, &diskv1beta1.CapacityRange{RequiredBytes: 274877906944}, []diskv1beta1.VolumeCapability{}, map[string]string{"kind": "managed", "maxShares": "2", "skuName": "Premium_LRS"}, nil, nil, nil)
				require.NoError(t, err)
			},
			testFunc: func(t *testing.T, volumeName, nodeName string) {
				conditionFunc := func() (bool, error) {
					return checkReplicaCount(volumeName, 1)
				}
				err := wait.PollImmediate(interval, timeout, conditionFunc)
				require.NoError(t, err)
				volumeID, err := getVolumeID(volumeName)
				require.NotEmpty(t, volumeID)
				require.NoError(t, err)

				diskName, err := azureutils.GetDiskName(volumeID)
				require.NotEmpty(t, diskName)
				require.NoError(t, err)

				klog.Infof("Unpublishing volume (%s) from node (%s): expecting primary AzVolumeAttachment (%s) to be deleted.", volumeID, nodeName, azureutils.GetAzVolumeAttachmentName(diskName, nodeName))
				err = crdProvisioner.UnpublishVolume(context.Background(), volumeID, nodeName, nil)
				require.NoError(t, err)

				klog.Infof("Sleeping for %.2f seconds... AzVolumeAttachment garbage collection for AzVolume (%s) should not have happened yet.", (controller.DefaultTimeUntilGarbageCollection / 2).Seconds(), volumeName)
				// sleep only half the amount of garbage collection wait time and make publishVolume call
				time.Sleep(controller.DefaultTimeUntilGarbageCollection / 2)

				klog.Infof("Publishing volume (%s) to node (%s)", volumeID, nodeName)
				response, err := crdProvisioner.PublishVolume(context.Background(), volumeID, nodeName, &diskv1beta1.VolumeCapability{}, false, nil, nil)
				if !assert.NotNil(t, response) || !assert.NoError(t, err) {
					return
				}

				klog.Infof("Checking if previously created replica AzVolumeAttachment still exists and have not been garbage collected...")
				replicaCountMatch, err := checkReplicaCount(volumeName, 1)
				require.True(t, replicaCountMatch)
				require.NoError(t, err)
			},
			cleanUpFunc: defaultCleanUpFunc,
		},
	}
	for i, test := range tests {
		t.Run(test.testName, func(t *testing.T) {
			klog.Infof("Test %d: %s", i, test.testName)
			if test.setUpFunc != nil {
				test.setUpFunc(t, test.volumeName)
			}
			volumeID, err := getVolumeID(test.volumeName)
			require.NotEmpty(t, volumeID)
			require.NoError(t, err)

			_, err = crdProvisioner.PublishVolume(context.Background(), volumeID, test.primaryNode, test.volumeCapability, test.readOnly, test.secrets, test.volumeContext)
			require.NoError(t, err)

			if test.cleanUpFunc != nil {
				defer func() {
					test.cleanUpFunc(t, test.volumeName, test.primaryNode)
					time.Sleep(time.Duration(15) * time.Second)
				}()
			}
			if test.testFunc != nil {
				test.testFunc(t, test.volumeName, test.primaryNode)
			}

		})
	}
}
