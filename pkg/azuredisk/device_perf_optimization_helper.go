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
)

const (
	PerfProfileNone          = "none"
	PerfProfileDefault       = "default"
	PremiumAccountPrefix     = "premium"
	StandardSsdAccountPrefix = "standardssd"
)

// isValidPerfProfile Checks to see if perf profile passed is correct
// Right now we are only supporing default profile
// Other advanced profiles to come later
func isValidPerfProfile(profile string) bool {
	return strings.EqualFold(profile, PerfProfileDefault) || strings.EqualFold(profile, PerfProfileNone)
}

// isPerfTuningEnabled checks to see if perf tuning is enabled
func isPerfTuningEnabled(profile string) bool {
	switch strings.ToLower(profile) {
	case PerfProfileDefault:
		return true
	default:
		return false
	}
}

// getDiskPerfAttributes gets the per tuning mode and profile set in attributes
func getDiskPerfAttributes(attributes map[string]string) (profile *string, accountType string, diskSizeGibStr string, diskIopsStr string, diskBwMbpsStr string, err error) {
	profile = nil
	for k, v := range attributes {
		switch strings.ToLower(k) {
		case perfProfileField:
			profileTemp := v
			profile = &profileTemp
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

	// if no perfProfile parameter was provided in the attributes
	// set it to 'None'. Which means no optimization will be done.
	if profile == nil {
		nonePerfProfileTemp := PerfProfileNone
		profile = &nonePerfProfileTemp
	} else if !isValidPerfProfile(*profile) {
		return profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr, fmt.Errorf("Perf profile %s is invalid", *profile)
	}

	return profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr, nil
}

// accountSupportsPerfOptimization checks to see if account type supports perf optimization
func accountSupportsPerfOptimization(accountType string) bool {
	accountTypeLower := strings.ToLower(accountType)
	if strings.HasPrefix(accountTypeLower, "premium") || strings.HasPrefix(accountTypeLower, "standardssd") {
		return true
	}

	return false
}
