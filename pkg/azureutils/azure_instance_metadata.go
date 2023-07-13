package azureutils

import (
	// "encoding/json"
	"fmt"
	// "io"
	// "net/http"
	// "strings"
	// "time"
	// "k8s.io/klog/v2"
)

// // metadata service
// const (
// 	// ImdsInstanceAPIVersion is the imds instance api version
// 	ImdsInstanceAPIVersion = "2021-10-01"
// 	// ImdsInstanceURI is the imds instance uri
// 	ImdsInstanceURI = "/metadata/instance"
// 	// ImdsLoadBalancerURI is the imds load balancer uri
// 	ImdsLoadBalancerURI = "/metadata/loadbalancer"
// 	// ImdsLoadBalancerAPIVersion is the imds load balancer api version
// 	ImdsLoadBalancerAPIVersion = "2020-10-01"
// )

const (
	// // MetadataCacheTTL is the TTL of the metadata service
	// MetadataCacheTTL = time.Minute
	// MetadataCacheKey is the metadata cache key
	MetadataCacheKey = "InstanceMetadata"
)

// NetworkData contains IP information for a network.
type NetworkData struct {
	IPAddress []IPAddress `json:"ipAddress"`
	Subnet    []Subnet    `json:"subnet"`
}

// NetworkInterface represents an instances network interface.
type NetworkInterface struct {
	IPV4 NetworkData `json:"ipv4"`
	IPV6 NetworkData `json:"ipv6"`
	MAC  string      `json:"macAddress"`
}

// IPAddress represents IP address information.
type IPAddress struct {
	PrivateIP string `json:"privateIpAddress"`
	PublicIP  string `json:"publicIpAddress"`
}

// Subnet represents subnet information.
type Subnet struct {
	Address string `json:"address"`
	Prefix  string `json:"prefix"`
}

// // PublicIPMetadata represents the public IP metadata.
// type PublicIPMetadata struct {
// 	FrontendIPAddress string `json:"frontendIpAddress,omitempty"`
// 	PrivateIPAddress  string `json:"privateIpAddress,omitempty"`
// }

// // LoadbalancerProfile represents load balancer profile in IMDS.
// type LoadbalancerProfile struct {
// 	PublicIPAddresses []PublicIPMetadata `json:"publicIpAddresses,omitempty"`
// }

// // LoadBalancerMetadata represents load balancer metadata.
// type LoadBalancerMetadata struct {
// 	LoadBalancer *LoadbalancerProfile `json:"loadbalancer,omitempty"`
// }

// ComputeMetadata represents compute information
type ComputeMetadata struct {
	Environment            string `json:"azEnvironment,omitempty"`
	SKU                    string `json:"sku,omitempty"`
	Name                   string `json:"name,omitempty"`
	Zone                   string `json:"zone,omitempty"`
	VMSize                 string `json:"vmSize,omitempty"`
	OSType                 string `json:"osType,omitempty"`
	Location               string `json:"location,omitempty"`
	FaultDomain            string `json:"platformFaultDomain,omitempty"`
	PlatformSubFaultDomain string `json:"platformSubFaultDomain,omitempty"`
	UpdateDomain           string `json:"platformUpdateDomain,omitempty"`
	ResourceGroup          string `json:"resourceGroupName,omitempty"`
	VMScaleSetName         string `json:"vmScaleSetName,omitempty"`
	SubscriptionID         string `json:"subscriptionId,omitempty"`
	ResourceID             string `json:"resourceId,omitempty"`
}

// NetworkMetadata contains metadata about an instance's network
type NetworkMetadata struct {
	Interface []NetworkInterface `json:"interface"`
}

// InstanceMetadata represents instance information.
type InstanceMetadata struct {
	Compute *ComputeMetadata `json:"compute,omitempty"`
	Network *NetworkMetadata `json:"network,omitempty"`
}

// InstanceMetadataService knows how to query the Azure instance metadata server.
type InstanceMetadataService struct {
	imdsServer string
	imsCache   *TimedCache
}

// GetMetadata gets instance metadata from cache.
// crt determines if we can get data from stalled cache/need fresh if cache expired.
func (ims *InstanceMetadataService) GetMetadata(crt AzureCacheReadType) (*InstanceMetadata, error) {
	cache, err := ims.imsCache.Get(MetadataCacheKey, crt)
	if err != nil {
		return nil, err
	}

	// Cache shouldn't be nil, but added a check in case something is wrong.
	if cache == nil {
		return nil, fmt.Errorf("failure of getting instance metadata")
	}

	if metadata, ok := cache.(*InstanceMetadata); ok {
		return metadata, nil
	}

	return nil, fmt.Errorf("failure of getting instance metadata")
}

// // NewInstanceMetadataService creates an instance of the InstanceMetadataService accessor object.
// func NewInstanceMetadataService(imdsServer string) (*InstanceMetadataService, error) {
// 	ims := &InstanceMetadataService{
// 		imdsServer: imdsServer,
// 	}

// 	imsCache, err := NewTimedcache(MetadataCacheTTL, ims.getMetadata)
// 	if err != nil {
// 		return nil, err
// 	}

// 	ims.imsCache = imsCache
// 	return ims, nil
// }

// // fillNetInterfacePublicIPs finds PIPs from imds load balancer and fills them into net interface config.
// func fillNetInterfacePublicIPs(publicIPs []PublicIPMetadata, netInterface *NetworkInterface) {
// 	// IPv6 IPs from imds load balancer are wrapped by brackets while those from imds are not.
// 	trimIP := func(ip string) string {
// 		return strings.Trim(strings.Trim(ip, "["), "]")
// 	}

// 	if len(netInterface.IPV4.IPAddress) > 0 && len(netInterface.IPV4.IPAddress[0].PrivateIP) > 0 {
// 		for _, pip := range publicIPs {
// 			if pip.PrivateIPAddress == netInterface.IPV4.IPAddress[0].PrivateIP {
// 				netInterface.IPV4.IPAddress[0].PublicIP = pip.FrontendIPAddress
// 				break
// 			}
// 		}
// 	}
// 	if len(netInterface.IPV6.IPAddress) > 0 && len(netInterface.IPV6.IPAddress[0].PrivateIP) > 0 {
// 		for _, pip := range publicIPs {
// 			privateIP := trimIP(pip.PrivateIPAddress)
// 			frontendIP := trimIP(pip.FrontendIPAddress)
// 			if privateIP == netInterface.IPV6.IPAddress[0].PrivateIP {
// 				netInterface.IPV6.IPAddress[0].PublicIP = frontendIP
// 				break
// 			}
// 		}
// 	}
// }

// func (ims *InstanceMetadataService) getMetadata(key string) (interface{}, error) {
// 	instanceMetadata, err := ims.getInstanceMetadata(key)
// 	if err != nil {
// 		return nil, err
// 	}

// 	if instanceMetadata.Network != nil && len(instanceMetadata.Network.Interface) > 0 {
// 		netInterface := instanceMetadata.Network.Interface[0]
// 		if (len(netInterface.IPV4.IPAddress) > 0 && len(netInterface.IPV4.IPAddress[0].PublicIP) > 0) ||
// 			(len(netInterface.IPV6.IPAddress) > 0 && len(netInterface.IPV6.IPAddress[0].PublicIP) > 0) {
// 			// Return if public IP address has already part of instance metadata.
// 			return instanceMetadata, nil
// 		}

// 		loadBalancerMetadata, err := ims.getLoadBalancerMetadata()
// 		if err != nil || loadBalancerMetadata == nil || loadBalancerMetadata.LoadBalancer == nil {
// 			// Log a warning since loadbalancer metadata may not be available when the VM
// 			// is not in standard LoadBalancer backend address pool.
// 			klog.V(4).Infof("Warning: failed to get loadbalancer metadata: %v", err)
// 			return instanceMetadata, nil
// 		}

// 		publicIPs := loadBalancerMetadata.LoadBalancer.PublicIPAddresses
// 		fillNetInterfacePublicIPs(publicIPs, &netInterface)
// 	}

// 	return instanceMetadata, nil
// }

// func (ims *InstanceMetadataService) getInstanceMetadata(key string) (*InstanceMetadata, error) {
// 	req, err := http.NewRequest("GET", ims.imdsServer+ImdsInstanceURI, nil)
// 	if err != nil {
// 		return nil, err
// 	}
// 	req.Header.Add("Metadata", "True")
// 	req.Header.Add("User-Agent", "golang/kubernetes-cloud-provider")

// 	q := req.URL.Query()
// 	q.Add("format", "json")
// 	q.Add("api-version", ImdsInstanceAPIVersion)
// 	req.URL.RawQuery = q.Encode()

// 	client := &http.Client{}
// 	resp, err := client.Do(req)
// 	if err != nil {
// 		return nil, err
// 	}
// 	defer resp.Body.Close()

// 	if resp.StatusCode != http.StatusOK {
// 		return nil, fmt.Errorf("failure of getting instance metadata with response %q", resp.Status)
// 	}

// 	data, err := io.ReadAll(resp.Body)
// 	if err != nil {
// 		return nil, err
// 	}

// 	obj := InstanceMetadata{}
// 	err = json.Unmarshal(data, &obj)
// 	if err != nil {
// 		return nil, err
// 	}

// 	return &obj, nil
// }

// func (ims *InstanceMetadataService) getLoadBalancerMetadata() (*LoadBalancerMetadata, error) {
// 	req, err := http.NewRequest("GET", ims.imdsServer+ImdsLoadBalancerURI, nil)
// 	if err != nil {
// 		return nil, err
// 	}
// 	req.Header.Add("Metadata", "True")
// 	req.Header.Add("User-Agent", "golang/kubernetes-cloud-provider")

// 	q := req.URL.Query()
// 	q.Add("format", "json")
// 	q.Add("api-version", ImdsLoadBalancerAPIVersion)
// 	req.URL.RawQuery = q.Encode()

// 	client := &http.Client{}
// 	resp, err := client.Do(req)
// 	if err != nil {
// 		return nil, err
// 	}
// 	defer resp.Body.Close()

// 	if resp.StatusCode != http.StatusOK {
// 		return nil, fmt.Errorf("failure of getting loadbalancer metadata with response %q", resp.Status)
// 	}

// 	data, err := io.ReadAll(resp.Body)
// 	if err != nil {
// 		return nil, err
// 	}

// 	obj := LoadBalancerMetadata{}
// 	err = json.Unmarshal(data, &obj)
// 	if err != nil {
// 		return nil, err
// 	}

// 	return &obj, nil
// }
