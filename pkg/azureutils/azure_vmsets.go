package azureutils

import (
	cloudprovider "k8s.io/cloud-provider"
)

// VMSet defines functions all vmsets (including scale set and availability
// set) should be implemented.
type VMSet interface {

	// GetZoneByNodeName gets cloudprovider.Zone by node name.
	GetZoneByNodeName(name string) (cloudprovider.Zone, error)
}
