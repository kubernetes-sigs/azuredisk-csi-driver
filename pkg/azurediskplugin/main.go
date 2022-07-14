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
	"sigs.k8s.io/yaml"

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
	driverConfig     azdiskv1beta2.AzDiskDriverConfiguration
	driverConfigPath = flag.String("config", "", "The configuration path for the driver")
	nodeID           = flag.String("nodeid", "", "node id")
	version          = flag.Bool("version", false, "Print the version and exit.")
	// Deprecated command-line parameters
	endpoint                          = flag.String("endpoint", consts.DefaultEndpoint, "CSI endpoint")
	metricsAddress                    = flag.String("metrics-address", consts.DefaultMetricsAddress, "export the metrics")
	kubeconfig                        = flag.String("kubeconfig", consts.DefaultKubeconfig, "Absolute path to the kubeconfig file. Required only when running out of cluster.")
	driverName                        = flag.String("drivername", consts.DefaultDriverName, "name of the driver")
	volumeAttachLimit                 = flag.Int64("volume-attach-limit", consts.DefaultVolumeAttachLimit, "maximum number of attachable volumes per node")
	supportZone                       = flag.Bool("support-zone", consts.DefaultSupportZone, "boolean flag to get zone info in NodeGetInfo")
	getNodeInfoFromLabels             = flag.Bool("get-node-info-from-labels", consts.DefaultGetNodeInfoFromLabels, "boolean flag to get zone info from node labels in NodeGetInfo")
	disableAVSetNodes                 = flag.Bool("disable-avset-nodes", consts.DefaultDisableAVSetNodes, "disable DisableAvailabilitySetNodes in cloud config for controller")
	vmType                            = flag.String("vm-type", consts.DefaultVMType, "type of agent node. available values: vmss, standard")
	enablePerfOptimization            = flag.Bool("enable-perf-optimization", consts.DefaultEnablePerfOptimization, "boolean flag to enable disk perf optimization")
	cloudConfigSecretName             = flag.String("cloud-config-secret-name", consts.DefaultCloudConfigSecretName, "cloud config secret name")
	cloudConfigSecretNamespace        = flag.String("cloud-config-secret-namespace", consts.DefaultCloudConfigSecretNamespace, "cloud config secret namespace")
	customUserAgent                   = flag.String("custom-user-agent", consts.DefaultCustomUserAgent, "custom userAgent")
	userAgentSuffix                   = flag.String("user-agent-suffix", consts.DefaultUserAgentSuffix, "userAgent suffix")
	useCSIProxyGAInterface            = flag.Bool("use-csiproxy-ga-interface", consts.DefaultUseCSIProxyGAInterface, "boolean flag to enable csi-proxy GA interface on Windows")
	enableDiskOnlineResize            = flag.Bool("enable-disk-online-resize", consts.DefaultEnableDiskOnlineResize, "boolean flag to enable disk online resize")
	allowEmptyCloudConfig             = flag.Bool("allow-empty-cloud-config", consts.DefaultAllowEmptyCloudConfig, "Whether allow running driver without cloud config")
	enableAsyncAttach                 = flag.Bool("enable-async-attach", consts.DefaultEnableAsyncAttach, "boolean flag to enable async attach")
	enableListVolumes                 = flag.Bool("enable-list-volumes", consts.DefaultEnableListVolumes, "boolean flag to enable ListVolumes on controller")
	enableListSnapshots               = flag.Bool("enable-list-snapshots", consts.DefaultEnableListSnapshots, "boolean flag to enable ListSnapshots on controller")
	enableDiskCapacityCheck           = flag.Bool("enable-disk-capacity-check", consts.DefaultEnableDiskCapacityCheck, "boolean flag to enable volume capacity check in CreateVolume")
	kubeClientQPS                     = flag.Int("kube-client-qps", consts.DefaultKubeClientQPS, "QPS for the rest client. Defaults to 15.")
	vmssCacheTTLInSeconds             = flag.Int64("vmss-cache-ttl-seconds", consts.DefaultVMSSCacheTTLInSeconds, "vmss cache TTL in seconds (600 by default)")
	isControllerPlugin                = flag.Bool("is-controller-plugin", consts.DefaultIsControllerPlugin, "Boolean flag to indicate this instance is running as controller.")
	isNodePlugin                      = flag.Bool("is-node-plugin", consts.DefaultIsNodePlugin, "Boolean flag to indicate this instance is running as node daemon.")
	driverObjectNamespace             = flag.String("driver-object-namespace", consts.DefaultAzureDiskCrdNamespace, "The namespace where driver related custom resources are created.")
	heartbeatFrequencyInSec           = flag.Int("heartbeat-frequency-in-sec", consts.DefaultHeartbeatFrequencyInSec, "Frequency in seconds at which node driver sends heartbeat.")
	controllerLeaseDurationInSec      = flag.Int("lease-duration-in-sec", consts.DefaultControllerLeaseDurationInSec, "The duration that non-leader candidates will wait to force acquire leadership")
	controllerLeaseRenewDeadlineInSec = flag.Int("lease-renew-deadline-in-sec", consts.DefaultControllerLeaseRenewDeadlineInSec, "The duration that the acting controlplane will retry refreshing leadership before giving up.")
	controllerLeaseRetryPeriodInSec   = flag.Int("lease-retry-period-in-sec", consts.DefaultControllerLeaseRetryPeriodInSec, "The duration the LeaderElector clients should wait between tries of actions.")
	leaderElectionNamespace           = flag.String("leader-election-namespace", consts.ReleaseNamespace, "The leader election namespace for controller")
	nodePartition                     = flag.String("node-partition", consts.DefaultNodePartitionName, "The partition name for node plugin.")
	controllerPartition               = flag.String("controller-partition", consts.DefaultControllerPartitionName, "The partition name for controller plugin.")
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
	if *driverConfigPath != "" {
		// Initialize driveConfig object with default values
		driverConfig = azdiskv1beta2.AzDiskDriverConfiguration{
			ControllerConfig: azdiskv1beta2.ControllerConfiguration{
				DisableAVSetNodes:       consts.DefaultDisableAVSetNodes,
				VMType:                  consts.DefaultVMType,
				EnableDiskOnlineResize:  consts.DefaultEnableDiskOnlineResize,
				EnableAsyncAttach:       consts.DefaultEnableAsyncAttach,
				EnableListVolumes:       consts.DefaultEnableListVolumes,
				EnableListSnapshots:     consts.DefaultEnableListSnapshots,
				EnableDiskCapacityCheck: consts.DefaultEnableDiskCapacityCheck,
				Enabled:                 consts.DefaultIsControllerPlugin,
				LeaseDurationInSec:      consts.DefaultControllerLeaseDurationInSec,
				LeaseRenewDeadlineInSec: consts.DefaultControllerLeaseRenewDeadlineInSec,
				LeaseRetryPeriodInSec:   consts.DefaultControllerLeaseRetryPeriodInSec,
				LeaderElectionNamespace: consts.ReleaseNamespace,
				PartitionName:           consts.DefaultControllerPartitionName,
			},
			NodeConfig: azdiskv1beta2.NodeConfiguration{
				VolumeAttachLimit:       consts.DefaultVolumeAttachLimit,
				SupportZone:             consts.DefaultSupportZone,
				EnablePerfOptimization:  consts.DefaultEnablePerfOptimization,
				UseCSIProxyGAInterface:  consts.DefaultUseCSIProxyGAInterface,
				GetNodeInfoFromLabels:   consts.DefaultGetNodeInfoFromLabels,
				Enabled:                 consts.DefaultIsNodePlugin,
				HeartbeatFrequencyInSec: consts.DefaultHeartbeatFrequencyInSec,
				PartitionName:           consts.DefaultNodePartitionName,
			},
			CloudConfig: azdiskv1beta2.CloudConfiguration{
				SecretName:            consts.DefaultCloudConfigSecretName,
				SecretNamespace:       consts.DefaultCloudConfigSecretNamespace,
				CustomUserAgent:       consts.DefaultCustomUserAgent,
				UserAgentSuffix:       consts.DefaultUserAgentSuffix,
				AllowEmptyCloudConfig: consts.DefaultAllowEmptyCloudConfig,
				VMSSCacheTTLInSeconds: consts.DefaultVMSSCacheTTLInSeconds,
			},
			ClientConfig: azdiskv1beta2.ClientConfiguration{
				Kubeconfig:    consts.DefaultKubeconfig,
				KubeClientQPS: consts.DefaultKubeClientQPS,
			},
			ObjectNamespace: consts.DefaultAzureDiskCrdNamespace,
			Endpoint:        consts.DefaultEndpoint,
			MetricsAddress:  consts.DefaultMetricsAddress,
			DriverName:      consts.DefaultDriverName,
		}

		// Read yaml file
		yamlFile, err := os.ReadFile(*driverConfigPath)
		if err != nil {
			klog.Fatalf("failed to get the driver config, error: %v", err)
		}

		// Convert yaml to a driveConfig object
		err = yaml.Unmarshal(yamlFile, &driverConfig)
		if err != nil {
			klog.Fatalf("failed to unmarshal the driver config, error: %v", err)
		}

		// Emit warning log for using deprecated command-line parameters
		flag.Visit(func(f *flag.Flag) {
			if slices.Contains(consts.CommandLineParams, f.Name) {
				klog.Warningf("the command-line parameter %v is deprecated and overridden by --config parameter.", f.Name)
			}
		})
	} else {
		driverConfig.Endpoint = *endpoint
		driverConfig.MetricsAddress = *metricsAddress
		driverConfig.DriverName = *driverName
		driverConfig.ControllerConfig.DisableAVSetNodes = *disableAVSetNodes
		driverConfig.ControllerConfig.VMType = *vmType
		driverConfig.ControllerConfig.EnableDiskOnlineResize = *enableDiskOnlineResize
		driverConfig.ControllerConfig.EnableAsyncAttach = *enableAsyncAttach
		driverConfig.ControllerConfig.EnableListVolumes = *enableListVolumes
		driverConfig.ControllerConfig.EnableListSnapshots = *enableListSnapshots
		driverConfig.ControllerConfig.EnableDiskCapacityCheck = *enableDiskCapacityCheck
		driverConfig.NodeConfig.VolumeAttachLimit = *volumeAttachLimit
		driverConfig.NodeConfig.SupportZone = *supportZone
		driverConfig.NodeConfig.EnablePerfOptimization = *enablePerfOptimization
		driverConfig.NodeConfig.UseCSIProxyGAInterface = *useCSIProxyGAInterface
		driverConfig.NodeConfig.GetNodeInfoFromLabels = *getNodeInfoFromLabels
		driverConfig.CloudConfig.SecretName = *cloudConfigSecretName
		driverConfig.CloudConfig.SecretNamespace = *cloudConfigSecretNamespace
		driverConfig.CloudConfig.CustomUserAgent = *customUserAgent
		driverConfig.CloudConfig.UserAgentSuffix = *userAgentSuffix
		driverConfig.CloudConfig.AllowEmptyCloudConfig = *allowEmptyCloudConfig
		driverConfig.CloudConfig.VMSSCacheTTLInSeconds = *vmssCacheTTLInSeconds
		driverConfig.ClientConfig.Kubeconfig = *kubeconfig
		driverConfig.ClientConfig.KubeClientQPS = *kubeClientQPS
		driverConfig.ObjectNamespace = *driverObjectNamespace
		driverConfig.ControllerConfig.Enabled = *isControllerPlugin
		driverConfig.ControllerConfig.LeaseDurationInSec = *controllerLeaseDurationInSec
		driverConfig.ControllerConfig.LeaseRenewDeadlineInSec = *controllerLeaseRenewDeadlineInSec
		driverConfig.ControllerConfig.LeaseRetryPeriodInSec = *controllerLeaseRetryPeriodInSec
		driverConfig.ControllerConfig.LeaderElectionNamespace = *leaderElectionNamespace
		driverConfig.ControllerConfig.PartitionName = *controllerPartition
		driverConfig.NodeConfig.Enabled = *isNodePlugin
		driverConfig.NodeConfig.HeartbeatFrequencyInSec = *heartbeatFrequencyInSec
		driverConfig.NodeConfig.PartitionName = *nodePartition

		// Emit warning log for using deprecated command-line parameters
		flag.Visit(func(f *flag.Flag) {
			if slices.Contains(consts.CommandLineParams, f.Name) {
				klog.Warningf("the command-line parameter %v is deprecated in favor of using the --config parameter.", f.Name)
			}
		})
	}
	driverConfig.NodeConfig.NodeID = *nodeID

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
	driver.Run(driverConfig.Endpoint, driverConfig.ClientConfig.Kubeconfig, driverConfig.ControllerConfig.DisableAVSetNodes, testingMock)
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
