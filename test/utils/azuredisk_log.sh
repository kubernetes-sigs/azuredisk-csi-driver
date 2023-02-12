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

# set -e

NS=kube-system
CONTAINER=azuredisk
DRIVER=azuredisk
if [[ "$#" -gt 0 ]]; then
    DRIVER=$1
fi

echo "print out all nodes status ..."
kubectl get nodes -o wide --show-labels
echo "======================================================================================"

echo "print out all default namespace pods status ..."
kubectl get pods -n default -o wide
echo "======================================================================================"

echo "print out all $NS namespace pods status ..."
kubectl get pods -n${NS} -o wide
echo "======================================================================================"

echo "print out controller logs ..."
echo "======================================================================================"
LABEL="app=csi-$DRIVER-controller"
kubectl get pods -n${NS} -l${LABEL} \
    | awk 'NR>1 {print $1}' \
    | xargs -I {} bash -c "echo 'dumping logs for ${NS}/{}/${DRIVER}' && kubectl logs {} -c${CONTAINER} -n${NS}"

#echo "print out csi-snapshot-controller logs ..."
#echo "======================================================================================"
#LABEL='app=csi-snapshot-controller'
#kubectl get pods -n${NS} -l${LABEL} \
#    | awk 'NR>1 {print $1}' \
#    | xargs -I {} bash -c "echo 'dumping logs for ${NS}/{}' && kubectl logs {} -n${NS}"

if [ -z "$NODE_MACHINE_TYPE" ]; then
  echo "NODE_MACHINE_TYPE is not defined"
else
  echo "print out controller proxy logs ..."
  echo "======================================================================================"
  kubectl get pods -n${NS} -l${LABEL} \
    | awk 'NR>1 {print $1}' \
    | xargs -I {} bash -c "echo 'dumping logs for ${NS}/{}/${DRIVER}' && kubectl logs {} -c proxy -n${NS}"
fi

echo "print out csi-$DRIVER-node logs ..."
echo "======================================================================================"
LABEL="app=csi-$DRIVER-node"
kubectl get pods -n${NS} -l${LABEL} \
    | awk 'NR>1 {print $1}' \
    | xargs -I {} bash -c "echo 'dumping logs for ${NS}/{}/${DRIVER}' && kubectl logs {} -c${CONTAINER} -n${NS}"

echo "print out csi-$DRIVER-node-win logs ..."
echo "======================================================================================"
LABEL="app=csi-$DRIVER-node-win"
kubectl get pods -n${NS} -l${LABEL} \
    | awk 'NR>1 {print $1}' \
    | xargs -I {} bash -c "echo 'dumping logs for ${NS}/{}/${DRIVER}' && kubectl logs {} -c${CONTAINER} -n${NS}"

mapfile -t CONTROLLER_PODS < <(kubectl get pods -n kube-system -l app=csi-azuredisk-controller --output jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
for CONTROLLER_POD in "${CONTROLLER_PODS[@]}"; do
    kubectl port-forward -n kube-system "pods/${CONTROLLER_POD}" 32000:29604 &
    KUBEPROXY=$!
    sleep 10
    echo "print out ${CONTROLLER_POD} metrics ..."
    echo "======================================================================================"
    curl http://localhost:32000/metrics
    kill -9 $KUBEPROXY
done
