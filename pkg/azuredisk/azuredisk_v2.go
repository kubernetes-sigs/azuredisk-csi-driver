//go:build azurediskv2
// +build azurediskv2

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

package azuredisk

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiRuntime "k8s.io/apimachinery/pkg/runtime"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	klogv1 "k8s.io/klog"
	"k8s.io/klog/klogr"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/volume/util/hostutil"

	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	azuredisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller"
	csicommon "sigs.k8s.io/azuredisk-csi-driver/pkg/csi-common"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/provisioner"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var isControllerPlugin = flag.Bool("is-controller-plugin", false, "Boolean flag to indicate this instance is running as controller.")
var isNodePlugin = flag.Bool("is-node-plugin", false, "Boolean flag to indicate this instance is running as node daemon.")
var driverObjectNamespace = flag.String("driver-object-namespace", consts.AzureDiskCrdNamespace, "namespace where driver related custom resources are created.")
var heartbeatFrequencyInSec = flag.Int("heartbeat-frequency-in-sec", 30, "Frequency in seconds at which node driver sends heartbeat.")
var controllerLeaseDurationInSec = flag.Int("lease-duration-in-sec", 15, "The duration that non-leader candidates will wait to force acquire leadership")
var controllerLeaseRenewDeadlineInSec = flag.Int("lease-renew-deadline-in-sec", 10, "The duration that the acting controlplane will retry refreshing leadership before giving up.")
var controllerLeaseRetryPeriodInSec = flag.Int("lease-retry-period-in-sec", 2, "The duration the LeaderElector clients should wait between tries of actions.")
var isTestRun = flag.Bool("is-test-run", false, "Boolean flag to indicate whether this instance is being used for sanity or integration tests")

// OutputCallDepth is the stack depth where we can find the origin of this call
const OutputCallDepth = 6

// DefaultPrefixLength is the length of the log prefix that we have to strip out
// when logging klogv1 to klogv2
const DefaultPrefixLength = 30

// DriverV2 implements all interfaces of CSI drivers
type DriverV2 struct {
	DriverCore
	nodeProvisioner                   NodeProvisioner
	cloudProvisioner                  controller.CloudProvisioner
	crdProvisioner                    CrdProvisioner
	volumeLocks                       *volumehelper.VolumeLocks
	objectNamespace                   string
	nodePartition                     string
	controllerPartition               string
	heartbeatFrequencyInSec           int
	controllerLeaseDurationInSec      int
	controllerLeaseRenewDeadlineInSec int
	controllerLeaseRetryPeriodInSec   int
	kubeConfig                        *rest.Config
	kubeClient                        *clientset.Clientset
	deviceChecker                     deviceChecker
}

// NewDriver creates a driver object.
func NewDriver(options *DriverOptions) CSIDriver {
	return newDriverV2(options, *driverObjectNamespace, "default", "default", *heartbeatFrequencyInSec, *controllerLeaseDurationInSec, *controllerLeaseRenewDeadlineInSec, *controllerLeaseRetryPeriodInSec)
}

// newDriverV2 Creates a NewCSIDriver object. Assumes vendor version is equal to driver version &
// does not support optional driver plugin info manifest field. Refer to CSI spec for more details.
func newDriverV2(options *DriverOptions,
	driverObjectNamespace string,
	nodePartition string,
	controllerPartition string,
	heartbeatFrequency int,
	leaseDurationInSec int,
	leaseRenewDeadlineInSec int,
	leaseRetryPeriodInSec int) *DriverV2 {

	klog.Warning("Using DriverV2")
	driver := DriverV2{}
	driver.Name = options.DriverName
	driver.Version = driverVersion
	driver.objectNamespace = driverObjectNamespace
	driver.nodePartition = nodePartition
	driver.controllerPartition = controllerPartition
	driver.heartbeatFrequencyInSec = heartbeatFrequency
	driver.controllerLeaseDurationInSec = leaseDurationInSec
	driver.controllerLeaseRenewDeadlineInSec = leaseRenewDeadlineInSec
	driver.controllerLeaseRetryPeriodInSec = leaseRetryPeriodInSec
	driver.NodeID = options.NodeID
	driver.VolumeAttachLimit = options.VolumeAttachLimit
	driver.volumeLocks = volumehelper.NewVolumeLocks()
	driver.ready = make(chan struct{})
	driver.perfOptimizationEnabled = options.EnablePerfOptimization
	driver.cloudConfigSecretName = options.CloudConfigSecretName
	driver.cloudConfigSecretNamespace = options.CloudConfigSecretNamespace
	driver.customUserAgent = options.CustomUserAgent
	driver.userAgentSuffix = options.UserAgentSuffix
	driver.useCSIProxyGAInterface = options.UseCSIProxyGAInterface
	driver.enableDiskOnlineResize = options.EnableDiskOnlineResize
	driver.ioHandler = azureutils.NewOSIOHandler()
	driver.hostUtil = hostutil.NewHostUtil()
	driver.deviceChecker = deviceChecker{lock: sync.RWMutex{}, entry: nil}

	topologyKey = fmt.Sprintf("topology.%s/zone", driver.Name)
	return &driver
}

// Run driver initialization
func (d *DriverV2) Run(endpoint, kubeconfig string, disableAVSetNodes, testingMock bool) {
	versionMeta, err := GetVersionYAML(d.Name)
	if err != nil {
		klog.Fatalf("%v", err)
	}
	klog.Infof("\nDRIVER INFORMATION:\n-------------------\n%s\n\nStreaming logs below:", versionMeta)

	d.kubeConfig, err = azureutils.GetKubeConfig(kubeconfig)
	if err != nil || d.kubeConfig == nil {
		klog.Fatalf("failed to get kube config (%s), error: %v. Exiting application...", kubeconfig, err)
	}

	d.kubeClient, err = azureutils.GetKubeClient(kubeconfig)
	if err != nil || d.kubeClient == nil {
		klog.Fatalf("failed to get kubeclient with kubeconfig (%s), error: %v. Exiting application...", kubeconfig, err)
	}

	// d.crdProvisioner is set by NewFakeDriver for unit tests.
	if d.crdProvisioner == nil {
		d.crdProvisioner, err = provisioner.NewCrdProvisioner(d.kubeConfig, d.objectNamespace)
		if err != nil {
			klog.Fatalf("Failed to get crd provisioner. Error: %v", err)
		}
	}

	// d.cloudProvisioner is set by NewFakeDriver for unit tests.
	if d.cloudProvisioner == nil {
		userAgent := GetUserAgent(d.Name, d.customUserAgent, d.userAgentSuffix)
		klog.V(2).Infof("driver userAgent: %s", userAgent)

		d.cloudProvisioner, err = provisioner.NewCloudProvisioner(d.kubeClient, d.cloudConfigSecretName, d.cloudConfigSecretNamespace, d.getPerfOptimizationEnabled(), topologyKey, userAgent, d.enableDiskOnlineResize)
		if err != nil {
			klog.Fatalf("Failed to get controller provisioner. Error: %v", err)
		}
	}

	if d.NodeID == "" {
		// Disable UseInstanceMetadata for controller to mitigate a timeout issue using IMDS
		// https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/168
		klog.Infoln("disable UseInstanceMetadata for controller")
		d.cloudProvisioner.GetCloud().Config.UseInstanceMetadata = false
	}

	d.deviceHelper = optimization.NewSafeDeviceHelper()

	if d.getPerfOptimizationEnabled() {
		d.nodeInfo, err = optimization.NewNodeInfo(d.cloudProvisioner.GetCloud(), d.NodeID)
		if err != nil {
			klog.Errorf("Failed to get node info. Error: %v", err)
		}
	}

	// d.nodeProvisioner is set by NewFakeDriver for unit tests.
	if d.nodeProvisioner == nil {
		d.nodeProvisioner, err = provisioner.NewNodeProvisioner(d.useCSIProxyGAInterface)
		if err != nil {
			klog.Fatalf("Failed to get node provisioner. Error: %v", err)
		}
	}

	d.AddControllerServiceCapabilities(
		[]csi.ControllerServiceCapability_RPC_Type{
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
			csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
			csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
			csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS,
			csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
			csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
			csi.ControllerServiceCapability_RPC_LIST_VOLUMES,
			csi.ControllerServiceCapability_RPC_LIST_VOLUMES_PUBLISHED_NODES,
			csi.ControllerServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
		})
	d.AddVolumeCapabilityAccessModes(
		[]csi.VolumeCapability_AccessMode_Mode{
			csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER,
			csi.VolumeCapability_AccessMode_SINGLE_NODE_READER_ONLY,
			csi.VolumeCapability_AccessMode_SINGLE_NODE_SINGLE_WRITER,
			csi.VolumeCapability_AccessMode_SINGLE_NODE_MULTI_WRITER,
		})
	d.AddNodeServiceCapabilities([]csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
		csi.NodeServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
	})

	// cancel the context the controller manager will be running under, if receive SIGTERM or SIGINT
	ctx := context.Background()

	// Start the controllers if this is a controller plug-in
	if *isControllerPlugin {
		go d.StartControllersAndDieOnExit(ctx)
	}

	// Register the AzDriverNode
	if *isNodePlugin {
		d.RegisterAzDriverNodeOrDie(ctx)
	}

	// Start the CSI endpoint/server
	s := csicommon.NewNonBlockingGRPCServer()
	// Driver d acts as IdentityServer, ControllerServer and NodeServer
	s.Start(endpoint, d, d, d, testingMock)

	// Start sending hearbeat and mark node as ready
	if *isNodePlugin {
		go d.RunAzDriverNodeHeartbeatLoop(ctx)
	}

	// Signal that the driver is ready.
	d.signalReady()

	// Wait for the GRPC Server to exit
	s.Wait()
}

// StartControllersAndDieOnExit starts all the controllers for a certain object partition
func (d *DriverV2) StartControllersAndDieOnExit(ctx context.Context) {
	// Version of klogr used here has a dependency on klog v1
	// The klogr -> klogv1 dependency is chained so we can't update klogr to a newer version right now
	// Using below workaround to redirect klogv1 to use same log files as klogv2
	// More details @ https://github.com/kubernetes/klog/blob/master/examples/coexist_klog_v1_and_v2/coexist_klog_v1_and_v2.go
	var klogv1Flags flag.FlagSet
	klogv1.InitFlags(&klogv1Flags)
	klogv1Flags.Set("logtostderr", "false") // By default klog v1 logs to stderr, switch that off
	klogv1.SetOutputBySeverity("INFO", klogWriter{})
	klogv1.SetOutputBySeverity("ERROR", klogWriter{})
	klogv1.SetOutputBySeverity("WARNING", klogWriter{})
	klogv1.SetOutputBySeverity("FATAL", klogWriter{})
	log := klogr.New().WithName("AzDiskControllerManager").WithValues("namespace", d.objectNamespace).WithValues("partition", d.controllerPartition)

	leaseDuration := time.Duration(d.controllerLeaseDurationInSec) * time.Second
	renewDeadline := time.Duration(d.controllerLeaseRenewDeadlineInSec) * time.Second
	retryPeriod := time.Duration(d.controllerLeaseRetryPeriodInSec) * time.Second
	scheme := apiRuntime.NewScheme()
	clientgoscheme.AddToScheme(scheme)
	azuredisk.AddToScheme(scheme)

	// Setup a Manager
	klog.V(2).Info("Setting up controller manager")
	mgr, err := manager.New(d.kubeConfig, manager.Options{
		Scheme:                        scheme,
		Logger:                        log,
		LeaderElection:                true,
		LeaderElectionResourceLock:    resourcelock.LeasesResourceLock,
		LeaderElectionNamespace:       d.objectNamespace,
		LeaderElectionID:              d.controllerPartition,
		LeaseDuration:                 &leaseDuration,
		RenewDeadline:                 &renewDeadline,
		RetryPeriod:                   &retryPeriod,
		LeaderElectionReleaseOnCancel: true,
		MetricsBindAddress:            ":8090"})
	if err != nil {
		klog.Errorf("Unable to set up overall controller manager. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	sharedState := controller.NewSharedState(d.Name)

	// Setup a new controller to clean-up AzDriverNodes
	// objects for the nodes which get deleted
	klog.V(2).Info("Initializing AzDriverNode controller")
	_, err = controller.NewAzDriverNodeController(mgr, d.crdProvisioner.GetDiskClientSet(), d.objectNamespace, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize AzDriverNodeController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	klog.V(2).Info("Initializing AzVolumeAttachment controller")
	attachReconciler, err := controller.NewAttachDetachController(mgr, d.crdProvisioner.GetDiskClientSet(), d.kubeClient, d.objectNamespace, d.cloudProvisioner, d.crdProvisioner, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize AzVolumeAttachmentController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	klog.V(2).Info("Initializing Pod controller")
	podReconciler, err := controller.NewPodController(mgr, d.crdProvisioner.GetDiskClientSet(), d.kubeClient, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize PodController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	klog.V(2).Info("Initializing Replica controller")
	_, err = controller.NewReplicaController(mgr, d.crdProvisioner.GetDiskClientSet(), d.kubeClient, d.objectNamespace, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize ReplicaController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	klog.V(2).Info("Initializing AzVolume controller")
	azvReconciler, err := controller.NewAzVolumeController(mgr, d.crdProvisioner.GetDiskClientSet(), d.kubeClient, d.objectNamespace, d.cloudProvisioner, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize AzVolumeController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	klog.V(2).Info("Initializing PV controller")
	pvReconciler, err := controller.NewPVController(mgr, d.crdProvisioner.GetDiskClientSet(), d.kubeClient, d.objectNamespace, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize PVController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	// This goroutine is preserved for leader controller manager
	// Leader controller manager should recover CRI if possible and clean them up before exiting.
	go func() {
		<-mgr.Elected()
		// recover lost states if necessary
		klog.Infof("Elected as leader; initiating CRI recovery...")
		if err := azvReconciler.Recover(ctx); err != nil {
			klog.Warningf("Failed to recover AzVolume: %v", err)
		}
		if err := attachReconciler.Recover(ctx); err != nil {
			klog.Warningf("Failed to recover AzVolumeAttachments: %v.", err)
		}
		if err := pvReconciler.Recover(ctx); err != nil {
			klog.Warningf("Failed to restore shared claimToVolumeMap and volumeToClaimMap: %v.", err)
		}
		if err := podReconciler.Recover(ctx); err != nil {
			klog.Warningf("Failed to recover replica AzVolumeAttachments: %v.", err)
		}
	}()

	klog.V(2).Info("Starting controller manager")
	if err := mgr.Start(ctx); err != nil {
		klog.Errorf("Controller manager exited: %v", err)
		os.Exit(1)
	}
	// If manager exits, exit the application
	// as recommended for the processes doing
	// leader election
	klog.V(2).Info("Controller manager exited without an error. Exiting application...")
	os.Exit(0)
}

// RegisterAzDriverNodeOrDie creates custom resource for this driverNode
func (d *DriverV2) RegisterAzDriverNodeOrDie(ctx context.Context) {
	var node *v1.Node
	var err error
	if d.NodeID == "" {
		klog.Errorf("NodeID is nil, can not initialize AzDriverNode")
	}

	if !*isTestRun {
		klog.V(2).Infof("Registering AzDriverNode for node (%s)", d.NodeID)
		node, err := d.kubeClient.CoreV1().Nodes().Get(ctx, d.NodeID, metav1.GetOptions{})
		if err != nil || node == nil {
			klog.Errorf("Failed to get node (%s), error: %v", d.NodeID, err)
			err = errors.NewBadRequest("Failed to get node or node not found, can not register the plugin.")
		}
	}

	if err == nil {
		err = d.crdProvisioner.RegisterDriverNode(ctx, node, d.nodePartition, d.NodeID)
	}

	if err != nil {
		klog.Fatalf("Failed to register AzDriverNode, error: %v. Exiting process...", err)
	}
}

// RunAzDriverNodeHeartbeatLoop runs a loop to send heartbeat from the node
func (d *DriverV2) RunAzDriverNodeHeartbeatLoop(ctx context.Context) {

	var err error
	var cachedAzDriverNode *azuredisk.AzDriverNode
	azN := d.crdProvisioner.GetDiskClientSet().DiskV1alpha1().AzDriverNodes(d.objectNamespace)
	heartbeatFrequency := time.Duration(d.heartbeatFrequencyInSec) * time.Second
	klog.V(1).Infof("Starting heartbeat loop with frequency (%v)", heartbeatFrequency)
	for {

		// Check if we have a cached copy of the
		// If not then get it
		if cachedAzDriverNode == nil {
			klog.V(2).Info("Don't have current AzDriverNodeStatus. Getting current AzDriverNodes status.")
			cachedAzDriverNode, err = azN.Get(context.Background(), strings.ToLower(d.NodeID), metav1.GetOptions{})
			// If we are not able to get the AzDriverNode, just wait and retry
			// In heartbeat loop we don't try to create the AzDriverNode object
			// If the object is deleted, the heartbeat for the node will become stale
			// Scheduler will stop scheduling nodes here
			// An external process should take action to recover this node
			if err != nil || cachedAzDriverNode == nil {
				klog.Errorf("Failed to get AzDriverNode resource from the api server. Error: %v", err)
				time.Sleep(heartbeatFrequency)
				cachedAzDriverNode = nil

				continue
			}
		}

		// Send heartbeat
		azDriverNodeToUpdate := cachedAzDriverNode.DeepCopy()
		timestamp := time.Now().UnixNano()
		readyForAllocation := true
		statusMessage := "Driver node healthy."
		klog.V(2).Infof("Updating status for (%v)", azDriverNodeToUpdate)
		if azDriverNodeToUpdate.Status == nil {
			azDriverNodeToUpdate.Status = &azuredisk.AzDriverNodeStatus{}
		}
		azDriverNodeToUpdate.Status.ReadyForVolumeAllocation = &readyForAllocation
		azDriverNodeToUpdate.Status.LastHeartbeatTime = &timestamp
		azDriverNodeToUpdate.Status.StatusMessage = &statusMessage
		klog.V(2).Infof("Sending heartbeat ReadyForVolumeAllocation=(%v) LastHeartbeatTime=(%v)", *azDriverNodeToUpdate.Status.ReadyForVolumeAllocation, *azDriverNodeToUpdate.Status.LastHeartbeatTime)
		cachedAzDriverNode, err = azN.UpdateStatus(ctx, azDriverNodeToUpdate, metav1.UpdateOptions{})
		if err != nil {
			klog.Errorf("Failed to update heartbeat for AzDriverNode resource. Error: %v", err)
			cachedAzDriverNode = nil
		}

		// If context is cancelled just return
		select {
		case <-ctx.Done():
			klog.Errorf("Context cancelled, stopped sending heartbeat.")
			return
		default:
			// sleep
			time.Sleep(heartbeatFrequency)
			continue
		}
	}
}

// klogWriter is used in SetOutputBySeverity call below to redirect
// any calls to klogv1 to end up in klogv2
type klogWriter struct{}

func (kw klogWriter) Write(p []byte) (n int, err error) {
	if len(p) < DefaultPrefixLength {
		klog.InfoDepth(OutputCallDepth, string(p))
		return len(p), nil
	}
	if p[0] == 'I' {
		klog.InfoDepth(OutputCallDepth, string(p[DefaultPrefixLength:]))
	} else if p[0] == 'W' {
		klog.WarningDepth(OutputCallDepth, string(p[DefaultPrefixLength:]))
	} else if p[0] == 'E' {
		klog.ErrorDepth(OutputCallDepth, string(p[DefaultPrefixLength:]))
	} else if p[0] == 'F' {
		klog.FatalDepth(OutputCallDepth, string(p[DefaultPrefixLength:]))
	} else {
		klog.InfoDepth(OutputCallDepth, string(p[DefaultPrefixLength:]))
	}
	return len(p), nil
}

func (d *DriverV2) getVolumeLocks() *volumehelper.VolumeLocks {
	return d.volumeLocks
}

func (d *DriverV2) addControllerFinalizer(finalizers []string, finalizerToAdd string) []string {
	for _, finalizer := range finalizers {
		if finalizer == finalizerToAdd {
			return finalizers
		}
	}
	finalizers = append(finalizers, finalizerToAdd)
	return finalizers
}

func (d *DriverV2) removeControllerFinalizer(finalizers []string, finalizerToRemove string) []string {
	removed := []string{}
	for _, finalizer := range finalizers {
		if finalizer != finalizerToRemove {
			removed = append(removed, finalizer)
		}
	}
	return removed
}
