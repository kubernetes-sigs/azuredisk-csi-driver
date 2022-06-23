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
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"strings"

	"gopkg.in/yaml.v2"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk"

	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"
	"k8s.io/utils/strings/slices"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

func init() {
	klog.InitFlags(nil)
}

var (
	driverConfig azdiskv1beta2.AzDiskDriverConfiguration
	driverConfigPath           = flag.String("config", "", "The configuration path for the driver")
	nodeID                     = flag.String("nodeid", "", "node id")
	version                    = flag.Bool("version", false, "Print the version and exit.")
	// Deprecated command-line parameters
	endpoint                   = flag.String("endpoint", "unix://tmp/csi.sock", "CSI endpoint")
	metricsAddress             = flag.String("metrics-address", "0.0.0.0:29604", "export the metrics")
	kubeconfig                 = flag.String("kubeconfig", "", "Absolute path to the kubeconfig file. Required only when running out of cluster.")
	driverName                 = flag.String("drivername", consts.DefaultDriverName, "name of the driver")
	volumeAttachLimit          = flag.Int64("volume-attach-limit", -1, "maximum number of attachable volumes per node")
	supportZone                = flag.Bool("support-zone", true, "boolean flag to get zone info in NodeGetInfo")
	getNodeInfoFromLabels      = flag.Bool("get-node-info-from-labels", false, "boolean flag to get zone info from node labels in NodeGetInfo")
	disableAVSetNodes          = flag.Bool("disable-avset-nodes", false, "disable DisableAvailabilitySetNodes in cloud config for controller")
	vmType                     = flag.String("vm-type", "", "type of agent node. available values: vmss, standard")
	enablePerfOptimization     = flag.Bool("enable-perf-optimization", false, "boolean flag to enable disk perf optimization")
	cloudConfigSecretName      = flag.String("cloud-config-secret-name", "azure-cloud-provider", "cloud config secret name")
	cloudConfigSecretNamespace = flag.String("cloud-config-secret-namespace", "kube-system", "cloud config secret namespace")
	customUserAgent            = flag.String("custom-user-agent", "", "custom userAgent")
	userAgentSuffix            = flag.String("user-agent-suffix", "", "userAgent suffix")
	useCSIProxyGAInterface     = flag.Bool("use-csiproxy-ga-interface", true, "boolean flag to enable csi-proxy GA interface on Windows")
	enableDiskOnlineResize     = flag.Bool("enable-disk-online-resize", true, "boolean flag to enable disk online resize")
	allowEmptyCloudConfig      = flag.Bool("allow-empty-cloud-config", true, "Whether allow running driver without cloud config")
	enableAsyncAttach          = flag.Bool("enable-async-attach", false, "boolean flag to enable async attach")
	enableListVolumes          = flag.Bool("enable-list-volumes", false, "boolean flag to enable ListVolumes on controller")
	enableListSnapshots        = flag.Bool("enable-list-snapshots", false, "boolean flag to enable ListSnapshots on controller")
	enableDiskCapacityCheck    = flag.Bool("enable-disk-capacity-check", false, "boolean flag to enable volume capacity check in CreateVolume")
	kubeClientQPS              = flag.Int("kube-client-qps", 15, "QPS for the rest client. Defaults to 15.")
	vmssCacheTTLInSeconds      = flag.Int64("vmss-cache-ttl-seconds", -1, "vmss cache TTL in seconds (600 by default)")
)

func main() {
	flag.Parse()
	// Emit warning log when deprecated command-line parameters are specified
	params := []string{"endpoint", "metrics-address", "kubeconfig", "drivername", "volume-attach-limit", "support-zone", "get-node-info-from-labels", "disable-avset-nodes",
		"vm-type", "enable-perf-optimization", "cloud-config-secret-name", "cloud-config-secret-namespace", "custom-user-agent", "user-agent-suffix", "use-csiproxy-ga-interface",
		"enable-disk-online-resize", "allow-empty-cloud-config", "enable-async-attach", "enable-list-volumes", "enable-list-snapshots", "enable-disk-capacity-check",
		"kube-client-qps", "vmss-cache-ttl-seconds", "is-controller-plugin", "is-node-plugin", "driver-object-namespace", "heartbeat-frequency-in-sec", "lease-duration-in-sec",
		"lease-renew-deadline-in-sec", "lease-retry-period-in-sec", "leader-election-namespace", "node-partition", "controller-partition"}
	flag.Visit(func(f *flag.Flag) {
		if slices.Contains(params, f.Name) {
			klog.Warningf("the command-line parameter %v is deprecated, using CongfigMap instead", f.Name)
		}
	})

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
	var driverOptions azuredisk.DriverOptions
	if *driverConfigPath != "" {
		// Read config file and convert to a strut object
		yamlFile, err := ioutil.ReadFile(*driverConfigPath)
		if err != nil {
			klog.Fatalf("failed to get the driver config, error: %v", err)
		}

		err = yaml.Unmarshal(yamlFile, driverConfig)
		if err != nil {
			klog.Fatalf("failed to unmarshal the driver config, error: %v", err)
		}
		azuredisk.DriverConfig = driverConfig

		driverOptions = azuredisk.DriverOptions{
			NodeID:                     *nodeID,
			DriverName:                 driverConfig.DriverName,
			VolumeAttachLimit:          driverConfig.NodeConfig.VolumeAttachLimit,
			EnablePerfOptimization:     driverConfig.NodeConfig.EnablePerfOptimization,
			CloudConfigSecretName:      driverConfig.CloudConfig.CloudConfigSecretName,
			CloudConfigSecretNamespace: driverConfig.CloudConfig.CloudConfigSecretNamespace,
			CustomUserAgent:            driverConfig.CloudConfig.CustomUserAgent,
			UserAgentSuffix:            driverConfig.CloudConfig.UserAgentSuffix,
			UseCSIProxyGAInterface:     driverConfig.NodeConfig.UseCSIProxyGAInterface,
			EnableDiskOnlineResize:     driverConfig.ControllerConfig.EnableDiskOnlineResize,
			AllowEmptyCloudConfig:      driverConfig.CloudConfig.AllowEmptyCloudConfig,
			EnableAsyncAttach:          driverConfig.ControllerConfig.EnableAsyncAttach,
			EnableListVolumes:          driverConfig.ControllerConfig.EnableListVolumes,
			EnableListSnapshots:        driverConfig.ControllerConfig.EnableListSnapshots,
			SupportZone:                driverConfig.NodeConfig.SupportZone,
			GetNodeInfoFromLabels:      driverConfig.NodeConfig.GetNodeInfoFromLabels,
			EnableDiskCapacityCheck:    driverConfig.ControllerConfig.EnableDiskCapacityCheck,
			VMSSCacheTTLInSeconds:      driverConfig.CloudConfig.VmssCacheTTLInSeconds,
			VMType:                     driverConfig.ControllerConfig.VMType,
			RestClientQPS:              driverConfig.ClientConfig.KubeClientQPS,
		}

		driver := azuredisk.NewDriver(&driverOptions)
		if driver == nil {
			klog.Fatalln("Failed to initialize azuredisk CSI Driver")
		}
		testingMock := false
		driver.Run(driverConfig.Endpoint, driverConfig.ClientConfig.Kubeconfig, driverConfig.ControllerConfig.DisableAVSetNodes, testingMock)
	} else {
		driverOptions = azuredisk.DriverOptions{
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
			EnableAsyncAttach:          *enableAsyncAttach,
			EnableListVolumes:          *enableListVolumes,
			EnableListSnapshots:        *enableListSnapshots,
			SupportZone:                *supportZone,
			GetNodeInfoFromLabels:      *getNodeInfoFromLabels,
			EnableDiskCapacityCheck:    *enableDiskCapacityCheck,
			VMSSCacheTTLInSeconds:      *vmssCacheTTLInSeconds,
			VMType:                     *vmType,
			RestClientQPS:              *kubeClientQPS,
		}
		driver := azuredisk.NewDriver(&driverOptions)
		if driver == nil {
			klog.Fatalln("Failed to initialize azuredisk CSI Driver")
		}
		testingMock := false
		driver.Run(*endpoint, *kubeconfig, *disableAVSetNodes, testingMock)
	}
}

func exportMetrics() {
	var l net.Listener
	var err error
	if *driverConfigPath != "" {
		l, err = net.Listen("tcp", driverConfig.MetricsAddress)
	} else {
		l, err = net.Listen("tcp", *metricsAddress)
	}

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
