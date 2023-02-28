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
	"fmt"
	"strings"
	"time"

	"golang.org/x/net/context"
	"google.golang.org/grpc"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/kubernetes-csi/csi-lib-utils/protosanitizer"

	"k8s.io/klog/v2"
)

type logLevels struct {
	normal  klog.Level
	verbose klog.Level
}

var (
	lowPriApis = map[string]logLevels{
		"/csi.v1.Identity/Probe":           {6, 6},
		"/csi.v1.Node/NodeGetCapabilities": {6, 6},
		"/csi.v1.Node/NodeGetVolumeStats":  {6, 6},
		"/csi.v1.Controller/ListVolumes":   {6, 6},
	}

	defaultLogLevels = logLevels{2, 6}
)

func ParseEndpoint(ep string) (string, string, error) {
	if strings.HasPrefix(strings.ToLower(ep), "unix://") || strings.HasPrefix(strings.ToLower(ep), "tcp://") {
		s := strings.SplitN(ep, "://", 2)
		if s[1] != "" {
			return s[0], s[1], nil
		}
	}
	return "", "", fmt.Errorf("Invalid endpoint: %v", ep)
}

func NewVolumeCapabilityAccessMode(mode csi.VolumeCapability_AccessMode_Mode) *csi.VolumeCapability_AccessMode {
	return &csi.VolumeCapability_AccessMode{Mode: mode}
}

func NewControllerServiceCapability(cap csi.ControllerServiceCapability_RPC_Type) *csi.ControllerServiceCapability {
	return &csi.ControllerServiceCapability{
		Type: &csi.ControllerServiceCapability_Rpc{
			Rpc: &csi.ControllerServiceCapability_RPC{
				Type: cap,
			},
		},
	}
}

func NewNodeServiceCapability(cap csi.NodeServiceCapability_RPC_Type) *csi.NodeServiceCapability {
	return &csi.NodeServiceCapability{
		Type: &csi.NodeServiceCapability_Rpc{
			Rpc: &csi.NodeServiceCapability_RPC{
				Type: cap,
			},
		},
	}
}

func getLogLevel(method string) logLevels {
	if levels, ok := lowPriApis[method]; ok {
		return levels
	}

	return defaultLogLevels
}

func logGRPC(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
	levels := getLogLevel(info.FullMethod)

	klog.V(levels.verbose).Infof("GRPC call: %s, request: %s", info.FullMethod, protosanitizer.StripSecrets(req))

	startTime := time.Now()
	resp, err := handler(ctx, req)
	latency := time.Since(startTime).Seconds()

	if err != nil {
		klog.Errorf("GRPC failed: %s, latency: %.9f, request: %s, response: %s, error: %q", info.FullMethod, latency, protosanitizer.StripSecrets(req), protosanitizer.StripSecrets(resp), err)
	} else {
		klog.V(levels.normal).Infof("GRPC succeeded: %s, latency: %.9f, request: %s, response: %s", info.FullMethod, latency, protosanitizer.StripSecrets(req), protosanitizer.StripSecrets(resp))
	}

	return resp, err
}
