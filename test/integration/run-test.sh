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

function cleanup {
  echo 'pkill -f azurediskplugin'
  pkill -f azurediskplugin
}

t="$(date +%s)"
readonly CSC_BIN="$GOBIN/csc"
readonly volname="citest-$t"

endpoint='tcp://127.0.0.1:10000'
if [[ "$#" -gt 0 ]]; then
  endpoint="$1"
fi

node='CSINode'
if [ $# -gt 1 ]; then
  node="$2"
fi

cloud='AzurePublicCloud'
if [[ "$#" -gt 2 ]]; then
  cloud="$3"
fi

version='v2'
if [[ "$#" -gt 3 ]]; then
  version="$4"
fi

echo "Begin to run $version integration test on $cloud..."

if [[ "$cloud" == 'AzureChinaCloud' ]]; then
  sleep 25
else
  sleep 5
fi

# begin to run CSI functions one by one
"$CSC_BIN" node get-info --endpoint "$endpoint"

echo 'Create volume test:'
value=$("$CSC_BIN" controller new --endpoint "$endpoint" --cap 1,block "$volname" --req-bytes 2147483648 --params skuname=Standard_LRS,kind=managed)
sleep 15

volumeid=$(echo "$value" | awk '{print $1}' | sed 's/"//g')
echo "Got volume id: $volumeid"
volumename=$(echo "$volumeid" | awk -F / '{print $9}')

"$CSC_BIN" controller validate-volume-capabilities --endpoint "$endpoint" --cap 1,block "$volumeid"

echo 'Expand volume test'
"$CSC_BIN" controller expand-volume --endpoint "$endpoint" --req-bytes 21474836480 --cap 1,block "$volumeid"

echo 'Attach volume test:'
"$CSC_BIN" controller publish --endpoint "$endpoint" --node-id "$node" --cap 1,block "$volumeid"
if [[ "$version" == 'v2' ]]; then
  test/integration/wait-for-attach.sh "$volumename" "$node"
fi
sleep 20

echo 'ListVolumes test:'
"$CSC_BIN" controller list-volumes --endpoint "$endpoint" --max-entries 1 --starting-token 0

echo 'Detach volume test:'
"$CSC_BIN" controller unpublish --endpoint "$endpoint" --node-id "$node" "$volumeid"
if [[ "$version" == 'v2' ]]; then
  test/integration/wait-for-detach.sh "$volumename" "$node"
fi
sleep 30

echo 'Create snapshot test:'
"$CSC_BIN" controller create-snapshot snapshot-test-name --endpoint "$endpoint" --source-volume "$volumeid"
sleep 5

echo 'List snapshots test:'
"$CSC_BIN" controller list-snapshots --endpoint "$endpoint"
sleep 5

echo 'Delete snapshot test:'
"$CSC_BIN" controller delete-snapshot snapshot-test-name --endpoint "$endpoint"
sleep 5

echo 'Delete volume test:'
"$CSC_BIN" controller del --endpoint "$endpoint" "$volumeid"
sleep 15

"$CSC_BIN" identity plugin-info --endpoint "$endpoint"

echo "Integration test on $cloud is completed."
