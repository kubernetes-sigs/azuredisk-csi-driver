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

package provider

import (
	reflect "reflect"

	azure "github.com/Azure/go-autorest/autorest/azure"
	gomock "github.com/golang/mock/gomock"
)

// MockFutureParser is a mock of futureParser interface
type MockFutureParser struct {
	ctrl     *gomock.Controller
	recorder *MockfutureParserMockRecorder
}

// MockfutureParserMockRecorder is the mock recorder for MockfutureParser
type MockfutureParserMockRecorder struct {
	mock *MockFutureParser
}

// NewMockfutureParser creates a new mock instance
func NewMockfutureParser(ctrl *gomock.Controller) *MockFutureParser {
	mock := &MockFutureParser{ctrl: ctrl}
	mock.recorder = &MockfutureParserMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use
func (m *MockFutureParser) EXPECT() *MockfutureParserMockRecorder {
	return m.recorder
}

// ConfigAccepted mocks base method
func (m *MockFutureParser) ConfigAccepted(future *azure.Future) bool {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ConfigAccepted", future)
	ret0, _ := ret[0].(bool)
	return ret0
}

// ConfigAccepted indicates an expected call of ConfigAccepted
func (mr *MockfutureParserMockRecorder) ConfigAccepted(future interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ConfigAccepted", reflect.TypeOf((*MockFutureParser)(nil).ConfigAccepted), future)
}
