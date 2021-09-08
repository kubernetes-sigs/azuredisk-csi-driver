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
	"io/ioutil"
	"os"
	"runtime"
	"testing"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"google.golang.org/grpc"
	"k8s.io/klog/v2"

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
			`GRPC request: {"secrets":"***stripped***","volume_id":"vol_1"}`,
		},
		{
			"without secrets",
			&csi.ListSnapshotsRequest{
				StartingToken: "testtoken",
			},
			`GRPC request: {"starting_token":"testtoken"}`,
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
			assert.Contains(t, buf.String(), "GRPC response: null")

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

func TestGetLogLevel(t *testing.T) {
	tests := []struct {
		method string
		level  int32
	}{
		{
			method: "/csi.v1.Identity/Probe",
			level:  6,
		},
		{
			method: "/csi.v1.Node/NodeGetCapabilities",
			level:  6,
		},
		{
			method: "/csi.v1.Node/NodeGetVolumeStats",
			level:  6,
		},
		{
			method: "",
			level:  2,
		},
		{
			method: "unknown",
			level:  2,
		},
	}

	for _, test := range tests {
		level := getLogLevel(test.method)
		if level != test.level {
			t.Errorf("returned level: (%v), expected level: (%v)", level, test.level)
		}
	}
}

func skipIfTestingOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Skipping tests on Windows")
	}
}

func TestGetKubeConfig(t *testing.T) {
	// skip for now as this is very flaky on Windows
	skipIfTestingOnWindows(t)
	kubeConfigEnvVariable := "KUBECONFIG"
	emptyKubeConfig := "empty-Kube-Config"
	validKubeConfig := "valid-Kube-Config"
	fakeContent := `
apiVersion: v1
clusters:
- cluster:
    server: https://localhost:8080
  name: foo-cluster
contexts:
- context:
    cluster: foo-cluster
    user: foo-user
    namespace: bar
  name: foo-context
current-context: foo-context
kind: Config
users:
- name: foo-user
  user:
    exec:
      apiVersion: client.authentication.k8s.io/v1alpha1
      args:
      - arg-1
      - arg-2
      command: foo-command
`

	err := createTestFile(emptyKubeConfig)
	if err != nil {
		t.Error(err)
	}
	defer func() {
		if err := os.Remove(emptyKubeConfig); err != nil {
			t.Error(err)
		}
	}()

	err = createTestFile(validKubeConfig)
	if err != nil {
		t.Error(err)
	}
	defer func() {
		if err := os.Remove(validKubeConfig); err != nil {
			t.Error(err)
		}
	}()

	if err := ioutil.WriteFile(validKubeConfig, []byte(fakeContent), 0666); err != nil {
		t.Error(err)
	}

	tests := []struct {
		desc                     string
		kubeconfig               string
		expectError              bool
		envVariableHasConfig     bool
		envVariableConfigIsValid bool
	}{
		{
			desc:                     "[success] valid kube config passed",
			kubeconfig:               validKubeConfig,
			expectError:              false,
			envVariableHasConfig:     false,
			envVariableConfigIsValid: false,
		},
		{
			desc:                     "[failure] invalid kube config passed",
			kubeconfig:               emptyKubeConfig,
			expectError:              true,
			envVariableHasConfig:     false,
			envVariableConfigIsValid: false,
		},
		{
			desc:                     "[failure] no config passed, invalid config in env.",
			kubeconfig:               "",
			expectError:              true,
			envVariableHasConfig:     true,
			envVariableConfigIsValid: false,
		},
		{
			desc:                     "[success] no config passed, valid config in env.",
			kubeconfig:               "",
			expectError:              false,
			envVariableHasConfig:     true,
			envVariableConfigIsValid: true,
		},
	}

	kubeConfigPathInEnv, ok := os.LookupEnv(kubeConfigEnvVariable)
	if ok {
		defer func() {
			if err := os.Setenv(kubeConfigEnvVariable, kubeConfigPathInEnv); err != nil {
				t.Error(ok)
			}
		}()
	}

	for _, test := range tests {
		if test.envVariableHasConfig {
			if test.envVariableConfigIsValid {
				if err := os.Setenv(kubeConfigEnvVariable, validKubeConfig); err != nil {
					t.Error(ok)
				}
			} else {
				if err := os.Setenv(kubeConfigEnvVariable, emptyKubeConfig); err != nil {
					t.Error(ok)
				}
			}

		} else {
			if err := os.Setenv(kubeConfigEnvVariable, ""); err != nil {
				t.Error(ok)
			}
		}
		_, err := GetKubeConfig(test.kubeconfig)
		receiveError := (err != nil)
		if test.expectError != receiveError {
			t.Errorf("desc: %s,\n input: %q, GetCloudProvider err: %v, expectErr: %v", test.desc, test.kubeconfig, err, test.expectError)
		}
	}
}

func createTestFile(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	return nil
}
