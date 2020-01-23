#!/bin/bash

set -e

NS=kube-system
CONTAINER=azuredisk

echo "check the driver pods if restarts ..."
restarts=$(kubectl get pods -n kube-system | grep azuredisk | awk '{print $4}')
for num in $restarts
do
    if [ "$num" -ne "0" ]
    then
        echo "there is a driver pod which has restarted"
        exit 3
    fi
done
echo "no driver pods have restarted"
echo "======================================================================================"
