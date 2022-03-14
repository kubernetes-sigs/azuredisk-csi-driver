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

endpoint=$1
image=$7

CONTROLLER_LISTEN_ADDRESS=$(echo $endpoint | awk -F ':' '{printf $2}' | sed -e 's/\/\///')
CONTROLLER_PORT=$(echo $endpoint | awk -F ':' '{printf $3}')

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
  - containerPort: ${CONTROLLER_PORT}
    hostPort: ${CONTROLLER_PORT}
    listenAddress: ${CONTROLLER_LISTEN_ADDRESS}
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
  --set controller.port="${CONTROLLER_PORT}" \
  --set azuredisk.nodeId="${NODEID_0}" \
  > /dev/null

echo "Registering AzDriverNode for ${NODEID_1} and ${NODEID_2}"
test/integration/register-azdrivernode.sh $NODEID_1
test/integration/register-azdrivernode.sh $NODEID_2

echo "Starting integration test"
test/integration/run-test.sh "$@"
# temporarily disable integration controller test
# echo "Run controller test"
# go test -v -timeout=45m ./test/integration/controller

