#!/bin/bash

function cleanup {
  echo 'Unistalling helm chart'
  helm uninstall azuredisk-csi-driver --namespace kube-system
  echo 'Cleaning up the minikube cache'
  minikube cache delete $3
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
minikube cache add $3

echo 'Image successfully loaded to minikube'

echo 'Installing helm charts'

helm install azuredisk-csi-driver charts/test-v2/azuredisk-csi-driver -n kube-system --wait --timeout=15m -v=5 --debug --set image.azuredisk.tag=$3

echo 'Begin to run sanity test v2'
readonly CSI_SANITY_BIN='csi-sanity'
"$CSI_SANITY_BIN" --ginkgo.v --csi.endpoint="127.0.0.1:32000" --ginkgo.skip='should work|should fail when volume does not exist on the specified path|should be idempotent|pagination should detect volumes added between pages and accept tokens when the last volume from a page is deleted'
