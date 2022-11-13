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
	"path/filepath"
	"strings"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

// IsValidPerfProfile Checks to see if perf profile passed is correct
// Right now we are only supporting basic profile
// Other advanced profiles to come later
func IsValidPerfProfile(profile string) bool {
	return isPerfTuningEnabled(profile) || strings.EqualFold(profile, consts.PerfProfileNone)
}

// getDiskPerfAttributes gets the per tuning mode and profile set in attributes
func GetDiskPerfAttributes(attributes map[string]string) (profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr string, deviceSettings map[string]string, err error) {
	deviceSettings = make(map[string]string)
	perfProfilePresent := false
	for k, v := range attributes {
		key := strings.ToLower(k)
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
			if setting, err := GetDeviceSettingFromAttribute(key); err == nil {
				deviceSettings[setting] = v
			}
		}
	}

	// If perfProfile was present in the volume attributes, use it
	if perfProfilePresent {
		// Make sure it's a valid perf profile
		if !IsValidPerfProfile(profile) {
			return profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr, deviceSettings, fmt.Errorf("Perf profile %s is invalid", profile)
		}
	} else {
		// if perfProfile parameter was not provided in the attributes
		// set it to 'None'. Which means no optimization will be done.
		profile = consts.PerfProfileNone
	}

	return profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr, deviceSettings, nil
}

// isPerfTuningEnabled checks to see if perf tuning is enabled
func isPerfTuningEnabled(profile string) bool {
	switch strings.ToLower(profile) {
	case consts.PerfProfileBasic:
		return true
	case consts.PerfProfileAdvanced:
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

// Gets device setting from the Disk attribute/param but trimming the consts.DeviceSettingsKeyPrefix prefix
func GetDeviceSettingFromAttribute(key string) (deviceSetting string, err error) {
	if !strings.HasPrefix(key, consts.DeviceSettingsKeyPrefix) {
		return key, fmt.Errorf("GetDeviceSettingFromAttribute: %s is not a valid device setting override", key)
	}
	return strings.TrimPrefix(key, consts.DeviceSettingsKeyPrefix), nil
}

// Checks to see if deviceSettings passed are valid
func AreDeviceSettingsValid(deviceRoot string, deviceSettings map[string]string) error {
	if len(deviceSettings) == 0 {
		return fmt.Errorf("AreDeviceSettingsValid: No deviceSettings passed")
	}

	for setting := range deviceSettings {
		// Use absolute setting path
		absSetting, err := filepath.Abs(setting)
		if err != nil {
			return fmt.Errorf("AreDeviceSettingsValid: Failed to get absolute path. Can not allow setting %s for device %s",
				setting,
				deviceRoot)
		}

		if !strings.HasPrefix(absSetting, filepath.Clean(deviceRoot)+"/") {
			return fmt.Errorf("AreDeviceSettingsValid: Setting %s is not a valid file path under %s", setting, deviceRoot)
		}
	}

	return nil
}
