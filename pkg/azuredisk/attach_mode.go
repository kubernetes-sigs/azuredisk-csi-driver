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

package azuredisk

import (
	"fmt"
	"strings"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

// getAttachMode returns the requested attachment architecture.
func getAttachMode(parameters map[string]string) (string, error) {
	mode, exists := azureutils.ParseDiskParametersForKey(parameters, consts.AttachModeField)
	if !exists {
		return consts.AttachModeControllerDriven, nil
	}

	switch {
	case strings.EqualFold(mode, consts.AttachModeControllerDriven):
		return consts.AttachModeControllerDriven, nil
	case strings.EqualFold(mode, consts.AttachModeNodeDriven):
		return consts.AttachModeNodeDriven, nil
	default:
		return "", fmt.Errorf("unsupported %s %q: supported values are %s and %s", consts.AttachModeField, mode, consts.AttachModeControllerDriven, consts.AttachModeNodeDriven)
	}
}
