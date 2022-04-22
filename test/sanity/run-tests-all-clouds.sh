#!/bin/bash

# Copyright 2019 The Kubernetes Authors.
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

function install_csi_sanity_bin {
  echo 'Installing CSI sanity test binary...'
  mkdir -p $GOPATH/src/github.com/kubernetes-csi
  pushd $GOPATH/src/github.com/kubernetes-csi
  export GO111MODULE=off
  git clone https://github.com/kubernetes-csi/csi-test.git -b v4.3.0
  pushd csi-test/cmd/csi-sanity
  make install
  popd
  popd
}

if [[ -z "$(command -v csi-sanity)" ]]; then
	install_csi_sanity_bin
fi

if [[ "$#" -ge 2 && "$1" == "v2" ]]; 
then
echo 'Running v2 sanity tests'
test/sanity/run-test-v2.sh "$NODEID" "$@"
else
echo 'Running v1 sanity tests'
test/sanity/run-test.sh "$NODEID" "$@"
fi
