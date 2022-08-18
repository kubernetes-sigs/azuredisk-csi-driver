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

package optimization

import (
	"fmt"
	"strings"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

// IsValidPerfProfile Checks to see if perf profile passed is correct
// Right now we are only supporting basic profile
// Other advanced profiles to come later
func IsValidPerfProfile(profile string) bool {
	return strings.EqualFold(profile, consts.PerfProfileBasic) || strings.EqualFold(profile, consts.PerfProfileNone)
}

// getDiskPerfAttributes gets the per tuning mode and profile set in attributes
func GetDiskPerfAttributes(attributes map[string]string) (profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr string, err error) {
	perfProfilePresent := false
	for k, v := range attributes {
		switch strings.ToLower(k) {
		case consts.PerfProfileField:
			perfProfilePresent = true
			profile = v
		case consts.SkuNameField:
			accountType = v
		case consts.RequestedSizeGib:
			diskSizeGibStr = v
		case consts.DiskIOPSReadWriteField:
			diskIopsStr = v
		case consts.DiskMBPSReadWriteField:
			diskBwMbpsStr = v
		default:
			continue
		}
	}

	// If perfProfile was present in the volume attributes, use it
	if perfProfilePresent {
		// Make sure it's a valid perf profile
		if !IsValidPerfProfile(profile) {
			return profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr, fmt.Errorf("Perf profile %s is invalid", profile)
		}
	} else {
		// if perfProfile parameter was not provided in the attributes
		// set it to 'None'. Which means no optimization will be done.
		profile = consts.PerfProfileNone
	}

	return profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr, nil
}

// isPerfTuningEnabled checks to see if perf tuning is enabled
func isPerfTuningEnabled(profile string) bool {
	switch strings.ToLower(profile) {
	case consts.PerfProfileBasic:
		return true
	default:
		return false
	}
}

// accountSupportsPerfOptimization checks to see if account type supports perf optimization
func accountSupportsPerfOptimization(accountType string) bool {
	accountTypeLower := strings.ToLower(accountType)
	if strings.HasPrefix(accountTypeLower, "premium") || strings.HasPrefix(accountTypeLower, "standardssd") {
		return true
	}
	return false
}
