/*
Copyright The Kubernetes Authors.

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

package mockvolumesnapshot

import (
	context "context"
	reflect "reflect"

	gomock "go.uber.org/mock/gomock"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v4/apis/volumesnapshot/v1"
)

// MockInterface is a mock of VolumeSnapshotInterface interface
type MockInterface struct {
	ctrl     *gomock.Controller
	recorder *MockInterfaceMockRecorder
}

// MockInterfaceMockRecorder is the mock recorder for MockInterface
type MockInterfaceMockRecorder struct {
	mock *MockInterface
}

// NewMockInterface creates a new mock instance
func NewMockInterface(ctrl *gomock.Controller) *MockInterface {
	mock := &MockInterface{ctrl: ctrl}
	mock.recorder = &MockInterfaceMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use
func (m *MockInterface) EXPECT() *MockInterfaceMockRecorder {
	return m.recorder
}

// Create mocks base method
func (m *MockInterface) Create(ctx context.Context, volumeSnapshot *snapshotv1.VolumeSnapshot, opts metav1.CreateOptions) (*snapshotv1.VolumeSnapshot, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Create", ctx, volumeSnapshot, opts)
	ret0, _ := ret[0].(*snapshotv1.VolumeSnapshot)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Create indicates an expected call of Create
func (mr *MockInterfaceMockRecorder) Create(ctx, volumeSnapshot, opts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Create", reflect.TypeOf((*MockInterface)(nil).Create), ctx, volumeSnapshot, opts)
}

// Update mocks base method
func (m *MockInterface) Update(ctx context.Context, volumeSnapshot *snapshotv1.VolumeSnapshot, opts metav1.UpdateOptions) (*snapshotv1.VolumeSnapshot, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Update", ctx, volumeSnapshot, opts)
	ret0, _ := ret[0].(*snapshotv1.VolumeSnapshot)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Update indicates an expected call of Update
func (mr *MockInterfaceMockRecorder) Update(ctx, volumeSnapshot, opts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Update", reflect.TypeOf((*MockInterface)(nil).Update), ctx, volumeSnapshot, opts)
}

// UpdateStatus mocks base method
func (m *MockInterface) UpdateStatus(ctx context.Context, volumeSnapshot *snapshotv1.VolumeSnapshot, opts metav1.UpdateOptions) (*snapshotv1.VolumeSnapshot, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "UpdateStatus", ctx, volumeSnapshot, opts)
	ret0, _ := ret[0].(*snapshotv1.VolumeSnapshot)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// UpdateStatus indicates an expected call of UpdateStatus
func (mr *MockInterfaceMockRecorder) UpdateStatus(ctx, volumeSnapshot, opts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "UpdateStatus", reflect.TypeOf((*MockInterface)(nil).UpdateStatus), ctx, volumeSnapshot, opts)
}

// Delete mocks base method
func (m *MockInterface) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Delete", ctx, name, opts)
	ret0, _ := ret[0].(error)
	return ret0
}

// Delete indicates an expected call of Delete
func (mr *MockInterfaceMockRecorder) Delete(ctx, name, opts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Delete", reflect.TypeOf((*MockInterface)(nil).Delete), ctx, name, opts)
}

// DeleteCollection mocks base method
func (m *MockInterface) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DeleteCollection", ctx, opts, listOpts)
	ret0, _ := ret[0].(error)
	return ret0
}

// DeleteCollection indicates an expected call of DeleteCollection
func (mr *MockInterfaceMockRecorder) DeleteCollection(ctx, opts, listOpts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DeleteCollection", reflect.TypeOf((*MockInterface)(nil).DeleteCollection), ctx, opts, listOpts)
}

// Get mocks base method
func (m *MockInterface) Get(ctx context.Context, name string, opts metav1.GetOptions) (*snapshotv1.VolumeSnapshot, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", ctx, name, opts)
	ret0, _ := ret[0].(*snapshotv1.VolumeSnapshot)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get indicates an expected call of Get
func (mr *MockInterfaceMockRecorder) Get(ctx, name, opts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*MockInterface)(nil).Get), ctx, name, opts)
}

// List mocks base method
func (m *MockInterface) List(ctx context.Context, opts metav1.ListOptions) (*snapshotv1.VolumeSnapshotList, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "List", ctx, opts)
	ret0, _ := ret[0].(*snapshotv1.VolumeSnapshotList)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// List indicates an expected call of List
func (mr *MockInterfaceMockRecorder) List(ctx, opts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "List", reflect.TypeOf((*MockInterface)(nil).List), ctx, opts)
}

// Watch mocks base method
func (m *MockInterface) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Watch", ctx, opts)
	ret0, _ := ret[0].(watch.Interface)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Watch indicates an expected call of Watch
func (mr *MockInterfaceMockRecorder) Watch(ctx, opts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Watch", reflect.TypeOf((*MockInterface)(nil).Watch), ctx, opts)
}

// Patch mocks base method
func (m *MockInterface) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*snapshotv1.VolumeSnapshot, error) {
	m.ctrl.T.Helper()
	varargs := []interface{}{ctx, name, pt, data, opts}
	for _, a := range subresources {
		varargs = append(varargs, a)
	}
	ret := m.ctrl.Call(m, "Patch", varargs...)
	ret0, _ := ret[0].(*snapshotv1.VolumeSnapshot)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Patch indicates an expected call of Patch
func (mr *MockInterfaceMockRecorder) Patch(ctx, name, pt, data, opts interface{}, subresources ...interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	varargs := append([]interface{}{ctx, name, pt, data, opts}, subresources...)
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Patch", reflect.TypeOf((*MockInterface)(nil).Patch), varargs...)
}
