// /*
// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */
//

// Code generated by MockGen. DO NOT EDIT.
// Source: subnetclient/interface.go
//
// Generated by this command:
//
//	mockgen -package mock_subnetclient -source subnetclient/interface.go -typed -write_generate_directive -copyright_file ../../hack/boilerplate/boilerplate.generatego.txt
//

// Package mock_subnetclient is a generated GoMock package.
package mock_subnetclient

import (
	context "context"
	reflect "reflect"

	armnetwork "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v6"
	gomock "go.uber.org/mock/gomock"
)

//go:generate mockgen -package mock_subnetclient -source subnetclient/interface.go -typed -write_generate_directive -copyright_file ../../hack/boilerplate/boilerplate.generatego.txt

// MockInterface is a mock of Interface interface.
type MockInterface struct {
	ctrl     *gomock.Controller
	recorder *MockInterfaceMockRecorder
	isgomock struct{}
}

// MockInterfaceMockRecorder is the mock recorder for MockInterface.
type MockInterfaceMockRecorder struct {
	mock *MockInterface
}

// NewMockInterface creates a new mock instance.
func NewMockInterface(ctrl *gomock.Controller) *MockInterface {
	mock := &MockInterface{ctrl: ctrl}
	mock.recorder = &MockInterfaceMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockInterface) EXPECT() *MockInterfaceMockRecorder {
	return m.recorder
}

// CreateOrUpdate mocks base method.
func (m *MockInterface) CreateOrUpdate(ctx context.Context, resourceGroupName, parentResourceName, resourceName string, resourceParam armnetwork.Subnet) (*armnetwork.Subnet, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CreateOrUpdate", ctx, resourceGroupName, parentResourceName, resourceName, resourceParam)
	ret0, _ := ret[0].(*armnetwork.Subnet)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// CreateOrUpdate indicates an expected call of CreateOrUpdate.
func (mr *MockInterfaceMockRecorder) CreateOrUpdate(ctx, resourceGroupName, parentResourceName, resourceName, resourceParam any) *MockInterfaceCreateOrUpdateCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CreateOrUpdate", reflect.TypeOf((*MockInterface)(nil).CreateOrUpdate), ctx, resourceGroupName, parentResourceName, resourceName, resourceParam)
	return &MockInterfaceCreateOrUpdateCall{Call: call}
}

// MockInterfaceCreateOrUpdateCall wrap *gomock.Call
type MockInterfaceCreateOrUpdateCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *MockInterfaceCreateOrUpdateCall) Return(arg0 *armnetwork.Subnet, arg1 error) *MockInterfaceCreateOrUpdateCall {
	c.Call = c.Call.Return(arg0, arg1)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *MockInterfaceCreateOrUpdateCall) Do(f func(context.Context, string, string, string, armnetwork.Subnet) (*armnetwork.Subnet, error)) *MockInterfaceCreateOrUpdateCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *MockInterfaceCreateOrUpdateCall) DoAndReturn(f func(context.Context, string, string, string, armnetwork.Subnet) (*armnetwork.Subnet, error)) *MockInterfaceCreateOrUpdateCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Delete mocks base method.
func (m *MockInterface) Delete(ctx context.Context, resourceGroupName, parentResourceName, resourceName string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Delete", ctx, resourceGroupName, parentResourceName, resourceName)
	ret0, _ := ret[0].(error)
	return ret0
}

// Delete indicates an expected call of Delete.
func (mr *MockInterfaceMockRecorder) Delete(ctx, resourceGroupName, parentResourceName, resourceName any) *MockInterfaceDeleteCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Delete", reflect.TypeOf((*MockInterface)(nil).Delete), ctx, resourceGroupName, parentResourceName, resourceName)
	return &MockInterfaceDeleteCall{Call: call}
}

// MockInterfaceDeleteCall wrap *gomock.Call
type MockInterfaceDeleteCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *MockInterfaceDeleteCall) Return(arg0 error) *MockInterfaceDeleteCall {
	c.Call = c.Call.Return(arg0)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *MockInterfaceDeleteCall) Do(f func(context.Context, string, string, string) error) *MockInterfaceDeleteCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *MockInterfaceDeleteCall) DoAndReturn(f func(context.Context, string, string, string) error) *MockInterfaceDeleteCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// Get mocks base method.
func (m *MockInterface) Get(ctx context.Context, resourceGroupName, parentResourceName, resourceName string, expand *string) (*armnetwork.Subnet, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", ctx, resourceGroupName, parentResourceName, resourceName, expand)
	ret0, _ := ret[0].(*armnetwork.Subnet)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get indicates an expected call of Get.
func (mr *MockInterfaceMockRecorder) Get(ctx, resourceGroupName, parentResourceName, resourceName, expand any) *MockInterfaceGetCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*MockInterface)(nil).Get), ctx, resourceGroupName, parentResourceName, resourceName, expand)
	return &MockInterfaceGetCall{Call: call}
}

// MockInterfaceGetCall wrap *gomock.Call
type MockInterfaceGetCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *MockInterfaceGetCall) Return(result *armnetwork.Subnet, rerr error) *MockInterfaceGetCall {
	c.Call = c.Call.Return(result, rerr)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *MockInterfaceGetCall) Do(f func(context.Context, string, string, string, *string) (*armnetwork.Subnet, error)) *MockInterfaceGetCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *MockInterfaceGetCall) DoAndReturn(f func(context.Context, string, string, string, *string) (*armnetwork.Subnet, error)) *MockInterfaceGetCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}

// List mocks base method.
func (m *MockInterface) List(ctx context.Context, resourceGroupName, parentResourceName string) ([]*armnetwork.Subnet, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "List", ctx, resourceGroupName, parentResourceName)
	ret0, _ := ret[0].([]*armnetwork.Subnet)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// List indicates an expected call of List.
func (mr *MockInterfaceMockRecorder) List(ctx, resourceGroupName, parentResourceName any) *MockInterfaceListCall {
	mr.mock.ctrl.T.Helper()
	call := mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "List", reflect.TypeOf((*MockInterface)(nil).List), ctx, resourceGroupName, parentResourceName)
	return &MockInterfaceListCall{Call: call}
}

// MockInterfaceListCall wrap *gomock.Call
type MockInterfaceListCall struct {
	*gomock.Call
}

// Return rewrite *gomock.Call.Return
func (c *MockInterfaceListCall) Return(result []*armnetwork.Subnet, rerr error) *MockInterfaceListCall {
	c.Call = c.Call.Return(result, rerr)
	return c
}

// Do rewrite *gomock.Call.Do
func (c *MockInterfaceListCall) Do(f func(context.Context, string, string) ([]*armnetwork.Subnet, error)) *MockInterfaceListCall {
	c.Call = c.Call.Do(f)
	return c
}

// DoAndReturn rewrite *gomock.Call.DoAndReturn
func (c *MockInterfaceListCall) DoAndReturn(f func(context.Context, string, string) ([]*armnetwork.Subnet, error)) *MockInterfaceListCall {
	c.Call = c.Call.DoAndReturn(f)
	return c
}