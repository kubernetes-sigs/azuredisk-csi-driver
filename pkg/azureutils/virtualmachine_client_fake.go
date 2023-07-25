package azureutils

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	azfake "github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	armcomputefake "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5/fake"
)

func (az *Cloud) CreateVMClientWithFunction(subscriptionID string, fget func(ctx context.Context, resourceGroupName string, vmName string, options *armcompute.VirtualMachinesClientGetOptions) (resp azfake.Responder[armcompute.VirtualMachinesClientGetResponse], errResp azfake.ErrorResponder),
		fupdate func(ctx context.Context, resourceGroupName string, vmName string, parameters armcompute.VirtualMachineUpdate, options *armcompute.VirtualMachinesClientBeginUpdateOptions) (resp azfake.PollerResponder[armcompute.VirtualMachinesClientUpdateResponse], errResp azfake.ErrorResponder)) *armcompute.VirtualMachinesClient {
	myFakeVirtualMachinesServer := armcomputefake.VirtualMachinesServer{}

	myFakeVirtualMachinesServer.Get = fget

	myFakeVirtualMachinesServer.BeginUpdate = fupdate

	client, err := armcompute.NewVirtualMachinesClient(subscriptionID, azfake.NewTokenCredential(), &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: armcomputefake.NewVirtualMachinesServerTransport(&myFakeVirtualMachinesServer),
		},
	})
	if (err != nil) {
		return nil
	}

	return client
}
