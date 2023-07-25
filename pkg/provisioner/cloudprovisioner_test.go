/*
Copyright 2021 The Kubernetes Authors.

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

package provisioner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	azfake "github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kfake "k8s.io/client-go/kubernetes/fake"
	cloudprovider "k8s.io/cloud-provider"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	azcache "sigs.k8s.io/cloud-provider-azure/pkg/cache"
	"sigs.k8s.io/cloud-provider-azure/pkg/retry"
)

var (
	computeDiskURIFormat     = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/disks/%s"
	computeSnapshotURIFormat = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/snapshots/%s"
	virtualMachineURIFormat  = "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Compute/virtualMachines/%s"

	testSubscription  = "12345678-90ab-cedf-1234-567890abcdef"
	testResourceGroup = "test-rg"

	notFoundError = &retry.Error{
		Retriable:      false,
		HTTPStatusCode: http.StatusNotFound,
		RawError:       errors.New(azureconstants.ResourceNotFound),
	}
	existingDiskError = &retry.Error{
		Retriable:      false,
		HTTPStatusCode: http.StatusConflict,
		RawError:       errors.New("existing disk"),
	}

	provisioningStateSucceeded = "Succeeded"

	testDiskName0             = "test-disk-0"
	testDiskURI0              = fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, testDiskName0)
	testDiskName1             = "test-disk-1"
	testDiskURI1              = fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, testDiskName1)
	testDiskSizeGiB     int32 = 10
	testDiskTimeCreated       = time.Now()

	testDisk = armcompute.Disk{
		Name: &testDiskName0,
		ID:   &testDiskURI0,
		Properties: &armcompute.DiskProperties{
			DiskSizeGB:        &testDiskSizeGiB,
			DiskState:         to.Ptr(armcompute.DiskStateUnattached),
			ProvisioningState: &provisioningStateSucceeded,
			TimeCreated:       &testDiskTimeCreated,
		},
	}

	missingDiskName = "missing-disk"
	missingDiskURI  = fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, missingDiskName)

	clonedDiskName          = "child-disk"
	clonedDiskURI           = fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, clonedDiskName)
	clonedDiskSizeGiB int32 = 100

	clonedDisk = armcompute.Disk{
		Name: &clonedDiskName,
		ID:   &clonedDiskURI,
		Properties: &armcompute.DiskProperties{
			CreationData: &armcompute.CreationData{
				CreateOption:     to.Ptr(armcompute.DiskCreateOptionCopy),
				SourceResourceID: &testDiskURI0,
			},
			DiskSizeGB:        &clonedDiskSizeGiB,
			DiskState:         to.Ptr(armcompute.DiskStateUnattached),
			ProvisioningState: &provisioningStateSucceeded,
			TimeCreated:       &testDiskTimeCreated,
		},
	}

	invalidDiskWithMissingPropertiesName = "disk-missing-properties"
	invalidDiskWithMissingPropertiesURI  = fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, invalidDiskWithMissingPropertiesName)

	invalidDiskWithMissingProperties = armcompute.Disk{
		Name: &invalidDiskWithMissingPropertiesName,
		ID:   &invalidDiskWithMissingPropertiesURI,
	}

	invalidDiskWithEmptyPropertiesName = "disk-empty-properties"
	invalidDiskWithEmptyPropertiesURI  = fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, invalidDiskWithEmptyPropertiesName)

	invalidDiskWithEmptyProperties = armcompute.Disk{
		Name:           &invalidDiskWithEmptyPropertiesName,
		ID:             &invalidDiskWithEmptyPropertiesURI,
		Properties: &armcompute.DiskProperties{},
	}

	testSnapshotName        = "test-snapshot"
	testSnapshotURI         = fmt.Sprintf(computeSnapshotURIFormat, testSubscription, testResourceGroup, testSnapshotName)
	testSnapshotSizeGiB     = testDiskSizeGiB
	testSnapshotTimeCreated = time.Now()

	testSnapshot = armcompute.Snapshot{
		Name: &testSnapshotName,
		ID:   &testSnapshotURI,
		Properties: &armcompute.SnapshotProperties{
			CreationData: &armcompute.CreationData{
				SourceResourceID: &testDiskURI0,
			},
			DiskSizeGB:        &testSnapshotSizeGiB,
			ProvisioningState: &provisioningStateSucceeded,
			TimeCreated:       &testSnapshotTimeCreated,
		},
	}

	testAzSnapshot *azdiskv1beta2.Snapshot

	missingSnapshotName = "missing-snapshot"
	missingSnapshotURI  = fmt.Sprintf(computeSnapshotURIFormat, testSubscription, testResourceGroup, missingSnapshotName)

	invalidSnapshotWithMissingPropertiesName = "snapshot-missing-properties"
	invalidSnapshotWithMissingPropertiesURI  = fmt.Sprintf(computeSnapshotURIFormat, testSubscription, testResourceGroup, invalidSnapshotWithMissingPropertiesName)

	invalidSnapshotWithMissingProperties = armcompute.Snapshot{
		Name: &invalidSnapshotWithMissingPropertiesName,
		ID:   &invalidSnapshotWithMissingPropertiesURI,
	}

	invalidSnapshotWithEmptyPropertiesName = "snapshot-empty-properties"
	invalidSnapshotWithEmptyPropertiesURI  = fmt.Sprintf(computeSnapshotURIFormat, testSubscription, testResourceGroup, invalidSnapshotWithEmptyPropertiesName)

	invalidSnapshotWithEmptyProperties = armcompute.Snapshot{
		Name:               &invalidSnapshotWithEmptyPropertiesName,
		ID:                 &invalidSnapshotWithEmptyPropertiesURI,
		Properties:			&armcompute.SnapshotProperties{},
	}

	testVMName = "test-vm"
	testVMID   = fmt.Sprint(virtualMachineURIFormat, testSubscription, testResourceGroup, "deadbeef-eded-eded-eded-0123456789abcd")
	testVMURI  = fmt.Sprint(virtualMachineURIFormat, testSubscription, testResourceGroup, testVMName)
	testVM     = armcompute.VirtualMachine{
		Name: &testVMName,
		ID:   &testVMURI,
		Properties: &armcompute.VirtualMachineProperties{
			ProvisioningState: &provisioningStateSucceeded,
			StorageProfile: &armcompute.StorageProfile{
				DataDisks: []*armcompute.DataDisk{},
			},
		},
	}

	missingVMName = "missing-vm"
)

func init() {
	var err error
	testAzSnapshot, err = azureutils.NewAzureDiskSnapshot(testDiskURI0, &testSnapshot)
	if err != nil {
		panic(err)
	}
}

func NewTestCloudProvisioner(controller *gomock.Controller) *CloudProvisioner {
	cloud := azureutils.GetTestCloud(controller)
	cloud.SubscriptionID = testSubscription
	cloud.ResourceGroup = testResourceGroup

	cache, err := azcache.NewTimedcache(time.Minute, func(key string) (interface{}, error) {
		return nil, nil
	})
	if err != nil {
		panic(err)
	}

	return &CloudProvisioner{
		cloud: cloud,
		config: &azdiskv1beta2.AzDiskDriverConfiguration{
			ControllerConfig: azdiskv1beta2.ControllerConfiguration{
				Enabled: true,
			},
			NodeConfig: azdiskv1beta2.NodeConfiguration{
				Enabled:                true,
				EnablePerfOptimization: util.IsLinuxOS(),
			},
		},
		getDiskThrottlingCache: cache,
	}
}

func mockExistingDisk(provisioner *CloudProvisioner) {		
	fget := func(ctx context.Context, resourceGroupName string, diskName string, options *armcompute.DisksClientGetOptions) (resp azfake.Responder[armcompute.DisksClientGetResponse], errResp azfake.ErrorResponder) {
		if (resourceGroupName == testResourceGroup && diskName == testDiskName0) {
			resp.SetResponse(http.StatusOK, armcompute.DisksClientGetResponse{
				Disk:	testDisk,
			}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}

		resp.SetResponse(http.StatusNotFound, armcompute.DisksClientGetResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}

	client := provisioner.cloud.CreateDisksClientWithFunction(testSubscription, fget, nil, nil, nil, nil)
	client.Get(context.Background(), testResourceGroup, testDiskName0, nil)
}

func mockClonedDisk(provisioner *CloudProvisioner) {
	fget := func(ctx context.Context, resourceGroupName string, diskName string, options *armcompute.DisksClientGetOptions) (resp azfake.Responder[armcompute.DisksClientGetResponse], errResp azfake.ErrorResponder) {
		if (resourceGroupName == testResourceGroup && diskName == clonedDiskName) {
			resp.SetResponse(http.StatusOK, armcompute.DisksClientGetResponse{
				Disk:	clonedDisk,
			}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}

		resp.SetResponse(http.StatusNotFound, armcompute.DisksClientGetResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}

	client := provisioner.cloud.CreateDisksClientWithFunction(testSubscription, fget, nil, nil, nil, nil)
	client.Get(context.Background(), testResourceGroup, clonedDiskName, nil)
}

func mockMissingDisk(provisioner *CloudProvisioner) {
	fget := func(ctx context.Context, resourceGroupName string, diskName string, options *armcompute.DisksClientGetOptions) (resp azfake.Responder[armcompute.DisksClientGetResponse], errResp azfake.ErrorResponder) {
		if (resourceGroupName == testResourceGroup && diskName == missingDiskName) {
			resp.SetResponse(http.StatusNotFound, armcompute.DisksClientGetResponse{
				Disk:	armcompute.Disk{},
			}, nil)
			errResp.SetError(notFoundError.RawError)
			return resp, errResp
		}

		resp.SetResponse(http.StatusNotFound, armcompute.DisksClientGetResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}

	client := provisioner.cloud.CreateDisksClientWithFunction(testSubscription, fget, nil, nil, nil, nil)
	client.Get(context.Background(), testResourceGroup, missingDiskName, nil)
}

func mockInvalidDisks(provisioner *CloudProvisioner) {
	fget1 := func(ctx context.Context, resourceGroupName string, diskName string, options *armcompute.DisksClientGetOptions) (resp azfake.Responder[armcompute.DisksClientGetResponse], errResp azfake.ErrorResponder) {
		if (resourceGroupName == testResourceGroup && diskName == invalidDiskWithMissingPropertiesName) {
			resp.SetResponse(http.StatusNotFound, armcompute.DisksClientGetResponse{
				Disk:	invalidDiskWithMissingProperties,
			}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}

		resp.SetResponse(http.StatusNotFound, armcompute.DisksClientGetResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}

	client1 := provisioner.cloud.CreateDisksClientWithFunction(testSubscription, fget1, nil, nil, nil, nil)
	client1.Get(context.Background(), testResourceGroup, invalidDiskWithMissingPropertiesName, nil)

	fget2 := func(ctx context.Context, resourceGroupName string, diskName string, options *armcompute.DisksClientGetOptions) (resp azfake.Responder[armcompute.DisksClientGetResponse], errResp azfake.ErrorResponder) {
		if (resourceGroupName == testResourceGroup && diskName == invalidDiskWithEmptyPropertiesName) {
			resp.SetResponse(http.StatusOK, armcompute.DisksClientGetResponse{
				Disk:	invalidDiskWithEmptyProperties,
			}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}

		resp.SetResponse(http.StatusOK, armcompute.DisksClientGetResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}

	client2 := provisioner.cloud.CreateDisksClientWithFunction(testSubscription, fget2, nil, nil, nil, nil)
	client2.Get(context.Background(), testResourceGroup, invalidDiskWithEmptyPropertiesName, nil)
}

func mockExistingSnapshot(provisioner *CloudProvisioner) {
	fget := func(ctx context.Context, resourceGroupName string, snapshotName string, options *armcompute.SnapshotsClientGetOptions) (resp azfake.Responder[armcompute.SnapshotsClientGetResponse], errResp azfake.ErrorResponder) {
		if resourceGroupName == testResourceGroup && snapshotName == testSnapshotName {
			resp.SetResponse(http.StatusOK, armcompute.SnapshotsClientGetResponse {
				Snapshot:	testSnapshot,
			}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}

		resp.SetResponse(http.StatusNotFound, armcompute.SnapshotsClientGetResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}

	client := provisioner.cloud.CreateSnapshotsClientWithFunction(testSubscription, fget, nil, nil, nil)
	client.Get(context.Background(), testResourceGroup, testSnapshotName, nil)
}

func mockMissingSnapshot(provisioner *CloudProvisioner) {
	fget := func(ctx context.Context, resourceGroupName string, snapshotName string, options *armcompute.SnapshotsClientGetOptions) (resp azfake.Responder[armcompute.SnapshotsClientGetResponse], errResp azfake.ErrorResponder) {
		if resourceGroupName == testResourceGroup && snapshotName == missingSnapshotName {
			resp.SetResponse(http.StatusNotFound, armcompute.SnapshotsClientGetResponse {
				Snapshot:	armcompute.Snapshot{},
			}, nil)
			errResp.SetError(notFoundError.RawError)
			return resp, errResp
		}

		resp.SetResponse(http.StatusNotFound, armcompute.SnapshotsClientGetResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}

	client := provisioner.cloud.CreateSnapshotsClientWithFunction(testSubscription, fget, nil, nil, nil)
	client.Get(context.Background(), testResourceGroup, missingSnapshotName, nil)
}

func mockInvalidSnapshots(provisioner *CloudProvisioner) {
	fget1 := func(ctx context.Context, resourceGroupName string, snapshotName string, options *armcompute.SnapshotsClientGetOptions) (resp azfake.Responder[armcompute.SnapshotsClientGetResponse], errResp azfake.ErrorResponder) {
		if resourceGroupName == testResourceGroup && snapshotName == invalidSnapshotWithMissingPropertiesName {
			resp.SetResponse(http.StatusOK, armcompute.SnapshotsClientGetResponse {
				Snapshot:	invalidSnapshotWithMissingProperties,
			}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}

		resp.SetResponse(http.StatusNotFound, armcompute.SnapshotsClientGetResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}
	client1 := provisioner.cloud.CreateSnapshotsClientWithFunction(testSubscription, fget1, nil, nil, nil)
	client1.Get(context.Background(), testResourceGroup, invalidSnapshotWithMissingPropertiesName, nil)

	fget2 := func(ctx context.Context, resourceGroupName string, snapshotName string, options *armcompute.SnapshotsClientGetOptions) (resp azfake.Responder[armcompute.SnapshotsClientGetResponse], errResp azfake.ErrorResponder) {
		if resourceGroupName == testResourceGroup && snapshotName == invalidSnapshotWithEmptyPropertiesName {
			resp.SetResponse(http.StatusOK, armcompute.SnapshotsClientGetResponse {
				Snapshot:	invalidSnapshotWithEmptyProperties,
			}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}

		resp.SetResponse(http.StatusNotFound, armcompute.SnapshotsClientGetResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}
	client2 := provisioner.cloud.CreateSnapshotsClientWithFunction(testSubscription, fget2, nil, nil, nil)
	client2.Get(context.Background(), testResourceGroup, invalidSnapshotWithEmptyPropertiesName, nil)
}

func mockUpdateVM(provisioner *CloudProvisioner) {
	fget := func(ctx context.Context, resourceGroupName string, vmName string, options *armcompute.VirtualMachinesClientGetOptions) (resp azfake.Responder[armcompute.VirtualMachinesClientGetResponse], errResp azfake.ErrorResponder) {
		if resourceGroupName == testResourceGroup && vmName == testVMName {
			resp.SetResponse(http.StatusOK, armcompute.VirtualMachinesClientGetResponse{
				VirtualMachine:	testVM,
			}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}
		resp.SetResponse(http.StatusNotFound, armcompute.VirtualMachinesClientGetResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}

	fupdate := func(ctx context.Context, resourceGroupName string, vmName string, parameters armcompute.VirtualMachineUpdate, options *armcompute.VirtualMachinesClientBeginUpdateOptions) (resp azfake.PollerResponder[armcompute.VirtualMachinesClientUpdateResponse], errResp azfake.ErrorResponder) {
		if resourceGroupName == testResourceGroup && vmName == testVMName {
			vm := &armcompute.VirtualMachine{
				Name:                     &vmName,
				Plan:                     parameters.Plan,
				Properties: 			  parameters.Properties,
				Identity:                 parameters.Identity,
				Zones:                    parameters.Zones,
				Tags:                     parameters.Tags,
				ID:                       &testVMID,
			}
			resp.SetTerminalResponse(http.StatusOK, armcompute.VirtualMachinesClientUpdateResponse{
				VirtualMachine:		*vm,
			}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}
		resp.SetTerminalResponse(http.StatusNotFound, armcompute.VirtualMachinesClientUpdateResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}
	client := provisioner.cloud.CreateVMClientWithFunction(provisioner.cloud.SubscriptionID, fget, fupdate)
	client.Get(context.Background(), testResourceGroup, testVMName, nil)
	client.BeginUpdate(context.Background(), testResourceGroup, testVMName, armcompute.VirtualMachineUpdate{
		Properties:	testVM.Properties,
	}, nil)
}

func mockPeristentVolumesList(provisioner *CloudProvisioner, pvCount int32) {
	var pvList = make([]runtime.Object, pvCount)
	var diskList = make([]*armcompute.Disk, pvCount)

	for i := int32(0); i < pvCount; i++ {
		diskName := fmt.Sprintf("pvc-%d", i+1)
		diskURI := fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, diskName)

		pvList[i] = &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name:      diskName,
				Namespace: "",
			},
			Spec: corev1.PersistentVolumeSpec{
				PersistentVolumeSource: corev1.PersistentVolumeSource{
					CSI: &corev1.CSIPersistentVolumeSource{
						Driver:       azureconstants.DefaultDriverName,
						VolumeHandle: diskURI,
					},
				},
			},
		}

		timeCreated := time.Now()
		diskList[i] = &armcompute.Disk{
			Name: &diskName,
			ID:   &diskURI,
			Properties: &armcompute.DiskProperties{
				DiskSizeGB:        &testDiskSizeGiB,
				DiskState:         to.Ptr(armcompute.DiskStateUnattached),
				ProvisioningState: &provisioningStateSucceeded,
				TimeCreated:       &timeCreated,
			},
		}

		fget := func(ctx context.Context, resourceGroupName string, name string, options *armcompute.DisksClientGetOptions) (resp azfake.Responder[armcompute.DisksClientGetResponse], errResp azfake.ErrorResponder) {
			if (resourceGroupName == testResourceGroup && name == diskName) {
				resp.SetResponse(http.StatusOK, armcompute.DisksClientGetResponse{
					Disk:	*diskList[i],
				}, nil)
				errResp.SetError(nil)
				return resp, errResp
			}

			resp.SetResponse(http.StatusNotFound, armcompute.DisksClientGetResponse{}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}

		client := provisioner.cloud.CreateDisksClientWithFunction(testSubscription, fget, nil, nil, nil, nil)
		client.Get(context.Background(), testResourceGroup, diskName, nil)
	}

	flist := func(resourceGroupName string, options *armcompute.DisksClientListByResourceGroupOptions) (resp azfake.PagerResponder[armcompute.DisksClientListByResourceGroupResponse]) {
		if resourceGroupName == testResourceGroup {
			resp.AddPage(http.StatusOK, armcompute.DisksClientListByResourceGroupResponse{
				DiskList: armcompute.DiskList{
					Value: diskList,
				},
			}, nil)
			resp.AddError(nil)
			return resp
		}
		resp.AddError(notFoundError.RawError)
		return resp
	}
	client := provisioner.cloud.CreateDisksClientWithFunction(testSubscription, nil, nil, nil, flist, nil)
	client.NewListByResourceGroupPager(testResourceGroup, nil)

	provisioner.cloud.KubeClient = kfake.NewSimpleClientset(pvList...)
}

func mockSnapshotsList(provisioner *CloudProvisioner, disk1Count, disk2Count int32) {
	totalCount := disk1Count + disk2Count
	azssList := make([]*armcompute.Snapshot, totalCount)
	sourceDiskURIs := [2]string{
		testDiskURI0,
		fmt.Sprintf(computeDiskURIFormat, testSubscription, testResourceGroup, "test-disk-2"),
	}

	for i := int32(0); i < totalCount; i++ {
		ssName := fmt.Sprintf("snapshot-%d", i+1)
		ssURI := fmt.Sprintf(computeSnapshotURIFormat, testSubscription, testResourceGroup, ssName)

		var sourceDiskURI string

		if i < disk1Count {
			sourceDiskURI = sourceDiskURIs[0]
		} else {
			sourceDiskURI = sourceDiskURIs[1]
		}

		timeCreated := time.Now()
		azssList[i] = &armcompute.Snapshot{
			Name: &ssName,
			ID:   &ssURI,
			Properties: &armcompute.SnapshotProperties{
				CreationData: &armcompute.CreationData{
					SourceResourceID: &sourceDiskURI,
				},
				DiskSizeGB:        &testDiskSizeGiB,
				ProvisioningState: &provisioningStateSucceeded,
				TimeCreated:       &timeCreated,
			},
		}

		fget := func(ctx context.Context, resourceGroupName string, snapshotName string, options *armcompute.SnapshotsClientGetOptions) (resp azfake.Responder[armcompute.SnapshotsClientGetResponse], errResp azfake.ErrorResponder) {
			if resourceGroupName == testResourceGroup && snapshotName == ssName {
				resp.SetResponse(http.StatusOK, armcompute.SnapshotsClientGetResponse{
					Snapshot: *azssList[i],
				}, nil)
				errResp.SetError(nil)
				return resp, errResp
			}
			
			resp.SetResponse(http.StatusNotFound, armcompute.SnapshotsClientGetResponse{}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}

		client := provisioner.cloud.CreateSnapshotsClientWithFunction(testSubscription, fget, nil, nil, nil)
		client.Get(nil, testResourceGroup, ssName, nil)
		
	}

	flist := func(resourceGroupName string, options *armcompute.SnapshotsClientListByResourceGroupOptions) (resp azfake.PagerResponder[armcompute.SnapshotsClientListByResourceGroupResponse]) {
		resp.AddPage(http.StatusOK, armcompute.SnapshotsClientListByResourceGroupResponse{
			SnapshotList: armcompute.SnapshotList{
				Value: azssList,
			},
		}, nil)
		resp.AddError(nil)
		return resp
	}

	client := provisioner.cloud.CreateSnapshotsClientWithFunction(testSubscription, nil, nil, nil, flist)
	client.NewListByResourceGroupPager(testResourceGroup, nil)
}

func TestCreateVolume(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCloudProvisioner(mockCtrl)

	mockExistingDisk(provisioner)

	fcreate := func(ctx context.Context, resourceGroupName string, diskName string, disk armcompute.Disk, options *armcompute.DisksClientBeginCreateOrUpdateOptions) (resp azfake.PollerResponder[armcompute.DisksClientCreateOrUpdateResponse], errResp azfake.ErrorResponder) {
		var mockedDisk armcompute.Disk

		if resourceGroupName == testResourceGroup && diskName == testDiskName0 {
			mockedDisk = testDisk
		} else {
			mockedDisk = disk
			diskID := fmt.Sprintf(computeDiskURIFormat, testSubscription, resourceGroupName, diskName)
			timeCreated := time.Now()

			mockedDisk.ID = &diskID
			mockedDisk.Name = &diskName
			mockedDisk.Properties.TimeCreated = &timeCreated
			mockedDisk.Properties.ProvisioningState = &provisioningStateSucceeded
		}

		fget := func(ctx context.Context, rg string, name string, options *armcompute.DisksClientGetOptions) (resp azfake.Responder[armcompute.DisksClientGetResponse], errResp azfake.ErrorResponder) {
			if rg == resourceGroupName && name == diskName {
				resp.SetResponse(http.StatusOK, armcompute.DisksClientGetResponse{
					Disk:	mockedDisk,
				}, nil)
				errResp.SetError(nil)
				return resp, errResp
			}
			resp.SetResponse(http.StatusNotFound, armcompute.DisksClientGetResponse{}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}
		
		client := provisioner.cloud.CreateDisksClientWithFunction(provisioner.cloud.SubscriptionID, fget, nil, nil, nil, nil)
		client.Get(context.Background(), resourceGroupName, diskName, nil) 

		resp.SetTerminalResponse(http.StatusOK, armcompute.DisksClientCreateOrUpdateResponse{
			Disk:	mockedDisk,
		}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}
	client := provisioner.cloud.CreateDisksClientWithFunction(provisioner.cloud.SubscriptionID, nil, fcreate, nil, nil, nil)
	client.BeginCreateOrUpdate(context.Background(), "", "", armcompute.Disk{}, nil)

	tests := []struct {
		description   string
		diskName      string
		capacity      *azdiskv1beta2.CapacityRange
		capabilities  []azdiskv1beta2.VolumeCapability
		parameter     map[string]string
		secrets       map[string]string
		contentSource *azdiskv1beta2.ContentVolumeSource
		topology      *azdiskv1beta2.TopologyRequirement
		expectedError error
		disabled      bool
	}{
		{
			description:   "[Success] Creates a disk with default parameters",
			diskName:      "disk-with-default-parameters",
			expectedError: nil,
		},
		{
			description: "[Success] Creates a disk with specific parameters",
			diskName:    "disk-with-specific-parameters",
			capacity: &azdiskv1beta2.CapacityRange{
				RequiredBytes: util.GiBToBytes(10),
				LimitBytes:    util.GiBToBytes(10),
			},
			capabilities: []azdiskv1beta2.VolumeCapability{
				{
					AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			parameter: map[string]string{
				"resourceGroup":      testResourceGroup,
				"maxShares":          "1",
				"storageAccountType": "Premium_LRS",
			},
			secrets: map[string]string{
				"sh": "a secret",
			},
			contentSource: &azdiskv1beta2.ContentVolumeSource{
				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
				ContentSourceID: testDiskURI0,
			},
			topology: &azdiskv1beta2.TopologyRequirement{
				Requisite: []azdiskv1beta2.Topology{
					{
						Segments: map[string]string{
							topologyKeyStr: "testregion-1",
						},
					},
				},
			},
			expectedError: nil,
		},
		{
			description: "[Failure] advanced perfProfile fails if no device settings provided",
			disabled:    !provisioner.isPerfOptimizationEnabled(),
			diskName:    "disk-with-specific-parameters",
			capacity: &azdiskv1beta2.CapacityRange{
				RequiredBytes: util.GiBToBytes(10),
				LimitBytes:    util.GiBToBytes(10),
			},
			capabilities: []azdiskv1beta2.VolumeCapability{
				{
					AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			parameter: map[string]string{
				"resourceGroup":      testResourceGroup,
				"maxShares":          "1",
				"storageAccountType": "Premium_LRS",
				"perfProfile":        "advanced",
			},
			secrets: map[string]string{
				"sh": "a secret",
			},
			contentSource: &azdiskv1beta2.ContentVolumeSource{
				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
				ContentSourceID: testDiskURI0,
			},
			topology: &azdiskv1beta2.TopologyRequirement{
				Requisite: []azdiskv1beta2.Topology{
					{
						Segments: map[string]string{
							topologyKeyStr: "testregion-1",
						},
					},
				},
			},
			expectedError: fmt.Errorf("AreDeviceSettingsValid: No deviceSettings passed"),
		},
		{
			description: "[Failure] advanced perfProfile fails if invalid device settings provided",
			disabled:    !provisioner.isPerfOptimizationEnabled(),
			diskName:    "disk-with-specific-parameters",
			capacity: &azdiskv1beta2.CapacityRange{
				RequiredBytes: util.GiBToBytes(10),
				LimitBytes:    util.GiBToBytes(10),
			},
			capabilities: []azdiskv1beta2.VolumeCapability{
				{
					AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			parameter: map[string]string{
				"resourceGroup":      testResourceGroup,
				"maxShares":          "1",
				"storageAccountType": "Premium_LRS",
				"perfProfile":        "advanced",
				azureconstants.DeviceSettingsKeyPrefix + "device/scheduler":        "8",
				azureconstants.DeviceSettingsKeyPrefix + "../../device/nr_request": "8",
			},
			secrets: map[string]string{
				"sh": "a secret",
			},
			contentSource: &azdiskv1beta2.ContentVolumeSource{
				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
				ContentSourceID: testDiskURI0,
			},
			topology: &azdiskv1beta2.TopologyRequirement{
				Requisite: []azdiskv1beta2.Topology{
					{
						Segments: map[string]string{
							topologyKeyStr: "testregion-1",
						},
					},
				},
			},
			expectedError: fmt.Errorf("AreDeviceSettingsValid: Setting /sys/device/nr_request is not a valid file path under %s",
				azureconstants.DummyBlockDevicePathLinux),
		},
		{
			description: "[Success] advanced perfProfile succeeds if valid device settings provided",
			disabled:    !provisioner.isPerfOptimizationEnabled(),
			diskName:    "disk-with-specific-parameters",
			capacity: &azdiskv1beta2.CapacityRange{
				RequiredBytes: util.GiBToBytes(10),
				LimitBytes:    util.GiBToBytes(10),
			},
			capabilities: []azdiskv1beta2.VolumeCapability{
				{
					AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
				},
			},
			parameter: map[string]string{
				"resourceGroup":      testResourceGroup,
				"maxShares":          "1",
				"storageAccountType": "Premium_LRS",
				"perfProfile":        "advanced",
				azureconstants.DeviceSettingsKeyPrefix + "device/nr_request": "8",
			},
			secrets: map[string]string{
				"sh": "a secret",
			},
			contentSource: &azdiskv1beta2.ContentVolumeSource{
				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
				ContentSourceID: testDiskURI0,
			},
			topology: &azdiskv1beta2.TopologyRequirement{
				Requisite: []azdiskv1beta2.Topology{
					{
						Segments: map[string]string{
							topologyKeyStr: "testregion-1",
						},
					},
				},
			},
			expectedError: nil,
		},
		{
			description: "[Success] Creates a disk with Premium_ZRS storage account type",
			diskName:    "disk-with-premium-zrs",
			parameter: map[string]string{
				"storageAccountType": "Premium_ZRS",
			},
			expectedError: nil,
		},
		{
			description: "[Success] Returns no error for existing disk when same creation parameters are used (CreateVolume is idempotent)",
			diskName:    testDiskName0,
			capacity: &azdiskv1beta2.CapacityRange{
				RequiredBytes: util.GiBToBytes(int64(testDiskSizeGiB)),
				LimitBytes:    util.GiBToBytes(int64(testDiskSizeGiB)),
			},
			parameter: map[string]string{
				"resourceGroup": testResourceGroup,
			},
			expectedError: nil,
		},
		{
			description: "[Failure] Returns an error for existing disk when different a different size is requested",
			diskName:    testDiskName0,
			capacity: &azdiskv1beta2.CapacityRange{
				RequiredBytes: util.GiBToBytes(int64(testDiskSizeGiB * 2)),
				LimitBytes:    util.GiBToBytes(int64(testDiskSizeGiB * 2)),
			},
			parameter: map[string]string{
				"resourceGroup": testResourceGroup,
			},
			expectedError: status.Errorf(codes.AlreadyExists, "the request volume already exists, but its capacity(%d) is different from (%d)", testDiskSizeGiB, testDiskSizeGiB*2),
		},
		{
			description: "[Failure] Returns an error when requested size is larger than limit",
			diskName:    "disk-with-invalid-capacity",
			capacity: &azdiskv1beta2.CapacityRange{
				RequiredBytes: util.GiBToBytes(100),
				LimitBytes:    util.GiBToBytes(10),
			},
			expectedError: status.Error(codes.InvalidArgument, "After round-up, volume size exceeds the limit specified"),
		},
		{
			description: "[Failure] Returns an error when maxShares is not a number",
			diskName:    "disk-with-invalid-max-shares",
			parameter: map[string]string{
				"maxShares": "NaN",
			},
			expectedError: status.Error(codes.InvalidArgument, "Failed parsing disk parameters: parse NaN failed with error: strconv.Atoi: parsing \"NaN\": invalid syntax"),
		},
		{
			description: "[Failure] Returns an error when an unsupported storage account type is specified",
			diskName:    "disk-with-invalid-storage-account-type",
			parameter: map[string]string{
				"storageAccountType": "SuperPremiumSSD_URS",
			},
			expectedError: status.Error(codes.InvalidArgument, "azureDisk - SuperPremiumSSD_URS is not supported sku/storageaccounttype. Supported values are [Premium_LRS Premium_ZRS Standard_LRS StandardSSD_LRS StandardSSD_ZRS UltraSSD_LRS PremiumV2_LRS]"),
		},
		{
			description: "[Failure] Returns an error when an unsupported caching mode is specified",
			diskName:    "disk-with-invalid-caching-mode",
			parameter: map[string]string{
				"cachingmode": "InvalidCachingMode",
			},
			expectedError: status.Error(codes.InvalidArgument, "azureDisk - InvalidCachingMode is not supported cachingmode. Supported values are [None ReadOnly ReadWrite]"),
		},
	}

	for _, test := range tests {
		if test.disabled {
			continue
		}
		tt := test
		t.Run(test.description, func(t *testing.T) {
			if tt.diskName != testDiskName0 {
				fget := func(ctx context.Context, resourceGroupName string, diskName string, options *armcompute.DisksClientGetOptions) (resp azfake.Responder[armcompute.DisksClientGetResponse], errResp azfake.ErrorResponder) {
					if diskName == tt.diskName {
						resp.SetResponse(http.StatusNotFound, armcompute.DisksClientGetResponse{
							Disk:	armcompute.Disk{},
						}, nil)
						errResp.SetError(notFoundError.RawError)
						return resp, errResp
					}
					resp.SetResponse(http.StatusNotFound, armcompute.DisksClientGetResponse{}, nil)
					errResp.SetError(nil)
					return resp, errResp
				}
		
				client := provisioner.cloud.CreateDisksClientWithFunction(provisioner.cloud.SubscriptionID, fget, nil, nil, nil, nil)
				client.Get(context.Background(), "", tt.diskName, nil)
			}

			volume, err := provisioner.CreateVolume(
				context.TODO(),
				tt.diskName,
				tt.capacity,
				tt.capabilities,
				tt.parameter,
				tt.secrets,
				tt.contentSource,
				tt.topology)

			assert.Equal(t, tt.expectedError, err)
			if err == nil {
				assert.NotNil(t, volume)
			}
		})
	}
}

func TestDeleteVolume(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCloudProvisioner(mockCtrl)

	mockExistingDisk(provisioner)

	fdelete := func(ctx context.Context, resourceGroupName string, diskName string, options *armcompute.DisksClientBeginDeleteOptions) (resp azfake.PollerResponder[armcompute.DisksClientDeleteResponse], errResp azfake.ErrorResponder) {
		if resourceGroupName == testResourceGroup && diskName == testDiskName0 {
			resp.SetTerminalResponse(http.StatusOK, armcompute.DisksClientDeleteResponse{}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}
		resp.AddNonTerminalResponse(http.StatusNotFound, nil)
		errResp.SetError(nil)
		return resp, errResp
	}

	client := provisioner.cloud.CreateDisksClientWithFunction(testSubscription, nil, nil, fdelete, nil, nil)
	client.BeginDelete(context.Background(), testResourceGroup, testDiskName0, nil)

	mockMissingDisk(provisioner)

	tests := []struct {
		description   string
		diskURI       string
		expectedError error
	}{
		{
			description:   "[Success] Deletes an existing disk",
			diskURI:       testDiskURI0,
			expectedError: nil,
		},
		{
			description:   "[Success] Returns no error for missing disk",
			diskURI:       missingDiskURI,
			expectedError: nil,
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			err := provisioner.DeleteVolume(context.TODO(), tt.diskURI, map[string]string{})
			assert.Equal(t, tt.expectedError, err)
		})
	}
}

func TestPublishVolume(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCloudProvisioner(mockCtrl)

	mockExistingDisk(provisioner)
	mockMissingDisk(provisioner)
	mockUpdateVM(provisioner)

	fget := func(ctx context.Context, rg, vmName string, option *armcompute.VirtualMachinesClientGetOptions) (resp azfake.Responder[armcompute.VirtualMachinesClientGetResponse], errResp azfake.ErrorResponder) {
		if rg == testResourceGroup && vmName == missingVMName {
			resp.SetResponse(http.StatusNotFound, armcompute.VirtualMachinesClientGetResponse{
				VirtualMachine:	armcompute.VirtualMachine{},
			}, nil) 
			errResp.SetError(notFoundError.RawError)
			return resp, errResp
		}
					
		resp.SetResponse(http.StatusNotFound, armcompute.VirtualMachinesClientGetResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}
	client := provisioner.cloud.CreateVMClientWithFunction(provisioner.cloud.SubscriptionID, fget, nil)
	client.Get(context.Background(), testResourceGroup, missingVMName, nil)

	tests := []struct {
		description        string
		nodeID             string
		diskURI            string
		expectedError      error
		expectedAsyncError error
	}{

		{
			description:        "[Success] Attaches an existing disk",
			nodeID:             testVMName,
			diskURI:            testDiskURI0,
			expectedError:      nil,
			expectedAsyncError: nil,
		},
		{
			description:        "[Failure] Returns error for missing VM",
			nodeID:             missingVMName,
			diskURI:            testDiskURI0,
			expectedError:      status.Errorf(codes.NotFound, "failed to get azure instance id for node %q: %v", missingVMName, cloudprovider.InstanceNotFound),
			expectedAsyncError: nil,
		},
		{
			description:        "[Failure] Returns error for missing disk",
			nodeID:             testVMName,
			diskURI:            missingDiskURI,
			expectedError:      status.Errorf(codes.NotFound, "Volume not found, failed with error: %v", notFoundError.Error()),
			expectedAsyncError: nil,
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			result := provisioner.PublishVolume(context.TODO(), tt.diskURI, tt.nodeID, map[string]string{})
			err := <-result.ResultChannel()
			assert.Equal(t, tt.expectedError, err)
		})
	}
}

func TestUnpublishVolume(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCloudProvisioner(mockCtrl)

	mockExistingDisk(provisioner)
	mockMissingDisk(provisioner)
	mockUpdateVM(provisioner)

	lun := int32(1)
	attachedDisks := []*armcompute.DataDisk{
		{
			Lun:  &lun,
			Name: &testDiskName0,
		},
	}

	testVMWithAttachedDisk := testVM
	testVMWithAttachedDisk.Properties.StorageProfile.DataDisks = attachedDisks

	fn1 := func(ctx context.Context, rg, vmName string, option *armcompute.VirtualMachinesClientGetOptions) (resp azfake.Responder[armcompute.VirtualMachinesClientGetResponse], errResp azfake.ErrorResponder) {
		if rg == testResourceGroup && vmName == testVMName {
			resp.SetResponse(http.StatusOK, armcompute.VirtualMachinesClientGetResponse{
				VirtualMachine:	testVMWithAttachedDisk,
			}, nil) 
			errResp.SetError(nil)
			return resp, errResp
		}
					
		resp.SetResponse(http.StatusNotFound, armcompute.VirtualMachinesClientGetResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}
	client1 := provisioner.cloud.CreateVMClientWithFunction(provisioner.cloud.SubscriptionID, fn1, nil)
	client1.Get(context.Background(), testResourceGroup, testVMName, nil)

	fn2 := func(ctx context.Context, rg, vmName string, option *armcompute.VirtualMachinesClientGetOptions) (resp azfake.Responder[armcompute.VirtualMachinesClientGetResponse], errResp azfake.ErrorResponder) {
		if rg == testResourceGroup && vmName == missingVMName {
			resp.SetResponse(http.StatusNotFound, armcompute.VirtualMachinesClientGetResponse{
				VirtualMachine:	armcompute.VirtualMachine{},
			}, nil) 
			errResp.SetError(notFoundError.RawError)
			return resp, errResp
		}
					
		resp.SetResponse(http.StatusNotFound, armcompute.VirtualMachinesClientGetResponse{}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}
	client2 := provisioner.cloud.CreateVMClientWithFunction(provisioner.cloud.SubscriptionID, fn2, nil)
	client2.Get(context.Background(), testResourceGroup, missingVMName, nil)

	tests := []struct {
		description   string
		nodeID        string
		diskURI       string
		expectedError error
	}{
		{
			description:   "[Success] Detaches an attached disk",
			nodeID:        testVMName,
			diskURI:       testDiskURI0,
			expectedError: nil,
		},
		{
			description:   "[Success] Returns no error for missing VM",
			nodeID:        missingVMName,
			diskURI:       testDiskURI0,
			expectedError: nil,
		},
		{
			description:   "[Success] Returns no error for unattached disk",
			nodeID:        testVMName,
			diskURI:       missingDiskURI,
			expectedError: nil,
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			err := provisioner.UnpublishVolume(context.TODO(), tt.diskURI, tt.nodeID)
			assert.Equal(t, tt.expectedError, err)
		})
	}
}

func TestExpandVolume(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCloudProvisioner(mockCtrl)

	mockExistingDisk(provisioner)

	fupdate := func(ctx context.Context, resourceGroupName string, diskName string, disk armcompute.DiskUpdate, options *armcompute.DisksClientBeginUpdateOptions) (resp azfake.PollerResponder[armcompute.DisksClientUpdateResponse], errResp azfake.ErrorResponder) {
		if resourceGroupName == testResourceGroup && diskName == testDiskName0 {
			resp.SetTerminalResponse(http.StatusOK, armcompute.DisksClientUpdateResponse{
				Disk:	testDisk,
			}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}
		resp.AddNonTerminalResponse(http.StatusNotFound, nil)
		errResp.SetError(nil)
		return resp, errResp
	}
	client := provisioner.cloud.CreateDisksClientWithFunction(testSubscription, nil, nil, nil, nil, fupdate)
	client.BeginUpdate(context.Background(), testResourceGroup, testDiskName0, armcompute.DiskUpdate{}, nil)
	mockMissingDisk(provisioner)

	tests := []struct {
		description   string
		diskURI       string
		newCapacity   azdiskv1beta2.CapacityRange
		expectedError error
	}{
		{
			description: "[Success] Expands an existing disk",
			diskURI:     testDiskURI0,
			newCapacity: azdiskv1beta2.CapacityRange{
				RequiredBytes: util.GiBToBytes(int64(testDiskSizeGiB * 2)),
				LimitBytes:    util.GiBToBytes(int64(testDiskSizeGiB * 2)),
			},
			expectedError: nil,
		},
		{
			description: "[Failure] Returns an error for missing disk",
			diskURI:     missingDiskURI,
			newCapacity: azdiskv1beta2.CapacityRange{
				RequiredBytes: util.GiBToBytes(int64(testDiskSizeGiB * 2)),
				LimitBytes:    util.GiBToBytes(int64(testDiskSizeGiB * 2)),
			},
			expectedError: status.Errorf(codes.Internal, "could not get the disk(%s) under rg(%s) with error(%v)", missingDiskName, testResourceGroup, notFoundError.Error()),
		},
		{
			description: "[Failure] Returns an error for invalid URI",
			diskURI:     "invalid URI",
			newCapacity: azdiskv1beta2.CapacityRange{
				RequiredBytes: util.GiBToBytes(int64(testDiskSizeGiB * 2)),
				LimitBytes:    util.GiBToBytes(int64(testDiskSizeGiB * 2)),
			},
			expectedError: status.Errorf(codes.InvalidArgument, "disk URI(invalid URI) is not valid: invalid DiskURI: invalid URI, correct format: [/subscriptions/{sub-id}/resourcegroups/{group-name}/providers/microsoft.compute/disks/{disk-id}]"),
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			volumeStatus, err := provisioner.ExpandVolume(context.TODO(), tt.diskURI, &tt.newCapacity, map[string]string{})
			assert.Equal(t, tt.expectedError, err)
			if err == nil {
				assert.Equal(t, tt.newCapacity.RequiredBytes, volumeStatus.CapacityBytes)
				assert.True(t, volumeStatus.NodeExpansionRequired)
			}
		})
	}
}

func TestCreateSnapshot(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCloudProvisioner(mockCtrl)

	mockExistingDisk(provisioner)
	mockMissingDisk(provisioner)

	fcreate := func(ctx context.Context, resourceGroupName string, snapshotName string, snapshot armcompute.Snapshot, options *armcompute.SnapshotsClientBeginCreateOrUpdateOptions) (resp azfake.PollerResponder[armcompute.SnapshotsClientCreateOrUpdateResponse], errResp azfake.ErrorResponder) {
		if resourceGroupName == testResourceGroup && snapshotName == testSnapshotName {
			resp.AddNonTerminalResponse(existingDiskError.HTTPStatusCode, nil)
			errResp.SetError(existingDiskError.RawError)
			return resp, errResp
		} else if *snapshot.Properties.CreationData.SourceURI == missingDiskURI {
			resp.AddNonTerminalResponse(notFoundError.HTTPStatusCode, nil)
			errResp.SetError(notFoundError.RawError)
			return resp, errResp
		}

		snapshotURI := fmt.Sprintf(computeSnapshotURIFormat, testSubscription, resourceGroupName, snapshotName)
		timeCreated := time.Now()

		mockedSnapshot := snapshot
		mockedSnapshot.Name = &snapshotName
		mockedSnapshot.ID = &snapshotURI
		mockedSnapshot.Properties.DiskSizeGB = &testSnapshotSizeGiB
		mockedSnapshot.Properties.ProvisioningState = &provisioningStateSucceeded
		mockedSnapshot.Properties.TimeCreated = &timeCreated

		fget := func(ctx context.Context, rg string, ssName string, options *armcompute.SnapshotsClientGetOptions) (resp azfake.Responder[armcompute.SnapshotsClientGetResponse], errResp azfake.ErrorResponder) {
			if rg == resourceGroupName && ssName == snapshotName {
				resp.SetResponse(http.StatusNotFound, armcompute.SnapshotsClientGetResponse {
					Snapshot:	mockedSnapshot,
				}, nil)
				errResp.SetError(nil)
				return resp, errResp
			}

			resp.SetResponse(http.StatusNotFound, armcompute.SnapshotsClientGetResponse{}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}

		client := provisioner.cloud.CreateSnapshotsClientWithFunction(testSubscription, fget, nil, nil, nil)
		client.Get(context.Background(), testResourceGroup, snapshotName, nil)

		resp.SetTerminalResponse(http.StatusOK, armcompute.SnapshotsClientCreateOrUpdateResponse{
			Snapshot: mockedSnapshot,
		}, nil)
		errResp.SetError(nil)
		return resp, errResp
	}

	client := provisioner.cloud.CreateSnapshotsClientWithFunction(provisioner.cloud.SubscriptionID, nil, fcreate, nil, nil)
	client.BeginCreateOrUpdate(context.Background(), "", "", armcompute.Snapshot{}, nil)

	tests := []struct {
		description   string
		sourceDiskURI string
		snapshotName  string
		expectedError error
	}{
		{
			description:   "[Success] Creates a snapshot for existing disk",
			sourceDiskURI: testDiskURI0,
			snapshotName:  "new-snapshot",
			expectedError: nil,
		},
		{
			description:   "[Failure] Returns an error for an existing snapshot",
			sourceDiskURI: missingDiskURI,
			snapshotName:  testSnapshotName,
			expectedError: status.Errorf(codes.AlreadyExists, "request snapshot(%s) under rg(%s) already exists, but the SourceVolumeId is different, error details: %v", testSnapshotName, testResourceGroup, existingDiskError.Error()),
		},
		{
			description:   "[Failure] Returns an error for missing disk",
			sourceDiskURI: missingDiskURI,
			snapshotName:  "new-snapshot",
			expectedError: status.Errorf(codes.Internal, "create snapshot error: %v", notFoundError.Error()),
		},
	}

	parameters := map[string]string{
		"resourceGroup": testResourceGroup,
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			snapshot, err := provisioner.CreateSnapshot(
				context.TODO(),
				tt.sourceDiskURI,
				tt.snapshotName,
				map[string]string{},
				parameters)

			assert.Equal(t, tt.expectedError, err)
			if err == nil {
				assert.NotNil(t, snapshot)
			}
		})
	}
}

func TestDeleteSnapshot(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCloudProvisioner(mockCtrl)

	fdelete1 := func(ctx context.Context, resourceGroupName string, snapshotName string, options *armcompute.SnapshotsClientBeginDeleteOptions) (resp azfake.PollerResponder[armcompute.SnapshotsClientDeleteResponse], errResp azfake.ErrorResponder) {
		if resourceGroupName == testResourceGroup && snapshotName == testSnapshotName {
			resp.SetTerminalResponse(http.StatusOK, armcompute.SnapshotsClientDeleteResponse {}, nil)
			errResp.SetError(nil)
			return resp, errResp
		}
		resp.AddNonTerminalResponse(http.StatusNotFound, nil)
		errResp.SetError(nil)
		return resp, errResp
	}
	client1 := provisioner.cloud.CreateSnapshotsClientWithFunction(testSubscription, nil, nil, fdelete1, nil)
	client1.BeginDelete(context.Background(), testResourceGroup, testSnapshotName, nil)
	
	fdelete2 := func(ctx context.Context, resourceGroupName string, snapshotName string, options *armcompute.SnapshotsClientBeginDeleteOptions) (resp azfake.PollerResponder[armcompute.SnapshotsClientDeleteResponse], errResp azfake.ErrorResponder) {
		if resourceGroupName == testResourceGroup && snapshotName == missingSnapshotName {
			resp.AddNonTerminalResponse(http.StatusNotFound, nil)
			errResp.SetError(notFoundError.RawError)
			return resp, errResp
		}
		resp.AddNonTerminalResponse(http.StatusNotFound, nil)
		errResp.SetError(nil)
		return resp, errResp
	}
	client2 := provisioner.cloud.CreateSnapshotsClientWithFunction(testSubscription, nil, nil, fdelete2, nil)
	client2.BeginDelete(context.Background(), testResourceGroup, missingSnapshotName, nil)

	tests := []struct {
		description   string
		snapshotURI   string
		expectedError error
	}{
		{
			description:   "[Success] Deletes an existing snapshot",
			snapshotURI:   testSnapshotURI,
			expectedError: nil,
		},
		{
			description:   "[Failure] Returns no error for missing snapshot",
			snapshotURI:   missingSnapshotURI,
			expectedError: status.Errorf(codes.Internal, "delete snapshot error: %v", notFoundError.Error()),
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			err := provisioner.DeleteSnapshot(context.TODO(), tt.snapshotURI, map[string]string{})
			assert.Equal(t, tt.expectedError, err)
		})
	}
}

func TestListVolumes(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCloudProvisioner(mockCtrl)

	pvCount := int32(5)

	mockPeristentVolumesList(provisioner, pvCount)

	tests := []struct {
		description          string
		maxEntries           int32
		expectedError        error
		useNodeResourceGroup bool
	}{
		{
			description:          "[Success] Lists all volumes in one call",
			maxEntries:           pvCount,
			expectedError:        nil,
			useNodeResourceGroup: false,
		},
		{
			description:          "[Success] Lists all volumes in multiple calls",
			maxEntries:           pvCount / 2,
			expectedError:        nil,
			useNodeResourceGroup: false,
		},
		{
			description:          "[Success] Lists all volumes in one call through node resource group",
			maxEntries:           pvCount,
			expectedError:        nil,
			useNodeResourceGroup: true,
		},
		{
			description:          "[Success] Lists all volumes in multiple calls through node resource group",
			maxEntries:           pvCount / 2,
			expectedError:        nil,
			useNodeResourceGroup: true,
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			if tt.useNodeResourceGroup {
				savedKubeClient := provisioner.cloud.KubeClient
				defer func() { provisioner.cloud.KubeClient = savedKubeClient }()
				provisioner.cloud.KubeClient = nil
			}

			startToken := ""
			volumeCount := int32(0)

			for volumeCount < pvCount {
				volumeList, err := provisioner.ListVolumes(context.TODO(), tt.maxEntries, startToken)
				assert.Equal(t, tt.expectedError, err)
				if err != nil {
					break
				}

				numVolumesThisTime := int32(len(volumeList.Entries))
				if numVolumesThisTime == 0 {
					break
				}

				assert.LessOrEqualf(t, numVolumesThisTime, tt.maxEntries, "Returned unexpected number of volumes starting from token \"%s\"", startToken)

				volumeCount += numVolumesThisTime
				startToken = volumeList.NextToken
			}

			assert.Equal(t, pvCount, volumeCount, "Returned unexpected total number of volumes")
		})
	}
}

func TestListSnapshots(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCloudProvisioner(mockCtrl)

	disk1Count := int32(5)
	disk2Count := int32(4)
	totalSnapshotCount := disk1Count + disk2Count

	mockMissingSnapshot(provisioner)
	mockSnapshotsList(provisioner, disk1Count, disk2Count)

	tests := []struct {
		description    string
		maxEntries     int32
		expectedTotal  int32
		sourceVolumeID string
		snapshotID     string
		expectedError  error
	}{
		{
			description:    "[Success] Lists all snapshots in one call",
			maxEntries:     totalSnapshotCount,
			expectedTotal:  totalSnapshotCount,
			sourceVolumeID: "",
			snapshotID:     "",
			expectedError:  nil,
		},
		{
			description:    "[Success] Lists all snapshots for a specific disk in one call",
			maxEntries:     totalSnapshotCount,
			expectedTotal:  disk1Count,
			sourceVolumeID: testDiskURI0,
			snapshotID:     "",
			expectedError:  nil,
		},
		{
			description:    "[Success] Lists all snapshots in multiple calls",
			maxEntries:     totalSnapshotCount / 2,
			expectedTotal:  totalSnapshotCount,
			sourceVolumeID: "",
			snapshotID:     "",
			expectedError:  nil,
		},
		{
			description:    "[Success] Lists all snapshots for a specific disk in multiple calls",
			maxEntries:     disk1Count / 2,
			expectedTotal:  disk1Count,
			sourceVolumeID: testDiskURI0,
			snapshotID:     "",
			expectedError:  nil,
		},
		{
			description:    "[Success] Lists no snapshots for disk with no snapshots",
			maxEntries:     totalSnapshotCount,
			expectedTotal:  0,
			sourceVolumeID: missingDiskURI,
			snapshotID:     "",
			expectedError:  nil,
		},
		{
			description:    "[Success] Lists one snapshots when snapshot ID is specified",
			maxEntries:     totalSnapshotCount,
			expectedTotal:  1,
			sourceVolumeID: testDiskURI0,
			snapshotID:     fmt.Sprintf(computeSnapshotURIFormat, testSubscription, testResourceGroup, "snapshot-2"),
			expectedError:  nil,
		},
		{
			description:    "[Success] Lists zero snapshots when missing snapshot ID is specified",
			maxEntries:     totalSnapshotCount,
			expectedTotal:  0,
			sourceVolumeID: testDiskURI0,
			snapshotID:     missingSnapshotURI,
			expectedError:  nil,
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			startToken := ""
			totalCount := int32(0)

			for totalCount < tt.expectedTotal {
				snapshotList, err := provisioner.ListSnapshots(
					context.TODO(),
					tt.maxEntries,
					startToken,
					tt.sourceVolumeID,
					tt.snapshotID,
					map[string]string{})

				assert.Equal(t, tt.expectedError, err)
				if err != nil {
					break
				}

				numSnapshotsThisTime := int32(len(snapshotList.Entries))
				if numSnapshotsThisTime == 0 {
					break
				}

				assert.LessOrEqualf(t, numSnapshotsThisTime, tt.maxEntries, "Returned unexpected number of snapshots starting from token \"%s\"", startToken)

				totalCount += numSnapshotsThisTime

				if len(tt.snapshotID) != 0 {
					break
				}

				startToken = snapshotList.NextToken
			}

			assert.Equal(t, tt.expectedTotal, totalCount, "Returned unexpected total number of snapshots")
		})
	}
}

func TestCheckDiskExists(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCloudProvisioner(mockCtrl)

	mockExistingDisk(provisioner)
	mockMissingDisk(provisioner)

	tests := []struct {
		description   string
		diskURI       string
		expectedError bool
	}{
		{
			description:   "[Success] Returns expected disk for existing disk",
			diskURI:       testDiskURI0,
			expectedError: false,
		},
		{
			description:   "[Failure] Returns error for missing disk",
			diskURI:       missingDiskURI,
			expectedError: true,
		},
		{
			description:   "[Failure] Invalid disk URI",
			diskURI:       "invalid uri",
			expectedError: true,
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			actualDisk, err := provisioner.CheckDiskExists(context.TODO(), tt.diskURI)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, actualDisk)
				assert.Equal(t, testDisk, *actualDisk)
			}
		})
	}
}

func TestGetSourceDiskSize(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCloudProvisioner(mockCtrl)

	mockExistingDisk(provisioner)
	mockClonedDisk(provisioner)
	mockMissingDisk(provisioner)
	mockInvalidDisks(provisioner)

	tests := []struct {
		description   string
		diskName      string
		curDepth      int
		expectedSize  int32
		expectedError error
	}{
		{
			description:   "[Success] Returns size for existing disk",
			diskName:      testDiskName0,
			curDepth:      0,
			expectedSize:  testDiskSizeGiB,
			expectedError: nil,
		},
		{
			description:   "[Success] Returns size for cloned disk with source",
			diskName:      clonedDiskName,
			curDepth:      0,
			expectedSize:  testDiskSizeGiB,
			expectedError: nil,
		},
		{
			description:   "[Failure] Returns error when exceeding recursion depth",
			diskName:      clonedDiskName,
			curDepth:      1,
			expectedSize:  0,
			expectedError: status.Error(codes.Internal, fmt.Sprintf("current depth (%d) surpassed the max depth (%d) while searching for the source disk size", 2, 1)),
		},
		{
			description:   "[Failure] Returns error for missing disk",
			diskName:      missingDiskName,
			curDepth:      0,
			expectedSize:  0,
			expectedError: notFoundError.Error(),
		},
		{
			description:   "[Failure] Returns error for disk with missing properties",
			diskName:      invalidDiskWithMissingPropertiesName,
			curDepth:      0,
			expectedSize:  0,
			expectedError: status.Error(codes.Internal, fmt.Sprintf("DiskProperty not found for disk (%s) in resource group (%s)", invalidDiskWithMissingPropertiesName, testResourceGroup)),
		},
		{
			description:   "[Failure] Returns error for disk with empty properties",
			diskName:      invalidDiskWithEmptyPropertiesName,
			curDepth:      0,
			expectedSize:  0,
			expectedError: status.Error(codes.Internal, fmt.Sprintf("DiskSizeGB for disk (%s) in resourcegroup (%s) is nil", invalidDiskWithEmptyPropertiesName, testResourceGroup)),
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			diskSize, err := provisioner.GetSourceDiskSize(context.TODO(), testResourceGroup, tt.diskName, tt.curDepth, 1)
			assert.Equal(t, tt.expectedError, err)
			if err == nil {
				assert.NotNil(t, diskSize)
				assert.Equal(t, tt.expectedSize, *diskSize)
			}
		})
	}

}

func TestCheckDiskCapacity(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCloudProvisioner(mockCtrl)

	mockExistingDisk(provisioner)
	mockMissingDisk(provisioner)

	tests := []struct {
		description    string
		diskName       string
		requestedSize  int
		expectedResult bool
		expectedError  error
	}{
		{
			description:    "[Success] Size check is successful for existing disk",
			diskName:       testDiskName0,
			requestedSize:  int(testDiskSizeGiB),
			expectedResult: true,
			expectedError:  nil,
		},
		{
			description:    "[Failure] Returns error for existing disk of unexpected size",
			diskName:       testDiskName0,
			requestedSize:  11,
			expectedResult: false,
			expectedError:  status.Errorf(codes.AlreadyExists, "the request volume already exists, but its capacity(10) is different from (11)"),
		},
		{
			description:    "[Failure] Size check is successful for a missing disk",
			diskName:       missingDiskName,
			expectedResult: true,
			expectedError:  nil,
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			result, err := provisioner.CheckDiskCapacity(context.TODO(), testResourceGroup, tt.diskName, tt.requestedSize)
			assert.Equal(t, tt.expectedResult, result)
			assert.Equal(t, tt.expectedError, err)
		})
	}

}

func TestGetSnapshotByID(t *testing.T) {
	mockCtrl := gomock.NewController(t)
	defer mockCtrl.Finish()
	provisioner := NewTestCloudProvisioner(mockCtrl)

	mockExistingDisk(provisioner)
	mockExistingSnapshot(provisioner)
	mockMissingSnapshot(provisioner)
	mockInvalidSnapshots(provisioner)

	tests := []struct {
		description    string
		snapshotURI    string
		sourceVolumeID string
		expectedResult *azdiskv1beta2.Snapshot
		expectedError  error
	}{
		{
			description:    "[Success] Returns expected snapshot for existing snapshot",
			snapshotURI:    testSnapshotURI,
			sourceVolumeID: testDiskURI0,
			expectedResult: testAzSnapshot,
			expectedError:  nil,
		},
		{
			description:    "[Failure] Returns error for missing snapshot",
			snapshotURI:    missingSnapshotURI,
			expectedResult: nil,
			expectedError:  status.Error(codes.Internal, fmt.Sprintf("get snapshot %s from rg(%s) error: %v", missingSnapshotName, testResourceGroup, notFoundError.Error())),
		},
		{
			description:    "[Failure] Returns error for snapshot with missing properties",
			snapshotURI:    invalidSnapshotWithMissingPropertiesURI,
			expectedResult: nil,
			expectedError:  fmt.Errorf("snapshot property is nil"),
		},
		{
			description:    "[Failure] Returns error for snapshot with empty properties",
			snapshotURI:    invalidSnapshotWithEmptyPropertiesURI,
			expectedResult: nil,
			expectedError:  fmt.Errorf("timeCreated of snapshot property is nil"),
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(test.description, func(t *testing.T) {
			snapshot, err := provisioner.getSnapshotByID(context.TODO(), testResourceGroup, tt.snapshotURI, tt.sourceVolumeID)
			assert.Equal(t, tt.expectedError, err)
			if err == nil {
				assert.NotNil(t, snapshot)
				assert.Equal(t, tt.expectedResult, snapshot)
				assert.Equal(t, tt.expectedError, err)
			}
		})
	}

}
