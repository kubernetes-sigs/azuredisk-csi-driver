package azureutils

import {
	"github.com/Azure/go-autorest/autorest/azure"
}

type cloudConfigType string

const (
	cloudConfigTypeFile   cloudConfigType = "file"
	cloudConfigTypeSecret cloudConfigType = "secret"
	cloudConfigTypeMerge  cloudConfigType = "merge"
)

type Cloud struct {
	Config
	InitSecretConfig
	Environment azure.Environment
}

type InitSecretConfig struct {
	SecretName      string `json:"secretName,omitempty" yaml:"secretName,omitempty"`
	SecretNamespace string `json:"secretNamespace,omitempty" yaml:"secretNamespace,omitempty"`
	CloudConfigKey  string `json:"cloudConfigKey,omitempty" yaml:"cloudConfigKey,omitempty"`
}

type Config struct {
	AzureAuthConfig
	CloudProviderRateLimitConfig

	// The cloud configure type for Azure cloud provider. Supported values are file, secret and merge.
	CloudConfigType cloudConfigType `json:"cloudConfigType,omitempty" yaml:"cloudConfigType,omitempty"`

	// The name of the resource group that the cluster is deployed in
	ResourceGroup string `json:"resourceGroup,omitempty" yaml:"resourceGroup,omitempty"`
	// The location of the resource group that the cluster is deployed in
	Location string `json:"location,omitempty" yaml:"location,omitempty"`
	// The name of site where the cluster will be deployed to that is more granular than the region specified by the "location" field.
	// Currently only public ip, load balancer and managed disks support this.
	ExtendedLocationName string `json:"extendedLocationName,omitempty" yaml:"extendedLocationName,omitempty"`
	// The type of site that is being targeted.
	// Currently only public ip, load balancer and managed disks support this.
	ExtendedLocationType string `json:"extendedLocationType,omitempty" yaml:"extendedLocationType,omitempty"`
	// The name of the VNet that the cluster is deployed in
	VnetName string `json:"vnetName,omitempty" yaml:"vnetName,omitempty"`
	// The name of the resource group that the Vnet is deployed in
	VnetResourceGroup string `json:"vnetResourceGroup,omitempty" yaml:"vnetResourceGroup,omitempty"`
	// The name of the subnet that the cluster is deployed in
	SubnetName string `json:"subnetName,omitempty" yaml:"subnetName,omitempty"`
	// The name of the security group attached to the cluster's subnet
	SecurityGroupName string `json:"securityGroupName,omitempty" yaml:"securityGroupName,omitempty"`
	// The name of the resource group that the security group is deployed in
	SecurityGroupResourceGroup string `json:"securityGroupResourceGroup,omitempty" yaml:"securityGroupResourceGroup,omitempty"`
	// (Optional in 1.6) The name of the route table attached to the subnet that the cluster is deployed in
	RouteTableName string `json:"routeTableName,omitempty" yaml:"routeTableName,omitempty"`
	// The name of the resource group that the RouteTable is deployed in
	RouteTableResourceGroup string `json:"routeTableResourceGroup,omitempty" yaml:"routeTableResourceGroup,omitempty"`
	// (Optional) The name of the availability set that should be used as the load balancer backend
	// If this is set, the Azure cloudprovider will only add nodes from that availability set to the load
	// balancer backend pool. If this is not set, and multiple agent pools (availability sets) are used, then
	// the cloudprovider will try to add all nodes to a single backend pool which is forbidden.
	// In other words, if you use multiple agent pools (availability sets), you MUST set this field.
	PrimaryAvailabilitySetName string `json:"primaryAvailabilitySetName,omitempty" yaml:"primaryAvailabilitySetName,omitempty"`
	// The type of azure nodes. Candidate values are: vmss and standard.
	// If not set, it will be default to standard.
	VMType string `json:"vmType,omitempty" yaml:"vmType,omitempty"`
	// The name of the scale set that should be used as the load balancer backend.
	// If this is set, the Azure cloudprovider will only add nodes from that scale set to the load
	// balancer backend pool. If this is not set, and multiple agent pools (scale sets) are used, then
	// the cloudprovider will try to add all nodes to a single backend pool which is forbidden in the basic sku.
	// In other words, if you use multiple agent pools (scale sets), and loadBalancerSku is set to basic, you MUST set this field.
	PrimaryScaleSetName string `json:"primaryScaleSetName,omitempty" yaml:"primaryScaleSetName,omitempty"`
	// Tags determines what tags shall be applied to the shared resources managed by controller manager, which
	// includes load balancer, security group and route table. The supported format is `a=b,c=d,...`. After updated
	// this config, the old tags would be replaced by the new ones.
	// Because special characters are not supported in "tags" configuration, "tags" support would be removed in a future release,
	// please consider migrating the config to "tagsMap".
	Tags string `json:"tags,omitempty" yaml:"tags,omitempty"`
	// TagsMap is similar to Tags but holds tags with special characters such as `=` and `,`.
	TagsMap map[string]string `json:"tagsMap,omitempty" yaml:"tagsMap,omitempty"`
	// SystemTags determines the tag keys managed by cloud provider. If it is not set, no tags would be deleted if
	// the `Tags` is changed. However, the old tags would be deleted if they are neither included in `Tags` nor
	// in `SystemTags` after the update of `Tags`.
	SystemTags string `json:"systemTags,omitempty" yaml:"systemTags,omitempty"`
	// Sku of Load Balancer and Public IP. Candidate values are: basic and standard.
	// If not set, it will be default to basic.
	LoadBalancerSku string `json:"loadBalancerSku,omitempty" yaml:"loadBalancerSku,omitempty"`
	// LoadBalancerName determines the specific name of the load balancer user want to use, working with
	// LoadBalancerResourceGroup
	LoadBalancerName string `json:"loadBalancerName,omitempty" yaml:"loadBalancerName,omitempty"`
	// LoadBalancerResourceGroup determines the specific resource group of the load balancer user want to use, working
	// with LoadBalancerName
	LoadBalancerResourceGroup string `json:"loadBalancerResourceGroup,omitempty" yaml:"loadBalancerResourceGroup,omitempty"`
	// PreConfiguredBackendPoolLoadBalancerTypes determines whether the LoadBalancer BackendPool has been preconfigured.
	// Candidate values are:
	//   "": exactly with today (not pre-configured for any LBs)
	//   "internal": for internal LoadBalancer
	//   "external": for external LoadBalancer
	//   "all": for both internal and external LoadBalancer
	PreConfiguredBackendPoolLoadBalancerTypes string `json:"preConfiguredBackendPoolLoadBalancerTypes,omitempty" yaml:"preConfiguredBackendPoolLoadBalancerTypes,omitempty"`

	// DisableAvailabilitySetNodes disables VMAS nodes support when "VMType" is set to "vmss".
	DisableAvailabilitySetNodes bool `json:"disableAvailabilitySetNodes,omitempty" yaml:"disableAvailabilitySetNodes,omitempty"`
	// EnableVmssFlexNodes enables vmss flex nodes support when "VMType" is set to "vmss".
	EnableVmssFlexNodes bool `json:"enableVmssFlexNodes,omitempty" yaml:"enableVmssFlexNodes,omitempty"`
	// DisableAzureStackCloud disables AzureStackCloud support. It should be used
	// when setting AzureAuthConfig.Cloud with "AZURESTACKCLOUD" to customize ARM endpoints
	// while the cluster is not running on AzureStack.
	DisableAzureStackCloud bool `json:"disableAzureStackCloud,omitempty" yaml:"disableAzureStackCloud,omitempty"`
	// Enable exponential backoff to manage resource request retries
	CloudProviderBackoff bool `json:"cloudProviderBackoff,omitempty" yaml:"cloudProviderBackoff,omitempty"`
	// Use instance metadata service where possible
	UseInstanceMetadata bool `json:"useInstanceMetadata,omitempty" yaml:"useInstanceMetadata,omitempty"`

	// Backoff exponent
	CloudProviderBackoffExponent float64 `json:"cloudProviderBackoffExponent,omitempty" yaml:"cloudProviderBackoffExponent,omitempty"`
	// Backoff jitter
	CloudProviderBackoffJitter float64 `json:"cloudProviderBackoffJitter,omitempty" yaml:"cloudProviderBackoffJitter,omitempty"`

	// ExcludeMasterFromStandardLB excludes master nodes from standard load balancer.
	// If not set, it will be default to true.
	ExcludeMasterFromStandardLB *bool `json:"excludeMasterFromStandardLB,omitempty" yaml:"excludeMasterFromStandardLB,omitempty"`
	// DisableOutboundSNAT disables the outbound SNAT for public load balancer rules.
	// It should only be set when loadBalancerSku is standard. If not set, it will be default to false.
	DisableOutboundSNAT *bool `json:"disableOutboundSNAT,omitempty" yaml:"disableOutboundSNAT,omitempty"`

	// Maximum allowed LoadBalancer Rule Count is the limit enforced by Azure Load balancer
	MaximumLoadBalancerRuleCount int `json:"maximumLoadBalancerRuleCount,omitempty" yaml:"maximumLoadBalancerRuleCount,omitempty"`
	// Backoff retry limit
	CloudProviderBackoffRetries int `json:"cloudProviderBackoffRetries,omitempty" yaml:"cloudProviderBackoffRetries,omitempty"`
	// Backoff duration
	CloudProviderBackoffDuration int `json:"cloudProviderBackoffDuration,omitempty" yaml:"cloudProviderBackoffDuration,omitempty"`
	// NonVmssUniformNodesCacheTTLInSeconds sets the Cache TTL for NonVmssUniformNodesCacheTTLInSeconds
	// if not set, will use default value
	NonVmssUniformNodesCacheTTLInSeconds int `json:"nonVmssUniformNodesCacheTTLInSeconds,omitempty" yaml:"nonVmssUniformNodesCacheTTLInSeconds,omitempty"`
	// AvailabilitySetNodesCacheTTLInSeconds sets the Cache TTL for availabilitySetNodesCache
	// if not set, will use default value
	AvailabilitySetNodesCacheTTLInSeconds int `json:"availabilitySetNodesCacheTTLInSeconds,omitempty" yaml:"availabilitySetNodesCacheTTLInSeconds,omitempty"`
	// VmssCacheTTLInSeconds sets the cache TTL for VMSS
	VmssCacheTTLInSeconds int `json:"vmssCacheTTLInSeconds,omitempty" yaml:"vmssCacheTTLInSeconds,omitempty"`
	// VmssVirtualMachinesCacheTTLInSeconds sets the cache TTL for vmssVirtualMachines
	VmssVirtualMachinesCacheTTLInSeconds int `json:"vmssVirtualMachinesCacheTTLInSeconds,omitempty" yaml:"vmssVirtualMachinesCacheTTLInSeconds,omitempty"`

	// VmssFlexCacheTTLInSeconds sets the cache TTL for VMSS Flex
	VmssFlexCacheTTLInSeconds int `json:"vmssFlexCacheTTLInSeconds,omitempty" yaml:"vmssFlexCacheTTLInSeconds,omitempty"`
	// VmssFlexVMCacheTTLInSeconds sets the cache TTL for vmss flex vms
	VmssFlexVMCacheTTLInSeconds int `json:"vmssFlexVMCacheTTLInSeconds,omitempty" yaml:"vmssFlexVMCacheTTLInSeconds,omitempty"`

	// VmCacheTTLInSeconds sets the cache TTL for vm
	VMCacheTTLInSeconds int `json:"vmCacheTTLInSeconds,omitempty" yaml:"vmCacheTTLInSeconds,omitempty"`
	// LoadBalancerCacheTTLInSeconds sets the cache TTL for load balancer
	LoadBalancerCacheTTLInSeconds int `json:"loadBalancerCacheTTLInSeconds,omitempty" yaml:"loadBalancerCacheTTLInSeconds,omitempty"`
	// NsgCacheTTLInSeconds sets the cache TTL for network security group
	NsgCacheTTLInSeconds int `json:"nsgCacheTTLInSeconds,omitempty" yaml:"nsgCacheTTLInSeconds,omitempty"`
	// RouteTableCacheTTLInSeconds sets the cache TTL for route table
	RouteTableCacheTTLInSeconds int `json:"routeTableCacheTTLInSeconds,omitempty" yaml:"routeTableCacheTTLInSeconds,omitempty"`
	// PlsCacheTTLInSeconds sets the cache TTL for private link service resource
	PlsCacheTTLInSeconds int `json:"plsCacheTTLInSeconds,omitempty" yaml:"plsCacheTTLInSeconds,omitempty"`
	// AvailabilitySetsCacheTTLInSeconds sets the cache TTL for VMAS
	AvailabilitySetsCacheTTLInSeconds int `json:"availabilitySetsCacheTTLInSeconds,omitempty" yaml:"availabilitySetsCacheTTLInSeconds,omitempty"`
	// PublicIPCacheTTLInSeconds sets the cache TTL for public ip
	PublicIPCacheTTLInSeconds int `json:"publicIPCacheTTLInSeconds,omitempty" yaml:"publicIPCacheTTLInSeconds,omitempty"`
	// RouteUpdateWaitingInSeconds is the delay time for waiting route updates to take effect. This waiting delay is added
	// because the routes are not taken effect when the async route updating operation returns success. Default is 30 seconds.
	RouteUpdateWaitingInSeconds int `json:"routeUpdateWaitingInSeconds,omitempty" yaml:"routeUpdateWaitingInSeconds,omitempty"`
	// The user agent for Azure customer usage attribution
	UserAgent string `json:"userAgent,omitempty" yaml:"userAgent,omitempty"`
	// LoadBalancerBackendPoolConfigurationType defines how vms join the load balancer backend pools. Supported values
	// are `nodeIPConfiguration`, `nodeIP` and `podIP`.
	// `nodeIPConfiguration`: vm network interfaces will be attached to the inbound backend pool of the load balancer (default);
	// `nodeIP`: vm private IPs will be attached to the inbound backend pool of the load balancer;
	// `podIP`: pod IPs will be attached to the inbound backend pool of the load balancer (not supported yet).
	LoadBalancerBackendPoolConfigurationType string `json:"loadBalancerBackendPoolConfigurationType,omitempty" yaml:"loadBalancerBackendPoolConfigurationType,omitempty"`
	// PutVMSSVMBatchSize defines how many requests the client send concurrently when putting the VMSS VMs.
	// If it is smaller than or equal to zero, the request will be sent one by one in sequence (default).
	PutVMSSVMBatchSize int `json:"putVMSSVMBatchSize" yaml:"putVMSSVMBatchSize"`
	// PrivateLinkServiceResourceGroup determines the specific resource group of the private link services user want to use
	PrivateLinkServiceResourceGroup string `json:"privateLinkServiceResourceGroup,omitempty" yaml:"privateLinkServiceResourceGroup,omitempty"`

	// EnableMigrateToIPBasedBackendPoolAPI uses the migration API to migrate from NIC-based to IP-based backend pool.
	// The migration API can provide a migration from NIC-based to IP-based backend pool without service downtime.
	// If the API is not used, the migration will be done by decoupling all nodes on the backend pool and then re-attaching
	// node IPs, which will introduce service downtime. The downtime increases with the number of nodes in the backend pool.
	EnableMigrateToIPBasedBackendPoolAPI bool `json:"enableMigrateToIPBasedBackendPoolAPI" yaml:"enableMigrateToIPBasedBackendPoolAPI"`
}

type AzureAuthConfig struct {
	// The cloud environment identifier. Takes values from https://github.com/Azure/go-autorest/blob/ec5f4903f77ed9927ac95b19ab8e44ada64c1356/autorest/azure/environments.go#L13
	Cloud string `json:"cloud,omitempty" yaml:"cloud,omitempty"`
	// The AAD Tenant ID for the Subscription that the cluster is deployed in
	TenantID string `json:"tenantId,omitempty" yaml:"tenantId,omitempty"`
	// The ClientID for an AAD application with RBAC access to talk to Azure RM APIs
	AADClientID string `json:"aadClientId,omitempty" yaml:"aadClientId,omitempty"`
	// The ClientSecret for an AAD application with RBAC access to talk to Azure RM APIs
	AADClientSecret string `json:"aadClientSecret,omitempty" yaml:"aadClientSecret,omitempty" datapolicy:"token"`
	// The path of a client certificate for an AAD application with RBAC access to talk to Azure RM APIs
	AADClientCertPath string `json:"aadClientCertPath,omitempty" yaml:"aadClientCertPath,omitempty"`
	// The password of the client certificate for an AAD application with RBAC access to talk to Azure RM APIs
	AADClientCertPassword string `json:"aadClientCertPassword,omitempty" yaml:"aadClientCertPassword,omitempty" datapolicy:"password"`
	// Use managed service identity for the virtual machine to access Azure ARM APIs
	UseManagedIdentityExtension bool `json:"useManagedIdentityExtension,omitempty" yaml:"useManagedIdentityExtension,omitempty"`
	// UserAssignedIdentityID contains the Client ID of the user assigned MSI which is assigned to the underlying VMs. If empty the user assigned identity is not used.
	// More details of the user assigned identity can be found at: https://docs.microsoft.com/en-us/azure/active-directory/managed-service-identity/overview
	// For the user assigned identity specified here to be used, the UseManagedIdentityExtension has to be set to true.
	UserAssignedIdentityID string `json:"userAssignedIdentityID,omitempty" yaml:"userAssignedIdentityID,omitempty"`
	// The ID of the Azure Subscription that the cluster is deployed in
	SubscriptionID string `json:"subscriptionId,omitempty" yaml:"subscriptionId,omitempty"`
	// IdentitySystem indicates the identity provider. Relevant only to hybrid clouds (Azure Stack).
	// Allowed values are 'azure_ad' (default), 'adfs'.
	IdentitySystem string `json:"identitySystem,omitempty" yaml:"identitySystem,omitempty"`
	// ResourceManagerEndpoint is the cloud's resource manager endpoint. If set, cloud provider queries this endpoint
	// in order to generate an autorest.Environment instance instead of using one of the pre-defined Environments.
	ResourceManagerEndpoint string `json:"resourceManagerEndpoint,omitempty" yaml:"resourceManagerEndpoint,omitempty"`
	// The AAD Tenant ID for the Subscription that the network resources are deployed in
	NetworkResourceTenantID string `json:"networkResourceTenantID,omitempty" yaml:"networkResourceTenantID,omitempty"`
	// The ID of the Azure Subscription that the network resources are deployed in
	NetworkResourceSubscriptionID string `json:"networkResourceSubscriptionID,omitempty" yaml:"networkResourceSubscriptionID,omitempty"`
	// The AAD federated token file
	AADFederatedTokenFile string `json:"aadFederatedTokenFile,omitempty" yaml:"aadFederatedTokenFile,omitempty"`
	// Use workload identity federation for the virtual machine to access Azure ARM APIs
	UseFederatedWorkloadIdentityExtension bool `json:"useFederatedWorkloadIdentityExtension,omitempty" yaml:"useFederatedWorkloadIdentityExtension,omitempty"`
}

type CloudProviderRateLimitConfig struct {
	// The default rate limit config options.
	RateLimitConfig

	// Rate limit config for each clients. Values would override default settings above.
	RouteRateLimit                  *RateLimitConfig `json:"routeRateLimit,omitempty" yaml:"routeRateLimit,omitempty"`
	SubnetsRateLimit                *RateLimitConfig `json:"subnetsRateLimit,omitempty" yaml:"subnetsRateLimit,omitempty"`
	InterfaceRateLimit              *RateLimitConfig `json:"interfaceRateLimit,omitempty" yaml:"interfaceRateLimit,omitempty"`
	RouteTableRateLimit             *RateLimitConfig `json:"routeTableRateLimit,omitempty" yaml:"routeTableRateLimit,omitempty"`
	LoadBalancerRateLimit           *RateLimitConfig `json:"loadBalancerRateLimit,omitempty" yaml:"loadBalancerRateLimit,omitempty"`
	PublicIPAddressRateLimit        *RateLimitConfig `json:"publicIPAddressRateLimit,omitempty" yaml:"publicIPAddressRateLimit,omitempty"`
	SecurityGroupRateLimit          *RateLimitConfig `json:"securityGroupRateLimit,omitempty" yaml:"securityGroupRateLimit,omitempty"`
	VirtualMachineRateLimit         *RateLimitConfig `json:"virtualMachineRateLimit,omitempty" yaml:"virtualMachineRateLimit,omitempty"`
	StorageAccountRateLimit         *RateLimitConfig `json:"storageAccountRateLimit,omitempty" yaml:"storageAccountRateLimit,omitempty"`
	DiskRateLimit                   *RateLimitConfig `json:"diskRateLimit,omitempty" yaml:"diskRateLimit,omitempty"`
	SnapshotRateLimit               *RateLimitConfig `json:"snapshotRateLimit,omitempty" yaml:"snapshotRateLimit,omitempty"`
	VirtualMachineScaleSetRateLimit *RateLimitConfig `json:"virtualMachineScaleSetRateLimit,omitempty" yaml:"virtualMachineScaleSetRateLimit,omitempty"`
	VirtualMachineSizeRateLimit     *RateLimitConfig `json:"virtualMachineSizesRateLimit,omitempty" yaml:"virtualMachineSizesRateLimit,omitempty"`
	AvailabilitySetRateLimit        *RateLimitConfig `json:"availabilitySetRateLimit,omitempty" yaml:"availabilitySetRateLimit,omitempty"`
	AttachDetachDiskRateLimit       *RateLimitConfig `json:"attachDetachDiskRateLimit,omitempty" yaml:"attachDetachDiskRateLimit,omitempty"`
	ContainerServiceRateLimit       *RateLimitConfig `json:"containerServiceRateLimit,omitempty" yaml:"containerServiceRateLimit,omitempty"`
	DeploymentRateLimit             *RateLimitConfig `json:"deploymentRateLimit,omitempty" yaml:"deploymentRateLimit,omitempty"`
	PrivateDNSRateLimit             *RateLimitConfig `json:"privateDNSRateLimit,omitempty" yaml:"privateDNSRateLimit,omitempty"`
	PrivateDNSZoneGroupRateLimit    *RateLimitConfig `json:"privateDNSZoneGroupRateLimit,omitempty" yaml:"privateDNSZoneGroupRateLimit,omitempty"`
	PrivateEndpointRateLimit        *RateLimitConfig `json:"privateEndpointRateLimit,omitempty" yaml:"privateEndpointRateLimit,omitempty"`
	PrivateLinkServiceRateLimit     *RateLimitConfig `json:"privateLinkServiceRateLimit,omitempty" yaml:"privateLinkServiceRateLimit,omitempty"`
	VirtualNetworkRateLimit         *RateLimitConfig `json:"virtualNetworkRateLimit,omitempty" yaml:"virtualNetworkRateLimit,omitempty"`
}

type RateLimitConfig struct {
	// Enable rate limiting
	CloudProviderRateLimit bool `json:"cloudProviderRateLimit,omitempty" yaml:"cloudProviderRateLimit,omitempty"`
	// Rate limit QPS (Read)
	CloudProviderRateLimitQPS float32 `json:"cloudProviderRateLimitQPS,omitempty" yaml:"cloudProviderRateLimitQPS,omitempty"`
	// Rate limit Bucket Size
	CloudProviderRateLimitBucket int `json:"cloudProviderRateLimitBucket,omitempty" yaml:"cloudProviderRateLimitBucket,omitempty"`
	// Rate limit QPS (Write)
	CloudProviderRateLimitQPSWrite float32 `json:"cloudProviderRateLimitQPSWrite,omitempty" yaml:"cloudProviderRateLimitQPSWrite,omitempty"`
	// Rate limit Bucket Size
	CloudProviderRateLimitBucketWrite int `json:"cloudProviderRateLimitBucketWrite,omitempty" yaml:"cloudProviderRateLimitBucketWrite,omitempty"`
}