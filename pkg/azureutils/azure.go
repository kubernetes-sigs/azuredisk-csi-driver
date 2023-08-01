package azureutils

import (
	"context"
	"fmt"
	"io"
	"os"

	"strconv"
	"strings"

	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"

	"github.com/edreed/go-batch"

	"github.com/Azure/go-autorest/autorest/azure"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	clientset "k8s.io/client-go/kubernetes"
	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"

	"sigs.k8s.io/yaml"
)

type DiskOperationBatchProcessor struct {
	AttachDiskProcessor			  *batch.Processor
	DetachDiskProcessor			  *batch.Processor
}

type AttachDiskResult struct {
	Lun int32
	Err error
}

type DetachDiskResult struct {
	Err error
}

var (
	// Master nodes are not added to standard load balancer by default.
	defaultExcludeMasterFromStandardLB = true
	// Outbound SNAT is enabled by default.
	defaultDisableOutboundSNAT = false
	// RouteUpdateWaitingInSeconds is 30 seconds by default.
	defaultRouteUpdateWaitingInSeconds = 30
)

// azure cloud config
const (
	// CloudProviderName is the value used for the --cloud-provider flag
	CloudProviderName = "azure"
	// AzureStackCloudName is the cloud name of Azure Stack
	AzureStackCloudName = "AZURESTACKCLOUD"
	// BackoffRetriesDefault is the default backoff retry count
	BackoffRetriesDefault = 6
	// BackoffExponentDefault is the default value of the backoff exponent
	BackoffExponentDefault = 1.5
	// BackoffDurationDefault is the default value of the backoff duration
	BackoffDurationDefault = 5 // in seconds
	// BackoffJitterDefault is the default value of the backoff jitter
	BackoffJitterDefault = 1.0
)

const (
	// VMTypeVMSS is the vmss vm type
	VMTypeVMSS = "vmss"
	// VMTypeStandard is the vmas vm type
	VMTypeStandard = "standard"
	// VMTypeVmssFlex is the vmssflex vm type
	VMTypeVmssFlex = "vmssflex"
)

const (
	ImdsServer = "http://169.254.169.254"
)

const (
	// MaximumLoadBalancerRuleCount is the maximum number of load balancer rules
	// ref: https://docs.microsoft.com/en-us/azure/azure-subscription-service-limits#load-balancer.
	MaximumLoadBalancerRuleCount = 250
	// LoadBalancerSkuStandard is the load balancer standard sku
	LoadBalancerSkuStandard = "standard"
)

const (
	// LoadBalancerBackendPoolConfigurationTypeNodeIPConfiguration is the lb backend pool config type node IP configuration
	LoadBalancerBackendPoolConfigurationTypeNodeIPConfiguration = "nodeIPConfiguration"
	// LoadBalancerBackendPoolConfigurationTypeNodeIP is the lb backend pool config type node ip
	LoadBalancerBackendPoolConfigurationTypeNodeIP = "nodeIP"
	// LoadBalancerBackendPoolConfigurationTypePODIP is the lb backend pool config type pod ip
	// TODO (nilo19): support pod IP in the future
	LoadBalancerBackendPoolConfigurationTypePODIP = "podIP"
)

const (
	// ZoneFetchingInterval defines the interval of performing zoneClient.GetZones
	ZoneFetchingInterval = 30 * time.Minute
)

// Cloud holds the config and clients
type Cloud struct {
	Config
	InitSecretConfig
	Environment azure.Environment

	DisksClient                   *armcompute.DisksClient
	KubeClient                    clientset.Interface
	VMClient                      *armcompute.VirtualMachinesClient
	SnapshotsClient               *armcompute.SnapshotsClient // placeholder
	VirtualMachineScaleSetsClient *armcompute.VirtualMachineScaleSetsClient
	VMSSVMClient				  *armcompute.VirtualMachineScaleSetVMsClient
	VMSSClient					  *armcompute.VirtualMachineScaleSetsClient
	ASClient					  *armcompute.AvailabilitySetsClient

	VMSSVMCache *VMSSVMCache

	DiskOperationBatchProcessor	  *DiskOperationBatchProcessor
}

type InitSecretConfig struct {
	SecretName      string `json:"secretName,omitempty" yaml:"secretName,omitempty"`
	SecretNamespace string `json:"secretNamespace,omitempty" yaml:"secretNamespace,omitempty"`
	CloudConfigKey  string `json:"cloudConfigKey,omitempty" yaml:"cloudConfigKey,omitempty"`
}

// Config holds the configuration parsed from the --cloud-config flag
// All fields are required unless otherwise specified
// NOTE: Cloud config files should follow the same Kubernetes deprecation policy as
// flags or CLIs. Config fields should not change behavior in incompatible ways and
// should be deprecated for at least 2 release prior to removing.
// See https://kubernetes.io/docs/reference/using-api/deprecation-policy/#deprecating-a-flag-or-cli
// for more details.
type Config struct {
	AzureAuthConfig

	// The cloud configure type for Azure cloud provider. Supported values are file, secret and merge.
	CloudConfigType cloudConfigType `json:"cloudConfigType,omitempty" yaml:"cloudConfigType,omitempty"`

	// The name of the resource group that the cluster is deployed in
	ResourceGroup string `json:"resourceGroup,omitempty" yaml:"resourceGroup,omitempty"`
	// The location of the resource group that the cluster is deployed in
	Location string `json:"location,omitempty" yaml:"location,omitempty"`

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

	// DisableAzureStackCloud disables AzureStackCloud support. It should be used
	// when setting AzureAuthConfig.Cloud with "AZURESTACKCLOUD" to customize ARM endpoints
	// while the cluster is not running on AzureStack.
	DisableAzureStackCloud bool `json:"disableAzureStackCloud,omitempty" yaml:"disableAzureStackCloud,omitempty"`

	// The user agent for Azure customer usage attribution
	UserAgent string `json:"userAgent,omitempty" yaml:"userAgent,omitempty"`

	// DisableAvailabilitySetNodes disables VMAS nodes support when "VMType" is set to "vmss".
	DisableAvailabilitySetNodes bool `json:"disableAvailabilitySetNodes,omitempty" yaml:"disableAvailabilitySetNodes,omitempty"`
}

// ParseConfig returns a parsed configuration for an Azure cloudprovider config file
func ParseConfig(configReader io.Reader) (*Config, error) {
	var config Config
	if configReader == nil {
		return nil, nil
	}

	configContents, err := io.ReadAll(configReader)
	if err != nil {
		return nil, err
	}

	err = yaml.Unmarshal(configContents, &config)
	if err != nil {
		return nil, err
	}

	// The resource group name may be in different cases from different Azure APIs, hence it is converted to lower here.
	// See more context at https://github.com/kubernetes/kubernetes/issues/71994.
	config.ResourceGroup = strings.ToLower(config.ResourceGroup)

	// these environment variables are injected by workload identity webhook
	if tenantID := os.Getenv("AZURE_TENANT_ID"); tenantID != "" {
		config.TenantID = tenantID
	}
	if clientID := os.Getenv("AZURE_CLIENT_ID"); clientID != "" {
		config.AADClientID = clientID
	}

	return &config, nil
}

func NewCloudWithoutFeatureGatesFromConfig(ctx context.Context, config *Config, fromSecret, callFromCCM bool) (*Cloud, error) {
	az := &Cloud{}

	err := az.InitializeCloudFromConfig(ctx, config, false, callFromCCM)
	if err != nil {
		return nil, err
	}

	return az, nil
}

// InitializeCloudFromConfig initializes the Cloud from config.
func (az *Cloud) InitializeCloudFromConfig(ctx context.Context, config *Config, fromSecret, callFromCCM bool) error {
	if config == nil {
		// should not reach here
		return fmt.Errorf("InitializeCloudFromConfig: cannot initialize from nil config")
	}

	if config.VMType == "" {
		// default to standard vmType if not set.
		config.VMType = VMTypeStandard
	}

	if config.CloudConfigType == "" {
		// The default cloud config type is cloudConfigTypeMerge.
		config.CloudConfigType = cloudConfigTypeMerge
	} else {
		supportedCloudConfigTypes := sets.New(
			string(cloudConfigTypeMerge),
			string(cloudConfigTypeFile),
			string(cloudConfigTypeSecret))
		if !supportedCloudConfigTypes.Has(string(config.CloudConfigType)) {
			return fmt.Errorf("cloudConfigType %v is not supported, supported values are %v", config.CloudConfigType, supportedCloudConfigTypes.UnsortedList())
		}
	}

	env, err := ParseAzureEnvironment(config.Cloud, config.ResourceManagerEndpoint, config.IdentitySystem)
	if err != nil {
		return err
	}

	az.Config = *config
	az.Environment = *env

	if err != nil {
		return err
	}

	return nil
}

func (az *Cloud) ConfigAzureClients() {
	klog.Infof("begin configuring clients")

	cred, err := azidentity.NewClientSecretCredential(az.TenantID, az.AADClientID, az.AADClientSecret, nil)
	if err != nil {
		klog.Fatalf("failed to obtain new credential: %v", err)
	}

	// Initialize all azure clients based on client config
	az.DisksClient, err = armcompute.NewDisksClient(az.SubscriptionID, cred, nil)
	if err != nil {
		klog.Fatalf("failed to create client: %v", err)
	}
	az.VMClient, err = armcompute.NewVirtualMachinesClient(az.SubscriptionID, cred, nil)
	if err != nil {
		klog.Fatalf("failed to create client: %v", err)
	}
	az.SnapshotsClient, err = armcompute.NewSnapshotsClient(az.SubscriptionID, cred, nil)
	if err != nil {
		klog.Fatalf("failed to create client: %v", err)
	}
	az.VirtualMachineScaleSetsClient, err = armcompute.NewVirtualMachineScaleSetsClient(az.SubscriptionID, cred, nil)
	if err != nil {
		klog.Fatalf("failed to create client: %v", err)
	}
	az.VMSSVMClient, err = armcompute.NewVirtualMachineScaleSetVMsClient(az.SubscriptionID, cred, nil)
	if err != nil {
		klog.Fatalf("failed to create client: %v", err)
	}
	az.VMSSClient, err = armcompute.NewVirtualMachineScaleSetsClient(az.SubscriptionID, cred, nil)
	if err != nil {
		klog.Fatalf("failed to create client: %v", err)
	}
	az.ASClient, err = armcompute.NewAvailabilitySetsClient(az.SubscriptionID, cred, nil)
	if err != nil {
		klog.Fatalf("failed to create client: %v", err)
	}
}

func (az *Cloud) GetZoneByNodeName(ctx context.Context, nodeName string) (cloudprovider.Zone, error) {
	response, err := az.VMClient.Get(ctx, az.ResourceGroup, nodeName, nil)
	if err != nil {
		return cloudprovider.Zone{}, fmt.Errorf("failed to get zone from nodename: %v", err)
	}

	var zones []*string
	if response.VirtualMachine.Zones != nil {
		zones = response.VirtualMachine.Zones
	}

	var failureDomain string
	if zones != nil && len(zones) > 0 {
		zoneID, err := strconv.Atoi(*zones[0])
		if err != nil {
			return cloudprovider.Zone{}, fmt.Errorf("failed to parse zone %v", err)
		}

		failureDomain = fmt.Sprintf("%s-%d", strings.ToLower(*response.VirtualMachine.Location), zoneID)
	} else if response.VirtualMachine.Properties.InstanceView != nil &&
		response.VirtualMachine.Properties.InstanceView.PlatformFaultDomain != nil {
		// Availability zone is not used for the node, falling back to fault domain.
		failureDomain = strconv.Itoa(int(*response.VirtualMachine.Properties.InstanceView.PlatformFaultDomain))
	} else {
		err = fmt.Errorf("failed to get zone info")
		klog.Errorf("GetZoneByNodeName: got unexpected error %v", err)
		az.DeleteVMFromCache(nodeName)
		return cloudprovider.Zone{}, err
	}

	return cloudprovider.Zone{
		FailureDomain: strings.ToLower(failureDomain),
		Region:        strings.ToLower(*response.VirtualMachine.Location),
	}, nil
}

func GetLastSegment(ID, separator string) (string, error) {
	parts := strings.Split(ID, separator)
	name := parts[len(parts)-1]
	if len(name) == 0 {
		return "", fmt.Errorf("resource name was missing from identifier")
	}

	return name, nil
}


// PLACEHOLDER

// Instances returns an instances interface. Also returns true if the interface is supported, false otherwise.
func (az *Cloud) Instances() (cloudprovider.Instances, bool) { return nil, true }

func (az *Cloud) DeleteManagedDisk(ctx context.Context, diskURI string) error { return nil }
func (az *Cloud) GetDiskLun(diskName, diskURI string, nodeName types.NodeName) (int32, *string, error) {
	return -1, nil, nil
}
func (az *Cloud) UpdateVM(ctx context.Context, nodeName types.NodeName) error { return nil }
func (az *Cloud) AttachDisk(ctx context.Context, async bool, diskName, diskURI string, nodeName types.NodeName, cachingMode armcompute.CachingTypes, disk *armcompute.Disk) (int32, error) {
	return -1, nil
}
func (az *Cloud) DetachDisk(ctx context.Context, diskName, diskURI string, nodeName types.NodeName) error {
	return nil
}
func (az *Cloud) ResizeDisk(ctx context.Context, diskURI string, oldSize resource.Quantity, requestSize resource.Quantity, enable bool) (resource.Quantity, error) {
	return resource.Quantity{}, nil
}