/*
Copyright 2026 The Kubernetes Authors.

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

package azuredisk

import (
	"flag"
	"fmt"

	"k8s.io/component-base/featuregate"
)

const (
	// NodeDrivenAttachDetach allows new volumes to opt in to the Alpha
	// node-driven attach/detach architecture.
	NodeDrivenAttachDetach featuregate.Feature = "NodeDrivenAttachDetach"
)

var defaultDriverFeatureGates = map[featuregate.Feature]featuregate.FeatureSpec{
	NodeDrivenAttachDetach: {
		Default:    false,
		PreRelease: featuregate.Alpha,
	},
}

// NewDriverFeatureGate returns a feature gate registered with the driver's known features.
func NewDriverFeatureGate() featuregate.MutableFeatureGate {
	gate := featuregate.NewFeatureGate()
	if err := gate.Add(defaultDriverFeatureGates); err != nil {
		panic(fmt.Sprintf("failed to register Azure Disk CSI driver feature gates: %v", err))
	}
	return gate
}

// NewGoFlagFeatureGate adapts a driver feature gate to the standard Go flag
// package so callers such as the e2e tests can register a --feature-gates flag.
func NewGoFlagFeatureGate(gate featuregate.MutableFeatureGate) flag.Value {
	return &goFlagFeatureGate{gate: gate}
}

// goFlagFeatureGate adapts component-base's feature gate to the standard Go
// flag package used by azurediskplugin.
type goFlagFeatureGate struct {
	gate featuregate.MutableFeatureGate
}

func (f *goFlagFeatureGate) String() string {
	return fmt.Sprint(f.gate)
}

func (f *goFlagFeatureGate) Set(value string) error {
	return f.gate.Set(value)
}
