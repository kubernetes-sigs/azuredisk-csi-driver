#!/bin/bash

set -e

NS=kube-system
CONTAINER=azuredisk

echo "print out csi-azuredisk-controller logs ..."
echo "======================================================================================"
LABEL='app=csi-azuredisk-controller'
kubectl get pods -n${NS} -l${LABEL} \
    | awk 'NR>1 {print $1}' \
    | xargs -I {} kubectl logs {} --prefix -c${CONTAINER} -n${NS}

echo "print out csi-azuredisk-node logs ..."
echo "======================================================================================"
LABEL='app=csi-azuredisk-node'
kubectl get pods -n${NS} -l${LABEL} \
    | awk 'NR>1 {print $1}' \
    | xargs -I {} kubectl logs {} --prefix -c${CONTAINER} -n${NS}
