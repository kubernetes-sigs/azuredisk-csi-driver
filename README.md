# Azure Disk CSI Driver for Kubernetes

![linux build status](https://github.com/kubernetes-sigs/azuredisk-csi-driver/actions/workflows/linux.yml/badge.svg)
![windows build status](https://github.com/kubernetes-sigs/azuredisk-csi-driver/actions/workflows/windows.yml/badge.svg)
[![Coverage Status](https://coveralls.io/repos/github/kubernetes-sigs/azuredisk-csi-driver/badge.svg?branch=master)](https://coveralls.io/github/kubernetes-sigs/azuredisk-csi-driver?branch=master)
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazuredisk-csi-driver.svg?type=shield)](https://app.fossa.io/projects/git%2Bgithub.com%2Fkubernetes-sigs%2Fazuredisk-csi-driver?ref=badge_shield)
[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/azuredisk-csi-driver)](https://artifacthub.io/packages/search?repo=azuredisk-csi-driver)

## About

This driver allows Kubernetes to access [Azure Disk](https://azure.microsoft.com/en-us/services/storage/disks/) volumes.

- **CSI plugin name:** `disk.csi.azure.com`
- **Supported access mode:** `ReadWriteOnce`
- **Project status:** GA

> [!NOTE]
> Deploying this driver manually is not an officially supported Microsoft product. For a fully managed and supported experience on Kubernetes, use [AKS with the managed Azure Disk CSI driver](https://learn.microsoft.com/en-us/azure/aks/azure-disk-csi).

## Container Images & Kubernetes Compatibility

| Driver Version | Image                                                                | Supported K8s Version |
|----------------|----------------------------------------------------------------------|-----------------------|
| master branch  | `mcr.microsoft.com/k8s/csi/azuredisk-csi:latest`                    | 1.21+                 |
| v1.34.2        | `mcr.microsoft.com/oss/v2/kubernetes-csi/azuredisk-csi:v1.34.2`     | 1.21+                 |
| v1.33.8        | `mcr.microsoft.com/oss/v2/kubernetes-csi/azuredisk-csi:v1.33.8`     | 1.21+                 |
| v1.32.12       | `mcr.microsoft.com/oss/v2/kubernetes-csi/azuredisk-csi:v1.32.12`    | 1.21+                 |

## Driver Parameters

Please refer to [`disk.csi.azure.com` driver parameters](./docs/driver-parameters.md).

> Storage class `disk.csi.azure.com` parameters are compatible with the built-in [azuredisk](https://kubernetes.io/docs/concepts/storage/volumes/#azuredisk) plugin.

## Prerequisites

The driver depends on a [cloud provider config file](https://github.com/kubernetes/cloud-provider-azure/blob/master/docs/cloud-provider-config.md) ([config example](./deploy/example/azure.json)). Config file paths on different clusters:

| Platform | Config Path |
|----------|-------------|
| [AKS](https://docs.microsoft.com/en-us/azure/aks/), [CAPZ](https://github.com/kubernetes-sigs/cluster-api-provider-azure), [aks-engine](https://github.com/Azure/aks-engine) | `/etc/kubernetes/azure.json` |
| [Azure Red Hat OpenShift](https://docs.openshift.com/container-platform/4.11/storage/container_storage_interface/persistent-storage-csi-azure.html) | `/etc/kubernetes/cloud.conf` |

<details>
<summary>Specify a different config file path via ConfigMap</summary>

Create the ConfigMap `azure-cred-file` before the driver starts up:

```bash
kubectl create configmap azure-cred-file \
  --from-literal=path="/etc/kubernetes/cloud.conf" \
  --from-literal=path-windows="C:\\k\\cloud.conf" \
  -n kube-system
```

</details>

<details>
<summary>Edge zone support in cloud provider config</summary>

Add `extendedLocationType` and `extendedLocationName` to the cloud provider config file. Available values for `extendedLocationName`: `attatlanta1`, `attdallas1`, `attnewyork1`, `attdetroit1`.

```json
"extendedLocationType": "edgezone",
"extendedLocationName": "attatlanta1",
```

</details>

- Cloud provider config can also be specified via a Kubernetes Secret — see [details](./docs/read-from-secret.md).
- Ensure the identity used by the driver has the `Contributor` role on the node resource group.
  - When installing the open source driver, ensure the agentpool service principal or managed service identity is assigned the `Contributor` role on the resource group used to store managed disks.

## Installation

Install the driver on a Kubernetes cluster:

- Install by [Helm charts](./charts)
- Install by [kubectl](./docs/install-azuredisk-csi-driver.md)

**Install open source CSI driver:**

| Platform | Guide |
|----------|-------|
| AKS | [Install on AKS](./docs/install-driver-on-aks.md) |
| Azure Red Hat OpenShift | [Install on ARO](https://github.com/ezYakaEagle442/aro-pub-storage/blob/master/setup-store-CSI-driver-azure-disk.md) |

**Install managed CSI driver:**

| Platform | Guide |
|----------|-------|
| AKS | [AKS CSI storage drivers](https://learn.microsoft.com/en-us/azure/aks/csi-storage-drivers) |
| Azure Red Hat OpenShift | [ARO CSI Azure Disk](https://docs.openshift.com/container-platform/4.11/storage/container_storage_interface/persistent-storage-csi-azure.html) |

## Examples

- [Basic usage](./deploy/example/e2e_usage.md)

## Features

- [Topology (Availability Zone)](./deploy/example/topology)
  - [ZRS disk support](./deploy/example/topology#zrs-disk-support)
- [Snapshot](./deploy/example/snapshot)
- [Volume Cloning](./deploy/example/cloning)
- [Volume Expansion](./deploy/example/resize)
- [Modify Volume Attributes](./deploy/example/modifyvolume)
- [Raw Block Volume](./deploy/example/rawblock)
- [Windows](./deploy/example/windows)
- [Volume Limits](./deploy/example/volumelimits)
- [fsGroupPolicy](./deploy/example/fsgroup)
- [Workload identity](./docs/workload-identity.md)
- [Advanced disk performance tuning (Preview)](./docs/perf-profiles.md)

## Troubleshooting

- [CSI driver troubleshooting guide](./docs/csi-debug.md)

## Support

Please see our [support policy](support.md).

## Limitations

Please refer to [Azure Disk CSI Driver Limitations](./docs/limitations.md).

## Development

Please refer to the [development guide](./docs/csi-dev.md).

## CI Results

Check the TestGrid [provider-azure-azuredisk-csi-driver](https://testgrid.k8s.io/provider-azure-azuredisk-csi-driver) dashboard.

## Links

- [Kubernetes CSI Documentation](https://kubernetes-csi.github.io/docs/)
- [Container Storage Interface (CSI) Specification](https://github.com/container-storage-interface/spec)
