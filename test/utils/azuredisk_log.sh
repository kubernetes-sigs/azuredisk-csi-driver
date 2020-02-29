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

set -e

NS=kube-system
CONTAINER=azuredisk

echo "print out all $NS namespace pods status ..."
kubectl get pods -n${NS} -o wide
echo "======================================================================================"

echo "print out csi-azuredisk-controller logs ..."
echo "======================================================================================"
LABEL='app=csi-azuredisk-controller'
kubectl get pods -n${NS} -l${LABEL} \
    | awk 'NR>1 {print $1}' \
    | xargs -I {} kubectl logs {} --prefix -c${CONTAINER} -n${NS}

echo "print out csi-snapshot-controller logs ..."
echo "======================================================================================"
LABEL='app=csi-snapshot-controller'
kubectl get pods -n${NS} -l${LABEL} \
    | awk 'NR>1 {print $1}' \
    | xargs -I {} kubectl logs {} --prefix -n${NS}

echo "print out csi-azuredisk-node logs ..."
echo "======================================================================================"
LABEL='app=csi-azuredisk-node'
kubectl get pods -n${NS} -l${LABEL} \
    | awk 'NR>1 {print $1}' \
    | xargs -I {} kubectl logs {} --prefix -c${CONTAINER} -n${NS}
