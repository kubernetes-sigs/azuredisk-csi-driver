# Installation with Helm

Quick start instructions for the setup and configuration of azuredisk CSI driver using Helm.

## Prerequisites

1. [install Helm Client](https://helm.sh/docs/using_helm/#installing-the-helm-client)

2. [initialize Helm and install Tiller](https://helm.sh/docs/using_helm/#initialize-helm-and-install-tiller)

## Install AzureDisk via `helm install`

```console
$ cd $GOPATH/src/github.com/kubernetes-sigs/azuredisk-csi-driver/charts/latest
$ helm package azuredisk-csi-driver
$ helm install azuredisk-csi-driver-latest.tgz --name azuredisk-csi-driver --namespace kube-system
```

## Uninstall

```console
$ helm delete --purge azuredisk-csi-driver
```
