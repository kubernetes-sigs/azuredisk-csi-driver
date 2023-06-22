// /*
// Copyright 2021 The Kubernetes Authors.

// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at

//     http://www.apache.org/licenses/LICENSE-2.0

// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

package provisioner

// import (
// 	"context"
// 	"fmt"
// 	"regexp"
// 	"testing"
// 	"time"

// 	"github.com/golang/mock/gomock"
// 	"github.com/stretchr/testify/assert"
// 	"github.com/stretchr/testify/require"
// 	"google.golang.org/grpc/codes"
// 	"google.golang.org/grpc/status"
// 	v1 "k8s.io/api/core/v1"
// 	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
// 	"k8s.io/apimachinery/pkg/runtime"
// 	"k8s.io/klog/v2"

// 	crdClientset "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
// 	crdfakes "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
// 	crdInformers "k8s.io/apiextensions-apiserver/pkg/client/informers/externalversions"
// 	"k8s.io/client-go/informers"
// 	kubeClientset "k8s.io/client-go/kubernetes"
// 	fakev1 "k8s.io/client-go/kubernetes/fake"
// 	testingClient "k8s.io/client-go/testing"
// 	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
// 	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
// 	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
// 	azdiskinformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
// 	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
// 	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
// 	"sigs.k8s.io/azuredisk-csi-driver/pkg/watcher"
// )

// const (
// 	testResync = time.Duration(1) * time.Second
// )

// var (
// 	managedDiskPathRE             = regexp.MustCompile(`(?i).*/subscriptions/(?:.*)/resourceGroups/(?:.*)/providers/Microsoft.Compute/disks/(.+)`)
// 	defaultVolumeNameWithParam    = "default-volume-name-with-param"
// 	defaultVolumeNameWithNilParam = "default-volume-name-with-nil-param"
// 	invalidVolumeNameLength       = "invalid-volume-name-length-with-length-above-sixty-three-characters"
// 	invalidVolumeNameConvention   = "invalid-volume-name-convention-special-char-%$%"
// 	invalidDiskURI                = "/subscriptions/12345678-90ab-cedf-1234-567890abcdef/resourceGroupsrandomtext/test-rg/providers/Microsoft.Compute/disks/test-disk"
// 	testNodeName0                 = "test-node-name-0"
// 	testNodeName1                 = "test-node-name-1"
// 	testNodeName2                 = "test-node-name-2"
// 	testNamespace                 = "test-ns"

// 	defaultAzVolumeWithParamForComparison = azdiskv1beta2.AzVolume{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name: defaultVolumeNameWithParam,
// 		},
// 		Spec: azdiskv1beta2.AzVolumeSpec{
// 			VolumeName:           defaultVolumeNameWithParam,
// 			MaxMountReplicaCount: 2,
// 			VolumeCapability: []azdiskv1beta2.VolumeCapability{
// 				{
// 					AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 					AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 				},
// 			},
// 			CapacityRange: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 8,
// 				LimitBytes:    10,
// 			},
// 			Parameters: map[string]string{"skuname": "testname", "location": "westus2"},
// 			Secrets:    map[string]string{"test1": "test2"},
// 			ContentVolumeSource: &azdiskv1beta2.ContentVolumeSource{
// 				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
// 				ContentSourceID: "content-volume-source",
// 			},
// 			AccessibilityRequirements: &azdiskv1beta2.TopologyRequirement{
// 				Preferred: []azdiskv1beta2.Topology{
// 					{
// 						Segments: map[string]string{"region": "R1", "zone": "Z1"},
// 					},
// 					{
// 						Segments: map[string]string{"region": "R2", "zone": "Z2"},
// 					},
// 				},
// 				Requisite: []azdiskv1beta2.Topology{
// 					{
// 						Segments: map[string]string{"region": "R3", "zone": "Z3"},
// 					},
// 				},
// 			},
// 		},
// 		Status: azdiskv1beta2.AzVolumeStatus{},
// 	}

// 	defaultAzVolumeWithNilParamForComparison = azdiskv1beta2.AzVolume{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name: defaultVolumeNameWithNilParam,
// 		},
// 		Spec: azdiskv1beta2.AzVolumeSpec{
// 			VolumeName:           defaultVolumeNameWithNilParam,
// 			MaxMountReplicaCount: 1,
// 			VolumeCapability: []azdiskv1beta2.VolumeCapability{
// 				{
// 					AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 					AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 				},
// 			},
// 			AccessibilityRequirements: &azdiskv1beta2.TopologyRequirement{},
// 		},
// 		Status: azdiskv1beta2.AzVolumeStatus{},
// 	}

// 	defaultTopology = azdiskv1beta2.TopologyRequirement{
// 		Preferred: []azdiskv1beta2.Topology{
// 			{
// 				Segments: map[string]string{"region": "R1", "zone": "Z1"},
// 			},
// 			{
// 				Segments: map[string]string{"region": "R2", "zone": "Z2"},
// 			},
// 		},
// 		Requisite: []azdiskv1beta2.Topology{
// 			{
// 				Segments: map[string]string{"region": "R3", "zone": "Z3"},
// 			},
// 		},
// 	}

// 	successAzVolStatus = azdiskv1beta2.AzVolumeStatus{
// 		Detail: &azdiskv1beta2.AzVolumeStatusDetail{
// 			VolumeID: testDiskURI0,
// 		},
// 	}

// 	successAzVADetail = azdiskv1beta2.AzVolumeAttachmentStatusDetail{
// 		PublishContext: map[string]string{"test_key": "test_value"},
// 		Role:           azdiskv1beta2.PrimaryRole,
// 	}
// )

// func NewTestCrdProvisioner(controller *gomock.Controller) *CrdProvisioner {
// 	fakeDiskClient := azdiskfakes.NewSimpleClientset()
// 	fakeKubeClient := fakev1.NewSimpleClientset()
// 	fakeCrdClient := crdfakes.NewSimpleClientset()

// 	kubeInformerFactory := informers.NewSharedInformerFactory(fakeKubeClient, testResync)
// 	azInformerFactory := azdiskinformers.NewSharedInformerFactory(fakeDiskClient, testResync)
// 	crdInformerFactory := crdInformers.NewSharedInformerFactory(fakeCrdClient, consts.DefaultInformerResync)

// 	nodeInformer := azureutils.NewNodeInformer(kubeInformerFactory)
// 	azNodeInformer := azureutils.NewAzNodeInformer(azInformerFactory)
// 	azVolumeAttachmentInformer := azureutils.NewAzVolumeAttachmentInformer(azInformerFactory)
// 	azVolumeInformer := azureutils.NewAzVolumeInformer(azInformerFactory)
// 	crdInformer := azureutils.NewCrdInformer(crdInformerFactory)
// 	waitForLunEnabled := true

// 	conditionWatcher, _ := watcher.NewConditionWatcher(azInformerFactory, testNamespace, azNodeInformer, azVolumeAttachmentInformer, azVolumeInformer, crdInformer)
// 	config := azdiskv1beta2.AzDiskDriverConfiguration{
// 		ControllerConfig: azdiskv1beta2.ControllerConfiguration{
// 			WaitForLunEnabled: waitForLunEnabled,
// 		},
// 		ObjectNamespace: testNamespace,
// 	}

// 	c := &CrdProvisioner{
// 		azDiskClient:     fakeDiskClient,
// 		kubeClient:       fakeKubeClient,
// 		crdClient:        fakeCrdClient,
// 		config:           &config,
// 		conditionWatcher: conditionWatcher,
// 		azCachedReader:   NewCachedReader(kubeInformerFactory, azInformerFactory, testNamespace),
// 	}
// 	azureutils.StartInformersAndWaitForCacheSync(context.Background(), nodeInformer, azNodeInformer, azVolumeAttachmentInformer, azVolumeInformer)

// 	return c
// }

// func UpdateTestCrdProvisionerWithNewClient(provisioner *CrdProvisioner, azDiskClient azdisk.Interface, kubeClient kubeClientset.Interface, crdClient crdClientset.Interface) {
// 	azInformerFactory := azdiskinformers.NewSharedInformerFactory(azDiskClient, testResync)
// 	kubeInformerFactory := informers.NewSharedInformerFactory(kubeClient, testResync)
// 	crdInformerFactory := crdInformers.NewSharedInformerFactory(crdClient, consts.DefaultInformerResync)

// 	nodeInformer := azureutils.NewNodeInformer(kubeInformerFactory)
// 	azNodeInformer := azureutils.NewAzNodeInformer(azInformerFactory)
// 	azVolumeAttachmentInformer := azureutils.NewAzVolumeAttachmentInformer(azInformerFactory)
// 	azVolumeInformer := azureutils.NewAzVolumeInformer(azInformerFactory)
// 	crdInformer := azureutils.NewCrdInformer(crdInformerFactory)

// 	conditionWatcher, _ := watcher.NewConditionWatcher(azInformerFactory, testNamespace, azNodeInformer, azVolumeAttachmentInformer, azVolumeInformer, crdInformer)

// 	provisioner.azDiskClient = azDiskClient
// 	provisioner.kubeClient = kubeClient
// 	provisioner.crdClient = crdClient
// 	provisioner.conditionWatcher = conditionWatcher
// 	provisioner.azCachedReader = NewCachedReader(kubeInformerFactory, azInformerFactory, testNamespace)

// 	azureutils.StartInformersAndWaitForCacheSync(context.Background(), nodeInformer, azNodeInformer, azVolumeAttachmentInformer, azVolumeInformer)
// }

// func TestCrdProvisionerCreateVolume(t *testing.T) {
// 	mockCtrl := gomock.NewController(t)
// 	defer mockCtrl.Finish()
// 	provisioner := NewTestCrdProvisioner(mockCtrl)

// 	tests := []struct {
// 		description          string
// 		existingAzVolumes    []azdiskv1beta2.AzVolume
// 		volumeName           string
// 		definePrependReactor bool
// 		capacity             *azdiskv1beta2.CapacityRange
// 		capabilities         []azdiskv1beta2.VolumeCapability
// 		parameters           map[string]string
// 		secrets              map[string]string
// 		contentSource        *azdiskv1beta2.ContentVolumeSource
// 		topology             *azdiskv1beta2.TopologyRequirement
// 		expectedError        error
// 	}{
// 		{
// 			description:          "[Success] Create an AzVolume CRI with default parameters",
// 			existingAzVolumes:    nil,
// 			volumeName:           testDiskName0,
// 			definePrependReactor: true,
// 			capacity:             &azdiskv1beta2.CapacityRange{},
// 			capabilities: []azdiskv1beta2.VolumeCapability{
// 				{
// 					AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 					AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 				},
// 			},
// 			parameters:    map[string]string{consts.PvNameKey: testDiskName0},
// 			secrets:       make(map[string]string),
// 			contentSource: &azdiskv1beta2.ContentVolumeSource{},
// 			topology:      &azdiskv1beta2.TopologyRequirement{},
// 			expectedError: nil,
// 		},
// 		{
// 			description:          "[Success] Create an AzVolume CRI with specified parameters",
// 			existingAzVolumes:    nil,
// 			volumeName:           testDiskName0,
// 			definePrependReactor: true,
// 			capacity: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 2,
// 				LimitBytes:    2,
// 			},
// 			parameters: map[string]string{"location": "westus2", consts.PvNameKey: testDiskName0},
// 			secrets:    map[string]string{"test1": "No secret"},
// 			contentSource: &azdiskv1beta2.ContentVolumeSource{
// 				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
// 				ContentSourceID: "content-volume-source",
// 			},
// 			topology:      &defaultTopology,
// 			expectedError: nil,
// 		},
// 		{
// 			description:          "[Success] Create an AzVolume CRI with invalid volume name length",
// 			existingAzVolumes:    nil,
// 			volumeName:           invalidVolumeNameLength,
// 			definePrependReactor: true,
// 			capacity:             &azdiskv1beta2.CapacityRange{},
// 			capabilities: []azdiskv1beta2.VolumeCapability{
// 				{
// 					AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 					AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 				},
// 			},
// 			parameters:    map[string]string{consts.PvNameKey: testDiskName0},
// 			secrets:       make(map[string]string),
// 			contentSource: &azdiskv1beta2.ContentVolumeSource{},
// 			topology:      &azdiskv1beta2.TopologyRequirement{},
// 			expectedError: nil,
// 		},
// 		{
// 			description:          "[Success] Create an AzVolume CRI with volume name not following the conventions",
// 			existingAzVolumes:    nil,
// 			volumeName:           invalidVolumeNameConvention,
// 			definePrependReactor: true,
// 			capacity:             &azdiskv1beta2.CapacityRange{},
// 			capabilities: []azdiskv1beta2.VolumeCapability{
// 				{
// 					AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 					AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 				},
// 			},
// 			parameters:    map[string]string{consts.PvNameKey: testDiskName0},
// 			secrets:       make(map[string]string),
// 			contentSource: &azdiskv1beta2.ContentVolumeSource{},
// 			topology:      &azdiskv1beta2.TopologyRequirement{},
// 			expectedError: nil,
// 		},
// 		{
// 			description: "[Success] Return no error when AzVolume CRI exists with identical CreateVolume request parameters",
// 			existingAzVolumes: []azdiskv1beta2.AzVolume{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name:      testDiskName0,
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeSpec{
// 						VolumeName: testDiskName0,
// 						CapacityRange: &azdiskv1beta2.CapacityRange{
// 							RequiredBytes: 2,
// 							LimitBytes:    2,
// 						},
// 						VolumeCapability: []azdiskv1beta2.VolumeCapability{
// 							{
// 								AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 								AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 							},
// 						},
// 						ContentVolumeSource: &azdiskv1beta2.ContentVolumeSource{
// 							ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
// 							ContentSourceID: "content-volume-source",
// 						},
// 						Parameters:                map[string]string{"location": "westus2"},
// 						Secrets:                   map[string]string{"secret": "not really"},
// 						AccessibilityRequirements: &defaultTopology,
// 					},
// 					Status: azdiskv1beta2.AzVolumeStatus{
// 						Detail: &azdiskv1beta2.AzVolumeStatusDetail{
// 							VolumeID: testDiskURI0,
// 						},
// 						State: azdiskv1beta2.VolumeCreated,
// 					},
// 				},
// 			},
// 			volumeName:           testDiskName0,
// 			definePrependReactor: true,
// 			capacity: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 2,
// 				LimitBytes:    2,
// 			},
// 			capabilities: []azdiskv1beta2.VolumeCapability{
// 				{
// 					AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 					AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 				},
// 			},
// 			parameters: map[string]string{"location": "westus2"},
// 			secrets:    map[string]string{"secret": "not really"},
// 			contentSource: &azdiskv1beta2.ContentVolumeSource{
// 				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
// 				ContentSourceID: "content-volume-source",
// 			},
// 			topology:      &defaultTopology,
// 			expectedError: nil,
// 		},
// 		{
// 			description: "[Success] Update previous creation error in existing AzVolume CRI when CreateVolume request for same volumeName is passed",
// 			existingAzVolumes: []azdiskv1beta2.AzVolume{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name:      testDiskName0,
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeSpec{
// 						VolumeName: testDiskName0,
// 						CapacityRange: &azdiskv1beta2.CapacityRange{
// 							RequiredBytes: 2,
// 							LimitBytes:    2,
// 						},
// 						VolumeCapability: []azdiskv1beta2.VolumeCapability{
// 							{
// 								AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 								AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 							},
// 						},
// 						ContentVolumeSource: &azdiskv1beta2.ContentVolumeSource{
// 							ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
// 							ContentSourceID: "content-volume-source",
// 						},
// 						Parameters:                map[string]string{"location": "westus2"},
// 						Secrets:                   map[string]string{"secret": "not really"},
// 						AccessibilityRequirements: &defaultTopology,
// 					},
// 					Status: azdiskv1beta2.AzVolumeStatus{
// 						Error: &azdiskv1beta2.AzError{
// 							Message: "Test error message here",
// 						},
// 						State: azdiskv1beta2.VolumeCreationFailed,
// 					},
// 				},
// 			},
// 			volumeName:           testDiskName0,
// 			definePrependReactor: true,
// 			capacity: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 2,
// 				LimitBytes:    2,
// 			},
// 			capabilities: []azdiskv1beta2.VolumeCapability{
// 				{
// 					AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 					AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 				},
// 			},
// 			parameters: map[string]string{"location": "westus2"},
// 			secrets:    map[string]string{"secret": "not really"},
// 			contentSource: &azdiskv1beta2.ContentVolumeSource{
// 				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
// 				ContentSourceID: "content-volume-source",
// 			},
// 			topology:      &defaultTopology,
// 			expectedError: nil,
// 		},
// 		{
// 			description: "[Failure] Return AlreadyExists error when an AzVolume CRI exists with same volume name but different CreateVolume request parameters",
// 			existingAzVolumes: []azdiskv1beta2.AzVolume{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name:      testDiskName0,
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeSpec{
// 						VolumeName: testDiskName0,
// 						VolumeCapability: []azdiskv1beta2.VolumeCapability{
// 							{
// 								AccessType: azdiskv1beta2.VolumeCapabilityAccessBlock,
// 								AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 							},
// 						},
// 						Parameters: map[string]string{"parameter": "new params"},
// 					},
// 					Status: azdiskv1beta2.AzVolumeStatus{
// 						Detail: &azdiskv1beta2.AzVolumeStatusDetail{
// 							VolumeID:      testDiskURI0,
// 							CapacityBytes: 2,
// 						},
// 						State: azdiskv1beta2.VolumeCreated,
// 					},
// 				},
// 			},
// 			volumeName:           testDiskName0,
// 			definePrependReactor: false,
// 			capacity:             &azdiskv1beta2.CapacityRange{},
// 			capabilities: []azdiskv1beta2.VolumeCapability{
// 				{
// 					AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 					AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 				},
// 			},
// 			parameters:    make(map[string]string),
// 			secrets:       make(map[string]string),
// 			contentSource: &azdiskv1beta2.ContentVolumeSource{},
// 			topology:      &azdiskv1beta2.TopologyRequirement{},
// 			expectedError: status.Errorf(codes.AlreadyExists, "Volume with name (%s) already exists with different specifications", testDiskName0),
// 		},
// 	}

// 	for _, test := range tests {
// 		tt := test
// 		t.Run(tt.description, func(t *testing.T) {
// 			existingWatcher := provisioner.conditionWatcher
// 			existingAzCacheReader := provisioner.azCachedReader
// 			defer func() { provisioner.azCachedReader = existingAzCacheReader }()
// 			defer func() { provisioner.conditionWatcher = existingWatcher }()
// 			defer func() { provisioner.azDiskClient = azdiskfakes.NewSimpleClientset() }()
// 			defer func() { provisioner.kubeClient = fakev1.NewSimpleClientset() }()

// 			if tt.existingAzVolumes != nil {
// 				existingList := make([]runtime.Object, len(tt.existingAzVolumes))
// 				for itr, azVol := range tt.existingAzVolumes {
// 					azVol := azVol
// 					existingList[itr] = &azVol
// 				}
// 				provisioner.azDiskClient = azdiskfakes.NewSimpleClientset(existingList...)
// 			}

// 			UpdateTestCrdProvisionerWithNewClient(provisioner, provisioner.azDiskClient, provisioner.kubeClient, provisioner.crdClient)

// 			if tt.definePrependReactor {
// 				// Using the tracker to insert new object or
// 				// update the existing object as required
// 				tracker := provisioner.azDiskClient.(*azdiskfakes.Clientset).Tracker()

// 				provisioner.azDiskClient.(*azdiskfakes.Clientset).Fake.PrependReactor(
// 					"create",
// 					"azvolumes",
// 					func(action testingClient.Action) (bool, runtime.Object, error) {
// 						objCreated := action.(testingClient.CreateAction).GetObject().(*azdiskv1beta2.AzVolume)
// 						objCreated.Status = successAzVolStatus

// 						var err error
// 						if action.GetSubresource() == "" {
// 							err = tracker.Create(action.GetResource(), objCreated, action.GetNamespace())
// 						} else {
// 							err = tracker.Update(action.GetResource(), objCreated, action.GetNamespace())
// 						}

// 						if err != nil {
// 							return true, nil, err
// 						}

// 						return true, objCreated, nil
// 					})

// 				provisioner.azDiskClient.(*azdiskfakes.Clientset).Fake.PrependReactor(
// 					"update",
// 					"azvolumes",
// 					func(action testingClient.Action) (bool, runtime.Object, error) {
// 						objCreated := action.(testingClient.UpdateAction).GetObject().(*azdiskv1beta2.AzVolume)
// 						objCreated.Status = successAzVolStatus
// 						err := tracker.Update(action.GetResource(), objCreated, action.GetNamespace())

// 						if err != nil {
// 							return true, nil, err
// 						}
// 						return true, objCreated, nil
// 					})
// 			}

// 			output, outputErr := provisioner.CreateVolume(
// 				context.TODO(),
// 				tt.volumeName,
// 				tt.capacity,
// 				tt.capabilities,
// 				tt.parameters,
// 				tt.secrets,
// 				tt.contentSource,
// 				tt.topology)

// 			assert.Equal(t, tt.expectedError, outputErr)
// 			if outputErr == nil {
// 				assert.NotNil(t, output)
// 			}
// 		})
// 	}
// }

// func TestCrdProvisionerDeleteVolume(t *testing.T) {
// 	mockCtrl := gomock.NewController(t)
// 	defer mockCtrl.Finish()
// 	provisioner := NewTestCrdProvisioner(mockCtrl)

// 	tests := []struct {
// 		description       string
// 		existingAzVolumes []azdiskv1beta2.AzVolume
// 		diskURI           string
// 		secrets           map[string]string
// 		expectedError     error
// 	}{
// 		{
// 			description: "[Success] Delete an existing AzVolume CRI entry",
// 			existingAzVolumes: []azdiskv1beta2.AzVolume{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name:      testDiskName0,
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeSpec{
// 						VolumeName:           testDiskName0,
// 						MaxMountReplicaCount: 2,
// 						VolumeCapability: []azdiskv1beta2.VolumeCapability{
// 							{
// 								AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 								AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 							},
// 						},
// 					},
// 					Status: azdiskv1beta2.AzVolumeStatus{
// 						Detail: &azdiskv1beta2.AzVolumeStatusDetail{
// 							VolumeID: testDiskURI0,
// 						},
// 					},
// 				},
// 			},
// 			diskURI:       testDiskURI0,
// 			secrets:       nil,
// 			expectedError: nil,
// 		},
// 		{
// 			description: "[Success] Delete an existing AzVolume CRI entry when secrets is passed",
// 			existingAzVolumes: []azdiskv1beta2.AzVolume{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name:      testDiskName0,
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeSpec{
// 						VolumeName:           testDiskName0,
// 						MaxMountReplicaCount: 2,
// 						VolumeCapability: []azdiskv1beta2.VolumeCapability{
// 							{
// 								AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 								AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 							},
// 						},
// 					},
// 					Status: azdiskv1beta2.AzVolumeStatus{
// 						Detail: &azdiskv1beta2.AzVolumeStatusDetail{
// 							VolumeID: testDiskURI0,
// 						},
// 					},
// 				},
// 			},
// 			diskURI:       testDiskURI0,
// 			secrets:       map[string]string{"secret": "not really"},
// 			expectedError: nil,
// 		},
// 		{
// 			description:       "[Success] Return no error on invalid Disk URI in the DeleteVolume request",
// 			existingAzVolumes: nil,
// 			diskURI:           invalidDiskURI,
// 			secrets:           nil,
// 			expectedError:     nil,
// 		},
// 		{
// 			description:       "[Success] Return no error on missing AzVolume CRI for given Disk URI in the DeleteVolume request",
// 			existingAzVolumes: nil,
// 			diskURI:           missingDiskURI,
// 			secrets:           nil,
// 			expectedError:     nil,
// 		},
// 	}

// 	for _, test := range tests {
// 		tt := test
// 		t.Run(test.description, func(t *testing.T) {
// 			existingClient := provisioner.azDiskClient
// 			existingWatcher := provisioner.conditionWatcher
// 			existingAzCacheReader := provisioner.azCachedReader
// 			defer func() { provisioner.azCachedReader = existingAzCacheReader }()
// 			defer func() { provisioner.conditionWatcher = existingWatcher }()
// 			defer func() { provisioner.azDiskClient = existingClient }()
// 			defer func() { provisioner.kubeClient = fakev1.NewSimpleClientset() }()

// 			if tt.existingAzVolumes != nil {
// 				existingList := make([]runtime.Object, len(tt.existingAzVolumes))
// 				for itr, azVol := range tt.existingAzVolumes {
// 					azVol := azVol
// 					existingList[itr] = &azVol
// 				}
// 				provisioner.azDiskClient = azdiskfakes.NewSimpleClientset(existingList...)
// 			}

// 			UpdateTestCrdProvisionerWithNewClient(provisioner, provisioner.azDiskClient, provisioner.kubeClient, provisioner.crdClient)

// 			actualError := provisioner.DeleteVolume(
// 				context.TODO(),
// 				tt.diskURI,
// 				tt.secrets)

// 			assert.Equal(t, tt.expectedError, actualError)
// 		})
// 	}
// }

// func TestCrdProvisionerPublishVolume(t *testing.T) {
// 	mockCtrl := gomock.NewController(t)
// 	defer mockCtrl.Finish()
// 	provisioner := NewTestCrdProvisioner(mockCtrl)

// 	tests := []struct {
// 		description             string
// 		existingAzVolAttachment []azdiskv1beta2.AzVolumeAttachment
// 		maxMountReplicaCount    int
// 		diskURI                 string
// 		nodeID                  string
// 		volumeContext           map[string]string
// 		registerVolume          bool
// 		registerNode            bool
// 		definePrependReactor    bool
// 		expectedError           error
// 		verifyFunc              func(t *testing.T)
// 	}{
// 		{
// 			description:             "[Success] Create an AzVolumeAttachment CRI for valid diskURI and nodeID",
// 			existingAzVolAttachment: nil,
// 			diskURI:                 testDiskURI0,
// 			nodeID:                  testNodeName0,
// 			volumeContext:           make(map[string]string),
// 			definePrependReactor:    true,
// 			registerVolume:          true,
// 			registerNode:            true,
// 			expectedError:           nil,
// 		},
// 		{
// 			description:             "[Success] Create an AzVolumeAttachment CRI for valid diskURI, nodeID and volumeContext",
// 			existingAzVolAttachment: nil,
// 			diskURI:                 testDiskURI0,
// 			nodeID:                  testNodeName0,
// 			volumeContext:           map[string]string{"volume": "context"},
// 			registerVolume:          true,
// 			registerNode:            true,
// 			definePrependReactor:    true,
// 			expectedError:           nil,
// 		},
// 		{
// 			description: "[Success] Return no error when AzVolumeAttachment CRI with Details and PublishContext exists for the diskURI and nodeID",
// 			existingAzVolAttachment: []azdiskv1beta2.AzVolumeAttachment{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName0, testNodeName0),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName0,
// 							consts.VolumeNameLabel: testDiskURI0,
// 							consts.RoleLabel:       string(azdiskv1beta2.PrimaryRole),
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName0,
// 						VolumeID:      testDiskURI0,
// 						NodeName:      testNodeName0,
// 						VolumeContext: make(map[string]string),
// 						RequestedRole: azdiskv1beta2.PrimaryRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
// 							Role:           azdiskv1beta2.PrimaryRole,
// 							PublishContext: map[string]string{},
// 						},
// 						State: azdiskv1beta2.Attached},
// 				},
// 			},
// 			diskURI:              testDiskURI0,
// 			nodeID:               testNodeName0,
// 			volumeContext:        make(map[string]string),
// 			registerVolume:       true,
// 			registerNode:         true,
// 			definePrependReactor: true,
// 			expectedError:        nil,
// 		},
// 		{
// 			description: "[Success] Update an existing AzVolumeAttachment CRI with Error for the diskURI and nodeID",
// 			existingAzVolAttachment: []azdiskv1beta2.AzVolumeAttachment{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName0, testNodeName0),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName0,
// 							consts.VolumeNameLabel: testDiskURI0,
// 							consts.RoleLabel:       string(azdiskv1beta2.PrimaryRole),
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName0,
// 						VolumeID:      testDiskURI0,
// 						NodeName:      testNodeName0,
// 						VolumeContext: make(map[string]string),
// 						RequestedRole: azdiskv1beta2.PrimaryRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Error:       &azdiskv1beta2.AzError{},
// 						State:       azdiskv1beta2.AttachmentFailed,
// 						Annotations: map[string]string{consts.VolumeAttachRequestAnnotation: "crdProvisioner"},
// 					},
// 				},
// 			},
// 			diskURI:              testDiskURI0,
// 			nodeID:               testNodeName0,
// 			volumeContext:        make(map[string]string),
// 			registerVolume:       true,
// 			registerNode:         true,
// 			definePrependReactor: true,
// 			expectedError:        nil,
// 		},
// 		{
// 			description: "[Success] Detach replica AzVolumeAttachment if volume's maxShares have been saturated.",
// 			existingAzVolAttachment: []azdiskv1beta2.AzVolumeAttachment{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName0, testNodeName0),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName0,
// 							consts.VolumeNameLabel: testDiskName0,
// 							consts.RoleLabel:       string(azdiskv1beta2.ReplicaRole),
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName0,
// 						VolumeID:      testDiskURI0,
// 						NodeName:      testNodeName0,
// 						VolumeContext: make(map[string]string),
// 						RequestedRole: azdiskv1beta2.ReplicaRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
// 							Role:           azdiskv1beta2.ReplicaRole,
// 							PublishContext: map[string]string{},
// 						},
// 						State: azdiskv1beta2.Attached},
// 				},
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName0, testNodeName1),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName1,
// 							consts.VolumeNameLabel: testDiskName0,
// 							consts.RoleLabel:       string(azdiskv1beta2.ReplicaRole),
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName0,
// 						VolumeID:      testDiskURI0,
// 						NodeName:      testNodeName1,
// 						VolumeContext: make(map[string]string),
// 						RequestedRole: azdiskv1beta2.ReplicaRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
// 							Role:           azdiskv1beta2.ReplicaRole,
// 							PublishContext: map[string]string{},
// 						},
// 						State: azdiskv1beta2.Attached},
// 				},
// 			},
// 			maxMountReplicaCount: 1,
// 			diskURI:              testDiskURI0,
// 			nodeID:               testNodeName2,
// 			volumeContext:        make(map[string]string),
// 			registerVolume:       true,
// 			registerNode:         true,
// 			definePrependReactor: true,
// 			expectedError:        nil,
// 			verifyFunc: func(t *testing.T) {
// 				azVolumeAttachments, err := azureutils.GetAzVolumeAttachmentsForVolume(context.Background(), provisioner.azCachedReader, testDiskName0, azureutils.ReplicaOnly)
// 				assert.NoError(t, err)
// 				assert.Len(t, azVolumeAttachments, 1)
// 			},
// 		},
// 		{
// 			description: "[Success] Detach replica AzVolumeAttachment if node's attach limit has been reached.",
// 			existingAzVolAttachment: []azdiskv1beta2.AzVolumeAttachment{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName1, testNodeName0),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName0,
// 							consts.VolumeNameLabel: testDiskName1,
// 							consts.RoleLabel:       string(azdiskv1beta2.ReplicaRole),
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName1,
// 						VolumeID:      testDiskURI1,
// 						NodeName:      testNodeName0,
// 						VolumeContext: make(map[string]string),
// 						RequestedRole: azdiskv1beta2.ReplicaRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
// 							Role:           azdiskv1beta2.ReplicaRole,
// 							PublishContext: map[string]string{},
// 						},
// 						State: azdiskv1beta2.Attached},
// 				},
// 			},
// 			diskURI:              testDiskURI0,
// 			nodeID:               testNodeName0,
// 			volumeContext:        make(map[string]string),
// 			registerVolume:       true,
// 			registerNode:         true,
// 			definePrependReactor: true,
// 			expectedError:        nil,
// 			verifyFunc: func(t *testing.T) {
// 				azVolumeAttachments, err := azureutils.GetAzVolumeAttachmentsForVolume(context.Background(), provisioner.azCachedReader, testDiskName1, azureutils.ReplicaOnly)
// 				assert.NoError(t, err)
// 				assert.Len(t, azVolumeAttachments, 0)
// 			},
// 		},
// 		{
// 			description:             "[Failure] Return NotFound error when invalid diskURI is passed",
// 			existingAzVolAttachment: nil,
// 			diskURI:                 invalidDiskURI,
// 			nodeID:                  testNodeName0,
// 			volumeContext:           make(map[string]string),
// 			definePrependReactor:    false,
// 			expectedError:           status.Errorf(codes.NotFound, fmt.Sprintf("Error finding volume : could not get disk name from %s, correct format: %s", invalidDiskURI, managedDiskPathRE)),
// 		},
// 		{
// 			description:             "[Failure] Return NotFound error when volume does not exist",
// 			existingAzVolAttachment: nil,
// 			diskURI:                 testDiskURI0,
// 			nodeID:                  testNodeName0,
// 			volumeContext:           make(map[string]string),
// 			registerVolume:          false,
// 			registerNode:            true,
// 			definePrependReactor:    false,
// 			expectedError:           status.Errorf(codes.NotFound, fmt.Sprintf("volume (%s) does not exist: azvolume.disk.csi.azure.com \"%s\" not found", testDiskName0, testDiskName0)),
// 		},
// 		{
// 			description:             "[Failure] Return NotFound error when node does not exist",
// 			existingAzVolAttachment: nil,
// 			diskURI:                 testDiskURI0,
// 			nodeID:                  testNodeName0,
// 			volumeContext:           make(map[string]string),
// 			registerVolume:          true,
// 			registerNode:            false,
// 			definePrependReactor:    false,
// 			expectedError:           status.Errorf(codes.NotFound, fmt.Sprintf("node (%s) does not exist: azdrivernode.disk.csi.azure.com \"%s\" not found", testNodeName0, testNodeName0)),
// 		},
// 	}

// 	for _, test := range tests {
// 		tt := test
// 		t.Run(test.description, func(t *testing.T) {
// 			existingWatcher := provisioner.conditionWatcher
// 			existingAzclient := provisioner.azDiskClient
// 			existingKubeclient := provisioner.kubeClient
// 			existingAzCacheReader := provisioner.azCachedReader
// 			defer func() { provisioner.conditionWatcher = existingWatcher }()
// 			defer func() { provisioner.azDiskClient = existingAzclient }()
// 			defer func() { provisioner.kubeClient = existingKubeclient }()
// 			defer func() { provisioner.azCachedReader = existingAzCacheReader }()

// 			if tt.existingAzVolAttachment != nil || tt.registerNode || tt.registerVolume {
// 				azExistingList := make([]runtime.Object, len(tt.existingAzVolAttachment))
// 				var kubeExistingList []runtime.Object
// 				for itr, azVA := range tt.existingAzVolAttachment {
// 					azVA := azVA
// 					azExistingList[itr] = &azVA
// 				}
// 				if tt.registerVolume {
// 					diskName, err := azureutils.GetDiskName(tt.diskURI)
// 					if err == nil {
// 						azExistingList = append(azExistingList, createAzVolume(provisioner.config.ObjectNamespace, diskName, tt.maxMountReplicaCount))
// 					}
// 				}
// 				if tt.registerNode {
// 					kubeExistingList = append(kubeExistingList, &v1.Node{
// 						ObjectMeta: metav1.ObjectMeta{
// 							Name:   tt.nodeID,
// 							Labels: map[string]string{v1.LabelInstanceTypeStable: "BASIC_A0"},
// 						},
// 					})
// 					azExistingList = append(azExistingList, &azdiskv1beta2.AzDriverNode{
// 						ObjectMeta: metav1.ObjectMeta{
// 							Name:      tt.nodeID,
// 							Namespace: provisioner.config.ObjectNamespace,
// 						},
// 					})
// 				}
// 				provisioner.azDiskClient = azdiskfakes.NewSimpleClientset(azExistingList...)
// 				provisioner.kubeClient = fakev1.NewSimpleClientset(kubeExistingList...)
// 			}
// 			UpdateTestCrdProvisionerWithNewClient(provisioner, provisioner.azDiskClient, provisioner.kubeClient, provisioner.crdClient)

// 			if tt.definePrependReactor {
// 				// Using the tracker to insert new object or
// 				// update the existing object as required
// 				tracker := provisioner.azDiskClient.(*azdiskfakes.Clientset).Tracker()
// 				provisioner.azDiskClient.(*azdiskfakes.Clientset).Fake.PrependReactor(
// 					"create",
// 					"azvolumeattachments",
// 					func(action testingClient.Action) (bool, runtime.Object, error) {
// 						objCreated := action.(testingClient.CreateAction).GetObject().(*azdiskv1beta2.AzVolumeAttachment)
// 						objCreated.Status.Detail = &successAzVADetail

// 						var err error
// 						if action.GetSubresource() == "" {
// 							err = tracker.Create(action.GetResource(), objCreated, action.GetNamespace())
// 						} else {
// 							err = tracker.Update(action.GetResource(), objCreated, action.GetNamespace())
// 						}

// 						if err != nil {
// 							return true, nil, err
// 						}

// 						return true, objCreated, nil
// 					})

// 				provisioner.azDiskClient.(*azdiskfakes.Clientset).Fake.PrependReactor(
// 					"update",
// 					"azvolumeattachments",
// 					func(action testingClient.Action) (bool, runtime.Object, error) {
// 						objCreated := action.(testingClient.UpdateAction).GetObject().(*azdiskv1beta2.AzVolumeAttachment)
// 						var err error
// 						if azureutils.MapContains(objCreated.Status.Annotations, consts.VolumeDetachRequestAnnotation) {
// 							err = tracker.Delete(action.GetResource(), objCreated.Namespace, objCreated.Name)
// 						} else if azureutils.MapContains(objCreated.Status.Annotations, consts.VolumeAttachRequestAnnotation) {
// 							objCreated.Status.Detail = &successAzVADetail
// 							err = tracker.Update(action.GetResource(), objCreated, action.GetNamespace())
// 						}

// 						if err != nil {
// 							return true, nil, err
// 						}

// 						return true, objCreated, nil
// 					})
// 			}

// 			output, outputErr := provisioner.PublishVolume(
// 				context.TODO(),
// 				tt.diskURI,
// 				tt.nodeID,
// 				nil,
// 				false,
// 				make(map[string]string),
// 				tt.volumeContext,
// 			)

// 			assert.Equal(t, tt.expectedError, outputErr)
// 			if outputErr == nil {
// 				assert.NotNil(t, output)
// 			}

// 			if tt.verifyFunc != nil {
// 				tt.verifyFunc(t)
// 			}
// 		})
// 	}
// }

// func TestCrdProvisionerWaitForAttach(t *testing.T) {
// 	mockCtrl := gomock.NewController(t)
// 	defer mockCtrl.Finish()
// 	provisioner := NewTestCrdProvisioner(mockCtrl)

// 	tests := []struct {
// 		description             string
// 		existingAzVolAttachment []azdiskv1beta2.AzVolumeAttachment
// 		maxMountReplicaCount    int
// 		diskURI                 string
// 		nodeID                  string
// 		volumeContext           map[string]string
// 		definePrependReactor    bool
// 		expectedError           error
// 		verifyFunc              func(t *testing.T)
// 	}{
// 		{
// 			description: "[Success] Overwrite previous error state in an AzVolumeAttachment CRI",
// 			existingAzVolAttachment: []azdiskv1beta2.AzVolumeAttachment{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName0, testNodeName0),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName0,
// 							consts.VolumeNameLabel: testDiskURI0,
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName0,
// 						VolumeID:      testDiskURI0,
// 						NodeName:      testNodeName0,
// 						VolumeContext: make(map[string]string),
// 						RequestedRole: azdiskv1beta2.PrimaryRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Error: &azdiskv1beta2.AzError{
// 							Message: "Test error message here",
// 						},
// 						State: azdiskv1beta2.AttachmentFailed,
// 					},
// 				},
// 			},
// 			diskURI:              testDiskURI0,
// 			nodeID:               testNodeName0,
// 			volumeContext:        make(map[string]string),
// 			definePrependReactor: true,
// 			expectedError:        nil,
// 		},
// 		{
// 			description: "[Success] Detach replica AzVolumeAttachment if volume's maxShares have been saturated.",
// 			existingAzVolAttachment: []azdiskv1beta2.AzVolumeAttachment{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName0, testNodeName2),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName2,
// 							consts.VolumeNameLabel: testDiskName0,
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName0,
// 						VolumeID:      testDiskURI0,
// 						NodeName:      testNodeName2,
// 						VolumeContext: make(map[string]string),
// 						RequestedRole: azdiskv1beta2.PrimaryRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Error: &azdiskv1beta2.AzError{
// 							Message: "Test error message here",
// 						},
// 						State: azdiskv1beta2.AttachmentFailed,
// 					},
// 				},
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName0, testNodeName0),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName0,
// 							consts.VolumeNameLabel: testDiskName0,
// 							consts.RoleLabel:       string(azdiskv1beta2.ReplicaRole),
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName0,
// 						VolumeID:      testDiskURI0,
// 						NodeName:      testNodeName0,
// 						VolumeContext: make(map[string]string),
// 						RequestedRole: azdiskv1beta2.ReplicaRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
// 							Role:           azdiskv1beta2.ReplicaRole,
// 							PublishContext: map[string]string{},
// 						},
// 						State: azdiskv1beta2.Attached},
// 				},
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName0, testNodeName1),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName1,
// 							consts.VolumeNameLabel: testDiskName0,
// 							consts.RoleLabel:       string(azdiskv1beta2.ReplicaRole),
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName0,
// 						VolumeID:      testDiskURI0,
// 						NodeName:      testNodeName1,
// 						VolumeContext: make(map[string]string),
// 						RequestedRole: azdiskv1beta2.ReplicaRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
// 							Role:           azdiskv1beta2.ReplicaRole,
// 							PublishContext: map[string]string{},
// 						},
// 						State: azdiskv1beta2.Attached},
// 				},
// 			},
// 			maxMountReplicaCount: 1,
// 			diskURI:              testDiskURI0,
// 			nodeID:               testNodeName2,
// 			volumeContext:        make(map[string]string),
// 			definePrependReactor: true,
// 			expectedError:        nil,
// 			verifyFunc: func(t *testing.T) {
// 				azVolumeAttachments, err := azureutils.GetAzVolumeAttachmentsForVolume(context.Background(), provisioner.azCachedReader, testDiskName0, azureutils.ReplicaOnly)
// 				assert.NoError(t, err)
// 				assert.Len(t, azVolumeAttachments, 1)
// 			},
// 		},
// 		{
// 			description: "[Success] Detach replica AzVolumeAttachment if node's attach limit has been reached.",
// 			existingAzVolAttachment: []azdiskv1beta2.AzVolumeAttachment{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName0, testNodeName0),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName0,
// 							consts.VolumeNameLabel: testDiskName0,
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName0,
// 						VolumeID:      testDiskURI0,
// 						NodeName:      testNodeName0,
// 						VolumeContext: make(map[string]string),
// 						RequestedRole: azdiskv1beta2.PrimaryRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Error: &azdiskv1beta2.AzError{
// 							Message: "Test error message here",
// 						},
// 						State: azdiskv1beta2.AttachmentFailed,
// 					},
// 				},
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName1, testNodeName0),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName0,
// 							consts.VolumeNameLabel: testDiskName1,
// 							consts.RoleLabel:       string(azdiskv1beta2.ReplicaRole),
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName1,
// 						VolumeID:      testDiskURI1,
// 						NodeName:      testNodeName0,
// 						VolumeContext: make(map[string]string),
// 						RequestedRole: azdiskv1beta2.ReplicaRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
// 							Role:           azdiskv1beta2.ReplicaRole,
// 							PublishContext: map[string]string{},
// 						},
// 						State: azdiskv1beta2.Attached},
// 				},
// 			},
// 			diskURI:              testDiskURI0,
// 			nodeID:               testNodeName0,
// 			volumeContext:        make(map[string]string),
// 			definePrependReactor: true,
// 			expectedError:        nil,
// 			verifyFunc: func(t *testing.T) {
// 				azVolumeAttachments, err := azureutils.GetAzVolumeAttachmentsForVolume(context.Background(), provisioner.azCachedReader, testDiskName0, azureutils.ReplicaOnly)
// 				assert.NoError(t, err)
// 				assert.Len(t, azVolumeAttachments, 0)
// 			},
// 		},
// 	}

// 	for _, test := range tests {
// 		tt := test
// 		t.Run(test.description, func(t *testing.T) {
// 			existingWatcher := provisioner.conditionWatcher
// 			existingAzClient := provisioner.azDiskClient
// 			existingKubeClent := provisioner.kubeClient
// 			existingAzCacheReader := provisioner.azCachedReader
// 			defer func() { provisioner.azCachedReader = existingAzCacheReader }()
// 			defer func() { provisioner.conditionWatcher = existingWatcher }()
// 			defer func() { provisioner.azDiskClient = existingAzClient }()
// 			defer func() { provisioner.kubeClient = existingKubeClent }()

// 			var existingAzList []runtime.Object
// 			var existingKubeList []runtime.Object

// 			if tt.existingAzVolAttachment != nil {
// 				for _, azVA := range tt.existingAzVolAttachment {
// 					azVA := azVA
// 					existingAzList = append(existingAzList, &azVA)
// 				}
// 			}

// 			diskName, err := azureutils.GetDiskName(tt.diskURI)
// 			require.Nil(t, err)
// 			azV := createAzVolume(provisioner.config.ObjectNamespace, diskName, tt.maxMountReplicaCount)
// 			existingAzList = append(existingAzList, azV)

// 			node := &v1.Node{
// 				ObjectMeta: metav1.ObjectMeta{
// 					Name:   tt.nodeID,
// 					Labels: map[string]string{v1.LabelInstanceTypeStable: "BASIC_A0"},
// 				}}

// 			existingKubeList = append(existingKubeList, node)

// 			provisioner.azDiskClient = azdiskfakes.NewSimpleClientset(existingAzList...)
// 			provisioner.kubeClient = fakev1.NewSimpleClientset(existingKubeList...)
// 			UpdateTestCrdProvisionerWithNewClient(provisioner, provisioner.azDiskClient, provisioner.kubeClient, provisioner.crdClient)

// 			if tt.definePrependReactor {
// 				// Using the tracker to insert new object or
// 				// update the existing object as required
// 				tracker := provisioner.azDiskClient.(*azdiskfakes.Clientset).Tracker()

// 				provisioner.azDiskClient.(*azdiskfakes.Clientset).Fake.PrependReactor(
// 					"update",
// 					"azvolumeattachments",
// 					func(action testingClient.Action) (bool, runtime.Object, error) {
// 						objCreated := action.(testingClient.UpdateAction).GetObject().(*azdiskv1beta2.AzVolumeAttachment)

// 						if azureutils.MapContains(objCreated.Status.Annotations, consts.VolumeDetachRequestAnnotation) {
// 							err = tracker.Delete(action.GetResource(), objCreated.Namespace, objCreated.Name)
// 						} else {
// 							objCreated.Status.Detail = &successAzVADetail
// 							objCreated.Status.State = azdiskv1beta2.Attached
// 							err = tracker.Update(action.GetResource(), objCreated, action.GetNamespace())
// 						}

// 						if err != nil {
// 							return true, nil, err
// 						}
// 						return true, objCreated, nil
// 					})
// 			}

// 			output, outputErr := provisioner.WaitForAttach(
// 				context.TODO(),
// 				tt.diskURI,
// 				tt.nodeID)

// 			assert.Equal(t, tt.expectedError, outputErr)
// 			if outputErr == nil {
// 				assert.NotNil(t, output)
// 			}

// 			if tt.verifyFunc != nil {
// 				tt.verifyFunc(t)
// 			}
// 		})
// 	}
// }

// func TestCrdProvisionerUnpublishVolume(t *testing.T) {
// 	mockCtrl := gomock.NewController(t)
// 	defer mockCtrl.Finish()
// 	provisioner := NewTestCrdProvisioner(mockCtrl)

// 	tests := []struct {
// 		description             string
// 		existingAzVolAttachment []azdiskv1beta2.AzVolumeAttachment
// 		existingAzVolume        []azdiskv1beta2.AzVolume
// 		diskURI                 string
// 		nodeID                  string
// 		secrets                 map[string]string
// 		verifyDemotion          bool
// 		definePrependReactor    bool
// 		expectedError           error
// 	}{
// 		{
// 			description: "[Success] Delete an AzVolumeAttachment CRI for valid diskURI and nodeID when volume's maxMountReplicaCount is 0",
// 			existingAzVolume: []azdiskv1beta2.AzVolume{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name:      testDiskName0,
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeSpec{
// 						MaxMountReplicaCount: 0,
// 					},
// 				},
// 			},
// 			existingAzVolAttachment: []azdiskv1beta2.AzVolumeAttachment{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName0, testNodeName0),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName0,
// 							consts.VolumeNameLabel: testDiskURI0,
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName0,
// 						VolumeID:      testDiskURI0,
// 						NodeName:      testNodeName0,
// 						VolumeContext: nil,
// 						RequestedRole: azdiskv1beta2.PrimaryRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
// 							Role:           azdiskv1beta2.PrimaryRole,
// 							PublishContext: map[string]string{},
// 						},
// 						State: azdiskv1beta2.Attached,
// 					},
// 				},
// 			},
// 			diskURI:              testDiskURI0,
// 			nodeID:               testNodeName0,
// 			definePrependReactor: true,
// 			secrets:              nil,
// 			expectedError:        nil,
// 		},
// 		{
// 			description: "[Success] Delete an AzVolumeAttachment CRI for valid diskURI, nodeID and secrets when volume's maxMountReplicaCount is 0",
// 			existingAzVolume: []azdiskv1beta2.AzVolume{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name:      testDiskName0,
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeSpec{
// 						MaxMountReplicaCount: 0,
// 					},
// 				},
// 			},
// 			existingAzVolAttachment: []azdiskv1beta2.AzVolumeAttachment{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName0, testNodeName0),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName0,
// 							consts.VolumeNameLabel: testDiskURI0,
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName0,
// 						VolumeID:      testDiskURI0,
// 						NodeName:      testNodeName0,
// 						VolumeContext: nil,
// 						RequestedRole: azdiskv1beta2.PrimaryRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
// 							Role:           azdiskv1beta2.PrimaryRole,
// 							PublishContext: map[string]string{},
// 						},
// 						State: azdiskv1beta2.Attached,
// 					},
// 				},
// 			},
// 			diskURI:              testDiskURI0,
// 			nodeID:               testNodeName0,
// 			definePrependReactor: true,
// 			secrets:              map[string]string{"secret": "not really"},
// 			expectedError:        nil,
// 		},
// 		{
// 			description: "[Success] Demote primary AzVolumeAttachment CRI for valid diskURI and nodeID when volume's maxMountReplicaCount is larger than 0",
// 			existingAzVolume: []azdiskv1beta2.AzVolume{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name:      testDiskName0,
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeSpec{
// 						MaxMountReplicaCount: 1,
// 					},
// 				},
// 			},
// 			existingAzVolAttachment: []azdiskv1beta2.AzVolumeAttachment{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName0, testNodeName0),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName0,
// 							consts.VolumeNameLabel: testDiskURI0,
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName0,
// 						VolumeID:      testDiskURI0,
// 						NodeName:      testNodeName0,
// 						VolumeContext: nil,
// 						RequestedRole: azdiskv1beta2.PrimaryRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
// 							Role:           azdiskv1beta2.PrimaryRole,
// 							PublishContext: map[string]string{},
// 						},
// 						State: azdiskv1beta2.Attached,
// 					},
// 				},
// 			},
// 			diskURI:              testDiskURI0,
// 			nodeID:               testNodeName0,
// 			secrets:              nil,
// 			verifyDemotion:       true,
// 			definePrependReactor: true,
// 			expectedError:        nil,
// 		},
// 		{
// 			description: "[Success] Demote primary AzVolumeAttachment CRI for valid diskURI, nodeID and secrets when volume's maxMountReplicaCount is larger than 0",
// 			existingAzVolume: []azdiskv1beta2.AzVolume{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name:      testDiskName0,
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeSpec{
// 						MaxMountReplicaCount: 1,
// 					},
// 				},
// 			},
// 			existingAzVolAttachment: []azdiskv1beta2.AzVolumeAttachment{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name: azureutils.GetAzVolumeAttachmentName(testDiskName0, testNodeName0),
// 						Labels: map[string]string{
// 							consts.NodeNameLabel:   testNodeName0,
// 							consts.VolumeNameLabel: testDiskURI0,
// 						},
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeAttachmentSpec{
// 						VolumeName:    testDiskName0,
// 						VolumeID:      testDiskURI0,
// 						NodeName:      testNodeName0,
// 						VolumeContext: nil,
// 						RequestedRole: azdiskv1beta2.PrimaryRole,
// 					},
// 					Status: azdiskv1beta2.AzVolumeAttachmentStatus{
// 						Detail: &azdiskv1beta2.AzVolumeAttachmentStatusDetail{
// 							Role:           azdiskv1beta2.PrimaryRole,
// 							PublishContext: map[string]string{},
// 						},
// 						State: azdiskv1beta2.Attached,
// 					},
// 				},
// 			},
// 			diskURI:              testDiskURI0,
// 			nodeID:               testNodeName0,
// 			secrets:              map[string]string{"secret": "not really"},
// 			verifyDemotion:       true,
// 			definePrependReactor: true,
// 			expectedError:        nil,
// 		},
// 		{
// 			description:             "[Success] Return no error when an AzVolumeAttachment CRI for diskURI and nodeID is not found",
// 			existingAzVolAttachment: nil,
// 			diskURI:                 missingDiskURI,
// 			nodeID:                  testNodeName0,
// 			secrets:                 nil,
// 			expectedError:           nil,
// 		},
// 		{
// 			description:             "[Failure] Return NotFound error when invalid diskURI is passed",
// 			existingAzVolAttachment: nil,
// 			diskURI:                 invalidDiskURI,
// 			nodeID:                  testNodeName0,
// 			secrets:                 nil,
// 			expectedError:           fmt.Errorf("could not get disk name from %s, correct format: %s", invalidDiskURI, managedDiskPathRE),
// 		},
// 	}

// 	for _, test := range tests {
// 		tt := test
// 		t.Run(test.description, func(t *testing.T) {
// 			existingWatcher := provisioner.conditionWatcher
// 			existingClient := provisioner.azDiskClient
// 			existingAzCacheReader := provisioner.azCachedReader
// 			defer func() { provisioner.azCachedReader = existingAzCacheReader }()
// 			defer func() { provisioner.conditionWatcher = existingWatcher }()
// 			defer func() { provisioner.azDiskClient = existingClient }()

// 			if tt.existingAzVolAttachment != nil || tt.existingAzVolume != nil {
// 				existingList := make([]runtime.Object, len(tt.existingAzVolAttachment)+len(tt.existingAzVolume))
// 				for itr, azVA := range tt.existingAzVolAttachment {
// 					azVA := azVA
// 					existingList[itr] = &azVA
// 				}
// 				for itr, azV := range tt.existingAzVolume {
// 					azV := azV
// 					existingList[itr+len(tt.existingAzVolAttachment)] = &azV
// 				}
// 				provisioner.azDiskClient = azdiskfakes.NewSimpleClientset(existingList...)
// 			}

// 			UpdateTestCrdProvisionerWithNewClient(provisioner, provisioner.azDiskClient, provisioner.kubeClient, provisioner.crdClient)

// 			if tt.definePrependReactor {
// 				// Using the tracker to insert new object or
// 				// update the existing object as required
// 				tracker := provisioner.azDiskClient.(*azdiskfakes.Clientset).Tracker()

// 				provisioner.azDiskClient.(*azdiskfakes.Clientset).Fake.PrependReactor(
// 					"update",
// 					"azvolumeattachments",
// 					func(action testingClient.Action) (bool, runtime.Object, error) {
// 						objCreated := action.(testingClient.UpdateAction).GetObject().(*azdiskv1beta2.AzVolumeAttachment)
// 						var err error
// 						if objCreated.Status.Detail != nil && (objCreated.Spec.RequestedRole != objCreated.Status.Detail.Role) {
// 							objCreated.Status.Detail.PreviousRole = objCreated.Status.Detail.Role
// 							objCreated.Status.Detail.Role = objCreated.Spec.RequestedRole
// 							err = tracker.Update(action.GetResource(), objCreated, action.GetNamespace())
// 						}
// 						klog.Info("oy?")

// 						if azureutils.MapContains(objCreated.Status.Annotations, consts.VolumeDetachRequestAnnotation) {
// 							err = tracker.Delete(action.GetResource(), objCreated.Namespace, objCreated.Name)
// 							klog.Info("oy")
// 						}

// 						if err != nil {
// 							return true, nil, err
// 						}
// 						return true, objCreated, nil
// 					})
// 			}

// 			outputErr := provisioner.UnpublishVolume(
// 				context.TODO(),
// 				tt.diskURI,
// 				tt.nodeID,
// 				tt.secrets,
// 				consts.DemoteOrDetach)

// 			assert.Equal(t, tt.expectedError, outputErr)

// 			if tt.verifyDemotion {
// 				for _, azVA := range tt.existingAzVolAttachment {
// 					updated, err := provisioner.azDiskClient.DiskV1beta2().AzVolumeAttachments(provisioner.config.ObjectNamespace).Get(context.TODO(), azVA.Name, metav1.GetOptions{})
// 					assert.NoError(t, err)
// 					assert.Equal(t, azdiskv1beta2.ReplicaRole, updated.Status.Detail.Role)
// 				}
// 			}
// 		})
// 	}
// }

// func TestCrdProvisionerExpandVolume(t *testing.T) {
// 	mockCtrl := gomock.NewController(t)
// 	defer mockCtrl.Finish()
// 	provisioner := NewTestCrdProvisioner(mockCtrl)

// 	tests := []struct {
// 		description          string
// 		existingAzVolumes    []azdiskv1beta2.AzVolume
// 		diskURI              string
// 		capacityRange        *azdiskv1beta2.CapacityRange
// 		secrets              map[string]string
// 		definePrependReactor bool
// 		expectedError        error
// 	}{
// 		{
// 			description: "[Success] Update the CapacityBytes for an existing AzVolume CRI with the given diskURI and new capacity range",
// 			existingAzVolumes: []azdiskv1beta2.AzVolume{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name:      testDiskName0,
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeSpec{
// 						VolumeName: testDiskName0,
// 						VolumeCapability: []azdiskv1beta2.VolumeCapability{
// 							{
// 								AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 								AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 							},
// 						},
// 						CapacityRange: &azdiskv1beta2.CapacityRange{
// 							RequiredBytes: 3,
// 							LimitBytes:    3,
// 						},
// 					},
// 					Status: azdiskv1beta2.AzVolumeStatus{
// 						Detail: &azdiskv1beta2.AzVolumeStatusDetail{
// 							VolumeID: testDiskURI0,
// 						},
// 						State: azdiskv1beta2.VolumeCreated,
// 					},
// 				},
// 			},
// 			diskURI: testDiskURI0,
// 			capacityRange: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 4,
// 				LimitBytes:    4,
// 			},
// 			secrets:              nil,
// 			definePrependReactor: true,
// 			expectedError:        nil,
// 		},
// 		{
// 			description: "[Success] Update the CapacityBytes for an existing AzVolume CRI with the given diskURI, new capacity range and secrets",
// 			existingAzVolumes: []azdiskv1beta2.AzVolume{
// 				{
// 					ObjectMeta: metav1.ObjectMeta{
// 						Name:      testDiskName0,
// 						Namespace: provisioner.config.ObjectNamespace,
// 					},
// 					Spec: azdiskv1beta2.AzVolumeSpec{
// 						VolumeName: testDiskName0,
// 						VolumeCapability: []azdiskv1beta2.VolumeCapability{
// 							{
// 								AccessType: azdiskv1beta2.VolumeCapabilityAccessMount,
// 								AccessMode: azdiskv1beta2.VolumeCapabilityAccessModeSingleNodeWriter,
// 							},
// 						},
// 						CapacityRange: &azdiskv1beta2.CapacityRange{
// 							RequiredBytes: 3,
// 							LimitBytes:    3,
// 						},
// 					},
// 					Status: azdiskv1beta2.AzVolumeStatus{
// 						Detail: &azdiskv1beta2.AzVolumeStatusDetail{
// 							VolumeID: testDiskURI0,
// 						},
// 						State: azdiskv1beta2.VolumeCreated,
// 					},
// 				},
// 			},
// 			diskURI: testDiskURI0,
// 			capacityRange: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 4,
// 				LimitBytes:    4,
// 			},
// 			secrets:              map[string]string{"secret": "not really"},
// 			definePrependReactor: true,
// 			expectedError:        nil,
// 		},
// 		{
// 			description:       "[Failure] Return an error when the AzVolume CRI with the given diskURI doesn't exist",
// 			existingAzVolumes: nil,
// 			diskURI:           testDiskURI0,
// 			capacityRange: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 4,
// 				LimitBytes:    4,
// 			},
// 			secrets:              nil,
// 			definePrependReactor: false,
// 			expectedError:        status.Error(codes.Internal, fmt.Sprintf("failed to retrieve volume id (%s): azvolume.disk.csi.azure.com \"%s\" not found", testDiskURI0, testDiskName0)),
// 		},
// 	}

// 	for _, test := range tests {
// 		tt := test
// 		t.Run(test.description, func(t *testing.T) {
// 			existingWatcher := provisioner.conditionWatcher
// 			existingClient := provisioner.azDiskClient
// 			existingAzCacheReader := provisioner.azCachedReader
// 			defer func() { provisioner.azCachedReader = existingAzCacheReader }()
// 			defer func() { provisioner.conditionWatcher = existingWatcher }()
// 			defer func() { provisioner.azDiskClient = existingClient }()

// 			if tt.existingAzVolumes != nil {
// 				existingList := make([]runtime.Object, len(tt.existingAzVolumes))
// 				for itr, azVol := range tt.existingAzVolumes {
// 					azVol := azVol
// 					existingList[itr] = &azVol
// 				}
// 				provisioner.azDiskClient = azdiskfakes.NewSimpleClientset(existingList...)
// 			}

// 			UpdateTestCrdProvisionerWithNewClient(provisioner, provisioner.azDiskClient, provisioner.kubeClient, provisioner.crdClient)

// 			if tt.definePrependReactor {
// 				// Using the tracker to insert new object or
// 				// update the existing object as required
// 				tracker := provisioner.azDiskClient.(*azdiskfakes.Clientset).Tracker()

// 				provisioner.azDiskClient.(*azdiskfakes.Clientset).Fake.PrependReactor(
// 					"update",
// 					"azvolumes",
// 					func(action testingClient.Action) (bool, runtime.Object, error) {
// 						objPresent := action.(testingClient.UpdateAction).GetObject().(*azdiskv1beta2.AzVolume)
// 						objPresent.Status.Detail.CapacityBytes = tt.capacityRange.RequiredBytes

// 						err := tracker.Update(action.GetResource(), objPresent, action.GetNamespace())
// 						if err != nil {
// 							return true, nil, err
// 						}

// 						return true, objPresent, nil
// 					})
// 			}

// 			output, outputErr := provisioner.ExpandVolume(
// 				context.TODO(),
// 				tt.diskURI,
// 				tt.capacityRange,
// 				tt.secrets)

// 			assert.Equal(t, tt.expectedError, outputErr)
// 			if outputErr == nil {
// 				assert.NotNil(t, output)
// 			}
// 		})
// 	}
// }

// func TestIsAzVolumeSpecSameAsRequestParams(t *testing.T) {
// 	tests := []struct {
// 		description          string
// 		azVolume             azdiskv1beta2.AzVolume
// 		maxMountReplicaCount int
// 		capacityRange        *azdiskv1beta2.CapacityRange
// 		parameters           map[string]string
// 		secrets              map[string]string
// 		volumeContentSource  *azdiskv1beta2.ContentVolumeSource
// 		accessibilityReq     *azdiskv1beta2.TopologyRequirement
// 		expectedOutput       bool
// 	}{
// 		{
// 			description:          "Verify comparison when all the values are identical and non-nil",
// 			azVolume:             defaultAzVolumeWithParamForComparison,
// 			maxMountReplicaCount: 2,
// 			capacityRange: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 8,
// 				LimitBytes:    10,
// 			},
// 			parameters: map[string]string{"skuname": "testname", "location": "westus2"},
// 			secrets:    map[string]string{"test1": "test2"},
// 			volumeContentSource: &azdiskv1beta2.ContentVolumeSource{
// 				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
// 				ContentSourceID: "content-volume-source",
// 			},
// 			accessibilityReq: &defaultTopology,
// 			expectedOutput:   true,
// 		},
// 		{
// 			description:          "Verify comparison when values are mismatched and non-nil Parameters map",
// 			azVolume:             defaultAzVolumeWithParamForComparison,
// 			maxMountReplicaCount: 2,
// 			capacityRange: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 8,
// 				LimitBytes:    10,
// 			},
// 			parameters: map[string]string{"skuname": "testname1", "location": "westus2"},
// 			secrets:    map[string]string{"test1": "test2"},
// 			volumeContentSource: &azdiskv1beta2.ContentVolumeSource{
// 				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
// 				ContentSourceID: "content-volume-source",
// 			},
// 			accessibilityReq: &defaultTopology,
// 			expectedOutput:   false,
// 		},
// 		{
// 			description:          "Verify comparison when values are mismatched and non-nil Secrets map",
// 			azVolume:             defaultAzVolumeWithParamForComparison,
// 			maxMountReplicaCount: 2,
// 			capacityRange: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 8,
// 				LimitBytes:    10,
// 			},
// 			parameters: map[string]string{"skuname": "testname", "location": "westus2"},
// 			secrets:    map[string]string{"test1": "test3"},
// 			volumeContentSource: &azdiskv1beta2.ContentVolumeSource{
// 				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
// 				ContentSourceID: "content-volume-source",
// 			},
// 			accessibilityReq: &defaultTopology,
// 			expectedOutput:   false,
// 		},
// 		{
// 			description:          "Verify comparison when values are mismatched and non-nil ContentVolumeSource object",
// 			azVolume:             defaultAzVolumeWithParamForComparison,
// 			maxMountReplicaCount: 2,
// 			capacityRange: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 8,
// 				LimitBytes:    10,
// 			},
// 			parameters: map[string]string{"skuname": "testname", "location": "westus2"},
// 			secrets:    map[string]string{"test1": "test2"},
// 			volumeContentSource: &azdiskv1beta2.ContentVolumeSource{
// 				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeSnapshot,
// 				ContentSourceID: "content-snapshot-source",
// 			},
// 			accessibilityReq: &defaultTopology,
// 			expectedOutput:   false,
// 		},
// 		{
// 			description:          "Verify comparison when values are mismatched for MaxMountReplicaCount value",
// 			azVolume:             defaultAzVolumeWithParamForComparison,
// 			maxMountReplicaCount: 4,
// 			capacityRange: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 8,
// 				LimitBytes:    10,
// 			},
// 			parameters: map[string]string{"skuname": "testname", "location": "westus2"},
// 			secrets:    map[string]string{"test1": "test2"},
// 			volumeContentSource: &azdiskv1beta2.ContentVolumeSource{
// 				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
// 				ContentSourceID: "content-volume-source",
// 			},
// 			accessibilityReq: &defaultTopology,
// 			expectedOutput:   false,
// 		},
// 		{
// 			description:          "Verify comparison when values are mismatched and non-nil CapacityRange object",
// 			azVolume:             defaultAzVolumeWithParamForComparison,
// 			maxMountReplicaCount: 2,
// 			capacityRange: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 9,
// 				LimitBytes:    10,
// 			},
// 			parameters: map[string]string{"skuname": "testname", "location": "westus2"},
// 			secrets:    map[string]string{"test1": "test2"},
// 			volumeContentSource: &azdiskv1beta2.ContentVolumeSource{
// 				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
// 				ContentSourceID: "content-volume-source",
// 			},
// 			accessibilityReq: &defaultTopology,
// 			expectedOutput:   false,
// 		},
// 		{
// 			description:          "Verify comparison when values are mismatched and non-nil AccessibilityRequirements object",
// 			azVolume:             defaultAzVolumeWithParamForComparison,
// 			maxMountReplicaCount: 2,
// 			capacityRange: &azdiskv1beta2.CapacityRange{
// 				RequiredBytes: 8,
// 				LimitBytes:    10,
// 			},
// 			parameters: map[string]string{"skuname": "testname", "location": "westus2"},
// 			secrets:    map[string]string{"test1": "test2"},
// 			volumeContentSource: &azdiskv1beta2.ContentVolumeSource{
// 				ContentSource:   azdiskv1beta2.ContentVolumeSourceTypeVolume,
// 				ContentSourceID: "content-volume-source",
// 			},
// 			accessibilityReq: &azdiskv1beta2.TopologyRequirement{
// 				Preferred: []azdiskv1beta2.Topology{
// 					{
// 						Segments: map[string]string{"region": "R1", "zone": "Z1"},
// 					},
// 					{
// 						Segments: map[string]string{"region": "R2", "zone": "Z3"},
// 					},
// 				},
// 				Requisite: []azdiskv1beta2.Topology{
// 					{
// 						Segments: map[string]string{"region": "R3", "zone": "Z2"},
// 					},
// 				},
// 			},
// 			expectedOutput: false,
// 		},
// 		{
// 			description:          "Verify comparison between empty and nil map objects",
// 			azVolume:             defaultAzVolumeWithNilParamForComparison,
// 			maxMountReplicaCount: 1,
// 			capacityRange:        &azdiskv1beta2.CapacityRange{},
// 			parameters:           map[string]string{},
// 			secrets:              map[string]string{},
// 			volumeContentSource:  &azdiskv1beta2.ContentVolumeSource{},
// 			accessibilityReq:     &azdiskv1beta2.TopologyRequirement{},
// 			expectedOutput:       true,
// 		},
// 	}

// 	for _, test := range tests {
// 		tt := test
// 		t.Run(test.description, func(t *testing.T) {
// 			output := isAzVolumeSpecSameAsRequestParams(
// 				&tt.azVolume,
// 				tt.maxMountReplicaCount,
// 				tt.capacityRange,
// 				tt.parameters,
// 				tt.secrets,
// 				tt.volumeContentSource,
// 				tt.accessibilityReq)

// 			assert.Equal(t, tt.expectedOutput, output)
// 		})
// 	}
// }

// func createAzVolume(namespace, diskName string, maxMountReplicaCount int) *azdiskv1beta2.AzVolume {
// 	return &azdiskv1beta2.AzVolume{
// 		ObjectMeta: metav1.ObjectMeta{
// 			Name:      diskName,
// 			Namespace: namespace,
// 		},
// 		Spec: azdiskv1beta2.AzVolumeSpec{
// 			MaxMountReplicaCount: maxMountReplicaCount,
// 		},
// 	}
// }
