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

package azure

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"

	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/stretchr/testify/assert"
	"k8s.io/utils/pointer"
)

type Client struct {
	environment         azure.Environment
	subscriptionID      string
	groupsClient        armresources.ResourceGroupsClient
	vmClient            armcompute.VirtualMachinesClient
	nicClient           armnetwork.InterfacesClient
	subnetsClient       armnetwork.SubnetsClient
	vnetClient          armnetwork.VirtualNetworksClient
	disksClient         armcompute.DisksClient
	sshPublicKeysClient armcompute.SSHPublicKeysClient
}

func GetAzureClient(cloud, subscriptionID, clientID, tenantID, clientSecret string) (*Client, error) {
	env, err := azure.EnvironmentFromName(cloud)
	if err != nil {
		return nil, err
	}

	options := azidentity.ClientSecretCredentialOptions{
		ClientOptions: azcore.ClientOptions{
			Cloud: getCloudConfig(env),
		},
	}
	cred, err := azidentity.NewClientSecretCredential(tenantID, clientID, clientSecret, &options)
	if err != nil {
		return nil, err
	}

	return getClient(env, subscriptionID, tenantID, cred, env.TokenAudience), nil
}

func (az *Client) GetAzureDisksClient() (armcompute.DisksClient, error) {

	return az.disksClient, nil
}

func (az *Client) EnsureSSHPublicKey(ctx context.Context, subscriptionID, resourceGroupName, location, keyName string) (publicKey string, err error) {
	_, err = az.sshPublicKeysClient.Create(ctx, resourceGroupName, keyName, armcompute.SSHPublicKeyResource{Location: &location}, nil)
	if err != nil {
		return "", err
	}
	result, err := az.sshPublicKeysClient.GenerateKeyPair(ctx, resourceGroupName, keyName, nil)
	if err != nil {
		return "", err
	}
	return *result.PublicKey, nil
}

func (az *Client) EnsureResourceGroup(ctx context.Context, name, location string, managedBy *string) (resourceGroup *armresources.ResourceGroup, err error) {
	var tags map[string]*string
	group, err := az.groupsClient.Get(ctx, name, nil)
	if err == nil && group.Tags != nil {
		tags = group.Tags
	} else {
		tags = make(map[string]*string)
	}
	if managedBy == nil {
		managedBy = group.ManagedBy
	}

	// Tags for correlating resource groups with prow jobs on testgrid
	tags["buildID"] = stringPointer(os.Getenv("BUILD_ID"))
	tags["jobName"] = stringPointer(os.Getenv("JOB_NAME"))
	tags["creationTimestamp"] = stringPointer(time.Now().UTC().Format(time.RFC3339))

	response, err := az.groupsClient.CreateOrUpdate(ctx, name, armresources.ResourceGroup{
		Name:      &name,
		Location:  &location,
		ManagedBy: managedBy,
		Tags:      tags,
	}, nil)
	if err != nil {
		return &response.ResourceGroup, err
	}

	return &response.ResourceGroup, nil
}

func (az *Client) DeleteResourceGroup(ctx context.Context, groupName string) error {
	_, err := az.groupsClient.Get(ctx, groupName, nil)
	if err == nil {
		poller, err := az.groupsClient.BeginDelete(ctx, groupName, nil)
		if err != nil {
			return fmt.Errorf("cannot delete resource group %v: %v", groupName, err)
		}
		_, err = poller.PollUntilDone(ctx, nil)
		if err != nil {
			// Skip the teardown errors because of https://github.com/Azure/go-autorest/issues/357
			// TODO(feiskyer): fix the issue by upgrading go-autorest version >= v11.3.2.
			log.Printf("Warning: failed to delete resource group %q with error %v", groupName, err)
		}
	}
	return nil
}

func (az *Client) EnsureVirtualMachine(ctx context.Context, groupName, location, vmName string) (vm armcompute.VirtualMachine, err error) {
	nic, err := az.EnsureNIC(ctx, groupName, location, vmName+"-nic", vmName+"-vnet", vmName+"-subnet")
	if err != nil {
		return vm, err
	}

	publicKey, err := az.EnsureSSHPublicKey(ctx, az.subscriptionID, groupName, location, fmt.Sprintf("%s-test-key", vmName))
	if err != nil {
		return vm, err
	}

	poller, err := az.vmClient.BeginCreateOrUpdate(
		ctx,
		groupName,
		vmName,
		armcompute.VirtualMachine{
			Location: pointer.String(location),
			Properties: &armcompute.VirtualMachineProperties{
				HardwareProfile: &armcompute.HardwareProfile{
					VMSize: to.Ptr(armcompute.VirtualMachineSizeTypesStandardDS2V2),
				},
				StorageProfile: &armcompute.StorageProfile{
					ImageReference: &armcompute.ImageReference{
						Publisher: pointer.String("Canonical"),
						Offer:     pointer.String("UbuntuServer"),
						SKU:       pointer.String("16.04.0-LTS"),
						Version:   pointer.String("latest"),
					},
				},
				OSProfile: &armcompute.OSProfile{
					ComputerName:  pointer.String(vmName),
					AdminUsername: pointer.String("azureuser"),
					AdminPassword: pointer.String("Azureuser1234"),
					LinuxConfiguration: &armcompute.LinuxConfiguration{
						DisablePasswordAuthentication: pointer.Bool(true),
						SSH: &armcompute.SSHConfiguration{
							PublicKeys: []*armcompute.SSHPublicKey{
								{
									Path:    pointer.String("/home/azureuser/.ssh/authorized_keys"),
									KeyData: &publicKey,
								},
							},
						},
					},
				},
				NetworkProfile: &armcompute.NetworkProfile{
					NetworkInterfaces: []*armcompute.NetworkInterfaceReference{
						{
							ID: nic.ID,
							Properties: &armcompute.NetworkInterfaceReferenceProperties{
								Primary: pointer.Bool(true),
							},
						},
					},
				},
			},
		},
	nil)
	if err != nil {
		return vm, fmt.Errorf("cannot create vm: %v", err)
	}

	resp, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return vm, fmt.Errorf("cannot get the vm create or update future response: %v", err)
	}

	return resp.VirtualMachine, nil
}

func (az *Client) EnsureNIC(ctx context.Context, groupName, location, nicName, vnetName, subnetName string) (nic armnetwork.Interface, err error) {
	_, err = az.EnsureVirtualNetworkAndSubnet(ctx, groupName, location, vnetName, subnetName)
	if err != nil {
		return nic, err
	}

	subnet, err := az.GetVirtualNetworkSubnet(ctx, groupName, vnetName, subnetName)
	if err != nil {
		return nic, fmt.Errorf("cannot get subnet %s of virtual network %s in %s: %v", subnetName, vnetName, groupName, err)
	}

	poller, err := az.nicClient.BeginCreateOrUpdate(
		ctx,
		groupName,
		nicName,
		armnetwork.Interface{
			Name:     pointer.String(nicName),
			Location: pointer.String(location),
			Properties: &armnetwork.InterfacePropertiesFormat{
				IPConfigurations: []*armnetwork.InterfaceIPConfiguration{
					{
						Name: pointer.String("ipConfig1"),
						Properties: &armnetwork.InterfaceIPConfigurationPropertiesFormat{
							Subnet:                    &subnet,
							PrivateIPAllocationMethod: to.Ptr(armnetwork.IPAllocationMethodDynamic),
						},
					},
				},
			},
		},
	nil)
	if err != nil {
		return nic, fmt.Errorf("cannot create nic: %v", err)
	}

	resp, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return nic, fmt.Errorf("cannot get nic create or update future response: %v", err)
	}

	return resp.Interface, nil
}

func (az *Client) EnsureVirtualNetworkAndSubnet(ctx context.Context, groupName, location, vnetName, subnetName string) (vnet armnetwork.VirtualNetwork, err error) {
	poller, err := az.vnetClient.BeginCreateOrUpdate(
		ctx,
		groupName,
		vnetName,
		armnetwork.VirtualNetwork{
			Location: pointer.String(location),
			Properties: &armnetwork.VirtualNetworkPropertiesFormat{
				AddressSpace: &armnetwork.AddressSpace{
					AddressPrefixes: []*string{to.Ptr("10.0.0.0/8")},
				},
				Subnets: []*armnetwork.Subnet{
					{
						Name: pointer.String(subnetName),
						Properties: &armnetwork.SubnetPropertiesFormat{
							AddressPrefix: pointer.String("10.0.0.0/16"),
						},
					},
				},
			},
		}, nil)

	if err != nil {
		return vnet, fmt.Errorf("cannot create virtual network: %v", err)
	}

	resp, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return vnet, fmt.Errorf("cannot get the vnet create or update future response: %v", err)
	}

	return resp.VirtualNetwork, nil
}

func (az *Client) GetVirtualNetworkSubnet(ctx context.Context, groupName, vnetName, subnetName string) (armnetwork.Subnet, error) {
	resp, err := az.subnetsClient.Get(ctx, groupName, vnetName, subnetName, nil)
	if err != nil {
		return armnetwork.Subnet{}, err
	}
	return resp.Subnet, nil
}

// AssertNoError asserts no error and exits with error upon seeing one
func AssertNoError(t *testing.T, err error, msgsAndArgs ...interface{}) {
	if !assert.NoError(t, err, msgsAndArgs) {
		t.Fatalf("Exiting... assertion failed: expected no error but received %v", err)
	}
}

// AssertNotNil asserts non-nil object and exits with error upon seeing one
func AssertNotNil(t *testing.T, obj interface{}, msgsAndArgs ...interface{}) {
	if !assert.NotNil(t, obj, msgsAndArgs) {
		t.Fatalf("Exiting... assertion failed: expected non-nil object but received nil.")
	}
}

func getCloudConfig(env azure.Environment) cloud.Configuration {
	switch env.Name {
	case azure.USGovernmentCloud.Name:
		return cloud.AzureGovernment
	case azure.ChinaCloud.Name:
		return cloud.AzureChina
	case azure.PublicCloud.Name:
		return cloud.AzurePublic
	default:
		return cloud.Configuration{
			ActiveDirectoryAuthorityHost: env.ActiveDirectoryEndpoint,
			Services: map[cloud.ServiceName]cloud.ServiceConfiguration{
				cloud.ResourceManager: {
					Audience: env.TokenAudience,
					Endpoint: env.ResourceManagerEndpoint,
				},
			},
		}
	}
}

func getClient(env azure.Environment, subscriptionID, tenantID string, cred *azidentity.ClientSecretCredential, scope string) *Client {	
	groupsClient, err := armresources.NewResourceGroupsClient(subscriptionID, cred, nil)
	if err != nil {
		return nil
	}
	vmClient, err := armcompute.NewVirtualMachinesClient(subscriptionID, cred, nil)
	if err != nil {
		return nil
	}
	nicClient, err := armnetwork.NewInterfacesClient(subscriptionID, cred, nil)
	if err != nil {
		return nil
	}
	subnetsClient, err := armnetwork.NewSubnetsClient(subscriptionID, cred, nil)
	if err != nil {
		return nil
	}
	vnetClient, err := armnetwork.NewVirtualNetworksClient(subscriptionID, cred, nil)
	if err != nil {
		return nil
	}
	disksClient, err := armcompute.NewDisksClient(subscriptionID, cred, nil)
	if err != nil {
		return nil
	}
	sshPublicKeysClient, err := armcompute.NewSSHPublicKeysClient(subscriptionID, cred, nil)
	if err != nil {
		return nil
	}
	c := &Client{
		environment:         env,
		subscriptionID:      subscriptionID,
		groupsClient:        *groupsClient,
		vmClient:            *vmClient,
		nicClient:           *nicClient,
		subnetsClient:       *subnetsClient,
		vnetClient:          *vnetClient,
		disksClient:         *disksClient,
		sshPublicKeysClient: *sshPublicKeysClient,
	}

	if !strings.HasSuffix(scope, "/.default") {
		scope += "/.default"
	}

	return c
}

func stringPointer(s string) *string {
	return &s
}
