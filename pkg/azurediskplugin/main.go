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
	"sigs.k8s.io/azuredisk-csi-driver/pkg/profiler"
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
	driverConfigPath = flag.String("config", "", "The configuration path for the driver")
	nodeID           = flag.String("nodeid", "", "node id")
	version          = flag.Bool("version", false, "Print the version and exit.")
	// Deprecated command-line parameters
	endpoint                                 = flag.String("endpoint", consts.DefaultEndpoint, "CSI endpoint")
	metricsAddress                           = flag.String("metrics-address", consts.DefaultMetricsAddress, "export the metrics")
	profilerAddress                          = flag.String("profiler-address", consts.DefaultProfilerAddress, "The address to listen on for profiling data requests. If empty or omitted, no server is started. See https://pkg.go.dev/net/http/pprof for supported requests.")
	kubeconfig                               = flag.String("kubeconfig", consts.DefaultKubeconfig, "Absolute path to the kubeconfig file. Required only when running out of cluster.")
	driverName                               = flag.String("drivername", consts.DefaultDriverName, "name of the driver")
	volumeAttachLimit                        = flag.Int64("volume-attach-limit", consts.DefaultVolumeAttachLimit, "maximum number of attachable volumes per node")
	supportZone                              = flag.Bool("support-zone", consts.DefaultSupportZone, "boolean flag to get zone info in NodeGetInfo")
	getNodeInfoFromLabels                    = flag.Bool("get-node-info-from-labels", consts.DefaultGetNodeInfoFromLabels, "boolean flag to get zone info from node labels in NodeGetInfo")
	disableAVSetNodes                        = flag.Bool("disable-avset-nodes", consts.DefaultDisableAVSetNodes, "disable DisableAvailabilitySetNodes in cloud config for controller")
	vmType                                   = flag.String("vm-type", consts.DefaultVMType, "type of agent node. available values: vmss, standard")
	enablePerfOptimization                   = flag.Bool("enable-perf-optimization", consts.DefaultEnablePerfOptimization, "boolean flag to enable disk perf optimization")
	cloudConfigSecretName                    = flag.String("cloud-config-secret-name", consts.DefaultCloudConfigSecretName, "cloud config secret name")
	cloudConfigSecretNamespace               = flag.String("cloud-config-secret-namespace", consts.DefaultCloudConfigSecretNamespace, "cloud config secret namespace")
	customUserAgent                          = flag.String("custom-user-agent", consts.DefaultCustomUserAgent, "custom userAgent")
	userAgentSuffix                          = flag.String("user-agent-suffix", consts.DefaultUserAgentSuffix, "userAgent suffix")
	useCSIProxyGAInterface                   = flag.Bool("use-csiproxy-ga-interface", consts.DefaultUseCSIProxyGAInterface, "boolean flag to enable csi-proxy GA interface on Windows")
	enableDiskOnlineResize                   = flag.Bool("enable-disk-online-resize", consts.DefaultEnableDiskOnlineResize, "boolean flag to enable disk online resize")
	allowEmptyCloudConfig                    = flag.Bool("allow-empty-cloud-config", consts.DefaultAllowEmptyCloudConfig, "Whether allow running driver without cloud config")
	enableAsyncAttach                        = flag.Bool("enable-async-attach", consts.DefaultEnableAsyncAttach, "boolean flag to enable async attach")
	enableListVolumes                        = flag.Bool("enable-list-volumes", consts.DefaultEnableListVolumes, "boolean flag to enable ListVolumes on controller")
	enableListSnapshots                      = flag.Bool("enable-list-snapshots", consts.DefaultEnableListSnapshots, "boolean flag to enable ListSnapshots on controller")
	enableDiskCapacityCheck                  = flag.Bool("enable-disk-capacity-check", consts.DefaultEnableDiskCapacityCheck, "boolean flag to enable volume capacity check in CreateVolume")
	kubeClientQPS                            = flag.Float64("kube-client-qps", consts.DefaultKubeClientQPS, "QPS for the rest client.")
	kubeClientBurst                          = flag.Int("kube-client-burst", consts.DefaultKubeClientQPS, "Burst for the rest client.")
	disableUpdateCache                       = flag.Bool("disable-update-cache", false, "boolean flag to disable update cache during disk attach/detach")
	vmssCacheTTLInSeconds                    = flag.Int64("vmss-cache-ttl-seconds", consts.DefaultVMSSCacheTTLInSeconds, "vmss cache TTL in seconds")
	enableAzureClientAttachDetachRateLimiter = flag.Bool("enable-attach-detach-rate-limiter", consts.DefaultEnableAzureClientAttachDetachRateLimiter, "Azure client rate limiter for attach/detach operations")
	azureClientAttachDetachRateLimiterQPS    = flag.Float64("attach-detach-rate-limiter-qps", consts.DefaultAzureClientAttachDetachRateLimiterQPS, "QPS for azure client rate limiter for attach/detach operations")
	azureClientAttachDetachRateLimiterBucket = flag.Int("attach-detach-rate-limiter-bucket", consts.DefaultAzureClientAttachDetachRateLimiterBucket, "Bucket for azure client rate limiter for attach/detach operations")
	azureClientAttachDetachBatchInitialDelay = flag.Duration("attach-detach-batch-initial-delay", consts.DefaultAzureClientAttachDetachBatchInitialDelay, "Initial batch processing start delay for attach/detach operations. A value of zero indicates the default of 1s and a negative value disables the delay. Otherwise, the supplied delay is used.")
	isControllerPlugin                       = flag.Bool("is-controller-plugin", consts.DefaultIsControllerPlugin, "Boolean flag to indicate this instance is running as controller.")
	isNodePlugin                             = flag.Bool("is-node-plugin", consts.DefaultIsNodePlugin, "Boolean flag to indicate this instance is running as node daemon.")
	driverObjectNamespace                    = flag.String("driver-object-namespace", consts.DefaultAzureDiskCrdNamespace, "The namespace where driver related custom resources are created.")
	heartbeatFrequencyInSec                  = flag.Int("heartbeat-frequency-in-sec", consts.DefaultHeartbeatFrequencyInSec, "Frequency in seconds at which node driver sends heartbeat.")
	controllerLeaseDurationInSec             = flag.Int("lease-duration-in-sec", consts.DefaultControllerLeaseDurationInSec, "The duration that non-leader candidates will wait to force acquire leadership")
	controllerLeaseRenewDeadlineInSec        = flag.Int("lease-renew-deadline-in-sec", consts.DefaultControllerLeaseRenewDeadlineInSec, "The duration that the acting controlplane will retry refreshing leadership before giving up.")
	controllerLeaseRetryPeriodInSec          = flag.Int("lease-retry-period-in-sec", consts.DefaultControllerLeaseRetryPeriodInSec, "The duration the LeaderElector clients should wait between tries of actions.")
	leaderElectionNamespace                  = flag.String("leader-election-namespace", consts.ReleaseNamespace, "The leader election namespace for controller")
	nodePartition                            = flag.String("node-partition", consts.DefaultNodePartitionName, "The partition name for node plugin.")
	controllerPartition                      = flag.String("controller-partition", consts.DefaultControllerPartitionName, "The partition name for controller plugin.")
	workerThreads                            = flag.Int("worker-threads", consts.DefaultWorkerThreads, "The number of worker thread per custom resource controller (AzVolume, attach/detach and replica controllers).")
	waitForLunEnabled                        = flag.Bool("wait-for-lun-enabled", consts.DefaultWaitForLunEnabled, "boolean field to enable waiting for lun in PublishVolume")
	enableTrafficManager                     = flag.Bool("enable-traffic-manager", false, "boolean flag to enable traffic manager")
	trafficManagerPort                       = flag.Int64("traffic-manager-port", 7788, "default traffic manager port")
	replicaVolumeAttachRetryLimit            = flag.Int("volume-attach-retry-limit", consts.DefaultReplicaVolumeAttachRetryLimit, "The maximum number of retries for creating a replica attachment.")
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

	handle()
	os.Exit(0)
}

func handle() {
	driverConfig := getDriverConfig()

	if len(driverConfig.ProfilerAddress) > 0 {
		go profiler.ListenAndServeOrDie(driverConfig.ProfilerAddress)
	}

	exportMetrics(driverConfig)
	driver := azuredisk.NewDriver(driverConfig)
	if driver == nil {
		klog.Fatalln("Failed to initialize azuredisk CSI Driver")
	}
	testingMock := false
	driver.Run(driverConfig.Endpoint, driverConfig.ClientConfig.Kubeconfig, driverConfig.ControllerConfig.DisableAVSetNodes, testingMock)
}

func getDriverConfig() *azdiskv1beta2.AzDiskDriverConfiguration {
	var driverConfig *azdiskv1beta2.AzDiskDriverConfiguration

	if *driverConfigPath != "" {
		// Initialize driveConfig object with default values
		driverConfig = azuredisk.NewDefaultDriverConfig()

		// Read yaml file
		yamlFile, err := os.ReadFile(*driverConfigPath)
		if err != nil {
			klog.Fatalf("failed to get the driver config, error: %v", err)
		}

		// Convert yaml to a driveConfig object
		err = yaml.Unmarshal(yamlFile, driverConfig)
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
		driverConfig = &azdiskv1beta2.AzDiskDriverConfiguration{
			ControllerConfig: azdiskv1beta2.ControllerConfiguration{
				DisableAVSetNodes:             *disableAVSetNodes,
				VMType:                        *vmType,
				EnableDiskOnlineResize:        *enableDiskOnlineResize,
				EnableAsyncAttach:             *enableAsyncAttach,
				EnableListVolumes:             *enableListVolumes,
				EnableListSnapshots:           *enableListSnapshots,
				EnableDiskCapacityCheck:       *enableDiskCapacityCheck,
				Enabled:                       *isControllerPlugin,
				LeaseDurationInSec:            *controllerLeaseDurationInSec,
				LeaseRenewDeadlineInSec:       *controllerLeaseRenewDeadlineInSec,
				LeaseRetryPeriodInSec:         *controllerLeaseRetryPeriodInSec,
				LeaderElectionNamespace:       *leaderElectionNamespace,
				PartitionName:                 *controllerPartition,
				WorkerThreads:                 *workerThreads,
				WaitForLunEnabled:             *waitForLunEnabled,
				ReplicaVolumeAttachRetryLimit: *replicaVolumeAttachRetryLimit,
			},
			NodeConfig: azdiskv1beta2.NodeConfiguration{
				VolumeAttachLimit:       *volumeAttachLimit,
				SupportZone:             *supportZone,
				EnablePerfOptimization:  *enablePerfOptimization,
				UseCSIProxyGAInterface:  *useCSIProxyGAInterface,
				GetNodeInfoFromLabels:   *getNodeInfoFromLabels,
				Enabled:                 *isNodePlugin,
				HeartbeatFrequencyInSec: *heartbeatFrequencyInSec,
				PartitionName:           *nodePartition,
			},
			CloudConfig: azdiskv1beta2.CloudConfiguration{
				SecretName:                                       *cloudConfigSecretName,
				SecretNamespace:                                  *cloudConfigSecretNamespace,
				CustomUserAgent:                                  *customUserAgent,
				UserAgentSuffix:                                  *userAgentSuffix,
				EnableTrafficManager:                             *enableTrafficManager,
				TrafficManagerPort:                               *trafficManagerPort,
				AllowEmptyCloudConfig:                            *allowEmptyCloudConfig,
				DisableUpdateCache:                               *disableUpdateCache,
				VMSSCacheTTLInSeconds:                            *vmssCacheTTLInSeconds,
				EnableAzureClientAttachDetachRateLimiter:         *enableAzureClientAttachDetachRateLimiter,
				AzureClientAttachDetachRateLimiterQPS:            float32(*azureClientAttachDetachRateLimiterQPS),
				AzureClientAttachDetachRateLimiterBucket:         *azureClientAttachDetachRateLimiterBucket,
				AzureClientAttachDetachBatchInitialDelayInMillis: int(azureClientAttachDetachBatchInitialDelay.Milliseconds()),
			},
			ClientConfig: azdiskv1beta2.ClientConfiguration{
				Kubeconfig:      *kubeconfig,
				KubeClientQPS:   float32(*kubeClientQPS),
				KubeClientBurst: *kubeClientBurst,
			},
			ObjectNamespace: *driverObjectNamespace,
			Endpoint:        *endpoint,
			MetricsAddress:  *metricsAddress,
			DriverName:      *driverName,
			ProfilerAddress: *profilerAddress,
		}

		// Emit warning log for using deprecated command-line parameters
		flag.Visit(func(f *flag.Flag) {
			if slices.Contains(consts.CommandLineParams, f.Name) {
				klog.Warningf("the command-line parameter %v is deprecated in favor of using the --config parameter.", f.Name)
			}
		})
	}

	driverConfig.NodeConfig.NodeID = *nodeID
	if driverConfig == nil || *driverConfig == (azdiskv1beta2.AzDiskDriverConfiguration{}) {
		klog.Fatal("failed to initialize the driverConfig object")
	}

	return driverConfig
}

func exportMetrics(driverConfig *azdiskv1beta2.AzDiskDriverConfiguration) {
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
