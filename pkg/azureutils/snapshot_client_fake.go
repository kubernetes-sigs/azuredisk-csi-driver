package azureutils

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	azfake "github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	armcomputefake "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5/fake"
)

func (az *Cloud) CreateSnapshotsClientWithFunction(subscriptionID string, fget func(ctx context.Context, resourceGroupName string, snapshotName string, options *armcompute.SnapshotsClientGetOptions) (resp azfake.Responder[armcompute.SnapshotsClientGetResponse], errResp azfake.ErrorResponder),
		fcreate func(ctx context.Context, resourceGroupName string, snapshotName string, snapshot armcompute.Snapshot, options *armcompute.SnapshotsClientBeginCreateOrUpdateOptions) (resp azfake.PollerResponder[armcompute.SnapshotsClientCreateOrUpdateResponse], errResp azfake.ErrorResponder),
		fdelete func(ctx context.Context, resourceGroupName string, snapshotName string, options *armcompute.SnapshotsClientBeginDeleteOptions) (resp azfake.PollerResponder[armcompute.SnapshotsClientDeleteResponse], errResp azfake.ErrorResponder),
		flist func(resourceGroupName string, options *armcompute.SnapshotsClientListByResourceGroupOptions) (resp azfake.PagerResponder[armcompute.SnapshotsClientListByResourceGroupResponse])) *armcompute.SnapshotsClient {
	myFakeSnapshotsServer := armcomputefake.SnapshotsServer{}

	myFakeSnapshotsServer.Get = fget

	myFakeSnapshotsServer.BeginCreateOrUpdate = fcreate

	myFakeSnapshotsServer.BeginDelete = fdelete

	myFakeSnapshotsServer.NewListByResourceGroupPager = flist

	client, err := armcompute.NewSnapshotsClient(subscriptionID, azfake.NewTokenCredential(), &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: armcomputefake.NewSnapshotsServerTransport(&myFakeSnapshotsServer),
		},
	})
	if (err != nil) {
		return nil
	}

	return client
}