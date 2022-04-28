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
	"fmt"

	"github.com/go-logr/logr"
)

type Logger struct {
	logr.Logger
}

func (l Logger) V(level int) Logger {
	return Logger{l.Logger.V(level)}
}

func (l Logger) Info(msg string) {
	l.WithCallDepth(1).Info(msg)
}

func (l Logger) Infof(msg string, params ...interface{}) {
	l.WithCallDepth(1).Info(fmt.Sprintf(msg, params...))
}

func (l Logger) Error(err error, msg string) {
	l.WithCallDepth(1).Error(err, msg)
}

func (l Logger) Errorf(err error, msg string, params ...interface{}) {
	l.WithCallDepth(1).Error(err, fmt.Sprintf(msg, params...))
}
