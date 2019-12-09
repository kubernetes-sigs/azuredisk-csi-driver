# Installation with Helm 3

Quick start instructions for the setup and configuration of azuredisk CSI driver using Helm.

## Prerequisites

1. [install Helm Client 3.0+ ](https://helm.sh/docs/intro/quickstart/#install-helm)

## Install AzureDisk via `helm install`

```console
$ cd $GOPATH/src/sigs.k8s.io/azuredisk-csi-driver/charts/latest
$ helm package azuredisk-csi-driver
$ helm install azuredisk-csi-driver azuredisk-csi-driver-latest.tgz --namespace kube-system
```

## Uninstall

```console
$ helm uninstall azuredisk-csi-driver -n kube-system
```
