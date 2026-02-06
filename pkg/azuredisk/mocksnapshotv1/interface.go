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

package mocksnapshotv1

import (
	reflect "reflect"

	gomock "go.uber.org/mock/gomock"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v4/clientset/versioned/typed/volumesnapshot/v1"
	rest "k8s.io/client-go/rest"
)

// MockSnapshotV1Interface is a mock of SnapshotV1Interface interface
type MockSnapshotV1Interface struct {
	ctrl     *gomock.Controller
	recorder *MockSnapshotV1InterfaceMockRecorder
}

// MockSnapshotV1InterfaceMockRecorder is the mock recorder for MockSnapshotV1Interface
type MockSnapshotV1InterfaceMockRecorder struct {
	mock *MockSnapshotV1Interface
}

// NewMockSnapshotV1Interface creates a new mock instance
func NewMockSnapshotV1Interface(ctrl *gomock.Controller) *MockSnapshotV1Interface {
	mock := &MockSnapshotV1Interface{ctrl: ctrl}
	mock.recorder = &MockSnapshotV1InterfaceMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use
func (m *MockSnapshotV1Interface) EXPECT() *MockSnapshotV1InterfaceMockRecorder {
	return m.recorder
}

// RESTClient mocks base method
func (m *MockSnapshotV1Interface) RESTClient() rest.Interface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RESTClient")
	ret0, _ := ret[0].(rest.Interface)
	return ret0
}

// RESTClient indicates an expected call of RESTClient
func (mr *MockSnapshotV1InterfaceMockRecorder) RESTClient() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RESTClient", reflect.TypeOf((*MockSnapshotV1Interface)(nil).RESTClient))
}

// VolumeSnapshots mocks base method
func (m *MockSnapshotV1Interface) VolumeSnapshots(namespace string) snapshotv1.VolumeSnapshotInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "VolumeSnapshots", namespace)
	ret0, _ := ret[0].(snapshotv1.VolumeSnapshotInterface)
	return ret0
}

// VolumeSnapshots indicates an expected call of VolumeSnapshots
func (mr *MockSnapshotV1InterfaceMockRecorder) VolumeSnapshots(namespace interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "VolumeSnapshots", reflect.TypeOf((*MockSnapshotV1Interface)(nil).VolumeSnapshots), namespace)
}

// VolumeSnapshotClasses mocks base method
func (m *MockSnapshotV1Interface) VolumeSnapshotClasses() snapshotv1.VolumeSnapshotClassInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "VolumeSnapshotClasses")
	ret0, _ := ret[0].(snapshotv1.VolumeSnapshotClassInterface)
	return ret0
}

// VolumeSnapshotClasses indicates an expected call of VolumeSnapshotClasses
func (mr *MockSnapshotV1InterfaceMockRecorder) VolumeSnapshotClasses() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "VolumeSnapshotClasses", reflect.TypeOf((*MockSnapshotV1Interface)(nil).VolumeSnapshotClasses))
}

// VolumeSnapshotContents mocks base method
func (m *MockSnapshotV1Interface) VolumeSnapshotContents() snapshotv1.VolumeSnapshotContentInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "VolumeSnapshotContents")
	ret0, _ := ret[0].(snapshotv1.VolumeSnapshotContentInterface)
	return ret0
}

// VolumeSnapshotContents indicates an expected call of VolumeSnapshotContents
func (mr *MockSnapshotV1InterfaceMockRecorder) VolumeSnapshotContents() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "VolumeSnapshotContents", reflect.TypeOf((*MockSnapshotV1Interface)(nil).VolumeSnapshotContents))
}
