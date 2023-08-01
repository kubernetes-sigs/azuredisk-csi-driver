package azureutils

import (
	"fmt"
	"strings"

	"github.com/Azure/go-autorest/autorest/azure"

	"k8s.io/klog/v2"
)

var (
	// ErrorNoAuth indicates that no credentials are provided.
	ErrorNoAuth = fmt.Errorf("no credentials provided for Azure cloud provider")
)

const (
	// ADFSIdentitySystem is the override value for tenantID on Azure Stack clouds.
	ADFSIdentitySystem = "adfs"
)

// AzureAuthConfig holds auth related part of cloud config
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
	// The AAD federated token file
	AADFederatedTokenFile string `json:"aadFederatedTokenFile,omitempty" yaml:"aadFederatedTokenFile,omitempty"`
	// Use workload identity federation for the virtual machine to access Azure ARM APIs
	UseFederatedWorkloadIdentityExtension bool `json:"useFederatedWorkloadIdentityExtension,omitempty" yaml:"useFederatedWorkloadIdentityExtension,omitempty"`
}

// ParseAzureEnvironment returns the azure environment.
// If 'resourceManagerEndpoint' is set, the environment is computed by querying the cloud's resource manager endpoint.
// Otherwise, a pre-defined Environment is looked up by name.
func ParseAzureEnvironment(cloudName, resourceManagerEndpoint, identitySystem string) (*azure.Environment, error) {
	var env azure.Environment
	var err error
	if resourceManagerEndpoint != "" {
		klog.V(4).Infof("Loading environment from resource manager endpoint: %s", resourceManagerEndpoint)
		nameOverride := azure.OverrideProperty{Key: azure.EnvironmentName, Value: cloudName}
		env, err = azure.EnvironmentFromURL(resourceManagerEndpoint, nameOverride)
		if err == nil {
			azureStackOverrides(&env, resourceManagerEndpoint, identitySystem)
		}
	} else if cloudName == "" {
		klog.V(4).Info("Using public cloud environment")
		env = azure.PublicCloud
	} else {
		klog.V(4).Infof("Using %s environment", cloudName)
		env, err = azure.EnvironmentFromName(cloudName)
	}
	return &env, err
}


// azureStackOverrides ensures that the Environment matches what AKSe currently generates for Azure Stack
func azureStackOverrides(env *azure.Environment, resourceManagerEndpoint, identitySystem string) {
	env.ManagementPortalURL = strings.Replace(resourceManagerEndpoint, "https://management.", "https://portal.", -1)
	env.ServiceManagementEndpoint = env.TokenAudience
	env.ResourceManagerVMDNSSuffix = strings.Replace(resourceManagerEndpoint, "https://management.", "cloudapp.", -1)
	env.ResourceManagerVMDNSSuffix = strings.TrimSuffix(env.ResourceManagerVMDNSSuffix, "/")
	if strings.EqualFold(identitySystem, ADFSIdentitySystem) {
		env.ActiveDirectoryEndpoint = strings.TrimSuffix(env.ActiveDirectoryEndpoint, "/")
		env.ActiveDirectoryEndpoint = strings.TrimSuffix(env.ActiveDirectoryEndpoint, "adfs")
	}
}
