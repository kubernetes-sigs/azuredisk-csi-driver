#!/bin/bash

PODNAME=$(kubectl get pods -n kube-system | grep csi-azuredisk-controller | awk '{print $1}')
kubectl logs ${PODNAME} -c azuredisk -n kube-system


