/*
Copyright 2020 The Kubernetes Authors.

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

package mockcorev1

import (
	reflect "reflect"

	gomock "go.uber.org/mock/gomock"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"
	rest "k8s.io/client-go/rest"
)

// MockInterface is a mock of CoreV1Interface interface
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

// RESTClient mocks base method
func (m *MockInterface) RESTClient() rest.Interface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "RESTClient")
	ret0, _ := ret[0].(rest.Interface)
	return ret0
}

// RESTClient indicates an expected call of RESTClient
func (mr *MockInterfaceMockRecorder) RESTClient() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "RESTClient", reflect.TypeOf((*MockInterface)(nil).RESTClient))
}

// ComponentStatuses mocks base method
func (m *MockInterface) ComponentStatuses() v1.ComponentStatusInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ComponentStatuses")
	ret0, _ := ret[0].(v1.ComponentStatusInterface)
	return ret0
}

// ComponentStatuses indicates an expected call of ComponentStatuses
func (mr *MockInterfaceMockRecorder) ComponentStatuses() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ComponentStatuses", reflect.TypeOf((*MockInterface)(nil).ComponentStatuses))
}

// ConfigMaps mocks base method
func (m *MockInterface) ConfigMaps(namespace string) v1.ConfigMapInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ConfigMaps", namespace)
	ret0, _ := ret[0].(v1.ConfigMapInterface)
	return ret0
}

// ConfigMaps indicates an expected call of ConfigMaps
func (mr *MockInterfaceMockRecorder) ConfigMaps(namespace interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ConfigMaps", reflect.TypeOf((*MockInterface)(nil).ConfigMaps), namespace)
}

// Endpoints mocks base method
func (m *MockInterface) Endpoints(namespace string) v1.EndpointsInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Endpoints", namespace)
	ret0, _ := ret[0].(v1.EndpointsInterface)
	return ret0
}

// Endpoints indicates an expected call of Endpoints
func (mr *MockInterfaceMockRecorder) Endpoints(namespace interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Endpoints", reflect.TypeOf((*MockInterface)(nil).Endpoints), namespace)
}

// Events mocks base method
func (m *MockInterface) Events(namespace string) v1.EventInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Events", namespace)
	ret0, _ := ret[0].(v1.EventInterface)
	return ret0
}

// Events indicates an expected call of Events
func (mr *MockInterfaceMockRecorder) Events(namespace interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Events", reflect.TypeOf((*MockInterface)(nil).Events), namespace)
}

// LimitRanges mocks base method
func (m *MockInterface) LimitRanges(namespace string) v1.LimitRangeInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LimitRanges", namespace)
	ret0, _ := ret[0].(v1.LimitRangeInterface)
	return ret0
}

// LimitRanges indicates an expected call of LimitRanges
func (mr *MockInterfaceMockRecorder) LimitRanges(namespace interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LimitRanges", reflect.TypeOf((*MockInterface)(nil).LimitRanges), namespace)
}

// Namespaces mocks base method
func (m *MockInterface) Namespaces() v1.NamespaceInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Namespaces")
	ret0, _ := ret[0].(v1.NamespaceInterface)
	return ret0
}

// Namespaces indicates an expected call of Namespaces
func (mr *MockInterfaceMockRecorder) Namespaces() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Namespaces", reflect.TypeOf((*MockInterface)(nil).Namespaces))
}

// Nodes mocks base method
func (m *MockInterface) Nodes() v1.NodeInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Nodes")
	ret0, _ := ret[0].(v1.NodeInterface)
	return ret0
}

// Nodes indicates an expected call of Nodes
func (mr *MockInterfaceMockRecorder) Nodes() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Nodes", reflect.TypeOf((*MockInterface)(nil).Nodes))
}

// PersistentVolumes mocks base method
func (m *MockInterface) PersistentVolumes() v1.PersistentVolumeInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "PersistentVolumes")
	ret0, _ := ret[0].(v1.PersistentVolumeInterface)
	return ret0
}

// PersistentVolumes indicates an expected call of PersistentVolumes
func (mr *MockInterfaceMockRecorder) PersistentVolumes() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "PersistentVolumes", reflect.TypeOf((*MockInterface)(nil).PersistentVolumes))
}

// PersistentVolumeClaims mocks base method
func (m *MockInterface) PersistentVolumeClaims(namespace string) v1.PersistentVolumeClaimInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "PersistentVolumeClaims", namespace)
	ret0, _ := ret[0].(v1.PersistentVolumeClaimInterface)
	return ret0
}

// PersistentVolumeClaims indicates an expected call of PersistentVolumeClaims
func (mr *MockInterfaceMockRecorder) PersistentVolumeClaims(namespace interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "PersistentVolumeClaims", reflect.TypeOf((*MockInterface)(nil).PersistentVolumeClaims), namespace)
}

// Pods mocks base method
func (m *MockInterface) Pods(namespace string) v1.PodInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Pods", namespace)
	ret0, _ := ret[0].(v1.PodInterface)
	return ret0
}

// Pods indicates an expected call of Pods
func (mr *MockInterfaceMockRecorder) Pods(namespace interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Pods", reflect.TypeOf((*MockInterface)(nil).Pods), namespace)
}

// PodTemplates mocks base method
func (m *MockInterface) PodTemplates(namespace string) v1.PodTemplateInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "PodTemplates", namespace)
	ret0, _ := ret[0].(v1.PodTemplateInterface)
	return ret0
}

// PodTemplates indicates an expected call of PodTemplates
func (mr *MockInterfaceMockRecorder) PodTemplates(namespace interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "PodTemplates", reflect.TypeOf((*MockInterface)(nil).PodTemplates), namespace)
}

// ReplicationControllers mocks base method
func (m *MockInterface) ReplicationControllers(namespace string) v1.ReplicationControllerInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ReplicationControllers", namespace)
	ret0, _ := ret[0].(v1.ReplicationControllerInterface)
	return ret0
}

// ReplicationControllers indicates an expected call of ReplicationControllers
func (mr *MockInterfaceMockRecorder) ReplicationControllers(namespace interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ReplicationControllers", reflect.TypeOf((*MockInterface)(nil).ReplicationControllers), namespace)
}

// ResourceQuotas mocks base method
func (m *MockInterface) ResourceQuotas(namespace string) v1.ResourceQuotaInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ResourceQuotas", namespace)
	ret0, _ := ret[0].(v1.ResourceQuotaInterface)
	return ret0
}

// ResourceQuotas indicates an expected call of ResourceQuotas
func (mr *MockInterfaceMockRecorder) ResourceQuotas(namespace interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ResourceQuotas", reflect.TypeOf((*MockInterface)(nil).ResourceQuotas), namespace)
}

// Secrets mocks base method
func (m *MockInterface) Secrets(namespace string) v1.SecretInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Secrets", namespace)
	ret0, _ := ret[0].(v1.SecretInterface)
	return ret0
}

// Secrets indicates an expected call of Secrets
func (mr *MockInterfaceMockRecorder) Secrets(namespace interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Secrets", reflect.TypeOf((*MockInterface)(nil).Secrets), namespace)
}

// Services mocks base method
func (m *MockInterface) Services(namespace string) v1.ServiceInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Services", namespace)
	ret0, _ := ret[0].(v1.ServiceInterface)
	return ret0
}

// Services indicates an expected call of Services
func (mr *MockInterfaceMockRecorder) Services(namespace interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Services", reflect.TypeOf((*MockInterface)(nil).Services), namespace)
}

// ServiceAccounts mocks base method
func (m *MockInterface) ServiceAccounts(namespace string) v1.ServiceAccountInterface {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ServiceAccounts", namespace)
	ret0, _ := ret[0].(v1.ServiceAccountInterface)
	return ret0
}

// ServiceAccounts indicates an expected call of ServiceAccounts
func (mr *MockInterfaceMockRecorder) ServiceAccounts(namespace interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ServiceAccounts", reflect.TypeOf((*MockInterface)(nil).ServiceAccounts), namespace)
}
