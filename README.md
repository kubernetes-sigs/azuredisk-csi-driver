# Azure Disk CSI driver for Kubernetes
[![Travis](https://travis-ci.org/kubernetes-sigs/azuredisk-csi-driver.svg)](https://travis-ci.org/kubernetes-sigs/azuredisk-csi-driver)
[![Coverage Status](https://coveralls.io/repos/github/kubernetes-sigs/azuredisk-csi-driver/badge.svg?branch=master)](https://coveralls.io/github/kubernetes-sigs/azuredisk-csi-driver?branch=master)
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazuredisk-csi-driver.svg?type=shield)](https://app.fossa.io/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazuredisk-csi-driver?ref=badge_shield)

### About
This driver allows Kubernetes to use [Azure Disk](https://azure.microsoft.com/en-us/services/storage/disks/) volume, csi plugin name: `disk.csi.azure.com`

### Project status: GA

### Container Images & Kubernetes Compatibility
|Driver Version  |Image                                           | supported k8s version |
|----------------|------------------------------------------------|-----------------------|
|master branch   |mcr.microsoft.com/k8s/csi/azuredisk-csi:latest  | 1.17+                 |
|v1.2.2          |mcr.microsoft.com/k8s/csi/azuredisk-csi:v1.2.2  | 1.17+                 |
|v1.1.1          |mcr.microsoft.com/k8s/csi/azuredisk-csi:v1.1.1  | 1.16+                 |
|v1.0.0          |mcr.microsoft.com/k8s/csi/azuredisk-csi:v1.0.0  | 1.16+                 |

### Driver parameters
Please refer to [`disk.csi.azure.com` driver parameters](./docs/driver-parameters.md)
 > storage class `disk.csi.azure.com` parameters are compatible with built-in [azuredisk](https://kubernetes.io/docs/concepts/storage/volumes/#azuredisk) plugin

### Prerequisite
 - The driver depends on [cloud provider config file](https://kubernetes-sigs.github.io/cloud-provider-azure/install/configs/#setting-azure-cloud-provider-from-kubernetes-secrets), usually it's `/etc/kubernetes/azure.json` on all kubernetes nodes deployed by [AKS](https://docs.microsoft.com/en-us/azure/aks/) or [aks-engine](https://github.com/Azure/aks-engine), here is [azure.json example](./deploy/example/azure.json).
 > To specify a different cloud provider config file, create `azure-cred-file` configmap before driver installation, e.g. for OpenShift, it's `/etc/kubernetes/cloud.conf` (make sure config file path is in the `volumeMounts.mountPath`)
 > ```console
 > kubectl create configmap azure-cred-file --from-literal=path="/etc/kubernetes/cloud.conf" --from-literal=path-windows="C:\\k\\cloud.conf" -n kube-system
 > ```
 - This driver also supports [read cloud config from kuberenetes secret](./docs/read-from-secret.md).
 - If cluster identity is [Managed Service Identity(MSI)](https://docs.microsoft.com/en-us/azure/aks/use-managed-identity), make sure user assigned identity has `Contributor` role on node resource group
 - [How to set up CSI driver on Azure RedHat OpenShift(ARO)](https://github.com/ezYakaEagle442/aro-pub-storage/blob/master/setup-store-CSI-driver-azure-disk.md)

### Install driver on a Kubernetes cluster
 - install via [kubectl](./docs/install-azuredisk-csi-driver.md) on public Azure (please use helm for other cloud environments, e.g. Azure Stack)
 - install via [helm charts](./charts) on public Azure and Azure Stack

### Examples
 - [Basic usage](./deploy/example/e2e_usage.md)
 
### Features
 - [Topology(Availability Zone)](./deploy/example/topology)
 - [Snapshot](./deploy/example/snapshot)
 - [Volume Cloning](./deploy/example/cloning)
 - [Volume Expansion](./deploy/example/resize) 
 - [Raw Block Volume](./deploy/example/rawblock)
 - [Windows](./deploy/example/windows)
 - [Shared Disk](./deploy/example/sharedisk)
 - [Volume Limits](./deploy/example/volumelimits)

### Troubleshooting
 - [CSI driver troubleshooting guide](./docs/csi-debug.md)
 
### Support
 - Please see our [support policy][support-policy]

### Limitations
 - Please refer to [Azure Disk CSI Driver Limitations](./docs/limitations.md)

## Kubernetes Development
 - Please refer to [development guide](./docs/csi-dev.md)

### View CI Results
 - Check testgrid [provider-azure-azuredisk-csi-driver](https://testgrid.k8s.io/provider-azure-azuredisk-csi-driver) dashboard.

### Links
 - [Kubernetes CSI Documentation](https://kubernetes-csi.github.io/docs/)
 - [CSI Drivers](https://github.com/kubernetes-csi/drivers)
 - [Container Storage Interface (CSI) Specification](https://github.com/container-storage-interface/spec)

[support-policy]: support.md
