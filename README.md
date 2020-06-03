# Azure Disk CSI driver for Kubernetes
[![Travis](https://travis-ci.org/kubernetes-sigs/azuredisk-csi-driver.svg)](https://travis-ci.org/kubernetes-sigs/azuredisk-csi-driver)
[![Coverage Status](https://coveralls.io/repos/github/kubernetes-sigs/azuredisk-csi-driver/badge.svg?branch=master)](https://coveralls.io/github/kubernetes-sigs/azuredisk-csi-driver?branch=master)
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazuredisk-csi-driver.svg?type=shield)](https://app.fossa.io/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazuredisk-csi-driver?ref=badge_shield)

### About
This driver allows Kubernetes to use [azure disk](https://azure.microsoft.com/en-us/services/storage/disks/) volume, csi plugin name: `disk.csi.azure.com`

### Container Images & Kubernetes Compatibility
|Azure Disk CSI Driver Version  | Image                                              | 1.14+ |
|-------------------------------|----------------------------------------------------|-------|
|master branch                  |mcr.microsoft.com/k8s/csi/azuredisk-csi:latest      | yes   |
|v0.7.0                         |mcr.microsoft.com/k8s/csi/azuredisk-csi:v0.7.0      | yes   |
|v0.6.0                         |mcr.microsoft.com/k8s/csi/azuredisk-csi:v0.6.0      | yes   |

### Driver parameters
Please refer to [`disk.csi.azure.com` driver parameters](./docs/driver-parameters.md)
 > storage class `disk.csi.azure.com` parameters are compatible with built-in [azuredisk](https://kubernetes.io/docs/concepts/storage/volumes/#azuredisk) plugin

### Prerequisite
 - The driver depends on [cloud provider config file](https://github.com/kubernetes/cloud-provider-azure/blob/master/docs/cloud-provider-config.md), usually it's `/etc/kubernetes/azure.json` on all kubernetes nodes deployed by [AKS](https://docs.microsoft.com/en-us/azure/aks/) or [aks-engine](https://github.com/Azure/aks-engine), here is [azure.json example](./deploy/example/azure.json).
 > To specify a different cloud provider config file, create `azure-cred-file` configmap before driver installation, e.g. for OpenShift, it's `/etc/kubernetes/cloud.conf` (make sure config file path is in the `volumeMounts.mountPath`)
 > ```console
 > kubectl create configmap azure-cred-file --from-literal=path="/etc/kubernetes/cloud.conf" --from-literal=path-windows="C:\\k\\cloud.conf" -n kube-system
 > ```
 - This driver also supports [read cloud config from kuberenetes secret](./docs/read-from-secret.md).
 - If cluster identity is [Managed Service Identity(MSI)](https://docs.microsoft.com/en-us/azure/aks/use-managed-identity), make sure user assigned identity has `Contributor` role on node resource group

### Install azuredisk CSI driver on a Kubernetes cluster
Please refer to [install azuredisk csi driver](./docs/install-azuredisk-csi-driver.md)

### Examples
 - [Basic usage](./deploy/example/e2e_usage.md)
 - [Topology(Availability Zone)](./deploy/example/topology)
 - [Snapshot](./deploy/example/snapshot)
 - [Volume Cloning](./deploy/example/cloning)
 - [Volume Expansion](./deploy/example/resize) 
 - [Raw Block Volume](./deploy/example/rawblock)
 - [Windows](./deploy/example/windows)
 - [Shared Disk](./deploy/example/sharedisk)
 
### Troubleshooting
 - [CSI driver troubleshooting guide](./docs/csi-debug.md)

## Kubernetes Development
Please refer to [development guide](./docs/csi-dev.md)

### Links
 - [Kubernetes CSI Documentation](https://kubernetes-csi.github.io/docs/)
 - [Analysis of the CSI Spec](https://blog.thecodeteam.com/2017/11/03/analysis-csi-spec/)
 - [CSI Drivers](https://github.com/kubernetes-csi/drivers)
 - [Container Storage Interface (CSI) Specification](https://github.com/container-storage-interface/spec)
