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
	"fmt"
	"os"
	"reflect"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	"k8s.io/client-go/tools/cache"
	diskv1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azurediskInformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
)

type objectType string

const (
	azVolumeAttachmentType objectType = "azvolumeattachments"
	azVolumeType           objectType = "azvolume"
	azDriverNodeType       objectType = "azdrivernode"
)

type eventType int

const (
	create eventType = iota
	update
	delete
)

type waitResult struct {
	obj runtime.Object
	err error
}

type waitEntry struct {
	conditionFunc func(obj interface{}, objectDeleted bool) (bool, error)
	waitChan      chan waitResult
}

type conditionWatcher struct {
	informerFactory azurediskInformers.SharedInformerFactory
	waitMap         sync.Map // maps namespaced name to waitEntry
	namespace       string
}

func newConditionWatcher(ctx context.Context, azDiskClient azDiskClientSet.Interface, informerFactory azurediskInformers.SharedInformerFactory, namespace string) *conditionWatcher {
	azVolumeAttachmentInformer := informerFactory.Disk().V1beta1().AzVolumeAttachments().Informer()
	azVolumeInformer := informerFactory.Disk().V1beta1().AzVolumes().Informer()
	azDriverNodeInformer := informerFactory.Disk().V1beta1().AzDriverNodes().Informer()

	c := conditionWatcher{
		informerFactory: informerFactory,
		waitMap:         sync.Map{},
	}

	azVolumeAttachmentInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onCreate,
		UpdateFunc: c.onUpdate,
		DeleteFunc: c.onDelete,
	})
	azVolumeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onCreate,
		UpdateFunc: c.onUpdate,
		DeleteFunc: c.onDelete,
	})
	azDriverNodeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.onCreate,
		UpdateFunc: c.onUpdate,
		DeleteFunc: c.onDelete,
	})

	go informerFactory.Start(ctx.Done())

	synced := cache.WaitForCacheSync(ctx.Done(), azVolumeAttachmentInformer.HasSynced, azVolumeInformer.HasSynced, azDriverNodeInformer.HasSynced)
	if !synced {
		klog.Fatalf("Unable to sync caches for azuredisk CRIs")
		os.Exit(1)
	}

	return &c
}

func (c *conditionWatcher) newConditionWaiter(ctx context.Context, objType objectType, objName string, conditionFunc func(obj interface{}, expectDelete bool) (bool, error)) (*conditionWaiter, error) {
	klog.V(5).Infof("Adding a condition function for %s (%s)", objType, objName)
	entry := waitEntry{
		conditionFunc: conditionFunc,
		waitChan:      make(chan waitResult, 1),
	}

	_, exists := c.waitMap.LoadOrStore(getTypedName(objType, objName), &entry)
	if exists {
		err := status.Errorf(codes.Aborted, "another wait operation in process for %s (%s)", objType, objName)
		klog.Error(err)
		return nil, err
	}

	return &conditionWaiter{
		objType: objType,
		objName: objName,
		entry:   &entry,
		watcher: c,
	}, nil
}

func (c *conditionWatcher) onCreate(obj interface{}) {
	c.handleEvent(obj, create)
}

func (c *conditionWatcher) onUpdate(_, newObj interface{}) {
	c.handleEvent(newObj, update)
}

func (c *conditionWatcher) onDelete(obj interface{}) {
	c.handleEvent(obj, delete)
}

func (c *conditionWatcher) handleEvent(obj interface{}, eventType eventType) {
	metaObj, err := meta.Accessor(obj)
	if err != nil {
		// this line should not be reached
		klog.Errorf("object (%v) has not implemented meta object interface.")
	}

	var objType objectType
	switch obj.(type) {
	case *diskv1beta1.AzVolume:
		objType = azVolumeType
	case *diskv1beta1.AzVolumeAttachment:
		objType = azVolumeAttachmentType
	case *diskv1beta1.AzDriverNode:
		objType = azDriverNodeType
	default:
		// unknown object type
		klog.Errorf("unsupported object type %v", reflect.TypeOf(obj))
		return
	}

	v, ok := c.waitMap.Load(getTypedName(objType, metaObj.GetName()))
	if !ok {
		return
	}
	klog.V(5).Infof("found a wait entry for object (%s)", metaObj.GetName())
	entry := v.(*waitEntry)
	conditionFunc := entry.conditionFunc
	waitChan := entry.waitChan

	result := waitResult{}

	ok, err = conditionFunc(obj, eventType == delete)
	klog.V(5).Infof("condition result: succeeded: %v, error: %v", ok, err)
	// if err found, send error through channel
	if err != nil {
		result.err = err
	} else if !ok {
		// if no error was found but condition not met, return
		return
	}

	runtimeObj, ok := obj.(runtime.Object)
	if !ok {
		result.err = status.Errorf(codes.Internal, "object does not implement runtime.Object interface.")
	}
	result.obj = runtimeObj

	select {
	case waitChan <- result: // send result through channel if not already occupied or channel closed
	default:
		klog.Infof("wait channel for object (%v) is either already occupied or closed.", metaObj.GetName())
	}
}

func getTypedName(objType objectType, objName string) string {
	return fmt.Sprintf("%s/%s", string(objType), objName)
}
