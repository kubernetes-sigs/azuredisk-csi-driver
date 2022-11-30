/*
Copyright 2022 The Kubernetes Authors.

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

package workflow

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"github.com/pborman/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"

	"k8s.io/apimachinery/pkg/api/meta"
	k8sRuntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2/klogr"
	"k8s.io/utils/pointer"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
)

type workflowKey struct{}

type Workflow struct {
	name           string
	startTime      time.Time
	requestID      string
	logger         Logger
	parentWorkflow *Workflow
	// shared values need to be pointers
	errSet       *sync.Map
	pendingCount *int32 // pendingCount should always be number of children + 1(itself)
}

func GetWorkflowFromObj(ctx context.Context, obj k8sRuntime.Object, opts ...Option) (context.Context, Workflow) {
	w := Workflow{}.applyDefault()

	w.requestID = GetRequestID(obj)
	startTimeStr := getAnnotationFromRuntimeObject(obj, consts.RequestStartimeKey)
	if startTimeStr != "" {
		w.startTime, _ = time.Parse(consts.RequestTimeFormat, startTimeStr)
	}
	w.logger = Logger{w.logger.WithValues(append(GetObjectDetails(obj), consts.RequestIDKey, w.requestID)...)}

	for _, opt := range opts {
		opt(&w)
	}

	return w.SaveToContext(ctx), w
}

func GetObjectDetails(obj k8sRuntime.Object) []interface{} {
	switch target := obj.(type) {
	case *azdiskv1beta2.AzVolume:
		if target != nil {
			return []interface{}{consts.VolumeNameLabel, target.Spec.VolumeName, consts.PvNameKey, target.Spec.PersistentVolume}
		}
	case *azdiskv1beta2.AzVolumeAttachment:
		if target != nil {
			return []interface{}{consts.VolumeNameLabel, target.Spec.VolumeName, consts.NodeNameLabel, target.Spec.NodeName, consts.RoleLabel, target.Spec.RequestedRole}
		}
	}
	return []interface{}{}
}

type Option func(*Workflow)

func WithDetails(details ...interface{}) Option {
	return func(w *Workflow) {
		w.logger = Logger{w.logger.WithValues(details...)}
	}
}

func WithCaller(skip int) Option {
	return func(w *Workflow) {
		w.logger = Logger{w.logger.WithValues("caller", getCallerName(skip+3))}
	}
}

// New starts a new child workflow if there is already a workflow saved to the context otherwise a new workflow
func New(ctx context.Context, opts ...Option) (context.Context, Workflow) {
	name := getCallerName(2)

	w := Workflow{}.applyDefault()
	w.name = name

	if parentWorkflow, ok := GetWorkflowFromContext(ctx); ok {
		w.requestID = parentWorkflow.requestID
		w.parentWorkflow = &parentWorkflow
		w.logger = parentWorkflow.Logger()
		_ = atomic.AddInt32(w.parentWorkflow.pendingCount, 1)
	} else {
		w.requestID = GenerateRequestID()
		w.logger = Logger{klogr.New().WithValues(consts.RequestIDKey, w.requestID)}
	}

	// TODO: if other functional options added in the future, evaluate use scenarios and assess whether the option needs to be applied preemptively before construct
	for _, opt := range opts {
		opt(&w)
	}

	// so workflow's numWait will always be number of children workflow + 1 (itself)
	return w.SaveToContext(ctx), w
}

func (w Workflow) applyDefault() Workflow {
	w.startTime = time.Now()
	w.logger = Logger{klogr.New()}
	w.pendingCount = pointer.Int32(1)
	w.errSet = &sync.Map{}
	return w
}

func (w Workflow) Finish(errs ...error) {
	pendingCount := atomic.AddInt32(w.pendingCount, -1)

	for _, err := range errs {
		w.errSet.Store(err, nil)
	}

	if pendingCount > 0 {
		return
	} else if pendingCount < 0 {
		panic(fmt.Sprintf("finish was called too many times for workflow (%s)", w.name))
	}

	latency := time.Since(w.startTime)
	errs = w.errors()
	if len(errs) > 0 {
		err := status.Errorf(codes.Internal, "%+v", errs)
		w.logger.WithValues(consts.Latency, latency, consts.WorkflowKey, w.name).WithCallDepth(1).Error(err, "Workflow completed with an error.")
	} else {
		w.logger.WithValues(consts.Latency, latency, consts.WorkflowKey, w.name).WithCallDepth(1).Info("Workflow completed with success.")
	}

	if w.parentWorkflow != nil {
		w.parentWorkflow.Finish(errs...)
	}
}

func (w Workflow) errors() (errs []error) {
	w.errSet.Range(func(v, _ interface{}) bool {
		if err, ok := v.(error); ok {
			errs = append(errs, err)
		}
		return true
	})
	return
}

func (w Workflow) AnnotateObject(obj k8sRuntime.Object) {
	meta, _ := meta.Accessor(obj)
	if meta == nil {
		return
	}
	annotations := meta.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[consts.RequesterKey] = w.name
	annotations[consts.RequestIDKey] = w.requestID
	annotations[consts.RequestStartimeKey] = w.startTime.Format(consts.RequestTimeFormat)
	meta.SetAnnotations(annotations)
}

func (w Workflow) StartTime() time.Time {
	return w.startTime
}

func (w Workflow) RequestID() string {
	return w.requestID
}

func (w Workflow) Logger() Logger {
	return w.logger
}

func (w Workflow) Name() string {
	return w.name
}

func (w *Workflow) AddDetailToLogger(details ...interface{}) {
	w.logger = Logger{w.logger.WithValues(details...)}
}

func (w Workflow) SaveToContext(ctx context.Context) context.Context {
	return logr.NewContext(context.WithValue(ctx, workflowKey{}, &w), w.logger.Logger)
}

func GetWorkflowFromContext(ctx context.Context) (Workflow, bool) {
	val := ctx.Value(workflowKey{})
	if val == nil {
		return Workflow{}.applyDefault(), false
	}
	w, ok := val.(*Workflow)
	if !ok {
		return Workflow{}.applyDefault(), false
	}
	return *w, true
}

func GetWorkflow(ctx context.Context, obj k8sRuntime.Object) (workflow Workflow) {
	var exists bool
	if workflow, exists = GetWorkflowFromContext(ctx); !exists {
		_, workflow = GetWorkflowFromObj(ctx, obj)
	}
	return
}

func getCallerName(skip int) (name string) {
	pc, _, _, ok := runtime.Caller(skip)
	if ok {
		details := runtime.FuncForPC(pc)
		name = details.Name()
	}
	return
}

func GetRequestID(obj k8sRuntime.Object) string {
	if requestID := getAnnotationFromRuntimeObject(obj, consts.RequestIDKey); requestID != "" {
		return requestID
	}
	return GenerateRequestID()
}

func getAnnotationFromRuntimeObject(obj k8sRuntime.Object, key string) string {
	meta, _ := meta.Accessor(obj)
	if meta == nil {
		return ""
	}
	labels := meta.GetAnnotations()
	if labels == nil {
		return ""
	}
	return labels[key]
}

func GenerateRequestID() string {
	return uuid.NewUUID().String()
}
