#!/bin/bash

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

set -euo pipefail

function cleanup {
  echo 'pkill -f azurediskplugin'
  pkill -f azurediskplugin
  echo 'Deleting CSI sanity test binary'
  rm -rf csi-test
}

trap cleanup EXIT

readonly endpoint='unix:///tmp/csi.sock'
nodeid='CSINode'
if [[ "$#" -gt 0 ]] && [[ -n "$1" ]]; then
  nodeid="$1"
fi

ARCH=$(uname -p)
if [[ "${ARCH}" == "x86_64" || ${ARCH} == "unknown" ]]; then
  ARCH="amd64"
fi

if [[ "$#" -lt 2 || "$2" != "v2" ]]; then
  _output/${ARCH}/azurediskplugin --endpoint "$endpoint" --nodeid "$nodeid" -v=5 -support-zone=false -enable-disk-capacity-check=true &
else
  _output/${ARCH}/azurediskpluginv2 --endpoint "$endpoint" --nodeid "$nodeid" -v=5 -support-zone=false -enable-disk-capacity-check=true &
fi

# sleep a while waiting for azurediskplugin start up
sleep 1

echo 'Begin to run sanity test...'
readonly CSI_SANITY_BIN='csi-sanity'
"$CSI_SANITY_BIN" --ginkgo.v --csi.endpoint="$endpoint" --ginkgo.skip='should work|should fail when volume does not exist on the specified path|should be idempotent|pagination should detect volumes added between pages and accept tokens when the last volume from a page is deleted|should remove target path'
