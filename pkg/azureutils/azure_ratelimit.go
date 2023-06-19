package azureutils

const (
	DefaultAtachDetachDiskQPS    = 6.0
	DefaultAtachDetachDiskBucket = 10
)

const (
	// RateLimitQPSDefault is the default value of the rate limit qps
	RateLimitQPSDefault = 1.0
	// RateLimitBucketDefault is the default value of rate limit bucket
	RateLimitBucketDefault = 5
)

// CloudProviderRateLimitConfig indicates the rate limit config for each clients.
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

// RateLimitConfig indicates the rate limit config options.
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

// InitializeCloudProviderRateLimitConfig initializes rate limit configs.
func InitializeCloudProviderRateLimitConfig(config *CloudProviderRateLimitConfig) {
	if config == nil {
		return
	}

	// Assign read rate limit defaults if no configuration was passed in.
	if config.CloudProviderRateLimitQPS == 0 {
		config.CloudProviderRateLimitQPS = RateLimitQPSDefault
	}
	if config.CloudProviderRateLimitBucket == 0 {
		config.CloudProviderRateLimitBucket = RateLimitBucketDefault
	}
	// Assign write rate limit defaults if no configuration was passed in.
	if config.CloudProviderRateLimitQPSWrite == 0 {
		config.CloudProviderRateLimitQPSWrite = config.CloudProviderRateLimitQPS
	}
	if config.CloudProviderRateLimitBucketWrite == 0 {
		config.CloudProviderRateLimitBucketWrite = config.CloudProviderRateLimitBucket
	}

	config.RouteRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.RouteRateLimit)
	config.SubnetsRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.SubnetsRateLimit)
	config.InterfaceRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.InterfaceRateLimit)
	config.RouteTableRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.RouteTableRateLimit)
	config.LoadBalancerRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.LoadBalancerRateLimit)
	config.PublicIPAddressRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.PublicIPAddressRateLimit)
	config.SecurityGroupRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.SecurityGroupRateLimit)
	config.VirtualMachineRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.VirtualMachineRateLimit)
	config.StorageAccountRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.StorageAccountRateLimit)
	config.DiskRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.DiskRateLimit)
	config.SnapshotRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.SnapshotRateLimit)
	config.VirtualMachineScaleSetRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.VirtualMachineScaleSetRateLimit)
	config.VirtualMachineSizeRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.VirtualMachineSizeRateLimit)
	config.AvailabilitySetRateLimit = overrideDefaultRateLimitConfig(&config.RateLimitConfig, config.AvailabilitySetRateLimit)

	atachDetachDiskRateLimitConfig := RateLimitConfig{
		CloudProviderRateLimit:            true,
		CloudProviderRateLimitQPSWrite:    DefaultAtachDetachDiskQPS,
		CloudProviderRateLimitBucketWrite: DefaultAtachDetachDiskBucket,
	}
	config.AttachDetachDiskRateLimit = overrideDefaultRateLimitConfig(&atachDetachDiskRateLimitConfig, config.AttachDetachDiskRateLimit)
}

// overrideDefaultRateLimitConfig overrides the default CloudProviderRateLimitConfig.
func overrideDefaultRateLimitConfig(defaults, config *RateLimitConfig) *RateLimitConfig {
	// If config not set, apply defaults.
	if config == nil {
		return defaults
	}

	// Remain disabled if it's set explicitly.
	if !config.CloudProviderRateLimit {
		return &RateLimitConfig{CloudProviderRateLimit: false}
	}

	// Apply default values.
	if config.CloudProviderRateLimitQPS == 0 {
		config.CloudProviderRateLimitQPS = defaults.CloudProviderRateLimitQPS
	}
	if config.CloudProviderRateLimitBucket == 0 {
		config.CloudProviderRateLimitBucket = defaults.CloudProviderRateLimitBucket
	}
	if config.CloudProviderRateLimitQPSWrite == 0 {
		config.CloudProviderRateLimitQPSWrite = defaults.CloudProviderRateLimitQPSWrite
	}
	if config.CloudProviderRateLimitBucketWrite == 0 {
		config.CloudProviderRateLimitBucketWrite = defaults.CloudProviderRateLimitBucketWrite
	}

	return config
}
