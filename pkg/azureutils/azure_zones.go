package azureutils

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	cloudprovider "k8s.io/cloud-provider"
	"k8s.io/klog/v2"
)

// GetZone returns the Zone containing the current availability zone and locality region that the program is running in.
// If the node is not running with availability zones, then it will fall back to fault domain.
func (az *Cloud) GetZone(ctx context.Context) (cloudprovider.Zone, error) {
	if az.UseInstanceMetadata {
		metadata, err := az.Metadata.GetMetadata(CacheReadTypeUnsafe)
		if err != nil {
			return cloudprovider.Zone{}, err
		}

		if metadata.Compute == nil {
			_ = az.Metadata.imsCache.Delete(MetadataCacheKey)
			return cloudprovider.Zone{}, fmt.Errorf("failure of getting compute information from instance metadata")
		}

		zone := ""
		location := metadata.Compute.Location
		if metadata.Compute.Zone != "" {
			zoneID, err := strconv.Atoi(metadata.Compute.Zone)
			if err != nil {
				return cloudprovider.Zone{}, fmt.Errorf("failed to parse zone ID %q: %w", metadata.Compute.Zone, err)
			}
			zone = az.makeZone(location, zoneID)
		} else {
			klog.V(3).Infof("Availability zone is not enabled for the node, falling back to fault domain")
			zone = metadata.Compute.FaultDomain
		}

		return cloudprovider.Zone{
			FailureDomain: strings.ToLower(zone),
			Region:        strings.ToLower(location),
		}, nil
	}
	// if UseInstanceMetadata is false, get Zone name by calling ARM
	hostname, err := os.Hostname()
	if err != nil {
		return cloudprovider.Zone{}, fmt.Errorf("failure getting hostname from kernel")
	}
	return az.VMSet.GetZoneByNodeName(strings.ToLower(hostname))
}

// makeZone returns the zone value in format of <region>-<zone-id>.
func (az *Cloud) makeZone(location string, zoneID int) string {
	return fmt.Sprintf("%s-%d", strings.ToLower(location), zoneID)
}
