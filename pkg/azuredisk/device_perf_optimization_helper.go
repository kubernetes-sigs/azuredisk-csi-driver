/*
Copyright 2021 The Kubernetes Authors.

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

package azuredisk

import (
	"fmt"
	"strings"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization"
)

// getDiskPerfAttributes gets the per tuning mode and profile set in attributes
func getDiskPerfAttributes(attributes map[string]string) (profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr string, err error) {
	perfProfilePresent := false
	for k, v := range attributes {
		switch strings.ToLower(k) {
		case perfProfileField:
			perfProfilePresent = true
			profile = v
		case skuNameField:
			accountType = v
		case requestedSizeGib:
			diskSizeGibStr = v
		case diskIOPSReadWriteField:
			diskIopsStr = v
		case diskMBPSReadWriteField:
			diskBwMbpsStr = v
		default:
			continue
		}
	}

	// If perfProfile was present in the volume attributes, use it
	if perfProfilePresent {
		// Make sure it's a valid perf profile
		if !optimization.IsValidPerfProfile(profile) {
			return profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr, fmt.Errorf("Perf profile %s is invalid", profile)
		}
	} else {
		// if perfProfile parameter was not provided in the attributes
		// set it to 'None'. Which means no optimization will be done.
		profile = optimization.PerfProfileNone
	}

	return profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr, nil
}
