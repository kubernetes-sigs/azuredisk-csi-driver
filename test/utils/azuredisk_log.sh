#!/bin/bash

set -e

NS=kube-system
LABEL='app=csi-azuredisk-controller'
CONTAINER=azuredisk

kubectl get pods -n${NS} -l${LABEL} \
    | awk 'NR>1 {print $1}' \
    | xargs -I {} kubectl logs {} --prefix -c${CONTAINER} -n${NS}