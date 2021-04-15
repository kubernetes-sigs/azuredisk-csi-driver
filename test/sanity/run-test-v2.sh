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

readonly image=$3
echo image

function cleanup {
  set +e

  echo 'Unistalling helm chart'
  helm uninstall azuredisk-csi-driver --namespace kube-system

  echo 'Cleaning up the minikube cache'
  minikube cache delete $image

  echo 'Stopping minikube'
  minikube stop

  echo 'Deleting minikube'
  minikube delete
  rm -r ~/.minikube

  echo 'Deleting CSI sanity test binary'
  rm -rf csi-test
}

trap cleanup EXIT

echo 'Creating minikube'
curl -Lo minikube https://storage.googleapis.com/minikube/releases/latest/minikube-linux-amd64 \
  && chmod +x minikube

echo 'Start minikube'
minikube start --driver=none

echo 'Load the image to minikube'
minikube cache add $image

echo 'Installing helm charts'
curl https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 | bash
helm install azuredisk-csi-driver test/latest/azuredisk-csi-driver -n kube-system --wait --timeout=15m -v=5 --debug --set image.azuredisk.tag=$image

echo 'Begin to run sanity test v2'
readonly CSI_SANITY_BIN='csi-sanity'
"$CSI_SANITY_BIN" --ginkgo.v --csi.endpoint="127.0.0.1:10000" --ginkgo.skip='should work|should fail when volume does not exist on the specified path|should be idempotent|pagination should detect volumes added between pages and accept tokens when the last volume from a page is deleted'
