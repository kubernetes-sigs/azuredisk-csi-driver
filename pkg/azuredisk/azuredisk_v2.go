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
	"math/rand"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/container-storage-interface/spec/lib/go/csi"
	"github.com/go-logr/logr"

	v1 "k8s.io/api/core/v1"
	crdClientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiRuntime "k8s.io/apimachinery/pkg/runtime"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	"k8s.io/kubernetes/pkg/volume/util/hostutil"

	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	clientmetrics "k8s.io/client-go/tools/metrics"
	"k8s.io/client-go/tools/record"
	"k8s.io/component-base/metrics"
	"k8s.io/component-base/metrics/legacyregistry"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azdiskinformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
	azdiskinformertypes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions/azuredisk/v1beta2"
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

var isTestRun = flag.Bool("is-test-run", false, "Boolean flag to indicate whether this instance is being used for sanity or integration tests")

// OutputCallDepth is the stack depth where we can find the origin of this call
const OutputCallDepth = 6

// DefaultPrefixLength is the length of the log prefix that we have to strip out
// when logging klogv1 to klogv2
const DefaultPrefixLength = 30

type azDriverNodeStatus string

const (
	azDriverNodeInitializing azDriverNodeStatus = "Driver node initializing."
	azDriverNodeHealthy      azDriverNodeStatus = "Driver node healthy."
)

// LatencyAdapter implements LatencyMetric.
type LatencyAdapter struct {
	metric *metrics.HistogramVec
}

// Observe increments the request latency metric for the given verb/URL.
func (l *LatencyAdapter) Observe(_ context.Context, verb string, u url.URL, latency time.Duration) {
	if latency > consts.LongThrottleLatency {
		l.metric.WithLabelValues(verb, u.String()).Observe(latency.Seconds())
	}
}

// RateLimiterLatency reports the rate limiter latency in seconds per verb/URL.
var rateLimiterLatency = metrics.NewHistogramVec(
	&metrics.HistogramOpts{
		Subsystem: consts.RestClientSubsystem,
		Name:      consts.LatencyKey,
		Help:      "Rate limiter latency in seconds. Broken down by verb and URL.",
		Buckets:   []float64{1, 5, 10, 15, 30, 45, 60},
	}, []string{"verb", "url"})

// DriverV2 implements all interfaces of CSI drivers
type DriverV2 struct {
	DriverCore
	config               *azdiskv1beta2.AzDiskDriverConfiguration
	nodeProvisioner      NodeProvisioner
	cloudProvisioner     controller.CloudProvisioner
	crdProvisioner       CrdProvisioner
	volumeLocks          *volumehelper.VolumeLocks
	kubeConfig           *rest.Config
	kubeClient           *clientset.Clientset
	azdiskClient         azdisk.Interface
	azDriverNodeInformer azdiskinformertypes.AzDriverNodeInformer
	deviceChecker        *deviceChecker
}

func init() {
	legacyregistry.MustRegister(rateLimiterLatency)
	clientmetrics.RateLimiterLatency = &LatencyAdapter{metric: rateLimiterLatency}
}

// NewDriver creates a driver object.
func NewDriver(config *azdiskv1beta2.AzDiskDriverConfiguration) CSIDriver {
	return newDriverV2(config)
}

// newDriverV2 Creates a NewCSIDriver object. Assumes vendor version is equal to driver version &
// does not support optional driver plugin info manifest field. Refer to CSI spec for more details.
func newDriverV2(config *azdiskv1beta2.AzDiskDriverConfiguration) *DriverV2 {

	klog.Warning("Using DriverV2")
	driver := DriverV2{}
	driver.config = config
	driver.Name = config.DriverName
	driver.Version = driverVersion
	driver.NodeID = config.NodeConfig.NodeID
	driver.VolumeAttachLimit = config.NodeConfig.VolumeAttachLimit
	driver.ready = make(chan struct{})
	driver.ioHandler = azureutils.NewOSIOHandler()
	driver.hostUtil = hostutil.NewHostUtil()
	driver.volumeLocks = volumehelper.NewVolumeLocks()
	driver.deviceChecker = &deviceChecker{lock: sync.RWMutex{}, entry: nil}

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

	d.kubeConfig.QPS = float32(d.config.ClientConfig.KubeClientQPS)
	d.kubeConfig.Burst = d.config.ClientConfig.KubeClientQPS * 2

	d.azdiskClient, err = azureutils.GetAzDiskClient(d.kubeConfig)
	if err != nil || d.azdiskClient == nil {
		klog.Fatalf("failed to get azdiskclient with kubeconfig (%s), error: %v. Exiting application...", kubeconfig, err)
	}

	// d.crdProvisioner is set by NewFakeDriver for unit tests.
	if d.crdProvisioner == nil {
		d.crdProvisioner, err = provisioner.NewCrdProvisioner(d.kubeConfig, d.config.ObjectNamespace)
		if err != nil {
			klog.Fatalf("Failed to get crd provisioner. Error: %v", err)
		}
	}

	// d.cloudProvisioner is set by NewFakeDriver for unit tests.
	if d.cloudProvisioner == nil {
		userAgent := GetUserAgent(d.Name, d.config.CloudConfig.CustomUserAgent, d.config.CloudConfig.UserAgentSuffix)
		klog.V(2).Infof("driver userAgent: %s", userAgent)

		d.cloudProvisioner, err = provisioner.NewCloudProvisioner(
			d.kubeClient,
			d.config.CloudConfig.SecretName,
			d.config.CloudConfig.SecretNamespace,
			d.getPerfOptimizationEnabled(),
			topologyKey,
			userAgent,
			d.config.ControllerConfig.EnableDiskOnlineResize,
			d.config.CloudConfig.AllowEmptyCloudConfig,
			d.config.ControllerConfig.EnableAsyncAttach,
		)
		if err != nil {
			klog.Fatalf("Failed to get controller provisioner. Error: %v", err)
		}
	}

	if d.config.ControllerConfig.VMType != "" {
		klog.V(2).Infof("override VMType(%s) in cloud config as %s", d.cloudProvisioner.GetCloud().VMType, d.config.ControllerConfig.VMType)
		d.cloudProvisioner.GetCloud().VMType = d.config.ControllerConfig.VMType
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
		d.nodeProvisioner, err = provisioner.NewNodeProvisioner(d.config.NodeConfig.UseCSIProxyGAInterface)
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
	if d.config.ControllerConfig.EnableListVolumes {
		controllerCap = append(controllerCap, csi.ControllerServiceCapability_RPC_LIST_VOLUMES, csi.ControllerServiceCapability_RPC_LIST_VOLUMES_PUBLISHED_NODES)
	}
	if d.config.ControllerConfig.EnableListSnapshots {
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
	if d.config.ControllerConfig.Enabled {
		go d.StartControllersAndDieOnExit(ctx)
	}

	// Register the AzDriverNode
	if d.config.NodeConfig.Enabled {
		d.registerAzDriverNodeOrDie(ctx)
	}

	// Start the CSI endpoint/server
	s := csicommon.NewNonBlockingGRPCServer()
	// Driver d acts as IdentityServer, ControllerServer and NodeServer
	s.Start(endpoint, d, d, d, testingMock)

	// Start sending heartbeat and mark node as ready
	if d.config.NodeConfig.Enabled {
		go d.runAzDriverNodeHeartbeatLoop(ctx)
	}

	// Signal that the driver is ready.
	d.signalReady()

	// Wait for the GRPC Server to exit
	s.Wait()
}

// StartControllersAndDieOnExit starts all the controllers for a certain object partition
func (d *DriverV2) StartControllersAndDieOnExit(ctx context.Context) {
	log := klogr.New().WithName("AzDiskControllerManager").WithValues("namespace", d.config.ObjectNamespace).WithValues("partition", d.config.ControllerConfig.PartitionName)

	leaseDuration := time.Duration(d.config.ControllerConfig.LeaseDurationInSec) * time.Second
	renewDeadline := time.Duration(d.config.ControllerConfig.LeaseRenewDeadlineInSec) * time.Second
	retryPeriod := time.Duration(d.config.ControllerConfig.LeaseRetryPeriodInSec) * time.Second
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
		LeaderElectionNamespace:       d.config.ControllerConfig.LeaderElectionNamespace,
		LeaderElectionID:              d.config.ControllerConfig.PartitionName,
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
	crdClient, err := crdClientset.NewForConfig(d.kubeConfig)
	if err != nil {
		klog.Errorf("failed to initialize crd clientset. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	sharedState := controller.NewSharedState(d.Name, d.config.ObjectNamespace, topologyKey, eventRecorder, mgr.GetClient(), d.crdProvisioner.GetDiskClientSet(), d.kubeClient, crdClient)

	// Setup a new controller to clean-up AzDriverNodes
	// objects for the nodes which get deleted
	klog.V(2).Info("Initializing AzDriverNode controller")
	nodeReconciler, err := controller.NewAzDriverNodeController(mgr, sharedState)
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
	klog.V(2).Info("Initializing VolumeAttachment controller")
	_, err = controller.NewVolumeAttachmentController(mgr, sharedState)
	if err != nil {
		klog.Errorf("Failed to initialize VolumeAttachment Controller. Error: %v. Exiting application...", err)
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
		w.Logger().Infof("Elected as leader; initiating CRI deperecation / recovery...")
		if err := nodeReconciler.Recover(ctx); err != nil {
			errors = append(errors, err)
		}
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
		if err := sharedState.DeleteAPIVersion(ctx, consts.V1beta1); err != nil {
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

// registerAzDriverNodeOrDie creates custom resource for this driverNode
func (d *DriverV2) registerAzDriverNodeOrDie(ctx context.Context) {
	if d.NodeID == "" {
		klog.Fatalf("cannot create azdrivernode because node id is not provided")
	}

	nodeSelector := fmt.Sprintf("metadata.name=%s", d.NodeID)

	azdiskInformer := azdiskinformers.NewSharedInformerFactoryWithOptions(
		d.azdiskClient,
		consts.DefaultInformerResync,
		azdiskinformers.WithNamespace(d.config.ObjectNamespace),
		azdiskinformers.WithTweakListOptions(func(opt *metav1.ListOptions) {
			opt.FieldSelector = nodeSelector
		}),
	)

	d.azDriverNodeInformer = azdiskInformer.Disk().V1beta2().AzDriverNodes()
	d.azDriverNodeInformer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		DeleteFunc: func(obj interface{}) {
			if azDriverNode, ok := obj.(*azdiskv1beta2.AzDriverNode); ok && strings.EqualFold(azDriverNode.Name, d.NodeID) {
				_ = d.updateAzDriverNodeHeartbeat(context.Background())
			}
		},
	})

	go azdiskInformer.Start(ctx.Done())

	if !cache.WaitForCacheSync(ctx.Done(), d.azDriverNodeInformer.Informer().HasSynced) {
		klog.Fatal("failed to sync azdrivernode informer")
	}

	err := d.registerAzDriverNode(ctx)

	if err != nil {
		klog.Fatalf("failed to register azdrivernode: %v", err)
	}
}

// runAzDriverNodeHeartbeatLoop runs a loop to send heartbeat from the node
func (d *DriverV2) runAzDriverNodeHeartbeatLoop(ctx context.Context) {
	_ = d.updateAzDriverNodeHeartbeat(ctx)

	// To prevent a regular update storm when lots of nodes start around the same time,
	// delay a random interval within the configured heartbeat frequency before starting
	// the timed loop.
	heartbeatFrequencyMillis := d.config.NodeConfig.HeartbeatFrequencyInSec * 1000
	initialDelay := time.Duration(rand.Int63n(int64(heartbeatFrequencyMillis))) * time.Millisecond
	heartbeatFrequency := time.Duration(heartbeatFrequencyMillis) * time.Millisecond

	klog.V(1).Infof("Starting heartbeat loop with initial delay %v and frequency %v", initialDelay, heartbeatFrequency)

	time.Sleep(initialDelay)

	ticker := time.NewTicker(heartbeatFrequency)
	defer ticker.Stop()

	for {
		select {
		// If context is cancelled just return
		case <-ctx.Done():
			klog.Info("Context cancelled, stopped sending heartbeat.")
			return
		// Loop and update heartbeat at update frequency
		case <-ticker.C:
			_ = d.updateAzDriverNodeHeartbeat(ctx)
		}
	}
}

// registerAzDriverNode initializes the AzDriverNode object
func (d *DriverV2) registerAzDriverNode(ctx context.Context) error {
	innerCtx, w := workflow.New(ctx, workflow.WithDetails(consts.NamespaceLabel, d.config.ObjectNamespace, consts.NodeNameLabel, d.NodeID))

	err := d.updateOrCreateAzDriverNode(innerCtx, azDriverNodeInitializing)

	w.Finish(err)

	return err
}

// updateAzDriverNodeHeartbeat updates the AzDriverNode health status
func (d *DriverV2) updateAzDriverNodeHeartbeat(ctx context.Context) error {
	innerCtx, w := workflow.New(ctx, workflow.WithDetails(consts.NamespaceLabel, d.config.ObjectNamespace, consts.NodeNameLabel, d.NodeID))

	err := d.updateOrCreateAzDriverNode(innerCtx, azDriverNodeHealthy)

	w.Finish(err)

	return err
}

// updateOrCreateAzDriverNode updates the AzDriverNode status, creating a new object if one does not exist
func (d *DriverV2) updateOrCreateAzDriverNode(ctx context.Context, status azDriverNodeStatus) error {
	logger := logr.FromContextOrDiscard(ctx)

	statusMessage := string(status)
	readyForAllocation := status == azDriverNodeHealthy
	lastHeartbeatTime := metav1.Now()

	logger.V(2).Info("Updating heartbeat", "ReadyForVolumeAllocation", readyForAllocation, "LastHeartbeatTime", lastHeartbeatTime, "StatusMessage", statusMessage)

	azDriverNodes := d.azdiskClient.DiskV1beta2().AzDriverNodes(d.config.ObjectNamespace)
	azDriverNodeLister := d.azDriverNodeInformer.Lister().AzDriverNodes(d.config.ObjectNamespace)

	thisNode, err := azDriverNodeLister.Get(d.NodeID)
	if err != nil {
		if !errors.IsNotFound(err) {
			logger.Error(err, "Failed to get AzDriverNode")
			return err
		}

		thisNode = &azdiskv1beta2.AzDriverNode{
			ObjectMeta: metav1.ObjectMeta{
				Name: d.NodeID,
			},
			Spec: azdiskv1beta2.AzDriverNodeSpec{
				NodeName: d.NodeID,
			},
		}

		if thisNode, err = azDriverNodes.Create(ctx, thisNode, metav1.CreateOptions{}); err != nil {
			logger.Error(err, "Failed to create AzDriverNode object")
			return err
		}
	}

	thisNode = thisNode.DeepCopy()
	thisNode.Status = &azdiskv1beta2.AzDriverNodeStatus{
		ReadyForVolumeAllocation: &readyForAllocation,
		StatusMessage:            &statusMessage,
		LastHeartbeatTime:        &lastHeartbeatTime,
	}

	if _, err := azDriverNodes.UpdateStatus(ctx, thisNode, metav1.UpdateOptions{}); err != nil {
		logger.Error(err, "Failed to update AzDriverNode status after creation")
		return err
	}

	return nil
}

func (d *DriverV2) getVolumeLocks() *volumehelper.VolumeLocks {
	return d.volumeLocks
}

// getPerfOptimizationEnabled returns the value of the perfOptimizationEnabled field. It is intended for use with unit tests.
func (d *DriverV2) getPerfOptimizationEnabled() bool {
	return d.config.NodeConfig.EnablePerfOptimization
}

// setPerfOptimizationEnabled sets the value of the perfOptimizationEnabled field. It is intended for use with unit tests.
func (d *DriverV2) setPerfOptimizationEnabled(enabled bool) {
	d.config.NodeConfig.EnablePerfOptimization = enabled
}
