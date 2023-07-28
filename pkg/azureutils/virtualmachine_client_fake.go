package azureutils

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	azfake "github.com/Azure/azure-sdk-for-go/sdk/azcore/fake"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	armcomputefake "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5/fake"
)

func (az *Cloud) CreateVMSSVMClientWithFunction(subscriptionID string, fget func(ctx context.Context, resourceGroupName string, vmScaleSetName string, instanceID string, options *armcompute.VirtualMachineScaleSetVMsClientGetOptions) (resp azfake.Responder[armcompute.VirtualMachineScaleSetVMsClientGetResponse], errResp azfake.ErrorResponder),
		fupdate func(ctx context.Context, resourceGroupName string, vmScaleSetName string, instanceID string, parameters armcompute.VirtualMachineScaleSetVM, options *armcompute.VirtualMachineScaleSetVMsClientBeginUpdateOptions) (resp azfake.PollerResponder[armcompute.VirtualMachineScaleSetVMsClientUpdateResponse], errResp azfake.ErrorResponder)) *armcompute.VirtualMachineScaleSetVMsClient {
	myFakeVirtualMachinesServer := armcomputefake.VirtualMachineScaleSetVMsServer{}

	myFakeVirtualMachinesServer.Get = fget

	myFakeVirtualMachinesServer.BeginUpdate = fupdate

	client, err := armcompute.NewVirtualMachineScaleSetVMsClient(subscriptionID, azfake.NewTokenCredential(), &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: armcomputefake.NewVirtualMachineScaleSetVMsServerTransport(&myFakeVirtualMachinesServer),
		},
	})
	if (err != nil) {
		return nil
	}

	az.VMSSVMClient = client

	return client
}

func (az *Cloud) CreateVirtualMachineClientWithFunction(subscriptionID string, fget func(ctx context.Context, rg, vmName string, option *armcompute.VirtualMachinesClientGetOptions) (resp azfake.Responder[armcompute.VirtualMachinesClientGetResponse], errResp azfake.ErrorResponder)) *armcompute.VirtualMachinesClient {
	myFakeVirtualMachinesServer := armcomputefake.VirtualMachinesServer{}

	myFakeVirtualMachinesServer.Get = fget

	client, err := armcompute.NewVirtualMachinesClient(subscriptionID, azfake.NewTokenCredential(), &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: armcomputefake.NewVirtualMachinesServerTransport(&myFakeVirtualMachinesServer),
		},
	})
	if (err != nil) {
		return nil
	}

	az.VMClient = client

	return client
}

func (az *Cloud) CreateVMSSClientWithFunction(subscriptionID string, fget func(ctx context.Context, resourceGroupName string, vmScaleSetName string, options *armcompute.VirtualMachineScaleSetsClientGetOptions) (resp azfake.Responder[armcompute.VirtualMachineScaleSetsClientGetResponse], errResp azfake.ErrorResponder)) *armcompute.VirtualMachineScaleSetsClient {
	myFakeScaleSetServer := armcomputefake.VirtualMachineScaleSetsServer{}

	myFakeScaleSetServer.Get = fget
	
	client, err := armcompute.NewVirtualMachineScaleSetsClient(subscriptionID, azfake.NewTokenCredential(), &arm.ClientOptions{
		ClientOptions: azcore.ClientOptions{
			Transport: armcomputefake.NewVirtualMachineScaleSetsServerTransport(&myFakeScaleSetServer),
		},
	})
	if err != nil {
		return nil
	}

	az.VMSSClient = client

	return client
}