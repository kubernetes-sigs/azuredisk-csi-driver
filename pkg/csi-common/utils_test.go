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
	"flag"
	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"k8s.io/klog"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
	klog.InitFlags(nil)
	if e := flag.Set("logtostderr", "false"); e != nil {
		t.Error(e)
	}
	if e := flag.Set("alsologtostderr", "false"); e != nil {
		t.Error(e)
	}
	if e := flag.Set("v", "100"); e != nil {
		t.Error(e)
	}
	flag.Parse()

	buf := new(bytes.Buffer)
	klog.SetOutput(buf)

	handler := func(ctx context.Context, req interface{}) (interface{}, error) { return nil, nil }
	info := grpc.UnaryServerInfo{
		FullMethod: "fake",
	}

	tests := []struct {
		name   string
		req    interface{}
		expStr string
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
			`GRPC request: volume_id:"vol_1" secrets:<key:"account_key" value:"****" > secrets:<key:"account_name" value:"****" >`,
		},
		{
			"without secrets",
			&csi.ListSnapshotsRequest{
				StartingToken: "testtoken",
			},
			`GRPC request: starting_token:"testtoken"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// EXECUTE
			_, _ = logGRPC(context.Background(), test.req, &info, handler)
			klog.Flush()

			// ASSERT
			assert.Contains(t, buf.String(), "GRPC call: fake")
			assert.Contains(t, buf.String(), test.expStr)
			assert.Contains(t, buf.String(), "GRPC response: <nil>")

			// CLEANUP
			buf.Reset()
		})
	}
}
func TestNewDefaultNodeServer(t *testing.T) {
	d := NewFakeCSIDriver()
	resp := NewDefaultNodeServer(d)
	assert.NotNil(t, resp)
	assert.Equal(t, resp.Driver.Name, fakeCSIDriverName)
	assert.Equal(t, resp.Driver.NodeID, fakeNodeID)
	assert.Equal(t, resp.Driver.Version, vendorVersion)
}

func TestNewDefaultIdentityServer(t *testing.T) {
	d := NewFakeCSIDriver()
	resp := NewDefaultIdentityServer(d)
	assert.NotNil(t, resp)
	assert.Equal(t, resp.Driver.Name, fakeCSIDriverName)
	assert.Equal(t, resp.Driver.NodeID, fakeNodeID)
	assert.Equal(t, resp.Driver.Version, vendorVersion)
}

func TestNewDefaultControllerServer(t *testing.T) {
	d := NewFakeCSIDriver()
	resp := NewDefaultControllerServer(d)
	assert.NotNil(t, resp)
	assert.Equal(t, resp.Driver.Name, fakeCSIDriverName)
	assert.Equal(t, resp.Driver.NodeID, fakeNodeID)
	assert.Equal(t, resp.Driver.Version, vendorVersion)
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
