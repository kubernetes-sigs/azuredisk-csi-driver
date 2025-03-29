/*
Copyright 2023 The Kubernetes Authors.

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
	"flag"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

// DriverOptions defines driver parameters specified in driver deployment
type DriverOptions struct {
	// Common options
	NodeID                     string
	DriverName                 string
	VolumeAttachLimit          int64
	ReservedDataDiskSlotNum    int64
	EnablePerfOptimization     bool
	CloudConfigSecretName      string
	CloudConfigSecretNamespace string
	CustomUserAgent            string
	UserAgentSuffix            string
	UseCSIProxyGAInterface     bool
	EnableOtelTracing          bool

	//only used in v1
	EnableDiskOnlineResize            bool
	AllowEmptyCloudConfig             bool
	EnableListVolumes                 bool
	EnableListSnapshots               bool
	SupportZone                       bool
	GetNodeInfoFromLabels             bool
	EnableDiskCapacityCheck           bool
	DisableUpdateCache                bool
	EnableTrafficManager              bool
	TrafficManagerPort                int64
	AttachDetachInitialDelayInMs      int64
	VMSSCacheTTLInSeconds             int64
	VolStatsCacheExpireInMinutes      int64
	GetDiskTimeoutInSeconds           int64
	VMType                            string
	EnableWindowsHostProcess          bool
	ListDisksUsingWinCIM              bool
	GetNodeIDFromIMDS                 bool
	WaitForSnapshotReady              bool
	CheckDiskLUNCollision             bool
	ForceDetachBackoff                bool
	Kubeconfig                        string
	Endpoint                          string
	DisableAVSetNodes                 bool
	RemoveNotReadyTaint               bool
	TaintRemovalInitialDelayInSeconds int64
	MaxConcurrentFormat               int64
	ConcurrentFormatTimeout           int64
	GoMaxProcs                        int64
}

func (o *DriverOptions) AddFlags() *flag.FlagSet {
	if o == nil {
		return nil
	}
	fs := flag.NewFlagSet("", flag.ExitOnError)
	fs.StringVar(&o.NodeID, "nodeid", "", "node id")
	fs.StringVar(&o.DriverName, "drivername", consts.DefaultDriverName, "name of the driver")
	fs.Int64Var(&o.VolumeAttachLimit, "volume-attach-limit", -1, "maximum number of attachable volumes per node")
	fs.Int64Var(&o.ReservedDataDiskSlotNum, "reserved-data-disk-slot-num", 0, "reserved data disk slot number per node")
	fs.BoolVar(&o.EnablePerfOptimization, "enable-perf-optimization", false, "boolean flag to enable disk perf optimization")
	fs.StringVar(&o.CloudConfigSecretName, "cloud-config-secret-name", "azure-cloud-provider", "cloud config secret name")
	fs.StringVar(&o.CloudConfigSecretNamespace, "cloud-config-secret-namespace", "kube-system", "cloud config secret namespace")
	fs.StringVar(&o.CustomUserAgent, "custom-user-agent", "", "custom userAgent")
	fs.StringVar(&o.UserAgentSuffix, "user-agent-suffix", "", "userAgent suffix")
	fs.BoolVar(&o.UseCSIProxyGAInterface, "use-csiproxy-ga-interface", true, "boolean flag to enable csi-proxy GA interface on Windows")
	fs.BoolVar(&o.EnableOtelTracing, "enable-otel-tracing", false, "If set, enable opentelemetry tracing for the driver. The tracing is disabled by default. Configure the exporter endpoint with OTEL_EXPORTER_OTLP_ENDPOINT and other env variables, see https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/#general-sdk-configuration.")
	//only used in v1
	fs.BoolVar(&o.EnableDiskOnlineResize, "enable-disk-online-resize", true, "boolean flag to enable disk online resize")
	fs.BoolVar(&o.AllowEmptyCloudConfig, "allow-empty-cloud-config", true, "Whether allow running driver without cloud config")
	fs.BoolVar(&o.EnableListVolumes, "enable-list-volumes", false, "boolean flag to enable ListVolumes on controller")
	fs.BoolVar(&o.EnableListSnapshots, "enable-list-snapshots", false, "boolean flag to enable ListSnapshots on controller")
	fs.BoolVar(&o.SupportZone, "support-zone", true, "boolean flag to get zone info in NodeGetInfo")
	fs.BoolVar(&o.GetNodeInfoFromLabels, "get-node-info-from-labels", false, "boolean flag to get zone info from node labels in NodeGetInfo")
	fs.BoolVar(&o.EnableDiskCapacityCheck, "enable-disk-capacity-check", false, "boolean flag to enable volume capacity check in CreateVolume")
	fs.BoolVar(&o.DisableUpdateCache, "disable-update-cache", false, "boolean flag to disable update cache during disk attach/detach")
	fs.BoolVar(&o.EnableTrafficManager, "enable-traffic-manager", false, "boolean flag to enable traffic manager")
	fs.Int64Var(&o.TrafficManagerPort, "traffic-manager-port", 7788, "default traffic manager port")
	fs.Int64Var(&o.AttachDetachInitialDelayInMs, "attach-detach-initial-delay-ms", 1000, "initial delay in milliseconds for batch disk attach/detach")
	fs.Int64Var(&o.VMSSCacheTTLInSeconds, "vmss-cache-ttl-seconds", -1, "vmss cache TTL in seconds (600 by default)")
	fs.Int64Var(&o.VolStatsCacheExpireInMinutes, "vol-stats-cache-expire-in-minutes", 10, "The cache expire time in minutes for volume stats cache")
	fs.Int64Var(&o.GetDiskTimeoutInSeconds, "get-disk-timeout-seconds", 15, "The timeout in seconds for getting disk")
	fs.StringVar(&o.VMType, "vm-type", "", "type of agent node. available values: vmss, standard")
	fs.BoolVar(&o.EnableWindowsHostProcess, "enable-windows-host-process", false, "enable windows host process")
	fs.BoolVar(&o.ListDisksUsingWinCIM, "list-disks-using-win-cim", true, "list disks using CIM API on Windows")
	fs.BoolVar(&o.GetNodeIDFromIMDS, "get-nodeid-from-imds", false, "boolean flag to get NodeID from IMDS")
	fs.BoolVar(&o.WaitForSnapshotReady, "wait-for-snapshot-ready", true, "boolean flag to wait for snapshot ready when creating snapshot in same region")
	fs.BoolVar(&o.CheckDiskLUNCollision, "check-disk-lun-collision", true, "boolean flag to check disk lun collisio before attaching disk")
	fs.BoolVar(&o.ForceDetachBackoff, "force-detach-backoff", true, "boolean flag to force detach in disk detach backoff")
	fs.StringVar(&o.Kubeconfig, "kubeconfig", "", "Absolute path to the kubeconfig file. Required only when running out of cluster.")
	fs.BoolVar(&o.DisableAVSetNodes, "disable-avset-nodes", false, "disable DisableAvailabilitySetNodes in cloud config for controller")
	fs.BoolVar(&o.RemoveNotReadyTaint, "remove-not-ready-taint", true, "remove NotReady taint from node when node is ready")
	fs.Int64Var(&o.TaintRemovalInitialDelayInSeconds, "taint-removal-initial-delay-seconds", 30, "initial delay in seconds for taint removal")
	fs.StringVar(&o.Endpoint, "endpoint", "unix://tmp/csi.sock", "CSI endpoint")
	fs.Int64Var(&o.MaxConcurrentFormat, "max-concurrent-format", 2, "maximum number of concurrent format exec calls")
	fs.Int64Var(&o.ConcurrentFormatTimeout, "concurrent-format-timeout", 300, "maximum time in seconds duration of a format operation before its concurrency token is released")
	fs.Int64Var(&o.GoMaxProcs, "max-procs", 2, "maximum number of CPUs that can be executing simultaneously in golang runtime")
	return fs
}
