#!/usr/bin/env bash

# Copyright 2020 The Kubernetes Authors.
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

set -o errexit
set -o nounset
set -o pipefail

SCRIPT_ROOT_RELATIVE=$(dirname "${BASH_SOURCE}")/..

SCRIPT_ROOT=$(realpath "${SCRIPT_ROOT_RELATIVE}")

go install k8s.io/code-generator/cmd/{client-gen,lister-gen,informer-gen,deepcopy-gen,register-gen}

# Go installs the above commands to get installed in $GOBIN if defined, and $GOPATH/bin otherwise:
GOBIN="$(go env GOBIN)"
gobin="${GOBIN:-$(go env GOPATH)/bin}"

GOROOT_TEMP="$GOROOT"
OUTPUT_PKG="github.com/abhisheksinghbaghel/azuredisk-csi-driver/pkg/apis/client"
APIS_PKG="github.com/abhisheksinghbaghel/azuredisk-csi-driver/pkg/apis"
API_NAME="azuredisk"
API_VERSION="v1alpha1"
CLIENTSET_NAME="versioned"
CLIENTSET_PKG_NAME="clientset"
INPUT_PKG="$APIS_PKG/$API_NAME/$API_VERSION"

if [[ "${VERIFY_CODEGEN:-}" == "true" ]]; then
  echo "Running in verification mode"
  VERIFY_FLAG="--verify-only"
fi
COMMON_FLAGS="${VERIFY_FLAG:-} --go-header-file ${SCRIPT_ROOT}/hack/boilerplate/boilerplate.go.txt"

echo "setting GOROOT=$GOPATH"
export GOROOT=$GOPATH
echo "Generating deepcopy funcs"
"${gobin}/deepcopy-gen" --input-dirs "${INPUT_PKG}" -O zz_generated.deepcopy --bounding-dirs "${APIS_PKG}" ${COMMON_FLAGS}

echo "Generating clientset at ${OUTPUT_PKG}/${CLIENTSET_PKG_NAME}"
"${gobin}/client-gen" --clientset-name "${CLIENTSET_NAME}" --input-base "" --input "${INPUT_PKG}" --output-package "${OUTPUT_PKG}/${CLIENTSET_PKG_NAME}" ${COMMON_FLAGS}

echo "Generating listers at ${OUTPUT_PKG}/listers"
"${gobin}/lister-gen" --input-dirs "${INPUT_PKG}" --output-package "${OUTPUT_PKG}/listers" -v 10 ${COMMON_FLAGS}

echo "Generating informers at ${OUTPUT_PKG}/informers"
"${gobin}/informer-gen" \
         --input-dirs "${INPUT_PKG}" \
         --versioned-clientset-package "${OUTPUT_PKG}/${CLIENTSET_PKG_NAME}/${CLIENTSET_NAME}" \
         --listers-package "${OUTPUT_PKG}/listers" \
         --output-package "${OUTPUT_PKG}/informers" \
         ${COMMON_FLAGS}

echo "setting GOROOT=$GOROOT_TEMP"
export GOROOT=$GOROOT_TEMP