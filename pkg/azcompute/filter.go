/*
Copyright 2026 The Kubernetes Authors.

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

package azcompute

// filter.go — driver-owned FilterNonExistingDisks fallback.
//
// When a VM/VMSS update fails because a referenced managed disk was deleted
// out-of-band, ARM rejects the whole update. Mirroring provider behavior, we
// re-check each data disk's existence, drop the missing ones, and retry once.
// This is implemented directly on azclient's disk client (no provider, no cache).

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v7"

	"k8s.io/klog/v2"

	"sigs.k8s.io/cloud-provider-azure/pkg/azclient"
)

// diskURIRE parses a managed-disk resource ID:
//
//	/subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/disks/<name>
var diskURIRE = regexp.MustCompile(`(?i)/subscriptions/([^/]+)/resourceGroups/([^/]+)/providers/Microsoft\.Compute/disks/([^/]+)$`)

func parseDiskURI(diskURI string) (subscriptionID, resourceGroup, diskName string, err error) {
	m := diskURIRE.FindStringSubmatch(diskURI)
	if len(m) != 4 {
		return "", "", "", fmt.Errorf("could not parse disk URI %q", diskURI)
	}
	return m[1], m[2], m[3], nil
}

// isNotFound reports whether err is an ARM 404 response. Inlined so this package
// carries no dependency on cloud-provider-azure error helpers.
func isNotFound(err error) bool {
	var respErr *azcore.ResponseError
	if errors.As(err, &respErr) && respErr != nil {
		return respErr.StatusCode == http.StatusNotFound
	}
	return false
}

// diskExists reports whether the managed disk identified by diskURI still exists.
func diskExists(ctx context.Context, clientFactory azclient.ClientFactory, diskURI string) (bool, error) {
	sub, rg, name, err := parseDiskURI(diskURI)
	if err != nil {
		return false, err
	}
	diskClient, err := clientFactory.GetDiskClientForSub(sub)
	if err != nil {
		return false, err
	}
	if _, err := diskClient.Get(ctx, rg, name); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// filterNonExistingDisks returns the input list with any data disks whose backing
// managed disk no longer exists removed. Disks are only dropped on a definitive
// not-found; transient errors leave the disk in place.
func filterNonExistingDisks(ctx context.Context, clientFactory azclient.ClientFactory, disks []*armcompute.DataDisk) []*armcompute.DataDisk {
	filtered := make([]*armcompute.DataDisk, 0, len(disks))
	for _, disk := range disks {
		if disk.ManagedDisk != nil && disk.ManagedDisk.ID != nil {
			exists, err := diskExists(ctx, clientFactory, *disk.ManagedDisk.ID)
			if err == nil && !exists {
				klog.Warningf("azureDisk - data disk %q no longer exists, dropping from update", *disk.ManagedDisk.ID)
				continue
			}
		}
		filtered = append(filtered, disk)
	}
	return filtered
}
