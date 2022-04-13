# Azure Disk CSI driver for Kubernetes

[![Travis](https://travis-ci.org/kubernetes-sigs/azuredisk-csi-driver.svg)](https://travis-ci.org/kubernetes-sigs/azuredisk-csi-driver)
[![Coverage Status](https://coveralls.io/repos/github/kubernetes-sigs/azuredisk-csi-driver/badge.svg?branch=master)](https://coveralls.io/github/kubernetes-sigs/azuredisk-csi-driver?branch=master)
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazuredisk-csi-driver.svg?type=shield)](https://app.fossa.io/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazuredisk-csi-driver?ref=badge_shield)

### About

This driver allows Kubernetes to use [Azure Disk](https://azure.microsoft.com/en-us/services/storage/disks/) volume, csi plugin name: `disk.csi.azure.com`

### Project status

V1: GA

V2: Preview

### Container Images & Kubernetes Compatibility

#### V1

|Driver Version  |Image                                                      | supported k8s version |
|----------------|-----------------------------------------------------------|-----------------------|
|`master` branch |mcr.microsoft.com/k8s/csi/azuredisk-csi:latest             | 1.20+                 |
|v1.16.0         |mcr.microsoft.com/oss/kubernetes-csi/azuredisk-csi:v1.16.0 | 1.20+                 |
|v1.15.0         |mcr.microsoft.com/oss/kubernetes-csi/azuredisk-csi:v1.15.0 | 1.20+                 |
|v1.14.0         |mcr.microsoft.com/oss/kubernetes-csi/azuredisk-csi:v1.14.0 | 1.20+                 |

#### V2

|Driver Version  |Image                                                            | supported k8s version |
|----------------|-----------------------------------------------------------------|-----------------------|
|`main_v2` branch|                                                                 | 1.21+                 |
|v2.0.0-alpha.1  |mcr.microsoft.com/oss/kubernetes-csi/azuredisk-csi:v2.0.0-alpha.1| 1.21+                 |

### Driver parameters

Please refer to [`disk.csi.azure.com` driver parameters](./docs/driver-parameters.md)
> storage class `disk.csi.azure.com` parameters are compatible with built-in [azuredisk](https://kubernetes.io/docs/concepts/storage/volumes/#azuredisk) plugin

### Set up CSI driver on AKS cluster (only for AKS users)

follow guide [here](./docs/install-driver-on-aks.md)

### Prerequisite

- The driver depends on [cloud provider config file](https://github.com/kubernetes/cloud-provider-azure/blob/master/docs/cloud-provider-config.md), usually it's `/etc/kubernetes/azure.json` on agent nodes deployed by [AKS](https://docs.microsoft.com/en-us/azure/aks/) or [aks-engine](https://github.com/Azure/aks-engine), here is [azure.json example](./deploy/example/azure.json). <details> <summary>specify a different cloud provider config file</summary></br>create `azure-cred-file` configmap before driver installation, e.g. for OpenShift, it's `/etc/kubernetes/cloud.conf` (make sure config file path is in the `volumeMounts.mountPath`)
</br><pre>```kubectl create configmap azure-cred-file --from-literal=path="/etc/kubernetes/cloud.conf" --from-literal=path-windows="C:\\k\\cloud.conf" -n kube-system```</pre></details>
- This driver also supports [read cloud config from kubernetes secret](./docs/read-from-secret.md) as first priority
- Make sure identity used by driver has `Contributor` role on node resource group
  - When installing side-by-side with an earlier version of the driver on an AKS cluster, ensure the agentpool service principal or managed service identity is assigned to the `Contributor` role on the resource groups used to store managed disks.
- [Set up CSI driver on Azure RedHat OpenShift(ARO)](https://github.com/ezYakaEagle442/aro-pub-storage/blob/master/setup-store-CSI-driver-azure-disk.md)

### Install driver on a Kubernetes cluster

- install via [kubectl](./docs/install-azuredisk-csi-driver.md) on public Azure (please use helm for Azure Stack, RedHat/CentOS)
- install via [helm charts](./charts) on public Azure, Azure Stack, RedHat/CentOS

### Install Azure Disk CSI Driver V2 on a Kubernetes cluster (Preview)

- install via [helm charts](./charts)

### Examples

- [Basic usage](./deploy/example/e2e_usage.md)

### Features

- [Topology (Availability Zone)](./deploy/example/topology)
  - [ZRS disk support](./deploy/example/topology#zrs-disk-support)
- [Snapshot](./deploy/example/snapshot)
- [Volume Cloning](./deploy/example/cloning)
- [Volume Expansion](./deploy/example/resize)
- [Raw Block Volume](./deploy/example/rawblock)
- [Windows](./deploy/example/windows)
- [Shared Disk](./deploy/example/sharedisk)
- [Volume Limits](./deploy/example/volumelimits)
- [fsGroupPolicy](./deploy/example/fsgroup)
- [Tune disk performance on Linux](./docs/perf-profiles.md)

#### New in V2

- [Attachments Replicas for Faster Pod Failover (Preview)](./docs/design-v2.md)
  - See [pod failover demo](./deploy/example/failover/README.md) for example configuration.
- [Advanced disk performance tuning (Preview)](./docs/perf-profiles.md)

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
- [Container Storage Interface (CSI) Specification](https://github.com/container-storage-interface/spec)

[support-policy]: support.md
