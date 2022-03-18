#!/bin/bash

# Copyright 2021 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

SCRIPT_ROOT_RELATIVE=$(dirname "${BASH_SOURCE}")/..
SCRIPT_ROOT=$(realpath "${SCRIPT_ROOT_RELATIVE}")
CONTROLLERTOOLS_PKG=${CONTROLLERTOOLS_PKG:-$(cd "${SCRIPT_ROOT}"; ls -d -1 ./vendor/sigs.k8s.io/controller-tools 2>/dev/null || echo ../code-controller-tools)}

# find or download controller-gen
pushd "${CONTROLLERTOOLS_PKG}"
trap popd exit
go run -v ./cmd/controller-gen crd:crdVersions=v1,trivialVersions=false paths="${SCRIPT_ROOT}/pkg/apis/azuredisk/..." output:crd:dir="${SCRIPT_ROOT}/pkg/apis/config"