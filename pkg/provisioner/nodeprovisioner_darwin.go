//go:build darwin
// +build darwin

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

package provisioner

import (
	"context"

	"github.com/container-storage-interface/spec/lib/go/csi"
)

// NeedsResize returns true if the volume needs to be resized.
func (p *NodeProvisioner) NeedsResize(devicePath, volumePath string) (bool, error) {
	return false, nil
}

// GetVolumeStats returns usage information for the specified volume.
func (p *NodeProvisioner) GetVolumeStats(ctx context.Context, target string) ([]*csi.VolumeUsage, error) {
	return []*csi.VolumeUsage{}, nil
}
