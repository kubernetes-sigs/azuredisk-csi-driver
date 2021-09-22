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

image=$3

function cleanup {
  set +e

  echo 'Uninstalling CRD'
  kubectl delete crd azvolumes.disk.csi.azure.com
  kubectl delete crd azvolumeattachments.disk.csi.azure.com

  echo 'Unistalling helm chart'
  helm uninstall azuredisk-csi-driver --namespace kube-system

  echo 'Stopping kind cluster'
  ./bin/kind delete cluster
}

trap cleanup EXIT

echo 'Installing kind'
mkdir -p ./bin/
curl -Lo ./bin/kind https://kind.sigs.k8s.io/dl/v0.11.1/kind-linux-amd64
chmod +x ./bin/kind

echo 'Starting kind'
KIND_CONFIG=$(cat <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraPortMappings:
  - containerPort: 10000
    hostPort: 10000
    listenAddress: 127.0.0.1
    protocol: TCP
EOF
)

echo "$KIND_CONFIG" | ./bin/kind create cluster --config -
./bin/kind load docker-image $image

echo 'Installing helm charts'
curl https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 | bash
helm install azuredisk-csi-driver test/latest/azuredisk-csi-driver -n kube-system --wait --timeout=15m -v=5 --debug \
  --set image.azuredisk.tag=$image \
  --set azuredisk.cloudConfig="$(cat "${AZURE_CREDENTIAL_FILE}" | base64 | awk '{printf $0}'; echo)" \
  --set controller.port="10000" \
  > /dev/null

echo 'Begin to run sanity test v2'
readonly CSI_SANITY_BIN='csi-sanity'
"$CSI_SANITY_BIN" --ginkgo.v --csi.endpoint="127.0.0.1:10000" --ginkgo.skip='should work|should fail when volume does not exist on the specified path|should be idempotent|pagination should detect volumes added between pages and accept tokens when the last volume from a page is deleted|should return next token when a limited number of entries are requested|check the presence of new volumes and absence of deleted ones in the volume list|should remove target path'
