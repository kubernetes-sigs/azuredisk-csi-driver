# Azure Disk CSI driver for Kubernetes

[![Travis](https://travis-ci.org/kubernetes-sigs/azuredisk-csi-driver.svg)](https://travis-ci.org/kubernetes-sigs/azuredisk-csi-driver)
[![Coverage Status](https://coveralls.io/repos/github/kubernetes-sigs/azuredisk-csi-driver/badge.svg?branch=master)](https://coveralls.io/github/kubernetes-sigs/azuredisk-csi-driver?branch=master)
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazuredisk-csi-driver.svg?type=shield)](https://app.fossa.io/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazuredisk-csi-driver?ref=badge_shield)

### About
This driver allows Kubernetes to access [Azure Disk](https://azure.microsoft.com/en-us/services/storage/disks/) volume, csi plugin name: `disk.csi.azure.com`

Disclaimer: Deploying this driver manually is not an officially supported Microsoft product. For a fully managed and supported experience on Kubernetes, use [AKS with the managed Azure disk csi driver](https://learn.microsoft.com/en-us/azure/aks/azure-disk-csi).

### Project status

V1: GA

V2: Preview

### Container Images & Kubernetes Compatibility

#### V1

|Driver Version  |Image                                                      | supported k8s version |
|----------------|-----------------------------------------------------------|-----------------------|
|`master` branch |mcr.microsoft.com/k8s/csi/azuredisk-csi:latest             | 1.21+                 |
|v1.26.2         |mcr.microsoft.com/oss/kubernetes-csi/azuredisk-csi:v1.26.2 | 1.21+                 |
|v1.25.0         |mcr.microsoft.com/oss/kubernetes-csi/azuredisk-csi:v1.25.0 | 1.21+                 |
|v1.24.0         |mcr.microsoft.com/oss/kubernetes-csi/azuredisk-csi:v1.24.0 | 1.21+                 |

#### V2

|Driver Version  |Image                                                            | supported k8s version |
|----------------|-----------------------------------------------------------------|-----------------------|
|`main_v2` branch|                                                                 | 1.21+                 |
|v2.0.0-beta.6   |mcr.microsoft.com/oss/kubernetes-csi/azuredisk-csi:v2.0.0-beta.6 | 1.21+                 |

### Driver parameters

Please refer to [`disk.csi.azure.com` driver parameters](./docs/driver-parameters.md)
> storage class `disk.csi.azure.com` parameters are compatible with built-in [azuredisk](https://kubernetes.io/docs/concepts/storage/volumes/#azuredisk) plugin

### Prerequisite

- The driver depends on [cloud provider config file](https://github.com/kubernetes/cloud-provider-azure/blob/master/docs/cloud-provider-config.md) (here is [config example](./deploy/example/azure.json)), config file path on different platforms:
   - [AKS](https://docs.microsoft.com/en-us/azure/aks/), [capz](https://github.com/kubernetes-sigs/cluster-api-provider-azure), [aks-engine](https://github.com/Azure/aks-engine): `/etc/kubernetes/azure.json`
   - [Azure RedHat OpenShift](https://docs.openshift.com/container-platform/4.11/storage/container_storage_interface/persistent-storage-csi-azure.html): `/etc/kubernetes/cloud.conf`
 - <details> <summary>specify a different config file path via configmap</summary></br>create configmap "azure-cred-file" before driver starts up</br><pre>kubectl create configmap azure-cred-file --from-literal=path="/etc/kubernetes/cloud.conf" --from-literal=path-windows="C:\\k\\cloud.conf" -n kube-system</pre></details>
- <details> <summary>edge zone support in cloud provider config</summary></br>`extendedLocationType` and `extendedLocationName` should be added into cloud provider config file, available values of `extendedLocationName` are `attatlanta1`, `attdallas1`, `attnewyork1`, `attdetroit1`</br><pre>```"extendedLocationType": "edgezone","extendedLocationName": "attatlanta1",```</pre></details>
 - Cloud provider config can also be specified via kubernetes secret, check details [here](./docs/read-from-secret.md)
 - Make sure identity used by driver has `Contributor` role on node resource group
   - When install open source driver on the cluster, ensure agentpool service principal or managed service identity is assigned to the `Contributor` role on the resource group used to store managed disks.

### Install driver on a Kubernetes cluster
 - install by [helm charts](./charts)
 - install by [kubectl](./docs/install-azuredisk-csi-driver.md)
 - install open source CSI driver on following platforms:
    - [AKS](./docs/install-driver-on-aks.md)
    - [Azure RedHat OpenShift](https://github.com/ezYakaEagle442/aro-pub-storage/blob/master/setup-store-CSI-driver-azure-disk.md)
 - install managed CSI driver on following platforms:
   - [AKS](https://learn.microsoft.com/en-us/azure/aks/csi-storage-drivers)
   - [Azure RedHat OpenShift](https://docs.openshift.com/container-platform/4.11/storage/container_storage_interface/persistent-storage-csi-azure.html)

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
- [Advanced disk performance tuning (Preview)](./docs/perf-profiles.md)

#### New in V2

- [Attachments Replicas for Faster Pod Failover (Preview)](./docs/design-v2.md)
  - See [pod failover demo](./deploy/example/failover/README.md) for example configuration.

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
