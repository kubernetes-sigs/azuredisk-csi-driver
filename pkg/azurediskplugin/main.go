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
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

func init() {
	klog.InitFlags(nil)
}

var (
	driverConfig     azdiskv1beta2.AzDiskDriverConfiguration
	driverConfigPath = flag.String("config", "", "The configuration path for the driver")
	nodeID           = flag.String("nodeid", "", "node id")
	version          = flag.Bool("version", false, "Print the version and exit.")
	// Deprecated command-line parameters
	endpoint                   = flag.String("endpoint", consts.Endpoint, "CSI endpoint")
	metricsAddress             = flag.String("metrics-address", consts.MetricsAddress, "export the metrics")
	kubeconfig                 = flag.String("kubeconfig", consts.Kubeconfig, "Absolute path to the kubeconfig file. Required only when running out of cluster.")
	driverName                 = flag.String("drivername", consts.DefaultDriverName, "name of the driver")
	volumeAttachLimit          = flag.Int64("volume-attach-limit", consts.VolumeAttachLimit, "maximum number of attachable volumes per node")
	supportZone                = flag.Bool("support-zone", consts.SupportZone, "boolean flag to get zone info in NodeGetInfo")
	getNodeInfoFromLabels      = flag.Bool("get-node-info-from-labels", consts.GetNodeInfoFromLabels, "boolean flag to get zone info from node labels in NodeGetInfo")
	disableAVSetNodes          = flag.Bool("disable-avset-nodes", consts.DisableAVSetNodes, "disable DisableAvailabilitySetNodes in cloud config for controller")
	vmType                     = flag.String("vm-type", consts.VMType, "type of agent node. available values: vmss, standard")
	enablePerfOptimization     = flag.Bool("enable-perf-optimization", consts.EnablePerfOptimization, "boolean flag to enable disk perf optimization")
	cloudConfigSecretName      = flag.String("cloud-config-secret-name", consts.CloudConfigSecretName, "cloud config secret name")
	cloudConfigSecretNamespace = flag.String("cloud-config-secret-namespace", consts.CloudConfigSecretNamespace, "cloud config secret namespace")
	customUserAgent            = flag.String("custom-user-agent", consts.CustomUserAgent, "custom userAgent")
	userAgentSuffix            = flag.String("user-agent-suffix", consts.UserAgentSuffix, "userAgent suffix")
	useCSIProxyGAInterface     = flag.Bool("use-csiproxy-ga-interface", consts.UseCSIProxyGAInterface, "boolean flag to enable csi-proxy GA interface on Windows")
	enableDiskOnlineResize     = flag.Bool("enable-disk-online-resize", consts.EnableDiskOnlineResize, "boolean flag to enable disk online resize")
	allowEmptyCloudConfig      = flag.Bool("allow-empty-cloud-config", consts.AllowEmptyCloudConfig, "Whether allow running driver without cloud config")
	enableAsyncAttach          = flag.Bool("enable-async-attach", consts.EnableAsyncAttach, "boolean flag to enable async attach")
	enableListVolumes          = flag.Bool("enable-list-volumes", consts.EnableListVolumes, "boolean flag to enable ListVolumes on controller")
	enableListSnapshots        = flag.Bool("enable-list-snapshots", consts.EnableListSnapshots, "boolean flag to enable ListSnapshots on controller")
	enableDiskCapacityCheck    = flag.Bool("enable-disk-capacity-check", consts.EnableDiskCapacityCheck, "boolean flag to enable volume capacity check in CreateVolume")
	kubeClientQPS              = flag.Int("kube-client-qps", consts.KubeClientQPS, "QPS for the rest client. Defaults to 15.")
	vmssCacheTTLInSeconds      = flag.Int64("vmss-cache-ttl-seconds", consts.VMSSCacheTTLInSeconds, "vmss cache TTL in seconds (600 by default)")
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

	getDriverConfig()
	exportMetrics()
	handle()
	os.Exit(0)
}

func getDriverConfig() {
	// Read config file and convert to a driveConfig object if config path is given
	if *driverConfigPath != "" {
		yamlFile, err := ioutil.ReadFile(*driverConfigPath)
		if err != nil {
			klog.Fatalf("failed to get the driver config, error: %v", err)
		}

		err = yaml.Unmarshal(yamlFile, driverConfig)
		if err != nil {
			klog.Fatalf("failed to unmarshal the driver config, error: %v", err)
		}
	}

	driverConfig.NodeConfig.NodeID = *nodeID

	// Mark the command-line parameters that have been given by user to 1
	flag.Visit(func(f *flag.Flag) {
		if _, ok := consts.CommandLineParams[f.Name]; ok {
			consts.CommandLineParams[f.Name] = 1
		}
	})

	// Initialize driverConfig object from command-line parameters (or default values) if not set from config file
	// Mark the command-line parameters that have been given and used to 2
	if driverConfig.Endpoint == "" {
		driverConfig.Endpoint = *endpoint
		if consts.CommandLineParams["endpoint"] == 1 {
			consts.CommandLineParams["endpoint"] = 2
		}
	}
	if driverConfig.MetricsAddress == "" {
		driverConfig.MetricsAddress = *metricsAddress
		if consts.CommandLineParams["metrics-address"] == 1 {
			consts.CommandLineParams["metrics-address"] = 2
		}
	}
	if driverConfig.DriverName == "" {
		driverConfig.DriverName = *driverName
		if consts.CommandLineParams["drivername"] == 1 {
			consts.CommandLineParams["drivername"] = 2
		}
	}
	if driverConfig.ControllerConfig.DisableAVSetNodes == nil {
		driverConfig.ControllerConfig.DisableAVSetNodes = disableAVSetNodes
		if consts.CommandLineParams["disable-avset-nodes"] == 1 {
			consts.CommandLineParams["disable-avset-nodes"] = 2
		}
	}
	if driverConfig.ControllerConfig.VMType == "" {
		driverConfig.Endpoint = *endpoint
		if consts.CommandLineParams["endpoint"] == 1 {
			consts.CommandLineParams["endpoint"] = 2
		}
	}
	if driverConfig.ControllerConfig.EnableDiskOnlineResize == nil {
		driverConfig.ControllerConfig.VMType = *vmType
		if consts.CommandLineParams["vm-type"] == 1 {
			consts.CommandLineParams["vm-type"] = 2
		}
	}
	if driverConfig.ControllerConfig.EnableAsyncAttach == nil {
		driverConfig.ControllerConfig.EnableAsyncAttach = enableAsyncAttach
		if consts.CommandLineParams["enable-async-attach"] == 1 {
			consts.CommandLineParams["enable-async-attach"] = 2
		}
	}
	if driverConfig.ControllerConfig.EnableListVolumes == nil {
		driverConfig.ControllerConfig.EnableListVolumes = enableListVolumes
		if consts.CommandLineParams["enable-list-volumes"] == 1 {
			consts.CommandLineParams["enable-list-volumes"] = 2
		}
	}
	if driverConfig.ControllerConfig.EnableListSnapshots == nil {
		driverConfig.ControllerConfig.EnableListSnapshots = enableListSnapshots
		if consts.CommandLineParams["enable-list-snapshots"] == 1 {
			consts.CommandLineParams["enable-list-snapshots"] = 2
		}
	}
	if driverConfig.ControllerConfig.EnableDiskCapacityCheck == nil {
		driverConfig.ControllerConfig.EnableDiskCapacityCheck = enableDiskCapacityCheck
		if consts.CommandLineParams["enable-disk-capacity-check"] == 1 {
			consts.CommandLineParams["enable-disk-capacity-check"] = 2
		}
	}
	if driverConfig.NodeConfig.VolumeAttachLimit == nil {
		driverConfig.NodeConfig.VolumeAttachLimit = volumeAttachLimit
		if consts.CommandLineParams["volume-attach-limit"] == 1 {
			consts.CommandLineParams["volume-attach-limit"] = 2
		}
	}
	if driverConfig.NodeConfig.SupportZone == nil {
		driverConfig.NodeConfig.SupportZone = supportZone
		if consts.CommandLineParams["support-zone"] == 1 {
			consts.CommandLineParams["support-zone"] = 2
		}
	}
	if driverConfig.NodeConfig.EnablePerfOptimization == nil {
		driverConfig.NodeConfig.EnablePerfOptimization = enablePerfOptimization
		if consts.CommandLineParams["enable-perf-optimization"] == 1 {
			consts.CommandLineParams["enable-perf-optimization"] = 2
		}
	}
	if driverConfig.NodeConfig.UseCSIProxyGAInterface == nil {
		driverConfig.NodeConfig.UseCSIProxyGAInterface = useCSIProxyGAInterface
		if consts.CommandLineParams["use-csiproxy-ga-interface"] == 1 {
			consts.CommandLineParams["use-csiproxy-ga-interface"] = 2
		}
	}
	if driverConfig.NodeConfig.GetNodeInfoFromLabels == nil {
		driverConfig.NodeConfig.GetNodeInfoFromLabels = getNodeInfoFromLabels
		if consts.CommandLineParams["get-node-info-from-labels"] == 1 {
			consts.CommandLineParams["get-node-info-from-labels"] = 2
		}
	}
	if driverConfig.CloudConfig.SecretName == "" {
		driverConfig.CloudConfig.SecretName = *cloudConfigSecretName
		if consts.CommandLineParams["cloud-config-secret-name"] == 1 {
			consts.CommandLineParams["cloud-config-secret-name"] = 2
		}
	}
	if driverConfig.CloudConfig.SecretNamespace == "" {
		driverConfig.CloudConfig.SecretNamespace = *cloudConfigSecretNamespace
		if consts.CommandLineParams["cloud-config-secret-namespace"] == 1 {
			consts.CommandLineParams["cloud-config-secret-namespace"] = 2
		}
	}
	if driverConfig.CloudConfig.CustomUserAgent == "" {
		driverConfig.CloudConfig.CustomUserAgent = *customUserAgent
		if consts.CommandLineParams["custom-user-agent"] == 1 {
			consts.CommandLineParams["custom-user-agent"] = 2
		}
	}
	if driverConfig.CloudConfig.UserAgentSuffix == "" {
		driverConfig.CloudConfig.UserAgentSuffix = *userAgentSuffix
		if consts.CommandLineParams["user-agent-suffix"] == 1 {
			consts.CommandLineParams["user-agent-suffix"] = 2
		}
	}
	if driverConfig.CloudConfig.AllowEmptyCloudConfig == nil {
		driverConfig.CloudConfig.AllowEmptyCloudConfig = allowEmptyCloudConfig
		if consts.CommandLineParams["allow-empty-cloud-config"] == 1 {
			consts.CommandLineParams["allow-empty-cloud-config"] = 2
		}
	}
	if driverConfig.CloudConfig.VMSSCacheTTLInSeconds == nil {
		driverConfig.CloudConfig.VMSSCacheTTLInSeconds = vmssCacheTTLInSeconds
		if consts.CommandLineParams["vmss-cache-ttl-seconds"] == 1 {
			consts.CommandLineParams["vmss-cache-ttl-seconds"] = 2
		}
	}
	if driverConfig.ClientConfig.Kubeconfig == "" {
		driverConfig.ClientConfig.Kubeconfig = *kubeconfig
		if consts.CommandLineParams["kubeconfig"] == 1 {
			consts.CommandLineParams["kubeconfig"] = 2
		}
	}
	if driverConfig.ClientConfig.KubeClientQPS == nil {
		driverConfig.ClientConfig.KubeClientQPS = kubeClientQPS
		if consts.CommandLineParams["kube-client-qps"] == 1 {
			consts.CommandLineParams["kube-client-qps"] = 2
		}
	}

	if driverConfig == (azdiskv1beta2.AzDiskDriverConfiguration{}) {
		klog.Fatal("failed to initialize the driverConfig object")
	}
}

func handle() {
	driver := azuredisk.NewDriver(&driverConfig)
	if driver == nil {
		klog.Fatalln("Failed to initialize azuredisk CSI Driver")
	}
	testingMock := false
	driver.Run(driverConfig.Endpoint, driverConfig.ClientConfig.Kubeconfig, *driverConfig.ControllerConfig.DisableAVSetNodes, testingMock)
}

func exportMetrics() {
	l, err := net.Listen("tcp", driverConfig.MetricsAddress)

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
