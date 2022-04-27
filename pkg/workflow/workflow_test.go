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

package workflow

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestWorkflow(t *testing.T) {
	tests := []struct {
		description string
		verifyFunc  func()
	}{
		{
			description: "Child workflow should inherit parent workflow's request id",
			verifyFunc: func() {
				ctx, parentWorkflow := New(context.Background())
				_, childWorkflow := New(ctx)
				require.Equal(t, childWorkflow.parentWorkflow, &parentWorkflow)
				require.Equal(t, parentWorkflow.requestID, childWorkflow.requestID)
			},
		},
		{
			description: "Parent workflow should only finish when child workflow finishes",
			verifyFunc: func() {
				ctx, parentWorkflow := New(context.Background())
				_, childWorkflow := New(ctx)
				parentWorkflow.Finish()
				require.Equal(t, atomic.LoadInt32(parentWorkflow.pendingCount), int32(1))
				childWorkflow.Finish()
				require.Equal(t, atomic.LoadInt32(parentWorkflow.pendingCount), int32(0))
			},
		},
		{
			description: "Parent workflow should only finish when all its child workflows finishes",
			verifyFunc: func() {
				ctx, parentWorkflow := New(context.Background())
				numChild := 3
				func() {
					for i := 0; i < numChild; i++ {
						_, childWorkflow := New(ctx)
						defer childWorkflow.Finish()
					}
					parentWorkflow.Finish()
				}()
				require.Equal(t, atomic.LoadInt32(parentWorkflow.pendingCount), int32(0))
			},
		},
		{
			description: "Parent workflow should contain all errors from its child workflows",
			verifyFunc: func() {
				ctx, parentWorkflow := New(context.Background())
				numChild := 3
				errs := make([]error, numChild)
				func() {
					for i := 0; i < numChild; i++ {
						err := status.Errorf(codes.Internal, "%d", i)
						errs[i] = err
						_, childWorkflow := New(ctx)
						defer childWorkflow.Finish(err)
					}
					parentWorkflow.Finish()
				}()
				require.Equal(t, atomic.LoadInt32(parentWorkflow.pendingCount), int32(0))
				for _, err := range errs {
					_, ok := parentWorkflow.errSet.Load(err)
					require.True(t, ok)
				}
			},
		},
	}

	for _, test := range tests {
		tt := test
		t.Run(tt.description, func(t *testing.T) {
			tt.verifyFunc()
		})
	}
}
