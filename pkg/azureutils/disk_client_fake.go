package azureutils

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	azfake "github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	armcomputefake "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5/fake"
	"k8s.io/klog/v2"
)

func (az *Cloud) CreateDisksClientWithFunction(subscriptionID string, fget func(ctx context.Context, resourceGroupName string, diskName string, options *armcompute.DisksClientGetOptions) (resp azfake.Responder[armcompute.DisksClientGetResponse], errResp azfake.ErrorResponder),
		fcreate func(ctx context.Context, resourceGroupName string, diskName string, disk armcompute.Disk, options *armcompute.DisksClientBeginCreateOrUpdateOptions) (resp azfake.PollerResponder[armcompute.DisksClientCreateOrUpdateResponse], errResp azfake.ErrorResponder),
		fdelete func(ctx context.Context, resourceGroupName string, diskName string, options *armcompute.DisksClientBeginDeleteOptions) (resp azfake.PollerResponder[armcompute.DisksClientDeleteResponse], errResp azfake.ErrorResponder),
		flist func(resourceGroupName string, options *armcompute.DisksClientListByResourceGroupOptions) (resp azfake.PagerResponder[armcompute.DisksClientListByResourceGroupResponse]), 
		fupdate func(ctx context.Context, resourceGroupName string, diskName string, disk armcompute.DiskUpdate, options *armcompute.DisksClientBeginUpdateOptions) (resp azfake.PollerResponder[armcompute.DisksClientUpdateResponse], errResp azfake.ErrorResponder)) *armcompute.DisksClient {
	klog.Infof("creating fake disksclient")
	
	myFakeDisksServer := armcomputefake.DisksServer{}

	myFakeDisksServer.Get = fget

	myFakeDisksServer.BeginCreateOrUpdate = fcreate

	myFakeDisksServer.BeginDelete = fdelete

	myFakeDisksServer.NewListByResourceGroupPager = flist

	myFakeDisksServer.BeginUpdate = fupdate

	if subscriptionID == "" {
		subscriptionID = "subscriptionID"
	}

	client, err := armcompute.NewDisksClient(subscriptionID, azfake.NewTokenCredential(), &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: armcomputefake.NewDisksServerTransport(&myFakeDisksServer),
		},
	})
	if (err != nil) {
		klog.Infof("failed to get client")
		return nil
	}

	az.DisksClient = client

	return client
}
