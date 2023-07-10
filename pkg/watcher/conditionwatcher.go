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

package watcher

import (
	"context"
	"fmt"
	"reflect"
	"sync"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdiskinformers "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/informers/externalversions"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azureutils"
)

type ObjectType string

const (
	AzVolumeAttachmentType ObjectType = "azvolumeattachments"
	AzVolumeType           ObjectType = "azvolume"
	AzDriverNodeType       ObjectType = "azdrivernode"
)

type eventType int

const (
	createEvent eventType = iota
	updateEvent
	deleteEvent
)

type waitResult struct {
	obj runtime.Object
	err error
}

type waitEntry struct {
	conditionFunc func(obj interface{}, objectDeleted bool) (bool, error)
	waitChan      chan waitResult
}

type ConditionWatcher struct {
	informerFactory azdiskinformers.SharedInformerFactory
	waitMap         sync.Map // maps namespaced name to waitEntry
	namespace       string
}

func NewConditionWatcher(informerFactory azdiskinformers.SharedInformerFactory, namespace string, informers ...azureutils.GenericInformer) (*ConditionWatcher, error) {
	c := &ConditionWatcher{
		informerFactory: informerFactory,
		waitMap:         sync.Map{},
		namespace:       namespace,
	}

	var err error
	for _, informer := range informers {
		informer := informer.Informer()
		var handlerReg cache.ResourceEventHandlerRegistration
		handlerReg, err = informer.AddEventHandler(cache.ResourceEventHandlerFuncs{
			AddFunc:    c.onCreate,
			UpdateFunc: c.onUpdate,
			DeleteFunc: c.onDelete,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to add event handler: %s", err)
		}

		defer func() {
			if err != nil {
				_ = informer.RemoveEventHandler(handlerReg)
			}
		}()
	}
	return c, nil
}

func (c *ConditionWatcher) NewConditionWaiter(ctx context.Context, objType ObjectType, objName string, conditionFunc func(obj interface{}, expectDelete bool) (bool, error)) *ConditionWaiter {
	klog.V(5).Infof("Adding a condition function for %s (%s)", objType, objName)
	entry := waitEntry{
		conditionFunc: conditionFunc,
		waitChan:      make(chan waitResult, 1),
	}

	key := getTypedName(objType, objName)
	entryMap := &sync.Map{}
	val, exists := c.waitMap.LoadOrStore(key, entryMap)
	if exists {
		entryMap = val.(*sync.Map)
	}
	entryMap.Store(&entry, struct{}{})
	c.waitMap.Store(key, entryMap)

	return &ConditionWaiter{
		objType: objType,
		objName: objName,
		entry:   &entry,
		watcher: c,
	}
}

func (c *ConditionWatcher) onCreate(obj interface{}) {
	c.handleEvent(obj, createEvent)
}

func (c *ConditionWatcher) onUpdate(_, newObj interface{}) {
	c.handleEvent(newObj, updateEvent)
}

func (c *ConditionWatcher) onDelete(obj interface{}) {
	c.handleEvent(obj, deleteEvent)
}

func (c *ConditionWatcher) handleEvent(obj interface{}, eventType eventType) {
	metaObj, err := meta.Accessor(obj)
	klog.Infof("conditionwatcher handleEvent 136 metaobject: %+v", metaObj)
	klog.Infof("conditionwatcher handleEvent 136 object: %+v", obj)
	if err != nil {
		// this line should not be reached
		klog.Errorf("object (%v) has not implemented meta object interface.")
	}

	var objType ObjectType
	switch obj.(type) {
	case *azdiskv1beta2.AzVolume:
		objType = AzVolumeType
	case *azdiskv1beta2.AzVolumeAttachment:
		objType = AzVolumeAttachmentType
	case *azdiskv1beta2.AzDriverNode:
		objType = AzDriverNodeType
	default:
		// unknown object type
		klog.Errorf("unsupported object type %v", reflect.TypeOf(obj))
		return
	}

	v, ok := c.waitMap.Load(getTypedName(objType, metaObj.GetName()))
	if !ok {
		return
	}
	entries := v.(*sync.Map)

	wg := sync.WaitGroup{}
	entries.Range(func(key, _ interface{}) bool {
		entry := key.(*waitEntry)
		wg.Add(1)
		go func() {
			defer wg.Done()
			conditionFunc := entry.conditionFunc
			waitChan := entry.waitChan

			result := waitResult{}

			ok, err = conditionFunc(obj, eventType == deleteEvent)
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
		}()
		return true
	})
	wg.Wait()
}

func getTypedName(objType ObjectType, objName string) string {
	return fmt.Sprintf("%s/%s", string(objType), objName)
}
