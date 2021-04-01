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
	"os"
	"os/signal"
	"reflect"
	"syscall"
	"time"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
	"github.com/container-storage-interface/spec/lib/go/csi"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiRuntime "k8s.io/apimachinery/pkg/runtime"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	klogv1 "k8s.io/klog"
	"k8s.io/klog/klogr"
	"k8s.io/klog/v2"

	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	azuredisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1alpha1"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/controller"
	csicommon "sigs.k8s.io/azuredisk-csi-driver/pkg/csi-common"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/provisioner"
	volumehelper "sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

var isControllerPlugin = flag.Bool("is-controller-plugin", false, "Boolean flag to indicate this instance is running as controller.")
var isNodePlugin = flag.Bool("is-node-plugin", false, "Boolean flag to indicate this instance is running as node daemon.")
var driverObjectNamespace = flag.String("driver-object-namespace", "azure-disk-csi", "namespace where driver related custom resources are created.")
var useDriverV2 = flag.Bool("temp-use-driver-v2", false, "A temporary flag to enable early test and development of Azure Disk CSI Driver V2. This will be removed in the future.")
var heartbeatFrequencyInSec = flag.Int("heartbeat-frequency-in-sec", 30, "Frequency in seconds at which node driver sends heartbeat.")
var controllerLeaseDurationInSec = flag.Int("lease-duration-in-sec", 15, "The duration that non-leader candidates will wait to force acquire leadership")
var controllerLeaseRenewDeadlineInSec = flag.Int("lease-renew-deadline-in-sec", 10, "The duration that the acting controlplane will retry refreshing leadership before giving up.")
var controllerLeaseRetryPeriodInSec = flag.Int("lease-retry-period-in-sec", 2, "The duration the LeaderElector clients should wait between tries of actions.")
var partitionLabel = "azdrivernodes.disk.csi.azure.com/partition"

// OutputCallDepth is the stack depth where we can find the origin of this call
const OutputCallDepth = 6

// DefaultPrefixLength is the length of the log prefix that we have to strip out
// when logging klogv1 to klogv2
const DefaultPrefixLength = 30

// DriverV2 implements all interfaces of CSI drivers
type DriverV2 struct {
	DriverCore
	nodeProvisioner                   NodeProvisioner
	volumeLocks                       *volumehelper.VolumeLocks
	objectNamespace                   string
	nodePartition                     string
	controllerPartition               string
	heartbeatFrequencyInSec           int
	controllerLeaseDurationInSec      int
	controllerLeaseRenewDeadlineInSec int
	controllerLeaseRetryPeriodInSec   int
	kubeConfig                        *rest.Config
	kubeClient                        clientset.Interface
	azDiskClient                      azDiskClientSet.Interface
}

// NewDriver creates a Driver or DriverV2 object depending on the --temp-use-driver-v2 flag.
func NewDriver(nodeID string) CSIDriver {
	var d CSIDriver

	if !*useDriverV2 {
		d = newDriverV1(nodeID)
	} else {
		d = newDriverV2(nodeID, *driverObjectNamespace, "default", "default", *heartbeatFrequencyInSec, *controllerLeaseDurationInSec, *controllerLeaseRenewDeadlineInSec, *controllerLeaseRetryPeriodInSec)
	}

	return d
}

// newDriverV2 Creates a NewCSIDriver object. Assumes vendor version is equal to driver version &
// does not support optional driver plugin info manifest field. Refer to CSI spec for more details.
func newDriverV2(nodeID string,
	driverObjectNamespace string,
	nodePartition string,
	controllerPartition string,
	heartbeatFrequency int,
	leaseDurationInSec int,
	leaseRenewDeadlineInSec int,
	leaseRetryPeriodInSec int) *DriverV2 {
	klog.Warning("Using DriverV2")
	driver := DriverV2{}
	driver.Name = DriverName
	driver.Version = driverVersion
	driver.NodeID = nodeID
	driver.volumeLocks = volumehelper.NewVolumeLocks()
	driver.objectNamespace = driverObjectNamespace
	driver.nodePartition = nodePartition
	driver.controllerPartition = controllerPartition
	driver.heartbeatFrequencyInSec = heartbeatFrequency
	driver.controllerLeaseDurationInSec = leaseDurationInSec
	driver.controllerLeaseRenewDeadlineInSec = leaseRenewDeadlineInSec
	driver.controllerLeaseRetryPeriodInSec = leaseRetryPeriodInSec
	return &driver
}

// Run driver initialization
func (d *DriverV2) Run(endpoint, kubeConfigPath string, testBool bool) {
	versionMeta, err := GetVersionYAML()
	if err != nil {
		klog.Fatalf("%v", err)
	}
	klog.Infof("\nDRIVER INFORMATION:\n-------------------\n%s\n\nStreaming logs below:", versionMeta)

	d.kubeConfig, err = GetKubeConfig(kubeConfigPath)
	if err != nil || d.kubeConfig == nil {
		klog.Fatalf("failed to get kube config (%s), error: %v. Exiting application...", kubeConfigPath, err)
	}

	d.kubeClient, err = clientset.NewForConfig(d.kubeConfig)
	if err != nil || d.kubeClient == nil {
		klog.Fatalf("failed to get kubeclient with kubeconfig (%s), error: %v. Exiting application...", kubeConfigPath, err)
	}

	d.azDiskClient, err = getAzDiskClient(d.kubeConfig)
	if err != nil || d.azDiskClient == nil {
		klog.Fatalf("failed to get azDiskClient config (%s), error: %v. Exiting application...", kubeConfigPath, err)
	}

	cloud, err := GetCloudProvider(d.kubeClient)
	if err != nil || cloud.TenantID == "" || cloud.SubscriptionID == "" {
		klog.Fatalf("failed to get Azure Cloud Provider, error: %v. Exiting application...", err)
	}
	d.cloud = cloud

	if d.NodeID == "" {
		// Disable UseInstanceMetadata for controller to mitigate a timeout issue using IMDS
		// https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/168
		klog.Infoln("disable UseInstanceMetadata for controller")
		d.cloud.Config.UseInstanceMetadata = false
	}

	d.nodeProvisioner, err = provisioner.NewNodeProvisioner()
	if err != nil {
		klog.Fatalf("Failed to get node provisioner. Error: %v", err)
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
		})
	d.AddVolumeCapabilityAccessModes([]csi.VolumeCapability_AccessMode_Mode{csi.VolumeCapability_AccessMode_SINGLE_NODE_WRITER})
	d.AddNodeServiceCapabilities([]csi.NodeServiceCapability_RPC_Type{
		csi.NodeServiceCapability_RPC_STAGE_UNSTAGE_VOLUME,
		csi.NodeServiceCapability_RPC_EXPAND_VOLUME,
		csi.NodeServiceCapability_RPC_GET_VOLUME_STATS,
	})

	// cancel the context the controller manager will be running under, if receive SIGTERM or SIGINT
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := make(chan os.Signal, 2)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-c
		cancel()
	}()

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
	s.Start(endpoint, d, d, d, testBool)

	// Start sending hearbeat and mark node as ready
	if *isNodePlugin {
		go d.RunAzDriverNodeHeartbeatLoop(ctx)
	}

	// Wait for the GRPC Server to exit
	s.Wait()
}

func (d *DriverV2) checkDiskExists(ctx context.Context, diskURI string) error {
	diskName, err := GetDiskName(diskURI)
	if err != nil {
		return err
	}

	resourceGroup, err := GetResourceGroupFromURI(diskURI)
	if err != nil {
		return err
	}

	if _, rerr := d.cloud.DisksClient.Get(ctx, resourceGroup, diskName); rerr != nil {
		return rerr.Error()
	}

	return nil
}

func (d *DriverV2) checkDiskCapacity(ctx context.Context, resourceGroup, diskName string, requestGiB int) (bool, error) {
	disk, err := d.cloud.DisksClient.Get(ctx, resourceGroup, diskName)
	// Because we can not judge the reason of the error. Maybe the disk does not exist.
	// So here we do not handle the error.
	if err == nil {
		if !reflect.DeepEqual(disk, compute.Disk{}) && disk.DiskSizeGB != nil && int(*disk.DiskSizeGB) != requestGiB {
			return false, status.Errorf(codes.AlreadyExists, "the request volume already exists, but its capacity(%v) is different from (%v)", *disk.DiskProperties.DiskSizeGB, requestGiB)
		}
	}
	return true, nil
}

//StartControllersAndDieOnExit starts all the controllers for a certain object partition
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
	log := klogr.New().WithName("AzDiskControllerManager").WithValues("namepace", d.objectNamespace).WithValues("partition", d.controllerPartition)

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
		Namespace:                     d.objectNamespace})
	if err != nil {
		klog.Errorf("Unable to set up overall controller manager. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

	// Setup a new controller to clean-up AzDriverNodes
	// objects for the nodes which get deleted
	klog.V(2).Info("Initializing AzDriverNode controller")
	err = controller.InitializeAzDriverNodeController(mgr, &d.azDiskClient, d.objectNamespace)
	if err != nil {
		klog.Errorf("Failed to initialize AzDriverNodeController. Error: %v. Exiting application...", err)
		os.Exit(1)
	}
	klog.V(2).Info("Initializing AzVolumeAttachment controller")
	err = controller.NewAzVolumeAttachmentController(ctx, mgr, &d.azDiskClient, d.objectNamespace)
	if err != nil {
		klog.Errorf("Failed to initialize AzVolumeAttachment. Error: %v. Exiting application...", err)
		os.Exit(1)
	}

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
	err := d.RegisterAzDriverNode(ctx)
	if err != nil {
		klog.Fatalf("Failed to register AzDriverNode, error: %v. Exiting process...", err)
	}
}

// RegisterAzDriverNode creates custom resource for this driverNode
func (d *DriverV2) RegisterAzDriverNode(ctx context.Context) error {

	if d.NodeID == "" {
		klog.Errorf("NodeID is nil, can not initialize AzDriverNode")
		return errors.NewBadRequest("NodeID is nil, can not register the plugin.")
	}

	klog.V(2).Infof("Registering AzDriverNode for node (%s)", d.NodeID)
	node, err := d.kubeClient.CoreV1().Nodes().Get(ctx, d.NodeID, metav1.GetOptions{})
	if err != nil || node == nil {
		klog.Errorf("Failed to get node (%s), error: %v", node, err)
		return errors.NewBadRequest("Failed to get node or node not found, can not register the plugin.")
	}

	azN := d.azDiskClient.DiskV1alpha1().AzDriverNodes(d.objectNamespace)
	azDriverNodeFromCache, err := azN.Get(ctx, d.NodeID, metav1.GetOptions{})
	azDriverNodeUpdate := &azuredisk.AzDriverNode{}

	if err == nil && azDriverNodeFromCache != nil {
		// We found that the object already exists.
		klog.V(2).Infof("AzDriverNode exists, will update status. azDriverNodeFromCache=(%v)", azDriverNodeFromCache)
		azDriverNodeUpdate = azDriverNodeFromCache.DeepCopy()
	} else if errors.IsNotFound(err) {
		// If AzDriverNode object is not there create it
		klog.Errorf("AzDriverNode is not registered yet, will create. error: %v", err)
		azDriverNodeNew := &azuredisk.AzDriverNode{
			ObjectMeta: metav1.ObjectMeta{
				Name: d.NodeID,
			},
			Spec: azuredisk.AzDriverNodeSpec{
				NodeName: d.NodeID,
			},
		}
		if azDriverNodeNew.Labels == nil {
			azDriverNodeNew.Labels = make(map[string]string)
		}
		azDriverNodeNew.Labels[partitionLabel] = d.nodePartition
		klog.V(2).Infof("Creating AzDriverNode with details (%v)", azDriverNodeNew)
		azDriverNodeCreated, err := azN.Create(ctx, azDriverNodeNew, metav1.CreateOptions{})
		if err != nil || azDriverNodeCreated == nil {
			klog.Errorf("Failed to create/update azdrivernode resource for node (%s), error: %v", d.NodeID, err)
			return err
		}
		azDriverNodeUpdate = azDriverNodeCreated.DeepCopy()
	} else {
		klog.Errorf("Failed to get AzDriverNode for node (%s), error: %v", node, err)
		return errors.NewBadRequest("Failed to get AzDriverNode or node not found, can not register the plugin.")
	}

	// Do an initial update to AzDriverNode status
	if azDriverNodeUpdate.Status == nil {
		azDriverNodeUpdate.Status = &azuredisk.AzDriverNodeStatus{}
	}
	readyForAllocation := false
	timestamp := time.Now().UnixNano()
	statusMessage := "Driver node initializing."
	azDriverNodeUpdate.Status.ReadyForVolumeAllocation = &readyForAllocation
	azDriverNodeUpdate.Status.LastHeartbeatTime = &timestamp
	azDriverNodeUpdate.Status.StatusMessage = &statusMessage
	klog.V(2).Infof("Updating status for AzDriverNode Status=(%v)", azDriverNodeUpdate)
	_, err = azN.UpdateStatus(ctx, azDriverNodeUpdate, metav1.UpdateOptions{})
	if err != nil {
		klog.Errorf("Failed to update status of azdrivernode resource for node (%s), error: %v", d.NodeID, err)
		return err
	}

	return nil
}

// RunAzDriverNodeHeartbeatLoop runs a loop to send heartbeat from the node
func (d *DriverV2) RunAzDriverNodeHeartbeatLoop(ctx context.Context) {

	var err error
	var cachedAzDriverNode *azuredisk.AzDriverNode
	azN := d.azDiskClient.DiskV1alpha1().AzDriverNodes(d.objectNamespace)
	heartbeatFrequency := time.Duration(d.heartbeatFrequencyInSec) * time.Second
	klog.V(1).Info("Starting heartbeat loop with frequency (%v)", heartbeatFrequency)
	for {

		// Check if we have a cached copy of the
		// If not then get it
		if cachedAzDriverNode == nil {
			klog.V(2).Info("Don't have current AzDriverNodeStatus. Getting current AzDriverNodes status.")
			cachedAzDriverNode, err = azN.Get(context.Background(), d.NodeID, metav1.GetOptions{})
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
		klog.V(2).Info("Updating status for (%v)", azDriverNodeToUpdate)
		if azDriverNodeToUpdate.Status == nil {
			azDriverNodeToUpdate.Status = &azuredisk.AzDriverNodeStatus{}
		}
		azDriverNodeToUpdate.Status.ReadyForVolumeAllocation = &readyForAllocation
		azDriverNodeToUpdate.Status.LastHeartbeatTime = &timestamp
		azDriverNodeToUpdate.Status.StatusMessage = &statusMessage
		klog.V(2).Info("Sending heartbeat ReadyForVolumeAllocation=(%v) LastHeartbeatTime=(%v)", *azDriverNodeToUpdate.Status.ReadyForVolumeAllocation, *azDriverNodeToUpdate.Status.LastHeartbeatTime)
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

// gets the AzVolume cluster client
func getAzDiskClient(config *rest.Config) (
	*azDiskClientSet.Clientset,
	error) {

	azDiskClient, err := azDiskClientSet.NewForConfig(config)
	if err != nil {
		return nil, err
	}

	return azDiskClient, nil
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
