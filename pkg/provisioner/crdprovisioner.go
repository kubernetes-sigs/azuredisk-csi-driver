/*
Copyright 2021 The Kubernetes Authors.

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

package provisioner

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	v1 "k8s.io/api/core/v1"
	apiErrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/informers"
	kubeClientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	apiext "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	crdClientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azdiskinformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/util"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/watcher"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type CrdProvisioner struct {
	azDiskClient         azdisk.Interface
	kubeClient           kubeClientset.Interface // for crdprovisioner_test
	crdClient            crdClientset.Interface  // for crdprovisioner_test
	config               *azdiskv1beta2.AzDiskDriverConfiguration
	conditionWatcher     *watcher.ConditionWatcher
	azCachedReader       CachedReader
	driverUninstallState uint32
}

const (
	// TODO: Figure out good interval and timeout values, and make them configurable.
	interval = time.Duration(1) * time.Second
)

type CachedReader struct {
	kubeInformer informers.SharedInformerFactory
	azInformer   azdiskinformers.SharedInformerFactory
	azNamespace  string
}

func NewCachedReader(kubeInformerFactory informers.SharedInformerFactory, azInformerFactory azdiskinformers.SharedInformerFactory, objNamespace string) CachedReader {
	return CachedReader{
		kubeInformer: kubeInformerFactory,
		azInformer:   azInformerFactory,
		azNamespace:  objNamespace,
	}
}

func (a CachedReader) Get(_ context.Context, namespacedName types.NamespacedName, object client.Object, opts ...client.GetOption) error {
	var err error
	switch target := object.(type) {
	case *v1.Node:
		var node *v1.Node
		node, err = a.kubeInformer.Core().V1().Nodes().Lister().Get(namespacedName.Name)
		if err != nil {
			return err
		}
		node.DeepCopyInto(target)
	case *azdiskv1beta2.AzVolume:
		var azVolume *azdiskv1beta2.AzVolume
		azVolume, err = a.azInformer.Disk().V1beta2().AzVolumes().Lister().AzVolumes(namespacedName.Namespace).Get(namespacedName.Name)
		if err != nil {
			return err
		}
		azVolume.DeepCopyInto(target)
	case *azdiskv1beta2.AzVolumeAttachment:
		var azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment
		azVolumeAttachment, err = a.azInformer.Disk().V1beta2().AzVolumeAttachments().Lister().AzVolumeAttachments(namespacedName.Namespace).Get(namespacedName.Name)
		if err != nil {
			return err
		}
		azVolumeAttachment.DeepCopyInto(target)
	case *azdiskv1beta2.AzDriverNode:
		var azDriverNode *azdiskv1beta2.AzDriverNode
		azDriverNode, err = a.azInformer.Disk().V1beta2().AzDriverNodes().Lister().AzDriverNodes(namespacedName.Namespace).Get(namespacedName.Name)
		if err != nil {
			return err
		}
		azDriverNode.DeepCopyInto(target)
	}
	return err
}

func (a CachedReader) List(_ context.Context, objectList client.ObjectList, opts ...client.ListOption) error {
	clientListOptions := client.ListOptions{}
	for _, opt := range opts {
		opt.ApplyToList(&clientListOptions)
	}
	labelSelector := clientListOptions.LabelSelector

	var err error
	switch target := objectList.(type) {
	case *v1.NodeList:
		var nodes []*v1.Node
		nodes, err = a.kubeInformer.Core().V1().Nodes().Lister().List(labelSelector)
		nodesDeref := make([]v1.Node, len(nodes))
		for i := range nodes {
			nodesDeref[i] = *nodes[i]
		}
		nodesList := v1.NodeList{Items: nodesDeref}
		if err != nil {
			return err
		}
		nodesList.DeepCopyInto(target)
	case *azdiskv1beta2.AzVolumeList:
		var azVolumes []*azdiskv1beta2.AzVolume
		azVolumes, err = a.azInformer.Disk().V1beta2().AzVolumes().Lister().AzVolumes(a.azNamespace).List(labelSelector)
		azVolumeDeref := make([]azdiskv1beta2.AzVolume, len(azVolumes))
		for i := range azVolumes {
			azVolumeDeref[i] = *azVolumes[i]
		}
		azVolumeList := azdiskv1beta2.AzVolumeList{Items: azVolumeDeref}
		if err != nil {
			return err
		}
		azVolumeList.DeepCopyInto(target)
	case *azdiskv1beta2.AzVolumeAttachmentList:
		var azVolumeAttachments []*azdiskv1beta2.AzVolumeAttachment
		azVolumeAttachments, err = a.azInformer.Disk().V1beta2().AzVolumeAttachments().Lister().AzVolumeAttachments(a.azNamespace).List(labelSelector)
		azVolumeAttachmentDeref := make([]azdiskv1beta2.AzVolumeAttachment, len(azVolumeAttachments))
		for i := range azVolumeAttachments {
			azVolumeAttachmentDeref[i] = *azVolumeAttachments[i]
		}
		azVolumeAttachmentList := azdiskv1beta2.AzVolumeAttachmentList{Items: azVolumeAttachmentDeref}
		if err != nil {
			return err
		}
		azVolumeAttachmentList.DeepCopyInto(target)
	case *azdiskv1beta2.AzDriverNodeList:
		var azDriverNodes []*azdiskv1beta2.AzDriverNode
		azDriverNodes, err = a.azInformer.Disk().V1beta2().AzDriverNodes().Lister().AzDriverNodes(a.azNamespace).List(labelSelector)
		azDriverNodeDeref := make([]azdiskv1beta2.AzDriverNode, len(azDriverNodes))
		for i := range azDriverNodes {
			azDriverNodeDeref[i] = *azDriverNodes[i]
		}
		azDriverNodeList := azdiskv1beta2.AzDriverNodeList{Items: azDriverNodeDeref}
		if err != nil {
			return err
		}
		azDriverNodeList.DeepCopyInto(target)
	}
	return err
}

func NewCrdProvisioner(azdiskClient azdisk.Interface, config *azdiskv1beta2.AzDiskDriverConfiguration, cw *watcher.ConditionWatcher, azCachedReader CachedReader, informer azureutils.GenericInformer) (*CrdProvisioner, error) {
	c := &CrdProvisioner{
		azDiskClient:     azdiskClient,
		config:           config,
		conditionWatcher: cw,
		azCachedReader:   azCachedReader,
	}

	_, err := informer.Informer().AddEventHandler(cache.ResourceEventHandlerFuncs{
		UpdateFunc: func(_, new interface{}) {
			crdObj := new.(*apiext.CustomResourceDefinition)
			if crdObj.DeletionTimestamp != nil && (crdObj.Name == consts.AzVolumeAttachmentCRDName || crdObj.Name == consts.AzVolumeCRDName) {
				go func() {
					atomic.StoreUint32(&c.driverUninstallState, 1)
				}()
			}
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to add CRD event handler: %s", err)
	}

	return c, nil
}

/*
CreateVolume creates AzVolume CRI to correspond with the given CSI request.
*/
func (c *CrdProvisioner) CreateVolume(
	ctx context.Context,
	volumeName string,
	capacityRange *azdiskv1beta2.CapacityRange,
	volumeCapabilities []azdiskv1beta2.VolumeCapability,
	parameters map[string]string,
	secrets map[string]string,
	volumeContentSource *azdiskv1beta2.ContentVolumeSource,
	accessibilityReq *azdiskv1beta2.TopologyRequirement) (*azdiskv1beta2.AzVolumeStatusDetail, error) {
	var err error
	azVolumeClient := c.azDiskClient.DiskV1beta2().AzVolumes(c.config.ObjectNamespace)

	_, maxMountReplicaCount := azureutils.GetMaxSharesAndMaxMountReplicaCount(parameters, azureutils.HasMultiNodeAzVolumeCapabilityAccessMode(volumeCapabilities))

	// Getting the validVolumeName here since after volume
	// creation the diskURI will consist of the validVolumeName
	volumeName = azureutils.CreateValidDiskName(volumeName, true)
	azVolumeName := strings.ToLower(volumeName)

	ctx, w := workflow.New(ctx, workflow.WithDetails(consts.VolumeNameLabel, volumeName, consts.PvNameKey, parameters[consts.PvNameKey]))
	defer func() { w.Finish(err) }()

	azVolumeInstance := &azdiskv1beta2.AzVolume{}
	err = c.azCachedReader.Get(ctx, types.NamespacedName{Namespace: c.config.ObjectNamespace, Name: azVolumeName}, azVolumeInstance)
	if err == nil {
		updateFunc := func(obj client.Object) error {
			updateInstance := obj.(*azdiskv1beta2.AzVolume)
			switch updateInstance.Status.State {
			case "":
				break
			case azdiskv1beta2.VolumeOperationPending:
				break
			case azdiskv1beta2.VolumeCreated:
				if updateInstance.Status.Detail != nil {
					// If current request has different specifications than the existing volume, return error.
					if !isAzVolumeSpecSameAsRequestParams(updateInstance, maxMountReplicaCount, capacityRange, parameters, secrets, volumeContentSource, accessibilityReq) {
						err = status.Errorf(codes.AlreadyExists, "Volume with name (%s) already exists with different specifications", volumeName)
						return err
					}
					return nil
				}
			case azdiskv1beta2.VolumeCreating:
				return nil
			case azdiskv1beta2.VolumeCreationFailed:
				w.Logger().V(5).Info("Requeuing CreateVolume request")
				// otherwise requeue operation
				updateInstance.Status.Error = nil
				updateInstance.Status.State = azdiskv1beta2.VolumeOperationPending
				updateInstance.Finalizers = []string{consts.AzVolumeFinalizer}

				if !isAzVolumeSpecSameAsRequestParams(updateInstance, maxMountReplicaCount, capacityRange, parameters, secrets, volumeContentSource, accessibilityReq) {
					// Updating the spec fields to keep it up to date with the request
					updateInstance.Spec.MaxMountReplicaCount = maxMountReplicaCount
					updateInstance.Spec.CapacityRange = capacityRange
					updateInstance.Spec.Parameters = parameters
					updateInstance.Spec.Secrets = secrets
					updateInstance.Spec.ContentVolumeSource = volumeContentSource
					updateInstance.Spec.AccessibilityRequirements = accessibilityReq
				}
			default:
				return status.Errorf(codes.Internal, "unexpected create volume request: volume is currently in %s state", updateInstance.Status.State)
			}
			return nil
		}

		if _, err = azureutils.UpdateCRIWithRetry(ctx, c.azCachedReader.azInformer, nil, c.azDiskClient, azVolumeInstance, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateAll); err != nil {
			return nil, err
		}
		// if the error was caused by errors other than IsNotFound, return failure
	} else if !apiErrors.IsNotFound(err) {
		err = status.Errorf(codes.Internal, "failed to get AzVolume CRI: %v", err)
		return nil, err
	} else {
		pv, pvExists := parameters[consts.PvNameKey]
		pvc, pvcExists := parameters[consts.PvcNameKey]
		namespace, namespaceExists := parameters[consts.PvcNamespaceKey]
		if !pvExists || !pvcExists || !namespaceExists {
			w.Logger().Info("CreateVolume request does not contain pv, pvc, namespace information. Please enable -extra-create-metadata flag in csi-provisioner")
		}
		azVolume := &azdiskv1beta2.AzVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name:        azVolumeName,
				Finalizers:  []string{consts.AzVolumeFinalizer},
				Labels:      map[string]string{consts.PvNameLabel: pv, consts.PvcNameLabel: pvc, consts.PvcNamespaceLabel: namespace},
				Annotations: map[string]string{consts.RequestIDKey: w.RequestID(), consts.RequestStartTimeKey: w.StartTime().Format(consts.RequestTimeFormat)},
			},
			Spec: azdiskv1beta2.AzVolumeSpec{
				MaxMountReplicaCount:      maxMountReplicaCount,
				VolumeName:                volumeName,
				VolumeCapability:          volumeCapabilities,
				CapacityRange:             capacityRange,
				Parameters:                parameters,
				Secrets:                   secrets,
				ContentVolumeSource:       volumeContentSource,
				AccessibilityRequirements: accessibilityReq,
				PersistentVolume:          pv,
			},
		}
		azureutils.AnnotateAPIVersion(azVolume)

		w.Logger().V(5).Info("Creating AzVolume CRI")

		_, err = azVolumeClient.Create(ctx, azVolume, metav1.CreateOptions{})
		if err != nil {
			err = status.Errorf(codes.Internal, "failed to create AzVolume CRI: %v", err)
			return nil, err
		}

		w.Logger().V(5).Info("Successfully created AzVolume CRI")
	}

	waiter := c.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeType, azVolumeName, waitForCreateVolumeFunc)
	defer waiter.Close()

	var obj runtime.Object
	obj, err = waiter.Wait(ctx)
	if obj == nil || err != nil {
		// if the error was due to context deadline exceeding, do not delete AzVolume CRI
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return nil, err
		}
		// if volume creation was unsuccessful, delete the AzVolume CRI and return error
		go func() {
			w.Logger().V(5).Info("Deleting volume due to failed volume creation")
			conditionFunc := func() (bool, error) {
				if err := azVolumeClient.Delete(context.Background(), azVolumeName, metav1.DeleteOptions{}); err != nil && !apiErrors.IsNotFound(err) {
					w.Logger().Error(err, "failed to make a delete request for AzVolume CRI")
					return false, nil
				}
				return true, nil
			}
			if err := wait.PollImmediateInfinite(interval, conditionFunc); err != nil {
				w.Logger().Error(err, "failed to delete AzVolume CRI")
			}
		}()
		return nil, err
	}
	azVolumeInstance = obj.(*azdiskv1beta2.AzVolume)

	if azVolumeInstance.Status.Detail == nil {
		// this line should not be reached
		err = status.Errorf(codes.Internal, "failed to create volume (%s)", volumeName)
		return nil, err
	}

	return azVolumeInstance.Status.Detail, nil
}

func (c *CrdProvisioner) DeleteVolume(ctx context.Context, volumeID string, secrets map[string]string) error {
	// TODO: Since the CRD provisioner needs to the AzVolume name and not the ARM disk URI, it should really
	// return the AzVolume name to the caller as the volume ID. To make this work, we would need to implement
	// snapshot APIs through the CRD provisioner.
	// Replace them in all instances in this file.
	var err error
	azVolumeClient := c.azDiskClient.DiskV1beta2().AzVolumes(c.config.ObjectNamespace)

	var volumeName string
	volumeName, err = azureutils.GetDiskName(volumeID)
	if err != nil {
		return nil
	}

	azVolumeName := strings.ToLower(volumeName)

	azVolumeInstance := &azdiskv1beta2.AzVolume{}
	err = c.azCachedReader.Get(ctx, types.NamespacedName{Namespace: c.config.ObjectNamespace, Name: azVolumeName}, azVolumeInstance)
	ctx, w := workflow.New(ctx, workflow.WithDetails(workflow.GetObjectDetails(azVolumeInstance)...))
	defer func() { w.Finish(err) }()

	if err != nil {
		if apiErrors.IsNotFound(err) {
			w.Logger().Logger.WithValues(consts.VolumeNameLabel, volumeName).V(5).Info("Deletion successful: could not find the AzVolume CRI")
			return nil
		}
		w.Logger().Logger.WithValues(consts.VolumeNameLabel, volumeName).Error(err, "failed to get AzVolume CRI")
		return err
	}

	// we don't want to delete pre-provisioned volumes
	if azureutils.MapContains(azVolumeInstance.Status.Annotations, consts.PreProvisionedVolumeAnnotation) {
		w.Logger().V(5).Info("AzVolume is pre-provisioned and won't be deleted.")
		return nil
	}

	// if deletion failed requeue deletion
	updateFunc := func(obj client.Object) error {
		updateInstance := obj.(*azdiskv1beta2.AzVolume)
		switch updateInstance.Status.State {
		case azdiskv1beta2.VolumeCreating:
			// if volume is still being created, wait for creation
			waiter := c.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeType, azVolumeName, waitForCreateVolumeFunc)
			obj, err := waiter.Wait(ctx)
			// close cannot be called on defer because this will interfere wait for delete
			waiter.Close()
			if err != nil {
				return err
			}
			azVolumeInstance = obj.(*azdiskv1beta2.AzVolume)
		case azdiskv1beta2.VolumeUpdating:
			// if volume is still being updated, wait for update
			if azVolumeInstance.Spec.CapacityRange != nil {
				waiter := c.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeType, azVolumeName, waitForExpandVolumeFunc(azVolumeInstance.Spec.CapacityRange.RequiredBytes))
				obj, err := waiter.Wait(ctx)
				// close cannot be called on defer because this will interfere wait for delete
				waiter.Close()
				if err != nil {
					return err
				}
				azVolumeInstance = obj.(*azdiskv1beta2.AzVolume)
			}
		case azdiskv1beta2.VolumeDeleting:
			// if volume is still being deleted, don't update
			return nil
		}
		// otherwise update the AzVolume with delete request annotation
		w.AnnotateObject(updateInstance)
		updateInstance.Status.Annotations = azureutils.AddToMap(updateInstance.Status.Annotations, consts.VolumeDeleteRequestAnnotation, consts.CloudDeleteVolume)
		// remove deletion failure error from AzVolume CRI to retrigger deletion
		updateInstance.Status.Error = nil
		// revert volume deletion state to avoid confusion
		updateInstance.Status.State = azdiskv1beta2.VolumeCreated

		return nil
	}

	var updateObj client.Object
	// update AzVolume CRI with annotation and reset state with retry upon conflict
	if updateObj, err = azureutils.UpdateCRIWithRetry(ctx, c.azCachedReader.azInformer, nil, c.azDiskClient, azVolumeInstance, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}

	// if the driver is uninstalling, the delete request is rejected.
	if c.IsDriverUninstall() {
		err = fmt.Errorf("VolumeDeleteRequestError")
		w.Logger().Error(err, "AzVolume deletion is being requested as the driver is uninstalling.")
		return err
	}

	waiter := c.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeType, azVolumeName, waitForDeleteVolumeFunc)
	defer waiter.Close()

	// only make delete request if object's deletion timestamp is not set
	azVolumeInstance = updateObj.(*azdiskv1beta2.AzVolume)
	if azVolumeInstance.DeletionTimestamp.IsZero() {
		err = azVolumeClient.Delete(ctx, azVolumeName, metav1.DeleteOptions{})
		if err != nil {
			if apiErrors.IsNotFound(err) {
				return nil
			}

			err = status.Errorf(codes.Internal, "failed to delete AzVolume CRI: %v", err)
			return err
		}
	}

	_, err = waiter.Wait(ctx)
	return err
}

func (c *CrdProvisioner) PublishVolume(
	ctx context.Context,
	volumeID string,
	nodeID string,
	volumeCapability *azdiskv1beta2.VolumeCapability,
	readOnly bool,
	secrets map[string]string,
	volumeContext map[string]string,
) (map[string]string, error) {
	var err error
	azVAClient := c.azDiskClient.DiskV1beta2().AzVolumeAttachments(c.config.ObjectNamespace)
	var volumeName string
	volumeName, err = azureutils.GetDiskName(volumeID)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "Error finding volume : %v", err)
	}
	volumeName = strings.ToLower(volumeName)

	ctx, w := workflow.New(ctx, workflow.WithDetails(consts.VolumeNameLabel, volumeName, consts.NodeNameLabel, nodeID, consts.RoleLabel, azdiskv1beta2.PrimaryRole))
	defer func() { w.Finish(err) }()

	// return error if volume is not found
	azVolume := &azdiskv1beta2.AzVolume{}
	err = c.azCachedReader.Get(ctx, types.NamespacedName{Namespace: c.config.ObjectNamespace, Name: volumeName}, azVolume)
	if apiErrors.IsNotFound(err) {
		err = status.Errorf(codes.NotFound, "volume (%s) does not exist: %v", volumeName, err)
		return nil, err
	}

	// return error if node is not found
	azDriverNode := &azdiskv1beta2.AzDriverNode{}
	if err = c.azCachedReader.Get(ctx, types.NamespacedName{Namespace: c.config.ObjectNamespace, Name: nodeID}, azDriverNode); apiErrors.IsNotFound(err) {
		err = status.Errorf(codes.NotFound, "node (%s) does not exist: %v", nodeID, err)
		return nil, err
	}

	if volumeContext == nil {
		volumeContext = map[string]string{}
	}

	publishContext := map[string]string{}

	attachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeID)

	attachmentObj := &azdiskv1beta2.AzVolumeAttachment{}
	var creationNeeded bool
	err = c.azCachedReader.Get(ctx, types.NamespacedName{Namespace: c.config.ObjectNamespace, Name: attachmentName}, attachmentObj)
	if err != nil {
		if !apiErrors.IsNotFound(err) {
			err = status.Errorf(codes.Internal, "failed to get AzVolumeAttachment CRI: %v", err)
			return nil, err
		}
		// if Replica AzVolumeAttachment CRI does not exist for the volume-node pair for failover, create a new Primary
		creationNeeded = true
		attachmentObj = &azdiskv1beta2.AzVolumeAttachment{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: c.config.ObjectNamespace,
				Name:      attachmentName,
				Labels: map[string]string{
					consts.NodeNameLabel:   nodeID,
					consts.VolumeNameLabel: volumeName,
					consts.RoleLabel:       string(azdiskv1beta2.PrimaryRole),
				},
				Finalizers: []string{consts.AzVolumeAttachmentFinalizer},
				Annotations: map[string]string{
					consts.VolumeAttachRequestAnnotation: "crdProvisioner",
					consts.RequestIDKey:                  w.RequestID(),
					consts.RequestStartTimeKey:           w.StartTime().Format(consts.RequestTimeFormat),
				},
			},
			Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
				VolumeName:    volumeName,
				VolumeID:      volumeID,
				NodeName:      nodeID,
				VolumeContext: volumeContext,
				RequestedRole: azdiskv1beta2.PrimaryRole,
			},
		}
		azureutils.AnnotateAPIVersion(attachmentObj)
	}

	filterVAToDetach := func(attachments []azdiskv1beta2.AzVolumeAttachment) (numAttachment int, unpublishOrder []*azdiskv1beta2.AzVolumeAttachment) {
		for i := range attachments {
			// only count if deletionTimestamp not set
			if attachments[i].GetDeletionTimestamp().IsZero() && !azureutils.MapContains(attachments[i].Status.Annotations, consts.VolumeDetachRequestAnnotation) {
				numAttachment++
				if attachments[i].Spec.RequestedRole == azdiskv1beta2.ReplicaRole {
					unpublishOrder = append(unpublishOrder, &attachments[i])
				}
			}
		}
		return
	}

	var unpublishOrder []*azdiskv1beta2.AzVolumeAttachment
	var requiredUnpublishCount int
	// there can be a race between replica management and primary creation,
	// so create primary first without triggering attach in case replicas need to be detached/
	var isPreemptiveCreate bool
	// check remaining node capacity to determine if replica detachment is necessary
	var maxDiskCount int
	if maxDiskCount, err = azureutils.GetNodeMaxDiskCount(ctx, c.azCachedReader, nodeID); err != nil {
		// continue if k8s node object is not found, we have already verified the node's existence through azdrivernode.
		if !apiErrors.IsNotFound(err) {
			return publishContext, err
		}
	} else {
		volumeLabel := azureutils.LabelPair{Key: consts.VolumeNameLabel, Operator: selection.NotEquals, Entry: volumeName}
		nodeLabel := azureutils.LabelPair{Key: consts.NodeNameLabel, Operator: selection.Equals, Entry: nodeID}
		var attachments []azdiskv1beta2.AzVolumeAttachment
		if attachments, err = azureutils.GetAzVolumeAttachmentsWithLabel(ctx, c.azCachedReader, volumeLabel, nodeLabel); err != nil {
			return publishContext, err
		}

		numVolumeAttachments, volumeUnpublishOrder := filterVAToDetach(attachments)

		requiredVolumeUnpublishCount := numVolumeAttachments - maxDiskCount + 1

		if requiredVolumeUnpublishCount > len(volumeUnpublishOrder) {
			err = status.Errorf(codes.Internal, "Cannot free up %d node capacity: not enough replicas (%d) attached to the node", requiredVolumeUnpublishCount, len(volumeUnpublishOrder))
		} else if requiredVolumeUnpublishCount > 0 {
			isPreemptiveCreate = true
			unpublishOrder = append(unpublishOrder, volumeUnpublishOrder[:requiredVolumeUnpublishCount]...)
			// if volume needs to be detached prior to attach operation, remove the attach annotation from CRI
			attachmentObj.Annotations = azureutils.RemoveFromMap(attachmentObj.Annotations, consts.VolumeAttachRequestAnnotation)
			requiredUnpublishCount += requiredVolumeUnpublishCount
		}
	}

	// detach replica volume if volume's maxShares have been fully saturated.
	if azVolume.Spec.MaxMountReplicaCount > 0 {
		// get undemoted AzVolumeAttachments for volume == volumeName and node != nodeID
		volumeLabel := azureutils.LabelPair{Key: consts.VolumeNameLabel, Operator: selection.Equals, Entry: volumeName}
		nodeLabel := azureutils.LabelPair{Key: consts.NodeNameLabel, Operator: selection.NotEquals, Entry: nodeID}

		var attachments []azdiskv1beta2.AzVolumeAttachment
		attachments, err = azureutils.GetAzVolumeAttachmentsWithLabel(ctx, c.azCachedReader, volumeLabel, nodeLabel)
		if err != nil && !apiErrors.IsNotFound(err) {
			err = status.Errorf(codes.Internal, "failed to list undemoted AzVolumeAttachments with volume (%s) not attached to node (%s): %v", volumeName, nodeID, err)
			return nil, err
		}

		// TODO: come up with a logic to select replica for unpublishing)
		sort.Slice(attachments, func(i, _ int) bool {
			iRole, iExists := attachments[i].Labels[consts.RoleChangeLabel]
			// if i is demoted, prioritize it. Otherwise, prioritize j
			return iExists && iRole == consts.Demoted
		})

		nodeAttachmentCount, nodeUnpublishOrder := filterVAToDetach(attachments)

		// if maxMountReplicaCount has been exceeded, unpublish demoted AzVolumeAttachment or if demoted AzVolumeAttachment does not exist, select one to unpublish
		requiredNodeUnpublishCount := nodeAttachmentCount - azVolume.Spec.MaxMountReplicaCount
		if requiredNodeUnpublishCount > 0 {
			isPreemptiveCreate = true
			unpublishOrder = append(unpublishOrder, nodeUnpublishOrder[:requiredNodeUnpublishCount]...)
			// if volume needs to be detached prior to attach operation, remove the attach annotation from CRI
			attachmentObj.Annotations = azureutils.RemoveFromMap(attachmentObj.Annotations, consts.VolumeAttachRequestAnnotation)
			requiredUnpublishCount += requiredNodeUnpublishCount
		}
	}

	// create AzVolumeAttachment object
	if creationNeeded {
		attachmentObj, err = azVAClient.Create(ctx, attachmentObj, metav1.CreateOptions{})
		if err != nil {
			err = status.Errorf(codes.Internal, "failed to create AzVolumeAttachment CRI: %v", err)
			return publishContext, err
		}
		if !isPreemptiveCreate {
			if c.config.ControllerConfig.WaitForLunEnabled {
				publishContext, err = c.waitForLun(ctx, volumeID, nodeID)
			}
			return publishContext, err
		}
	}

	for i := 0; requiredUnpublishCount > 0 && i < len(unpublishOrder); i++ {
		if err = c.detachVolume(ctx, unpublishOrder[i]); err != nil {
			err = status.Errorf(codes.Internal, "failed to make request to unpublish volume (%s) from node (%s): %v", unpublishOrder[i].Spec.VolumeName, unpublishOrder[i].Spec.NodeName, err)
			return nil, err
		}
		requiredUnpublishCount--
	}

	var updateFunc func(obj client.Object) error
	updateMode := azureutils.UpdateCRIStatus
	// if attachment is scheduled for deletion, new attachment should only be created after the deletion
	if attachmentObj.DeletionTimestamp != nil || azureutils.MapContains(attachmentObj.Status.Annotations, consts.VolumeDetachRequestAnnotation) {
		err = status.Error(codes.Aborted, "need to wait until attachment is fully deleted before attaching")
		return nil, err
	}

	if attachmentObj.Spec.RequestedRole == azdiskv1beta2.PrimaryRole {
		// if azVolumeAttachment was preempitvely created without attach trigger, then add attach trigger now
		if isPreemptiveCreate {
			// make sure CRI is created
			waiter := c.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeAttachmentType, attachmentName, waitForCRICreateFunc)
			_, _ = waiter.Wait(ctx)
			waiter.Close()
			updateMode = azureutils.UpdateCRI
			updateFunc = func(obj client.Object) error {
				updateInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
				updateInstance.Annotations = azureutils.AddToMap(updateInstance.Annotations, consts.VolumeAttachRequestAnnotation, "crdProvisioner")
				return nil
			}
		} else {
			if attachmentObj.Status.Error == nil {
				if c.config.ControllerConfig.WaitForLunEnabled {
					publishContext, err = c.waitForLun(ctx, volumeID, nodeID)
				}
				return publishContext, err
			}
			// if primary attachment failed with an error, and for whatever reason, controllerPublishVolume request was made instead of NodeStageVolume request, reset the state, detail and error here if ever reached
			updateFunc = func(obj client.Object) error {
				updateInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
				w.AnnotateObject(updateInstance)
				updateInstance.Status.State = azdiskv1beta2.AttachmentPending
				updateInstance.Status.Detail = nil
				updateInstance.Status.Error = nil
				return nil
			}
		}
	} else {
		if attachmentObj.Status.Error != nil {
			// or if replica attachment failed with an error, ideally we would want to reset the error to retrigger the attachment here but this exposes the logic to a race between replica controller deleting failed replica attachment and crd provisioner resetting error. So, return error here to wait for the failed replica attachment to be deleted
			err = status.Errorf(codes.Aborted, "replica attachment (%s) failed with an error (%v). Will retry once the attachment is deleted.", attachmentName, attachmentObj.Status.Error)
			return nil, err
		}

		// otherwise, there is a replica attachment for this volume-node pair, so promote it to primary and return success
		updateFunc = func(obj client.Object) error {
			updateInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
			w.AnnotateObject(updateInstance)
			updateInstance.Spec.RequestedRole = azdiskv1beta2.PrimaryRole
			// Keeping the spec fields up to date with the request parameters
			updateInstance.Spec.VolumeContext = volumeContext
			// Update the label of the AzVolumeAttachment
			if updateInstance.Labels == nil {
				updateInstance.Labels = map[string]string{}
			}
			updateInstance.Labels[consts.RoleLabel] = string(azdiskv1beta2.PrimaryRole)
			updateInstance.Labels[consts.RoleChangeLabel] = consts.Promoted

			return nil
		}
		updateMode = azureutils.UpdateAll
	}
	if _, err = azureutils.UpdateCRIWithRetry(ctx, c.azCachedReader.azInformer, nil, c.azDiskClient, attachmentObj, updateFunc, consts.NormalUpdateMaxNetRetry, updateMode); err != nil {
		return publishContext, err
	}
	if c.config.ControllerConfig.WaitForLunEnabled {
		publishContext, err = c.waitForLun(ctx, volumeID, nodeID)
	}
	return publishContext, err
}

func (c *CrdProvisioner) waitForLun(ctx context.Context, volumeID, nodeID string) (map[string]string, error) {
	var err error
	ctx, w := workflow.New(ctx)
	defer func() { w.Finish(err) }()

	var azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment
	azVolumeAttachment, err = c.waitForLunOrAttach(ctx, volumeID, nodeID, waitForLunFunc)
	if err != nil {
		return nil, err
	}
	if azVolumeAttachment.Status.Detail == nil {
		return nil, err
	}
	return azVolumeAttachment.Status.Detail.PublishContext, err
}

func (c *CrdProvisioner) WaitForAttach(ctx context.Context, volumeID, nodeID string) (*azdiskv1beta2.AzVolumeAttachment, error) {
	var err error
	ctx, w := workflow.New(ctx, workflow.WithCaller(1))
	defer func() { w.Finish(err) }()

	var azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment
	azVolumeAttachment, err = c.waitForLunOrAttach(ctx, volumeID, nodeID, waitForAttachVolumeFunc)
	return azVolumeAttachment, err
}

func (c *CrdProvisioner) waitForLunOrAttach(ctx context.Context, volumeID, nodeID string, waitFunc func(interface{}, bool) (bool, error)) (*azdiskv1beta2.AzVolumeAttachment, error) {
	volumeName, err := azureutils.GetDiskName(volumeID)
	if err != nil {
		return nil, err
	}
	volumeName = strings.ToLower(volumeName)
	attachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeID)

	azVolumeAttachmentInstance := &azdiskv1beta2.AzVolumeAttachment{}
	err = c.azCachedReader.Get(ctx, types.NamespacedName{Namespace: c.config.ObjectNamespace, Name: attachmentName}, azVolumeAttachmentInstance)
	if err != nil {
		if !apiErrors.IsNotFound(err) {
			err = status.Errorf(codes.Internal, "unexpected error while getting AzVolumeAttachment (%s): %v", attachmentName, err)
			return nil, err
		}
	} else {
		// If the AzVolumeAttachment is attached, return without triggering informer wait
		if success, err := waitFunc(azVolumeAttachmentInstance, false); success {
			return azVolumeAttachmentInstance, err
		} else if err != nil {
			// If the attachment had previously failed with an error, reset the error, detail and state, and retrigger attach
			updateFunc := func(obj client.Object) error {
				updateInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
				switch updateInstance.Status.State {
				case azdiskv1beta2.AttachmentFailed:
					if w, ok := workflow.GetWorkflowFromContext(ctx); ok {
						w.AnnotateObject(updateInstance)
					}
					updateInstance.Status.State = azdiskv1beta2.AttachmentPending
					updateInstance.Status.Detail = nil
					updateInstance.Status.Error = nil
				case azdiskv1beta2.Attached:
				case azdiskv1beta2.Attaching:
				case azdiskv1beta2.AttachmentPending:
					// ensure that volumeAttachRequestAnnotation is present to retrigger attach operation.
					azVolumeAttachmentInstance.Annotations = azureutils.AddToMap(azVolumeAttachmentInstance.Annotations, consts.VolumeAttachRequestAnnotation, "crdProvisioner")
				default:
					return status.Errorf(codes.Internal, "unexpected publish/stage volume request: AzVolumeAttachment is currently in %s state", updateInstance.Status.State)
				}
				return nil
			}

			if _, err = azureutils.UpdateCRIWithRetry(ctx, c.azCachedReader.azInformer, nil, c.azDiskClient, azVolumeAttachmentInstance, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
				return nil, err
			}
		}
	}

	waiter := c.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeAttachmentType, attachmentName, waitFunc)
	defer waiter.Close()

	obj, err := waiter.Wait(ctx)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		err = status.Errorf(codes.Aborted, "failed to wait for attachment for volume (%s) and node (%s) to complete: unknown error", volumeID, nodeID)
		return nil, err
	}
	azVolumeAttachmentInstance = obj.(*azdiskv1beta2.AzVolumeAttachment)
	if azVolumeAttachmentInstance.Status.Detail == nil {
		err = status.Errorf(codes.Internal, "failed to attach azvolume attachment resource for volume id (%s) to node (%s)", volumeID, nodeID)
		return nil, err
	}
	return azVolumeAttachmentInstance, nil
}

func (c *CrdProvisioner) UnpublishVolume(
	ctx context.Context,
	volumeID string,
	nodeID string,
	secrets map[string]string,
	mode consts.UnpublishMode) error {
	var err error
	var volumeName string
	volumeName, err = azureutils.GetDiskName(volumeID)
	if err != nil {
		return err
	}

	attachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeID)
	volumeName = strings.ToLower(volumeName)

	azVolumeAttachmentInstance := &azdiskv1beta2.AzVolumeAttachment{}
	err = c.azCachedReader.Get(ctx, types.NamespacedName{Namespace: c.config.ObjectNamespace, Name: attachmentName}, azVolumeAttachmentInstance)
	ctx, w := workflow.New(ctx, workflow.WithDetails(workflow.GetObjectDetails(azVolumeAttachmentInstance)...))
	defer func() { w.Finish(err) }()

	if err != nil {
		if apiErrors.IsNotFound(err) {
			w.Logger().WithValues(consts.VolumeNameLabel, volumeName, consts.NodeNameLabel, nodeID).V(5).Info("Volume detach successful")
			err = nil
			return nil
		}
		w.Logger().WithValues(consts.VolumeNameLabel, volumeName, consts.NodeNameLabel, nodeID).Error(err, "failed to get AzVolumeAttachment")
		return err
	}

	if demote, derr := c.shouldDemote(volumeName, mode); derr != nil {
		err = derr
		return err
	} else if demote {
		err = c.demoteVolume(ctx, azVolumeAttachmentInstance)
		return err
	}
	err = c.detachVolume(ctx, azVolumeAttachmentInstance)
	return err
}

func (c *CrdProvisioner) shouldDemote(volumeName string, mode consts.UnpublishMode) (bool, error) {
	if mode == consts.Detach {
		return false, nil
	}
	azVolumeInstance, err := c.azCachedReader.azInformer.Disk().V1beta2().AzVolumes().Lister().AzVolumes(c.config.ObjectNamespace).Get(volumeName)
	if err != nil {
		return false, err
	}
	// if volume's maxMountReplicaCount == 0, detach and wait for detachment to complete
	// otherwise, demote volume to replica
	return azVolumeInstance.Spec.MaxMountReplicaCount > 0, nil
}

func (c *CrdProvisioner) demoteVolume(ctx context.Context, azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment) error {
	ctx, w := workflow.GetWorkflowFromObj(ctx, azVolumeAttachment)
	w.Logger().V(5).Infof("Requesting AzVolumeAttachment (%s) demotion", azVolumeAttachment.Name)

	updateFunc := func(obj client.Object) error {
		updateInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
		w.AnnotateObject(updateInstance)
		updateInstance.Spec.RequestedRole = azdiskv1beta2.ReplicaRole

		if updateInstance.Labels == nil {
			updateInstance.Labels = map[string]string{}
		}

		updateInstance.Labels[consts.RoleLabel] = string(azdiskv1beta2.ReplicaRole)
		updateInstance.Labels[consts.RoleChangeLabel] = consts.Demoted
		return nil
	}
	_, err := azureutils.UpdateCRIWithRetry(ctx, c.azCachedReader.azInformer, nil, c.azDiskClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateAll)
	return err
}

func (c *CrdProvisioner) detachVolume(ctx context.Context, azVolumeAttachment *azdiskv1beta2.AzVolumeAttachment) error {
	var err error
	nodeName := azVolumeAttachment.Spec.NodeName
	volumeID := azVolumeAttachment.Spec.VolumeID
	if err != nil {
		return err
	}

	ctx, w := workflow.GetWorkflowFromObj(ctx, azVolumeAttachment)

	updateFunc := func(obj client.Object) error {
		updateInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
		switch azVolumeAttachment.Status.State {
		// if detachment still in progress, return without update
		case azdiskv1beta2.Detaching:
			return nil
		case azdiskv1beta2.Attaching:
			// if attachment still in progress, wait for attach to complete
			w.Logger().V(5).Info("Attachment is in process... Waiting for the attachment to complete.")
			if azVolumeAttachment, err = c.WaitForAttach(ctx, volumeID, nodeName); err != nil {
				return err
			}
		// if detachment failed, reset error and state to retrigger operation
		case azdiskv1beta2.DetachmentFailed:
			// remove detachment failure error from AzVolumeAttachment CRI to retrigger detachment
			updateInstance.Status.Error = nil
			// revert attachment state to avoid confusion
			updateInstance.Status.State = azdiskv1beta2.Attached
		}
		w.AnnotateObject(updateInstance)
		updateInstance.Status.Annotations = azureutils.AddToMap(updateInstance.Status.Annotations, consts.VolumeDetachRequestAnnotation, "crdProvisioner")
		return nil
	}

	w.Logger().V(5).Infof("Requesting AzVolumeAttachment (%s) detachment", azVolumeAttachment.Name)
	if _, err = azureutils.UpdateCRIWithRetry(ctx, c.azCachedReader.azInformer, nil, c.azDiskClient, azVolumeAttachment, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateCRIStatus); err != nil {
		return err
	}

	return c.WaitForDetach(ctx, volumeID, nodeName)
}

func (c *CrdProvisioner) WaitForDetach(ctx context.Context, volumeID, nodeID string) error {
	var err error
	ctx, w := workflow.New(ctx, workflow.WithCaller(1))
	defer func() { w.Finish(err) }()

	var volumeName string
	volumeName, err = azureutils.GetDiskName(volumeID)
	if err != nil {
		return err
	}
	volumeName = strings.ToLower(volumeName)
	attachmentName := azureutils.GetAzVolumeAttachmentName(volumeName, nodeID)

	lister := c.azCachedReader.azInformer.Disk().V1beta2().AzVolumeAttachments().Lister().AzVolumeAttachments(c.config.ObjectNamespace)

	waiter := c.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeAttachmentType, attachmentName, waitForDetachVolumeFunc)
	defer waiter.Close()

	if _, err := lister.Get(attachmentName); apiErrors.IsNotFound(err) {
		return nil
	}

	_, err = waiter.Wait(ctx)
	return err
}

func (c *CrdProvisioner) ExpandVolume(
	ctx context.Context,
	volumeID string,
	capacityRange *azdiskv1beta2.CapacityRange,
	secrets map[string]string) (*azdiskv1beta2.AzVolumeStatusDetail, error) {
	var err error
	volumeName, err := azureutils.GetDiskName(volumeID)
	if err != nil {
		return nil, err
	}
	azVolumeName := strings.ToLower(volumeName)

	azVolume := &azdiskv1beta2.AzVolume{}
	err = c.azCachedReader.Get(ctx, types.NamespacedName{Namespace: c.config.ObjectNamespace, Name: azVolumeName}, azVolume)
	if err != nil || azVolume == nil {
		err = status.Errorf(codes.Internal, "failed to retrieve volume id (%s): %v", volumeID, err)
		return nil, err
	}

	ctx, w := workflow.New(ctx, workflow.WithDetails(workflow.GetObjectDetails(azVolume)...))
	defer func() { w.Finish(err) }()

	waiter := c.conditionWatcher.NewConditionWaiter(ctx, watcher.AzVolumeType, azVolumeName, waitForExpandVolumeFunc(capacityRange.RequiredBytes))
	defer waiter.Close()

	updateFunc := func(obj client.Object) error {
		updateInstance := obj.(*azdiskv1beta2.AzVolume)
		switch azVolume.Status.State {
		// if volume is still updating, return without updating
		case azdiskv1beta2.VolumeUpdating:
			return nil
		case azdiskv1beta2.VolumeUpdated:
			return nil
		case azdiskv1beta2.VolumeCreated:
		case azdiskv1beta2.VolumeUpdateFailed:
			updateInstance := obj.(*azdiskv1beta2.AzVolume)
			updateInstance.Status.Error = nil
			updateInstance.Status.State = azdiskv1beta2.VolumeCreated
		default:
			err = status.Errorf(codes.Internal, "unexpected expand volume request: AzVolume is currently in %s state", azVolume.Status.State)
			return err
		}
		w.AnnotateObject(updateInstance)
		updateInstance.Spec.CapacityRange = capacityRange
		return nil
	}

	if _, err = azureutils.UpdateCRIWithRetry(ctx, c.azCachedReader.azInformer, nil, c.azDiskClient, azVolume, updateFunc, consts.NormalUpdateMaxNetRetry, azureutils.UpdateAll); err != nil {
		return nil, err
	}

	var obj runtime.Object
	obj, err = waiter.Wait(ctx)
	if err != nil {
		err = status.Errorf(codes.Internal, "failed to update AzVolume CRI: %v", err)
		return nil, err
	}
	if obj == nil {
		err = status.Error(codes.Aborted, "failed to expand volume: unknown error")
		return nil, err
	}

	azVolumeInstance := obj.(*azdiskv1beta2.AzVolume)
	if azVolumeInstance.Status.Detail.CapacityBytes < capacityRange.RequiredBytes {
		err = status.Errorf(codes.Internal, "AzVolume status was not updated with the new capacity: current capacity (%d), requested capacity (%d)", azVolumeInstance.Status.Detail.CapacityBytes, capacityRange.RequiredBytes)
		return nil, err
	}

	return azVolumeInstance.Status.Detail, nil
}

func (c *CrdProvisioner) GetAzVolumeAttachment(ctx context.Context, volumeID string, nodeID string) (*azdiskv1beta2.AzVolumeAttachment, error) {
	diskName, err := azureutils.GetDiskName(volumeID)
	if err != nil {
		return nil, err
	}
	attachmentName := azureutils.GetAzVolumeAttachmentName(diskName, nodeID)
	azVolumeAttachmentInstance := &azdiskv1beta2.AzVolumeAttachment{}
	err = c.azCachedReader.Get(ctx, types.NamespacedName{Namespace: c.config.ObjectNamespace, Name: attachmentName}, azVolumeAttachmentInstance)
	if err != nil {
		return nil, err
	}

	return azVolumeAttachmentInstance, nil
}

func (c *CrdProvisioner) GetDiskClientSet() azdisk.Interface {
	return c.azDiskClient
}

func (c *CrdProvisioner) GetConditionWatcher() *watcher.ConditionWatcher {
	return c.conditionWatcher
}

func (c *CrdProvisioner) IsDriverUninstall() bool {
	return atomic.LoadUint32(&c.driverUninstallState) == 1
}

// Compares the fields in the AzVolumeSpec with the other parameters.
// Returns true if they are equal, false otherwise.
func isAzVolumeSpecSameAsRequestParams(defaultAzVolume *azdiskv1beta2.AzVolume,
	maxMountReplicaCount int,
	capacityRange *azdiskv1beta2.CapacityRange,
	parameters map[string]string,
	secrets map[string]string,
	volumeContentSource *azdiskv1beta2.ContentVolumeSource,
	accessibilityReq *azdiskv1beta2.TopologyRequirement) bool {
	// Since, reflect. DeepEqual doesn't treat nil and empty map/array as equal.
	// For comparison purpose, we want nil and empty map/array as equal.
	// Thus, modifyng the nil values to empty map/array for desired result.
	defaultParams := defaultAzVolume.Spec.Parameters
	defaultSecret := defaultAzVolume.Spec.Secrets
	defaultAccReq := defaultAzVolume.Spec.AccessibilityRequirements
	defaultVolContentSource := defaultAzVolume.Spec.ContentVolumeSource
	defaultCapRange := defaultAzVolume.Spec.CapacityRange
	if defaultParams == nil {
		defaultParams = make(map[string]string)
	}
	if parameters == nil {
		parameters = make(map[string]string)
	}
	if defaultSecret == nil {
		defaultSecret = make(map[string]string)
	}
	if secrets == nil {
		secrets = make(map[string]string)
	}
	if defaultAccReq == nil {
		defaultAccReq = &azdiskv1beta2.TopologyRequirement{}
	}
	if defaultAccReq.Preferred == nil {
		defaultAccReq.Preferred = []azdiskv1beta2.Topology{}
	}
	if defaultAccReq.Requisite == nil {
		defaultAccReq.Requisite = []azdiskv1beta2.Topology{}
	}
	if accessibilityReq == nil {
		accessibilityReq = &azdiskv1beta2.TopologyRequirement{}
	}
	if accessibilityReq.Preferred == nil {
		accessibilityReq.Preferred = []azdiskv1beta2.Topology{}
	}
	if accessibilityReq.Requisite == nil {
		accessibilityReq.Requisite = []azdiskv1beta2.Topology{}
	}
	if defaultVolContentSource == nil {
		defaultVolContentSource = &azdiskv1beta2.ContentVolumeSource{}
	}
	if volumeContentSource == nil {
		volumeContentSource = &azdiskv1beta2.ContentVolumeSource{}
	}
	if defaultCapRange == nil {
		defaultCapRange = &azdiskv1beta2.CapacityRange{}
	}
	if capacityRange == nil {
		capacityRange = &azdiskv1beta2.CapacityRange{}
	}

	return (defaultAzVolume.Spec.MaxMountReplicaCount == maxMountReplicaCount &&
		reflect.DeepEqual(defaultCapRange, capacityRange) &&
		reflect.DeepEqual(defaultParams, parameters) &&
		reflect.DeepEqual(defaultSecret, secrets) &&
		reflect.DeepEqual(defaultVolContentSource, volumeContentSource) &&
		reflect.DeepEqual(defaultAccReq, accessibilityReq))
}

func waitForCreateVolumeFunc(obj interface{}, objectDeleted bool) (bool, error) {
	if obj == nil || objectDeleted {
		return false, nil
	}
	azVolumeInstance := obj.(*azdiskv1beta2.AzVolume)
	if azVolumeInstance.Status.Detail != nil {
		return true, nil
	} else if azVolumeInstance.Status.Error != nil {
		return false, util.ErrorFromAzError(azVolumeInstance.Status.Error)
	}
	return false, nil
}

func waitForDeleteVolumeFunc(obj interface{}, objectDeleted bool) (bool, error) {
	// if no object is found, object is deleted
	if obj == nil || objectDeleted {
		return true, nil
	}

	// otherwise, the volume deletion has either failed with error or pending
	azVolumeInstance := obj.(*azdiskv1beta2.AzVolume)
	if azVolumeInstance.Status.Error != nil {
		return false, util.ErrorFromAzError(azVolumeInstance.Status.Error)
	}
	return false, nil
}

func waitForExpandVolumeFunc(newCapacity int64) func(interface{}, bool) (bool, error) {
	return func(obj interface{}, objectDeleted bool) (bool, error) {
		if obj == nil || objectDeleted {
			return false, nil
		}
		azVolumeInstance := obj.(*azdiskv1beta2.AzVolume)
		// Checking that the status is updated with the required capacityRange
		if azVolumeInstance.Status.Detail != nil && azVolumeInstance.Status.Detail.CapacityBytes >= newCapacity {
			return true, nil
		}
		if azVolumeInstance.Status.Error != nil {
			return false, util.ErrorFromAzError(azVolumeInstance.Status.Error)
		}
		return false, nil
	}
}

func waitForLunFunc(obj interface{}, objectDeleted bool) (bool, error) {
	if obj == nil || objectDeleted {
		return false, nil
	}
	azVolumeAttachmentInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
	if azVolumeAttachmentInstance.Status.Detail != nil && azVolumeAttachmentInstance.Status.Detail.PublishContext != nil {
		return true, nil
	}
	if azVolumeAttachmentInstance.Status.Error != nil {
		return false, util.ErrorFromAzError(azVolumeAttachmentInstance.Status.Error)
	}
	return false, nil
}

func waitForCRICreateFunc(obj interface{}, objectDeleted bool) (bool, error) {
	return obj != nil && !objectDeleted, nil
}

func waitForAttachVolumeFunc(obj interface{}, objectDeleted bool) (bool, error) {
	if obj == nil || objectDeleted {
		return false, nil
	}
	azVolumeAttachmentInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
	if azVolumeAttachmentInstance.Status.Detail != nil && azVolumeAttachmentInstance.Status.Detail.PublishContext != nil && azVolumeAttachmentInstance.Status.Detail.Role == azdiskv1beta2.PrimaryRole && azVolumeAttachmentInstance.Status.State == azdiskv1beta2.Attached {
		return true, nil
	}
	if azVolumeAttachmentInstance.Status.Error != nil {
		return false, util.ErrorFromAzError(azVolumeAttachmentInstance.Status.Error)
	}
	return false, nil
}

func waitForDetachVolumeFunc(obj interface{}, objectDeleted bool) (bool, error) {
	// if no object is found, return
	if obj == nil || objectDeleted {
		return true, nil
	}

	// otherwise, the volume detachment has either failed with error or pending
	azVolumeAttachmentInstance := obj.(*azdiskv1beta2.AzVolumeAttachment)
	if azVolumeAttachmentInstance.Status.Error != nil {
		return false, util.ErrorFromAzError(azVolumeAttachmentInstance.Status.Error)
	}
	return false, nil
}
