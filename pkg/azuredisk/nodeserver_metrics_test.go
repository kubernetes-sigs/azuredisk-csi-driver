/*
Copyright 2024 The Kubernetes Authors.

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
	"os"
	"runtime"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"k8s.io/component-base/metrics/legacyregistry"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/mounter"
)

// These tests are regression guards for the metric-emission fix in
// https://github.com/kubernetes-sigs/azuredisk-csi-driver/pull/3789.
//
// Before the fix, several early-success return paths in NodeStageVolume and
// NodePublishVolume bypassed the `isOperationSucceeded = true` assignment,
// so the deferred ObserveWithLabels recorded success="false" for what were
// semantically successful CSI calls. The existing table-driven tests only
// checked the returned error and never asserted on the emitted metric, which
// is why the regression could be introduced in the first place.
//
// Each test resets the operation counter, drives the exact early-success
// return path being covered, and asserts a single sample with
// success="true" for the corresponding CSI operation.

// countOperationSamples returns the total number of samples recorded in the
// azuredisk_csi_driver_operations_total counter for a given (operation,
// success) label combination.
func countOperationSamples(t *testing.T, operation, success string) float64 {
	t.Helper()
	families, err := legacyregistry.DefaultGatherer.Gather()
	require.NoError(t, err)
	var total float64
	for _, family := range families {
		if family.GetName() != "azuredisk_csi_driver_operations_total" {
			continue
		}
		for _, m := range family.GetMetric() {
			var gotOp, gotSuccess string
			for _, lp := range m.GetLabel() {
				switch lp.GetName() {
				case "operation":
					gotOp = lp.GetValue()
				case "success":
					gotSuccess = lp.GetValue()
				}
			}
			if gotOp == operation && gotSuccess == success {
				total += counterValue(m)
			}
		}
	}
	return total
}

func counterValue(m *dto.Metric) float64 {
	if c := m.GetCounter(); c != nil {
		return c.GetValue()
	}
	return 0
}

// TestNodeStageVolume_BlockAccessType_EmitsSuccessMetric guards the
// success-metric fix on the block-access-type early return in
// NodeStageVolume (pkg/azuredisk/nodeserver.go line ~130 in the PR diff).
func TestNodeStageVolume_BlockAccessType_EmitsSuccessMetric(t *testing.T) {
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, _ := NewFakeDriver(cntl)
	fakeMounter, err := mounter.NewFakeSafeMounter()
	require.NoError(t, err)
	d.setMounter(fakeMounter)

	before := countOperationSamples(t, "node_stage_volume", "true")

	req := &csi.NodeStageVolumeRequest{
		VolumeId:          "vol_1",
		StagingTargetPath: sourceTest,
		VolumeCapability: &csi.VolumeCapability{
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: 2},
			AccessType: &csi.VolumeCapability_Block{
				Block: &csi.VolumeCapability_BlockVolume{},
			},
		},
	}
	_, err = d.NodeStageVolume(context.Background(), req)
	require.NoError(t, err, "block-access-type NodeStageVolume should short-circuit successfully")

	after := countOperationSamples(t, "node_stage_volume", "true")
	assert.InDelta(t, before+1, after, 0.0001,
		"NodeStageVolume block-access-type early return must record success=\"true\" on node_stage_volume counter")
}

// TestNodePublishVolume_AlreadyMounted_EmitsSuccessMetric guards the
// success-metric fix on the already-mounted early return in
// NodePublishVolume (pkg/azuredisk/nodeserver.go line ~285 in the PR diff).
//
// Uses the same guard as TestNodePublishVolumeIdempotentMount: this path
// requires the real mounter and root privileges to reach the
// ensureMountPoint(target) == already-mounted branch.
func TestNodePublishVolume_AlreadyMounted_EmitsSuccessMetric(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() != 0 {
		t.Skip("requires root on Linux to exercise the already-mounted early return")
	}
	cntl := gomock.NewController(t)
	defer cntl.Finish()
	d, _ := NewFakeDriver(cntl)

	_ = makeDir(sourceTest)
	_ = makeDir(targetTest)
	defer func() {
		_ = d.getMounter().Unmount(targetTest)
		_ = d.getMounter().Unmount(targetTest)
		_ = os.RemoveAll(sourceTest)
		_ = os.RemoveAll(targetTest)
	}()

	stdVolCap := &csi.VolumeCapability_Mount{
		Mount: &csi.VolumeCapability_MountVolume{FsType: defaultLinuxFsType},
	}
	req := &csi.NodePublishVolumeRequest{
		VolumeId:          "vol_1",
		StagingTargetPath: sourceTest,
		TargetPath:        targetTest,
		VolumeCapability: &csi.VolumeCapability{
			AccessMode: &csi.VolumeCapability_AccessMode{Mode: csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER},
			AccessType: stdVolCap,
		},
		Readonly: true,
	}

	// First publish performs the initial mount.
	_, err := d.NodePublishVolume(context.Background(), req)
	require.NoError(t, err)

	before := countOperationSamples(t, "node_publish_volume", "true")

	// Second publish hits the "already mounted" early return.
	_, err = d.NodePublishVolume(context.Background(), req)
	require.NoError(t, err, "already-mounted NodePublishVolume should short-circuit successfully")

	after := countOperationSamples(t, "node_publish_volume", "true")
	assert.GreaterOrEqual(t, after, before+1,
		"NodePublishVolume already-mounted early return must record success=\"true\" on node_publish_volume counter")
}
