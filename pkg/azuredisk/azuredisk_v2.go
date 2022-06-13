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
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	"k8s.io/kubernetes/pkg/volume/util/hostutil"

	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller"
	csicommon "sigs.k8s.io/azuredisk-csi-driver/pkg/csi-common"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/optimization"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/provisioner"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"
	azurecloudconsts "sigs.k8s.io/cloud-provider-azure/pkg/consts"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var isControllerPlugin = flag.Bool("is-controller-plugin", false, "Boolean flag to indicate this instance is running as controller.")
var isNodePlugin = flag.Bool("is-node-plugin", false, "Boolean flag to indicate this instance is running as node daemon.")
var driverObjectNamespace = flag.String("driver-object-namespace", consts.DefaultAzureDiskCrdNamespace, "The namespace where driver related custom resources are created.")
var heartbeatFrequencyInSec = flag.Int("heartbeat-frequency-in-sec", 30, "Frequency in seconds at which node driver sends heartbeat.")
var controllerLeaseDurationInSec = flag.Int("lease-duration-in-sec", 15, "The duration that non-leader candidates will wait to force acquire leadership")
var controllerLeaseRenewDeadlineInSec = flag.Int("lease-renew-deadline-in-sec", 10, "The duration that the acting controlplane will retry refreshing leadership before giving up.")
var controllerLeaseRetryPeriodInSec = flag.Int("lease-retry-period-in-sec", 2, "The duration the LeaderElector clients should wait between tries of actions.")
var leaderElectionNamespace = flag.String("leader-election-namespace", consts.ReleaseNamespace, "The leader election namespace for controller")
var nodePartition = flag.String("node-partition", consts.DefaultNodePartitionName, "The partition name for node plugin.")
var controllerPartition = flag.String("controller-partition", consts.DefaultControllerPartitionName, "The partition name for controller plugin.")
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
	leaderElectionNamespace           string
	kubeConfig                        *rest.Config
	kubeClient                        *clientset.Clientset
	deviceChecker                     *deviceChecker
	kubeClientQPS                     int
}

// NewDriver creates a driver object.
func NewDriver(options *DriverOptions) CSIDriver {
	return newDriverV2(options, *driverObjectNamespace, *nodePartition, *controllerPartition, *heartbeatFrequencyInSec, *controllerLeaseDurationInSec, *controllerLeaseRenewDeadlineInSec, *controllerLeaseRetryPeriodInSec, *leaderElectionNamespace)
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
	leaseRetryPeriodInSec int,
	leaderElectionNamespace string) *DriverV2 {

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
	driver.leaderElectionNamespace = leaderElectionNamespace
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
	driver.allowEmptyCloudConfig = options.AllowEmptyCloudConfig
	driver.enableAsyncAttach = options.EnableAsyncAttach
	driver.enableListVolumes = options.EnableListVolumes
	driver.enableListSnapshots = options.EnableListVolumes
	driver.supportZone = options.SupportZone
	driver.getNodeInfoFromLabels = options.GetNodeInfoFromLabels
	driver.enableDiskCapacityCheck = options.EnableDiskCapacityCheck
	driver.volumeLocks = volumehelper.NewVolumeLocks()
	driver.ioHandler = azureutils.NewOSIOHandler()
	driver.hostUtil = hostutil.NewHostUtil()
	driver.deviceChecker = &deviceChecker{lock: sync.RWMutex{}, entry: nil}
	driver.kubeClientQPS = options.RestClientQPS

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

	d.kubeConfig.QPS = float32(d.kubeClientQPS)
	d.kubeConfig.Burst = d.kubeClientQPS * 2
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

		d.cloudProvisioner, err = provisioner.NewCloudProvisioner(
			d.kubeClient,
			d.cloudConfigSecretName,
			d.cloudConfigSecretNamespace,
			d.getPerfOptimizationEnabled(),
			topologyKey,
			userAgent,
			d.enableDiskOnlineResize,
			d.allowEmptyCloudConfig,
			d.enableAsyncAttach,
		)
		if err != nil {
			klog.Fatalf("Failed to get controller provisioner. Error: %v", err)
		}
	}

	if d.vmType != "" {
		klog.V(2).Infof("override VMType(%s) in cloud config as %s", d.cloudProvisioner.GetCloud().VMType, d.vmType)
		d.cloudProvisioner.GetCloud().VMType = d.vmType
	}

	if d.NodeID == "" {
		// Disable UseInstanceMetadata for controller to mitigate a timeout issue using IMDS
		// https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/168
		klog.V(2).Infof("disable UseInstanceMetadata for controller")
		d.cloudProvisioner.GetCloud().Config.UseInstanceMetadata = false

		if d.cloudProvisioner.GetCloud().VMType == azurecloudconsts.VMTypeStandard && d.cloudProvisioner.GetCloud().DisableAvailabilitySetNodes {
			klog.V(2).Infof("set DisableAvailabilitySetNodes as false since VMType is %s", d.cloudProvisioner.GetCloud().VMType)
			d.cloudProvisioner.GetCloud().DisableAvailabilitySetNodes = false
		}

		if d.cloudProvisioner.GetCloud().VMType == azurecloudconsts.VMTypeVMSS && !d.cloudProvisioner.GetCloud().DisableAvailabilitySetNodes {
			if disableAVSetNodes {
				klog.V(2).Infof("DisableAvailabilitySetNodes for controller since current VMType is vmss")
				d.cloudProvisioner.GetCloud().DisableAvailabilitySetNodes = true
			} else {
				klog.Warningf("DisableAvailabilitySetNodes for controller is set as false while current VMType is vmss")
			}
		}
		klog.V(2).Infof("cloud: %s, location: %s, rg: %s, VMType: %s, PrimaryScaleSetName: %s, PrimaryAvailabilitySetName: %s, DisableAvailabilitySetNodes: %v", d.cloudProvisioner.GetCloud().Cloud, d.cloudProvisioner.GetCloud().Location, d.cloudProvisioner.GetCloud().ResourceGroup, d.cloudProvisioner.GetCloud().VMType, d.cloudProvisioner.GetCloud().PrimaryScaleSetName, d.cloudProvisioner.GetCloud().PrimaryAvailabilitySetName, d.cloudProvisioner.GetCloud().DisableAvailabilitySetNodes)
	}

	d.deviceHelper = optimization.NewSafeDeviceHelper()

	if d.getPerfOptimizationEnabled() {
		d.nodeInfo, err = optimization.NewNodeInfo(context.Background(), d.cloudProvisioner.GetCloud(), d.NodeID)
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

	controllerCap := []csi.ControllerServiceCapability_RPC_Type{
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_VOLUME,
		csi.ControllerServiceCapability_RPC_PUBLISH_UNPUBLISH_VOLUME,
		csi.ControllerServiceCapability_RPC_CREATE_DELETE_SNAPSHOT,
		csi.ControllerServiceCapability_RPC_CLONE_VOLUME,
		csi.ControllerServiceCapability_RPC_EXPAND_VOLUME,
		csi.ControllerServiceCapability_RPC_SINGLE_NODE_MULTI_WRITER,
	}
	if d.enableListVolumes {
		controllerCap = append(controllerCap, csi.ControllerServiceCapability_RPC_LIST_VOLUMES, csi.ControllerServiceCapability_RPC_LIST_VOLUMES_PUBLISHED_NODES)
	}
	if d.enableListSnapshots {
		controllerCap = append(controllerCap, csi.ControllerServiceCapability_RPC_LIST_SNAPSHOTS)
	}

	d.AddControllerServiceCapabilities(controllerCap)
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
	log := klogr.New().WithName("AzDiskControllerManager").WithValues("namespace", d.objectNamespace).WithValues("partition", d.controllerPartition)

	leaseDuration := time.Duration(d.controllerLeaseDurationInSec) * time.Second
	renewDeadline := time.Duration(d.controllerLeaseRenewDeadlineInSec) * time.Second
	retryPeriod := time.Duration(d.controllerLeaseRetryPeriodInSec) * time.Second
	scheme := apiRuntime.NewScheme()
	clientgoscheme.AddToScheme(scheme)
	azdiskv1beta2.AddToScheme(scheme)

	// Setup a Manager
	klog.V(2).Info("Setting up controller manager")
	mgr, err := manager.New(d.kubeConfig, manager.Options{
		Scheme:                        scheme,
		Logger:                        log,
		LeaderElection:                true,
		LeaderElectionResourceLock:    resourcelock.LeasesResourceLock,
		LeaderElectionNamespace:       d.leaderElectionNamespace,
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

	// Initialize the driver event recorder
	eventBroadcaster := record.NewBroadcaster()
	eventBroadcaster.StartRecordingToSink(&v1core.EventSinkImpl{Interface: d.kubeClient.CoreV1().Events("")})
	eventRecorder := eventBroadcaster.NewRecorder(clientgoscheme.Scheme, v1.EventSource{Component: consts.AzureDiskCSIDriverName})
	sharedState := controller.NewSharedState(d.Name, d.objectNamespace, topologyKey, eventRecorder, mgr.GetClient(), d.crdProvisioner.GetDiskClientSet(), d.kubeClient)

	// Setup a new controller to clean-up AzDriverNodes
	// objects for the nodes which get deleted
	klog.V(2).Info("Initializing AzDriverNode controller")
	_, err = controller.NewAzDriverNodeController(mgr, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize AzDriverNodeController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	klog.V(2).Info("Initializing AzVolumeAttachment controller")
	attachReconciler, err := controller.NewAttachDetachController(mgr, d.cloudProvisioner, d.crdProvisioner, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize AzVolumeAttachmentController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	klog.V(2).Info("Initializing Pod controller")
	podReconciler, err := controller.NewPodController(mgr, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize PodController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	klog.V(2).Info("Initializing Replica controller")
	_, err = controller.NewReplicaController(mgr, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize ReplicaController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	klog.V(2).Info("Initializing AzVolume controller")
	azvReconciler, err := controller.NewAzVolumeController(mgr, d.cloudProvisioner, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize AzVolumeController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	klog.V(2).Info("Initializing PV controller")
	pvReconciler, err := controller.NewPVController(mgr, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize PVController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}
	klog.V(2).Info("Initializing Node Availability controller")
	_, err = controller.NewNodeAvailabilityController(mgr, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize NodeAvailabilityController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}
	// This goroutine is preserved for leader controller manager
	// Leader controller manager should recover CRI if possible and clean them up before exiting.
	go func() {
		<-mgr.Elected()
		var errors []error
		ctx, w := workflow.New(ctx)
		defer func() { w.Finish(err) }()
		// recover lost states if necessary
		w.Logger().Infof("Elected as leader; initiating CRI recovery...")
		if err := azvReconciler.Recover(ctx); err != nil {
			errors = append(errors, err)
		}
		if err := attachReconciler.Recover(ctx); err != nil {
			errors = append(errors, err)
		}
		if err := pvReconciler.Recover(ctx); err != nil {
			errors = append(errors, err)
		}
		if err := podReconciler.Recover(ctx); err != nil {
			errors = append(errors, err)
		}
		sharedState.MarkRecoveryComplete()
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
	var cachedAzDriverNode *azdiskv1beta2.AzDriverNode
	azN := d.crdProvisioner.GetDiskClientSet().DiskV1beta2().AzDriverNodes(d.objectNamespace)
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
		timestamp := metav1.Now()
		readyForAllocation := true
		statusMessage := "Driver node healthy."
		klog.V(2).Infof("Updating status for (%v)", azDriverNodeToUpdate)
		if azDriverNodeToUpdate.Status == nil {
			azDriverNodeToUpdate.Status = &azdiskv1beta2.AzDriverNodeStatus{}
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
