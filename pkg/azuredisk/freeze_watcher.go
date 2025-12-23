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
	"runtime"

	"k8s.io/client-go/kubernetes/scheme"
	clientcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk/freeze"

	corev1 "k8s.io/api/core/v1"
)

const (
	// DefaultFSFreezeWaitTimeoutInMins for best-effort mode is 2 mins
	DefaultFSFreezeWaitTimeoutInMinsBestEffort = 2

	// DefaultFSFreezeWaitTimeoutInMinsStrict for strict mode is 30 mins
	DefaultFSFreezeWaitTimeoutInMinsStrict = 30
)

// startVolumeAttachmentWatcher starts the VolumeAttachment watcher for freeze/unfreeze operations
func (d *Driver) startVolumeAttachmentWatcher(ctx context.Context) {
	if runtime.GOOS == "windows" {
		klog.V(2).Infof("VolumeAttachment watcher not supported on Windows, skipping")
		return
	}

	if d.NodeID == "" {
		klog.V(2).Infof("NodeID is empty, not starting VolumeAttachment watcher")
		return
	}

	klog.V(2).Infof("Starting VolumeAttachment watcher for node %s", d.NodeID)

	// Create event recorder if not exists
	eventRecorder := d.eventRecorder
	if eventRecorder == nil && d.cloud != nil && d.cloud.KubeClient != nil {
		eventBroadcaster := record.NewBroadcaster()
		eventBroadcaster.StartRecordingToSink(&clientcorev1.EventSinkImpl{
			Interface: d.cloud.KubeClient.CoreV1().Events(""),
		})
		eventRecorder = eventBroadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{
			Component: d.Name,
		})
	}

	// Create freeze manager
	// Use /tmp as state directory (can be changed to /host-tmp if needed)
	freezeManager := freeze.NewFreezeManager("/tmp")

	// Create and start watcher
	timeoutMins := int(d.fsFreezeWaitTimeoutInMins)
	switch d.snapshotConsistencyMode {
	case "strict":
		if timeoutMins == 0 {
			// For strict mode with 0 timeout, use a large timeout (indefinite)
			timeoutMins = DefaultFSFreezeWaitTimeoutInMinsStrict
		}
	case "best-effort":
		// For best-effort, use at least 2 minutes
		if timeoutMins < DefaultFSFreezeWaitTimeoutInMinsBestEffort {
			timeoutMins = DefaultFSFreezeWaitTimeoutInMinsBestEffort
		}
	}

	watcher := freeze.NewVolumeAttachmentWatcher(
		d.NodeID,
		d.cloud.KubeClient,
		freezeManager,
		eventRecorder,
		d.mounter,
		timeoutMins,
	)

	watcher.Start(ctx)
	d.vaWatcher = watcher

	klog.V(2).Infof("VolumeAttachment watcher started successfully for node %s with timeout %d minutes", d.NodeID, timeoutMins)
}

// stopVolumeAttachmentWatcher stops the VolumeAttachment watcher
func (d *Driver) stopVolumeAttachmentWatcher() {
	if d.vaWatcher != nil {
		if watcher, ok := d.vaWatcher.(*freeze.VolumeAttachmentWatcher); ok {
			klog.V(2).Infof("Stopping VolumeAttachment watcher")
			watcher.Stop()
			d.vaWatcher = nil
		}
	}
}
