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

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/workflow"
)

type ConditionWaiter struct {
	objType ObjectType
	objName string
	entry   *waitEntry
	watcher *ConditionWatcher
}

func (w *ConditionWaiter) Wait(ctx context.Context) (runtime.Object, error) {
	var obj runtime.Object
	var err error
	namespace := w.watcher.namespace

	switch w.objType {
	case AzVolumeType:
		obj, err = w.watcher.informerFactory.Disk().V1beta2().AzVolumes().Lister().AzVolumes(namespace).Get(w.objName)
	case AzVolumeAttachmentType:
		obj, err = w.watcher.informerFactory.Disk().V1beta2().AzVolumeAttachments().Lister().AzVolumeAttachments(namespace).Get(w.objName)
	case AzDriverNodeType:
		obj, err = w.watcher.informerFactory.Disk().V1beta2().AzDriverNodes().Lister().AzDriverNodes(namespace).Get(w.objName)
	}

	_, wf := workflow.New(ctx, workflow.WithDetails(workflow.GetObjectDetails(obj)...))
	defer func() { wf.Finish(err) }()

	// if there exists an object in cache, evaluate condition function on it
	// if condition function returns error, the error could be coming from stale cache, so wait for another condition assessment from event handler.
	if notFound := errors.IsNotFound(err); err == nil || notFound {
		success, _ := w.entry.conditionFunc(obj, notFound)
		if success {
			return obj, nil
		}
	}

	// if not wait for the event handler signal
	select {
	case <-ctx.Done():
		err = ctx.Err()
		return nil, err
	case waitResult := <-w.entry.waitChan:
		err = waitResult.err
		return waitResult.obj, waitResult.err
	}
}

func (w *ConditionWaiter) Close() {
	w.watcher.waitMap.Delete(getTypedName(w.objType, w.objName))
}
