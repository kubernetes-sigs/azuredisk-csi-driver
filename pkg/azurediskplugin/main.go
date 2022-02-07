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

package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk"

	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

func init() {
	klog.InitFlags(nil)
}

var (
	endpoint                   = flag.String("endpoint", "unix://tmp/csi.sock", "CSI endpoint")
	nodeID                     = flag.String("nodeid", "", "node id")
	version                    = flag.Bool("version", false, "Print the version and exit.")
	metricsAddress             = flag.String("metrics-address", "0.0.0.0:29604", "export the metrics")
	kubeconfig                 = flag.String("kubeconfig", "", "Absolute path to the kubeconfig file. Required only when running out of cluster.")
	driverName                 = flag.String("drivername", consts.DefaultDriverName, "name of the driver")
	volumeAttachLimit          = flag.Int64("volume-attach-limit", -1, "maximum number of attachable volumes per node")
	disableAVSetNodes          = flag.Bool("disable-avset-nodes", true, "disable DisableAvailabilitySetNodes in cloud config for controller")
	enablePerfOptimization     = flag.Bool("enable-perf-optimization", false, "boolean flag to enable disk perf optimization")
	cloudConfigSecretName      = flag.String("cloud-config-secret-name", "azure-cloud-provider", "cloud config secret name")
	cloudConfigSecretNamespace = flag.String("cloud-config-secret-namespace", "kube-system", "cloud config secret namespace")
	customUserAgent            = flag.String("custom-user-agent", "", "custom userAgent")
	userAgentSuffix            = flag.String("user-agent-suffix", "", "userAgent suffix")
	useCSIProxyGAInterface     = flag.Bool("use-csiproxy-ga-interface", true, "boolean flag to enable csi-proxy GA interface on Windows")
	enableDiskOnlineResize     = flag.Bool("enable-disk-online-resize", true, "boolean flag to enable disk online resize")
	allowEmptyCloudConfig      = flag.Bool("allow-empty-cloud-config", true, "Whether allow running driver without cloud config")
)

func main() {
	flag.Parse()
	if *version {
		info, err := azuredisk.GetVersionYAML(*driverName)
		if err != nil {
			klog.Fatalln(err)
		}
		fmt.Println(info) // nolint
		os.Exit(0)
	}

	if *nodeID == "" {
		// nodeid is not needed in controller component
		klog.Warning("nodeid is empty")
	}

	exportMetrics()
	handle()
	os.Exit(0)
}

func handle() {
	driverOptions := azuredisk.DriverOptions{
		NodeID:                     *nodeID,
		DriverName:                 *driverName,
		VolumeAttachLimit:          *volumeAttachLimit,
		EnablePerfOptimization:     *enablePerfOptimization,
		CloudConfigSecretName:      *cloudConfigSecretName,
		CloudConfigSecretNamespace: *cloudConfigSecretNamespace,
		CustomUserAgent:            *customUserAgent,
		UserAgentSuffix:            *userAgentSuffix,
		UseCSIProxyGAInterface:     *useCSIProxyGAInterface,
		EnableDiskOnlineResize:     *enableDiskOnlineResize,
		AllowEmptyCloudConfig:      *allowEmptyCloudConfig,
	}
	driver := azuredisk.NewDriver(&driverOptions)
	if driver == nil {
		klog.Fatalln("Failed to initialize azuredisk CSI Driver")
	}
	testingMock := false
	driver.Run(*endpoint, *kubeconfig, *disableAVSetNodes, testingMock)
}

func exportMetrics() {
	l, err := net.Listen("tcp", *metricsAddress)
	if err != nil {
		klog.Warningf("failed to get listener for metrics endpoint: %v", err)
		return
	}
	serve(context.Background(), l, serveMetrics)
}

func serve(ctx context.Context, l net.Listener, serveFunc func(net.Listener) error) {
	path := l.Addr().String()
	klog.V(2).Infof("set up prometheus server on %v", path)
	go func() {
		defer l.Close()
		if err := serveFunc(l); err != nil {
			klog.Fatalf("serve failure(%v), address(%v)", err, path)
		}
	}()
}

func serveMetrics(l net.Listener) error {
	m := http.NewServeMux()
	m.Handle("/metrics", legacyregistry.Handler()) //nolint, because azure cloud provider uses legacyregistry currently
	return trapClosedConnErr(http.Serve(l, m))
}

func trapClosedConnErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "use of closed network connection") {
		return nil
	}
	return err
}
