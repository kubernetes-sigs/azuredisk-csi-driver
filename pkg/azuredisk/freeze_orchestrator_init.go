/*
Copyright 2025 The Kubernetes Authors.

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
	"context"

	"k8s.io/klog/v2"
)

// InitializeVolumeAttachmentTracking bootstraps and starts the VolumeAttachment tracking for freeze orchestrator
// This should be called during driver initialization after creating the FreezeOrchestrator
func (d *Driver) InitializeVolumeAttachmentTracking(ctx context.Context) error {
	if d.freezeOrchestrator == nil {
		klog.V(2).Infof("Freeze orchestrator not initialized, skipping VolumeAttachment tracking")
		return nil
	}

	klog.V(2).Infof("Initializing VolumeAttachment tracker for freeze orchestrator")

	// Bootstrap VolumeAttachment tracking - populate tracker with existing VolumeAttachments
	if err := d.freezeOrchestrator.BootstrapVolumeAttachmentTracking(ctx); err != nil {
		klog.Errorf("Failed to bootstrap VolumeAttachment tracking: %v", err)
		return err
	}

	klog.V(2).Infof("VolumeAttachment tracker bootstrapped successfully")

	// Start VolumeAttachment informer to keep tracker up-to-date
	if err := d.freezeOrchestrator.StartVolumeAttachmentInformer(ctx); err != nil {
		klog.Errorf("Failed to start VolumeAttachment informer: %v", err)
		return err
	}

	klog.V(2).Infof("VolumeAttachment informer started successfully")
	return nil
}

// StopVolumeAttachmentTracking stops the VolumeAttachment informer
// This should be called during driver shutdown
func (d *Driver) StopVolumeAttachmentTracking() {
	if d.freezeOrchestrator != nil {
		d.freezeOrchestrator.StopVolumeAttachmentInformer()
	}
}
