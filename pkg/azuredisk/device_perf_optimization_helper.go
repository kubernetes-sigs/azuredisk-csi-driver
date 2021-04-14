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
	"strings"

	"k8s.io/klog/v2"
)

const (
	PerfTuningModeNone       = "none"
	PerfTuningModeAuto       = "auto"
	PerfProfileDefault       = "default"
	PremiumAccountPrefix     = "premium"
	StandardSsdAccountPrefix = "standardssd"
)

// IsValidPerfTuningMode Checks to see if perf tuning mode passed is correct
// Right now we only support auto perf tuning mode
// Manual mode to come later
func IsValidPerfTuningMode(mode string) bool {

	switch strings.ToLower(mode) {
	case PerfTuningModeNone, PerfTuningModeAuto:
		return true
	default:
		return false
	}
}

// IsValidPerfProfile Checks to see if perf profile passed is correct
// Right now we are only supporing default profile
// Other advanced profiles to come later
func IsValidPerfProfile(mode string, profile string) bool {

	if strings.EqualFold(mode, PerfTuningModeNone) {
		return true
	}

	return strings.EqualFold(profile, PerfProfileDefault)
}

// IsPerfTuningEnabled checks to see if perf tuning is enabled
func IsPerfTuningEnabled(mode string) bool {

	switch strings.ToLower(mode) {
	case PerfTuningModeAuto:
		return true
	default:
		return false
	}
}

// GetPerfTuningModeAndProfile gets the per tuning mode and profile set in attributes
func GetDiskPerfAttributes(attributes map[string]string) (mode string, profile string, accountType string, diskSizeGibStr string, diskIopsStr string, diskBwMbpsStr string) {

	for k, v := range attributes {
		switch strings.ToLower(k) {
		case perfTuningModeField:
			mode = v
		case perfProfileField:
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

	// If perf tuning mode is not valid, set it to none
	if !IsValidPerfTuningMode(mode) {
		klog.Warningf("Invalid perf tuning mode %s. Defaulting to: %s", mode, PerfTuningModeNone)
		mode = PerfTuningModeNone
	}

	// If profile is not valid, set it to default
	if !IsValidPerfProfile(mode, profile) {
		klog.Warningf("Invalid perf profile %s for mode %s. Defaulting to: %s", profile, mode, PerfProfileDefault)
		profile = PerfProfileDefault
	}

	return mode, profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr
}

func AccountSupportsPerfOptimization(accountType string) bool {

	accountTypeLower := strings.ToLower(accountType)
	if strings.HasPrefix(accountTypeLower, "premium") || strings.HasPrefix(accountTypeLower, "standardssd") {
		return true
	}

	return false
}
