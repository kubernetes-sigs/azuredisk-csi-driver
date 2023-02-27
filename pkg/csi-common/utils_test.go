/*
Copyright 2017 The Kubernetes Authors.

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

package csicommon

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"os"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	"github.com/stretchr/testify/assert"
)

func init() {
	klog.InitFlags(nil)
}

func TestMain(m *testing.M) {
	if e := flag.Set("logtostderr", "false"); e != nil {
		klog.Error(e)
	}
	if e := flag.Set("alsologtostderr", "false"); e != nil {
		klog.Error(e)
	}
	if e := flag.Set("v", "100"); e != nil {
		klog.Error(e)
	}
	flag.Parse()
	exitVal := m.Run()
	os.Exit(exitVal)
}

func TestParseEndpoint(t *testing.T) {

	//Valid unix domain socket endpoint
	sockType, addr, err := ParseEndpoint("unix://fake.sock")
	assert.NoError(t, err)
	assert.Equal(t, sockType, "unix")
	assert.Equal(t, addr, "fake.sock")

	sockType, addr, err = ParseEndpoint("unix:///fakedir/fakedir/fake.sock")
	assert.NoError(t, err)
	assert.Equal(t, sockType, "unix")
	assert.Equal(t, addr, "/fakedir/fakedir/fake.sock")

	//Valid unix domain socket with uppercase
	sockType, addr, err = ParseEndpoint("UNIX://fake.sock")
	assert.NoError(t, err)
	assert.Equal(t, sockType, "UNIX")
	assert.Equal(t, addr, "fake.sock")

	//Valid TCP endpoint with ip
	sockType, addr, err = ParseEndpoint("tcp://127.0.0.1:80")
	assert.NoError(t, err)
	assert.Equal(t, sockType, "tcp")
	assert.Equal(t, addr, "127.0.0.1:80")

	//Valid TCP endpoint with uppercase
	sockType, addr, err = ParseEndpoint("TCP://127.0.0.1:80")
	assert.NoError(t, err)
	assert.Equal(t, sockType, "TCP")
	assert.Equal(t, addr, "127.0.0.1:80")

	//Valid TCP endpoint with hostname
	sockType, addr, err = ParseEndpoint("tcp://fakehost:80")
	assert.NoError(t, err)
	assert.Equal(t, sockType, "tcp")
	assert.Equal(t, addr, "fakehost:80")

	_, _, err = ParseEndpoint("unix:/fake.sock/")
	assert.NotNil(t, err)

	_, _, err = ParseEndpoint("fake.sock")
	assert.NotNil(t, err)

	_, _, err = ParseEndpoint("unix://")
	assert.NotNil(t, err)

	_, _, err = ParseEndpoint("://")
	assert.NotNil(t, err)

	_, _, err = ParseEndpoint("")
	assert.NotNil(t, err)
}

func TestLogGRPC(t *testing.T) {
	// SET UP

	buf := new(bytes.Buffer)
	klog.SetOutput(buf)

	var handlerErr error
	handler := func(ctx context.Context, req interface{}) (interface{}, error) { return nil, handlerErr }
	info := grpc.UnaryServerInfo{
		FullMethod: "fake",
	}

	tests := []struct {
		name          string
		req           interface{}
		err           error
		callStr       string
		completionStr string
	}{
		{
			"with secrets",
			&csi.NodeStageVolumeRequest{
				VolumeId: "vol_1",
				Secrets: map[string]string{
					"account_name": "k8s",
					"account_key":  "testkey",
				},
				XXX_sizecache: 100,
			},
			nil,
			`GRPC call: fake, request: {"secrets":"***stripped***","volume_id":"vol_1"}`,
			`request: {"secrets":"***stripped***","volume_id":"vol_1"}, response: null`,
		},
		{
			"with error and secrets",
			&csi.NodeStageVolumeRequest{
				VolumeId: "vol_1",
				Secrets: map[string]string{
					"account_name": "k8s",
					"account_key":  "testkey",
				},
				XXX_sizecache: 100,
			},
			errors.New("failed"),
			`GRPC call: fake, request: {"secrets":"***stripped***","volume_id":"vol_1"}`,
			`request: {"secrets":"***stripped***","volume_id":"vol_1"}, response: null, error: "failed"`,
		},
		{
			"without secrets",
			&csi.ListSnapshotsRequest{
				StartingToken: "testtoken",
			},
			nil,
			`GRPC call: fake, request: {"starting_token":"testtoken"}`,
			`request: {"starting_token":"testtoken"}, response: null`,
		},
		{
			"with error and without secrets",
			&csi.ListSnapshotsRequest{
				StartingToken: "testtoken",
			},
			errors.New("failed"),
			`GRPC call: fake, request: {"starting_token":"testtoken"}`,
			`request: {"starting_token":"testtoken"}, response: null, error: "failed"`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			handlerErr = test.err
			// EXECUTE
			_, _ = logGRPC(context.Background(), test.req, &info, handler)
			klog.Flush()

			// ASSERT
			assert.Contains(t, buf.String(), "GRPC call: fake")
			assert.Contains(t, buf.String(), test.callStr)
			if handlerErr == nil {
				assert.Contains(t, buf.String(), "GRPC succeeded: fake")
			} else {
				assert.Contains(t, buf.String(), "GRPC failed: fake")
			}
			assert.Contains(t, buf.String(), test.completionStr)

			// CLEANUP
			buf.Reset()
		})
	}
}

func TestNewControllerServiceCapability(t *testing.T) {
	tests := []struct {
		cap csi.ControllerServiceCapability_RPC_Type
	}{
		{
			cap: csi.ControllerServiceCapability_RPC_UNKNOWN,
		},
		{
			cap: csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		},
		{
			cap: csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
		},
		{
			cap: csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
		},
		{
			cap: csi.ControllerServiceCapability_RPC_GET_CAPACITY,
		},
	}
	for _, test := range tests {
		resp := NewControllerServiceCapability(test.cap)
		assert.NotNil(t, resp)
		assert.Equal(t, resp.XXX_sizecache, int32(0))
	}
}

func TestNewNodeServiceCapability(t *testing.T) {
	tests := []struct {
		cap csi.NodeServiceCapability_RPC_Type
	}{
		{
			cap: csi.NodeServiceCapability_RPC_UNKNOWN,
		},
		{
			cap: csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		},
		{
			cap: csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
		},
		{
			cap: csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
		},
	}
	for _, test := range tests {
		resp := NewNodeServiceCapability(test.cap)
		assert.NotNil(t, resp)
		assert.Equal(t, resp.XXX_sizecache, int32(0))
	}
}

func TestGetLogLevel(t *testing.T) {
	tests := []struct {
		method string
		levels logLevels
	}{
		{
			method: "/csi.v1.Identity/Probe",
			levels: logLevels{6, 6},
		},
		{
			method: "/csi.v1.Node/NodeGetCapabilities",
			levels: logLevels{6, 6},
		},
		{
			method: "/csi.v1.Node/NodeGetVolumeStats",
			levels: logLevels{6, 6},
		},
		{
			method: "/csi.v1.Controller/ListVolumes",
			levels: logLevels{6, 6},
		},
		{
			method: "",
			levels: logLevels{2, 6},
		},
		{
			method: "unknown",
			levels: logLevels{2, 6},
		},
	}

	for _, test := range tests {
		levels := getLogLevel(test.method)
		if levels != test.levels {
			t.Errorf("returned level: (%v), expected level: (%v)", levels, test.levels)
		}
	}
}
