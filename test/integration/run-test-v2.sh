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

function cleanup {
  echo 'Unistalling helm chart'
  helm uninstall azuredisk-csi-driver --namespace kube-system

  echo 'Cleaning up the minikube cache'
  minikube cache delete $7

  echo 'Stopping minikube'
  minikube stop

  echo 'Deleting minikube'
  minikube delete
  rm -r ~/.minikube
}

trap cleanup EXIT

echo 'Creating minikube'
curl -Lo minikube https://storage.googleapis.com/minikube/releases/latest/minikube-linux-amd64 \
  && chmod +x minikube

echo 'Starting minikube'
minikube start --driver=none

echo 'Load the image to minikube'
minikube cache add $7

echo 'Installing helm charts'
curl https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 | bash
helm install azuredisk-csi-driver charts/test-v2/azuredisk-csi-driver -n kube-system --wait --timeout=15m -v=5 --debug --set image.azuredisk.tag=$7

test/integration/run-test.sh $*

