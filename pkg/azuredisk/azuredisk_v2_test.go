//go:build azurediskv2
// +build azurediskv2

/*
Copyright 2017 The Kubernetes Authors.

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
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/klog/v2"
	azdiskfakes "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned/fake"
)

var osExitErr = errors.New("OSExit called")

func init() {
	klog.OsExit = func(code int) {
		panic(osExitErr)
	}
}

func TestRegisterAzDriverNodeOrDie(t *testing.T) {
	tests := []struct {
		description string
		setupFn     func(t *testing.T, d *fakeDriverV2) func()
	}{
		{
			description: "Should die on empty node ID",
			setupFn: func(t *testing.T, d *fakeDriverV2) func() {
				d.NodeID = ""

				return func() {
					err := recover()
					require.Equal(t, osExitErr, err)
				}
			},
		},
		{
			description: "Should die on update access denied",
			setupFn: func(t *testing.T, d *fakeDriverV2) func() {
				fakecs := d.azdiskClient.(*azdiskfakes.Clientset)
				fakecs.PrependReactor(
					"update",
					"azdrivernodes",
					func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
						return true, nil, apierrors.NewUnauthorized("access denied")
					},
				)

				return func() {
					err := recover()
					require.Equal(t, osExitErr, err)
				}
			},
		},
		{
			description: "Should die on create access denied",
			setupFn: func(t *testing.T, d *fakeDriverV2) func() {
				fakecs := d.azdiskClient.(*azdiskfakes.Clientset)
				fakecs.PrependReactor(
					"create",
					"azdrivernodes",
					func(action k8stesting.Action) (handled bool, ret runtime.Object, err error) {
						return true, nil, apierrors.NewUnauthorized("access denied")
					},
				)

				return func() {
					err := recover()
					require.Equal(t, osExitErr, err)
				}
			},
		},
		{
			description: "Should successfully create AzDriverNode",
			setupFn: func(t *testing.T, d *fakeDriverV2) func() {
				return func() {
					verifyAzDriverNode(t, d, "Driver node initializing.", false)
				}
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.description, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.TODO())
			defer cancel()

			d, err := newFakeDriverV2(t, newFakeDriverConfig())
			assert.NoError(t, err)

			cleanUpFn := test.setupFn(t, d)
			defer cleanUpFn()

			d.registerAzDriverNodeOrDie(ctx)
		})
	}
}

func TestRunAzDriverNodeHeartbeatLoop(t *testing.T) {
	tests := []struct {
		description  string
		newContextFn func(t *testing.T, d *fakeDriverV2) (context.Context, context.CancelFunc)
	}{
		{
			description: "Should return when context canceled",
			newContextFn: func(t *testing.T, d *fakeDriverV2) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.TODO())
				go func() {
					<-time.After(time.Duration(2*d.config.NodeConfig.HeartbeatFrequencyInSec) * time.Second)
					cancel()
				}()

				return ctx, func() {
					verifyAzDriverNode(t, d, "Driver node healthy.", true)
				}
			},
		},
		{
			description: "Should sleep and return when context deadline exceeded",
			newContextFn: func(t *testing.T, d *fakeDriverV2) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithTimeout(context.TODO(), time.Duration(2*d.config.NodeConfig.HeartbeatFrequencyInSec)*time.Second)

				return ctx, func() {
					defer cancel()
					verifyAzDriverNode(t, d, "Driver node healthy.", true)
				}
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.description, func(t *testing.T) {
			regCtx, regCancel := context.WithCancel(context.TODO())
			defer regCancel()

			d, err := newFakeDriverV2(t, newFakeDriverConfig())
			assert.NoError(t, err)

			d.registerAzDriverNodeOrDie(regCtx)

			// wait for azDriverNode to be created
			err = wait.PollImmediate(time.Duration(1)*time.Second, time.Duration(1)*time.Minute, func() (bool, error) {
				_, err := d.azDriverNodeInformer.Lister().AzDriverNodes(d.config.ObjectNamespace).Get(d.NodeID)
				if err != nil {
					if apierrors.IsNotFound(err) {
						return false, nil
					}
					return false, err
				}
				return true, nil
			})

			d.config.NodeConfig.HeartbeatFrequencyInSec = 1

			ctx, cancel := test.newContextFn(t, d)
			defer cancel()

			d.runAzDriverNodeHeartbeatLoop(ctx)
		})
	}
}

func verifyAzDriverNode(t *testing.T, d *fakeDriverV2, expectedStatusMessage string, expectedReadyForAllocation bool) {
	azDriverNode, err := d.azdiskClient.DiskV1beta2().AzDriverNodes(d.config.ObjectNamespace).Get(context.TODO(), d.NodeID, metav1.GetOptions{})
	require.NoError(t, err)
	require.NotNil(t, azDriverNode.Status)
	assert.NotNil(t, azDriverNode.Status.StatusMessage)
	assert.Equal(t, expectedStatusMessage, *azDriverNode.Status.StatusMessage)
	require.NotNil(t, azDriverNode.Status.ReadyForVolumeAllocation)
	assert.Equal(t, expectedReadyForAllocation, *azDriverNode.Status.ReadyForVolumeAllocation)
	require.NotNil(t, azDriverNode.Status.LastHeartbeatTime)
}
