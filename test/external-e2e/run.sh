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

# Ensure that we have the desired version of the ginkgo test runner.

set -xe

PROJECT_ROOT=$(git rev-parse --show-toplevel)

install_ginkgo () {
    apt update -y
    apt install -y golang-ginkgo-dev
}

setup_e2e_binaries() {
    mkdir /tmp/csi-azuredisk

    # download k8s external e2e binary for kubernetes v1.21
    curl -sL https://storage.googleapis.com/kubernetes-release/release/v1.21.0/kubernetes-test-linux-amd64.tar.gz --output e2e-tests.tar.gz
    tar -xvf e2e-tests.tar.gz && rm e2e-tests.tar.gz

    # enable fsGroupPolicy (only available from k8s 1.20)
    export EXTRA_HELM_OPTIONS="--set feature.enableFSGroupPolicy=true --set snapshot.image.csiSnapshotter.tag=v4.0.0 --set snapshot.image.csiSnapshotController.tag=v4.0.0 --set snapshot.apiVersion=ga"
    # install the azuredisk-csi-driver driver
    make e2e-bootstrap
    make create-metrics-svc
}

print_logs() {
    echo "print out driver logs ..."
    bash ./test/utils/azuredisk_log.sh
}


install_ginkgo
setup_e2e_binaries
trap print_logs EXIT

ginkgo -p --progress --v -focus='External.Storage.*disk.csi.azure.com' \
       -skip='\[Disruptive\]|\[Slow\]' kubernetes/test/bin/e2e.test -- \
       -storage.testdriver=$PROJECT_ROOT/test/external-e2e/manifest/testdriver.yaml \
       --kubeconfig=$KUBECONFIG
