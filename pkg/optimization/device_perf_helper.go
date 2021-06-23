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
	"strings"
)

const (
	PerfProfileNone          = "none"
	PerfProfileBasic         = "basic"
	PremiumAccountPrefix     = "premium"
	StandardSsdAccountPrefix = "standardssd"
)

// IsValidPerfProfile Checks to see if perf profile passed is correct
// Right now we are only supporing basic profile
// Other advanced profiles to come later
func IsValidPerfProfile(profile string) bool {
	return strings.EqualFold(profile, PerfProfileBasic) || strings.EqualFold(profile, PerfProfileNone)
}

// isPerfTuningEnabled checks to see if perf tuning is enabled
func isPerfTuningEnabled(profile string) bool {
	switch strings.ToLower(profile) {
	case PerfProfileBasic:
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
