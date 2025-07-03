//go:build windows
// +build windows

/*
Copyright 2025 The Kubernetes Authors.

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

package cim

import (
	"fmt"

	"github.com/microsoft/wmi/pkg/base/query"
	"github.com/microsoft/wmi/pkg/errors"
	cim "github.com/microsoft/wmi/pkg/wmiinstance"
)

const (
	WMINamespaceCimV2   = "Root\\CimV2"
	WMINamespaceStorage = "Root\\Microsoft\\Windows\\Storage"
)

type InstanceHandler func(instance *cim.WmiInstance) (bool, error)

// NewWMISession creates a new local WMI session for the given namespace, defaulting
// to root namespace if none specified.
func NewWMISession(namespace string) (*cim.WmiSession, error) {
	if namespace == "" {
		namespace = WMINamespaceCimV2
	}

	sessionManager := cim.NewWmiSessionManager()
	defer sessionManager.Dispose()

	session, err := sessionManager.GetLocalSession(namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to get local WMI session for namespace %s. error: %w", namespace, err)
	}

	connected, err := session.Connect()
	if !connected || err != nil {
		return nil, fmt.Errorf("failed to connect to WMI. error: %w", err)
	}

	return session, nil
}

// QueryFromWMI executes a WMI query in the specified namespace and processes each result
// through the provided handler function. Stops processing if handler returns false or encounters an error.
func QueryFromWMI(namespace string, query *query.WmiQuery, handler InstanceHandler) error {
	session, err := NewWMISession(namespace)
	if err != nil {
		return err
	}

	defer session.Close()

	instances, err := session.QueryInstances(query.String())
	if err != nil {
		return fmt.Errorf("failed to query WMI class %s. error: %w", query.ClassName, err)
	}

	if len(instances) == 0 {
		return errors.NotFound
	}

	var cont bool
	for _, instance := range instances {
		cont, err = handler(instance)
		if err != nil {
			err = fmt.Errorf("failed to query WMI class %s instance (%s). error: %w", query.ClassName, instance.String(), err)
		}
		if !cont {
			break
		}
	}

	return err
}

// QueryInstances retrieves all WMI instances matching the given query in the specified namespace.
func QueryInstances(namespace string, query *query.WmiQuery) ([]*cim.WmiInstance, error) {
	var instances []*cim.WmiInstance
	err := QueryFromWMI(namespace, query, func(instance *cim.WmiInstance) (bool, error) {
		instances = append(instances, instance)
		return true, nil
	})
	return instances, err
}

// InvokeCimMethod calls a static method on a specific WMI class with given input parameters,
// returning the method's return value, output parameters, and any error encountered.
func InvokeCimMethod(namespace, class, methodName string, inputParameters map[string]interface{}) (int, map[string]interface{}, error) {
	session, err := NewWMISession(namespace)
	if err != nil {
		return -1, nil, err
	}

	defer session.Close()

	rawResult, err := session.Session.CallMethod("Get", class)
	if err != nil {
		return -1, nil, err
	}

	classInst, err := cim.CreateWmiInstance(rawResult, session)
	if err != nil {
		return -1, nil, err
	}

	method, err := cim.NewWmiMethod(methodName, classInst)
	if err != nil {
		return -1, nil, err
	}

	var inParam cim.WmiMethodParamCollection
	for k, v := range inputParameters {
		inParam = append(inParam, &cim.WmiMethodParam{
			Name:  k,
			Value: v,
		})
	}

	var outParam cim.WmiMethodParamCollection
	var result *cim.WmiMethodResult
	result, err = method.Execute(inParam, outParam)
	if err != nil {
		return -1, nil, err
	}

	outputParameters := make(map[string]interface{})
	for _, v := range result.OutMethodParams {
		outputParameters[v.Name] = v.Value
	}

	return int(result.ReturnValue), outputParameters, nil
}

// IgnoreNotFound returns nil if the error is nil or a "not found" error,
// otherwise returns the original error.
func IgnoreNotFound(err error) error {
	if err == nil || errors.IsNotFound(err) {
		return nil
	}
	return err
}
