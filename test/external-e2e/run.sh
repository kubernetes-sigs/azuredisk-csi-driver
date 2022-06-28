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
DRIVER="test"

install_ginkgo () {
    apt update -y
    apt install -y golang-ginkgo-dev
}

setup_e2e_binaries() {
    mkdir /tmp/csi-azuredisk

    # download k8s external e2e binary for kubernetes
    curl -sL https://storage.googleapis.com/kubernetes-release/release/v1.24.0/kubernetes-test-linux-amd64.tar.gz --output e2e-tests.tar.gz
    tar -xvf e2e-tests.tar.gz && rm e2e-tests.tar.gz

    # test on alternative driver name
    export EXTRA_HELM_OPTIONS="--set controller.disableAvailabilitySetNodes=true --set controller.replicas=1 --set schedulerExtender.replicas=1 --set driver.name=$DRIVER.csi.azure.com --set controller.name=csi-$DRIVER-controller --set schedulerExtender.name=csi-$DRIVER-scheduler-extender --set linux.dsName=csi-$DRIVER-node --set windows.dsName=csi-$DRIVER-node-win --set controller.vmssCacheTTLInSeconds=60 --set node.logLevel=6"

    # install the azuredisk-csi-driver driver
    make e2e-bootstrap
    sed -i "s/csi-azuredisk-controller/csi-$DRIVER-controller/g" deploy/example/metrics/csi-azuredisk-controller-svc.yaml
    make create-metrics-svc
}

print_logs() {
    sed -i "s/disk.csi.azure.com/$DRIVER.csi.azure.com/g" deploy/example/storageclass-azuredisk-csi.yaml
    bash ./hack/verify-examples.sh linux azurepubliccloud ephemeral $DRIVER
    echo "print out driver logs ..."
    bash ./test/utils/azuredisk_log.sh $DRIVER
}


install_ginkgo
setup_e2e_binaries
trap print_logs EXIT

SKIP_TESTS='\[Disruptive\]|should resize volume when PVC is edited while pod is using it|should provision storage with any volume data source|should mount multiple PV pointing to the same storage on the same node'

# Run non-serial tests in parallel
ginkgo -p --progress --v -focus="External.Storage.*$DRIVER.csi.azure.com" \
       -skip="\[Serial\]|$SKIP_TESTS" \
       kubernetes/test/bin/e2e.test -- \
       -storage.testdriver=$PROJECT_ROOT/test/external-e2e/manifest/testdriver.yaml \
       --kubeconfig=$KUBECONFIG
# Run serial tests sequentially
ginkgo --progress --v -focus="External.Storage.*$DRIVER.csi.azure.com.*\[Serial\]" \
       -skip="$SKIP_TESTS" \
       kubernetes/test/bin/e2e.test -- \
       -storage.testdriver=$PROJECT_ROOT/test/external-e2e/manifest/testdriver.yaml \
       --kubeconfig=$KUBECONFIG
