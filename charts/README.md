# Install CSI driver with Helm 3

- [Install CSI driver with Helm 3](#install-csi-driver-with-helm-3)
  - [Prerequisites](#prerequisites)
    - [Tips](#tips)
  - [Helm Chart Repository Management](#helm-chart-repository-management)
    - [add the Helm chart repository](#add-the-helm-chart-repository)
    - [search for all available chart versions](#search-for-all-available-chart-versions)
    - [update the repository](#update-the-repository)
    - [install a specific version](#install-a-specific-version)
    - [install on Azure Stack](#install-on-azure-stack)
    - [install on RedHat/CentOS](#install-on-redhatcentos)
    - [install driver with customized driver name, deployment name](#install-driver-with-customized-driver-name-deployment-name)
    - [uninstall CSI driver](#uninstall-csi-driver)
    - [latest chart configuration](#latest-chart-configuration)
  - [Troubleshooting](#troubleshooting)

---

## Prerequisites

- [install Helm](https://helm.sh/docs/intro/quickstart/#install-helm)
- [add the Chart repository](#add-the-helm-chart-repository)

### Tips

- schedule controller running on control plane node: `--set controller.runOnControlPlane=true`
- set replica of controller as `1`: `--set controller.replicas=1`
- specify different cloud config secret for the driver:
  - `--set controller.cloudConfigSecretName`
  - `--set controller.cloudConfigSecretNamesapce`
  - `--set node.cloudConfigSecretName`
  - `--set node.cloudConfigSecretNamesapce`
- switch to `mcr.azk8s.cn` repository in Azure China: `--set image.baseRepo=mcr.azk8s.cn`
- Microk8s based kubernetes recommended settings:
  - `--set linux.kubelet="/var/snap/microk8s/common/var/lib/kubelet"` - sets correct path to microk8s kubelet even though a user has a folder link to it.
---

## Helm Chart Repository Management

### add the Helm chart repository

```console
helm repo add azuredisk-csi-driver https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/charts
```

### search for all available chart versions

```console
helm search repo -l azuredisk-csi-driver
```

### update the repository

```console
helm repo update azuredisk-csi-driver
```

---

### install a specific version

```console
helm install azuredisk-csi-driver azuredisk-csi-driver/azuredisk-csi-driver --namespace kube-system --version 1.33.7
```

### install on Azure Stack

```console
helm install azuredisk-csi-driver azuredisk-csi-driver/azuredisk-csi-driver --namespace kube-system --set cloud=AzureStackCloud
```

### install on RedHat/CentOS

```console
helm install azuredisk-csi-driver azuredisk-csi-driver/azuredisk-csi-driver --namespace kube-system --set linux.distro=fedora
```

### install driver with customized driver name, deployment name

> only supported from `v1.5.0`+

- following example would install a driver with name `disk2`

```console
helm install azuredisk-csi-driver azuredisk-csi-driver/azuredisk-csi-driver \
  --namespace kube-system \
  --set driver.name="disk2.csi.azure.com" \
  --set controller.name="csi-azuredisk2-controller" \
  --set rbac.name=azuredisk2 \
  --set serviceAccount.controller=csi-azuredisk2-controller-sa \
  --set serviceAccount.node=csi-azuredisk2-node-sa \
  --set linux.dsName=csi-azuredisk2-node \
  --set windows.dsName=csi-azuredisk2-node-win \
  --set node.livenessProbe.healthPort=39705
```

### uninstall CSI driver

```console
helm uninstall azuredisk-csi-driver -n kube-system
```

### latest chart configuration

The following table lists the configurable parameters of the latest Azure Disk CSI Driver chart and default values.

| Parameter                                         | Description                                                | Default                                                      |
| ------------------------------------------------- | ---------------------------------------------------------- | ------------------------------------------------------------ |
| `azureCredentialFileConfigMap`                    | alternative ConfigMap name for the credentials file        | `azure-cred-file` |
| `driver.name`                                     | alternative driver name                                    | `disk.csi.azure.com` |
| `driver.customUserAgent`                          | custom userAgent                                           | `` |
| `driver.userAgentSuffix`                          | userAgent suffix                                           | `OSS-helm` |
| `driver.volumeAttachLimit`                        | maximum number of attachable volumes per node maximum number is defined according to node instance type by default(`-1`)                        | `-1` |
| `driver.azureGoSDKLogLevel`                       | [Azure go sdk log level](https://github.com/Azure/azure-sdk-for-go/blob/main/documentation/previous-versions-quickstart.md#built-in-basic-requestresponse-logging)  | ``(no logs), `DEBUG`, `INFO`, `WARNING`, `ERROR`, [etc](https://github.com/Azure/go-autorest/blob/50e09bb39af124f28f29ba60efde3fa74a4fe93f/logger/logger.go#L65-L73) |
| `feature.enableFSGroupPolicy`                     | enable `fsGroupPolicy` on a k8s 1.20+ cluster              | `true`                      |
| `image.baseRepo`                                  | base repository of driver images                           | `mcr.microsoft.com`                      |
| `image.azuredisk.repository`                      | azuredisk-csi-driver container image                          | `/oss/kubernetes-csi/azuredisk-csi`                      |
| `image.azuredisk.tag`                             | azuredisk-csi-driver container image tag                      | ``                                                       |
| `image.azuredisk.pullPolicy`                      | azuredisk-csi-driver image pull policy                     | `IfNotPresent`                                                 |
| `image.csiProvisioner.repository`                 | csi-provisioner container image                               | `/oss/kubernetes-csi/csi-provisioner`         |
| `image.csiProvisioner.tag`                        | csi-provisioner container image tag                           | `v6.0.0`                                                       |
| `image.csiProvisioner.pullPolicy`                 | csi-provisioner image pull policy                          | `IfNotPresent`                                                 |
| `image.csiAttacher.repository`                    | csi-attacher container image                                  | `/oss/kubernetes-csi/csi-attacher`            |
| `image.csiAttacher.tag`                           | csi-attacher container image tag                              | `v4.8.1`                                                       |
| `image.csiAttacher.pullPolicy`                    | csi-attacher image pull policy                             | `IfNotPresent`                                                 |
| `image.csiResizer.repository`                     | csi-resizer container image                                   | `/oss/kubernetes-csi/csi-resizer`             |
| `image.csiResizer.tag`                            | csi-resizer container image tag                               | `v2.0.0`                                                       |
| `image.csiResizer.pullPolicy`                     | csi-resizer image pull policy                              | `IfNotPresent`                                                 |
| `image.livenessProbe.repository`                  | liveness-probe container image                                | `/oss/kubernetes-csi/livenessprobe`           |
| `image.livenessProbe.tag`                         | liveness-probe container image tag                            | `v2.17.0`                                                       |
| `image.livenessProbe.pullPolicy`                  | liveness-probe image pull policy                           | `IfNotPresent`                                                 |
| `image.nodeDriverRegistrar.repository`            | csi-node-driver-registrar container image                     | `/oss/kubernetes-csi/csi-node-driver-registrar` |
| `image.nodeDriverRegistrar.tag`                   | csi-node-driver-registrar container image tag                 | `v2.15.0`                                                       |
| `image.nodeDriverRegistrar.pullPolicy`            | csi-node-driver-registrar image pull policy                | `IfNotPresent`                                                 |
| `imagePullSecrets`                                | Specify docker-registry secret names as an array           | [] (does not add image pull secrets to deployed pods)        |                                       |
| `serviceAccount.create`                           | whether create service account of csi-azuredisk-controller, csi-azuredisk-node, and snapshot-controller| `true`                                                    |
| `serviceAccount.controller`                       | name of service account for csi-azuredisk-controller       | `csi-azuredisk-controller-sa`                                  |
| `serviceAccount.node`                             | name of service account for csi-azuredisk-node             | `csi-azuredisk-node-sa`                                        |
| `serviceAccount.snapshotController`               | name of service account for csi-snapshot-controller        | `csi-snapshot-controller-sa`                                   |
| `rbac.create`                                     | whether create rbac of csi-azuredisk-controller            | `true`                                                         |
| `rbac.name`                                       | driver name in rbac role                                   | `azuredisk`                                                         |
| `controller.name`                                 | name of driver deployment                                  | `csi-azuredisk-controller`
| `controller.cloudConfigSecretName`                | cloud config secret name of controller driver              | `azure-cloud-provider`
| `controller.cloudConfigSecretNamespace`           | cloud config secret namespace of controller driver         | `kube-system`
| `controller.allowEmptyCloudConfig`                | Whether allow running controller driver without cloud config          | `false`
| `controller.enableTrafficManager`                 | Whether enable [traffic manager proxy](https://github.com/Azure/aks-traffic-manager) sidecar          | `false`
| `controller.otelTracing.enabled`                  | whether enable opentelemetry tracing                       | `false`                                                        |
| `controller.otelTracing.otelServiceName`          | name of the service associated with the produced traces          | `csi-azuredisk-controller`                               |
| `controller.otelTracing.otelExporterEndpoint`     | opentelemetry-compatible endpoint where traces are sent          | `"http://localhost:4317"`                                |
| `controller.trafficManagerPort`                   | default [traffic manager proxy](https://github.com/Azure/aks-traffic-manager) port number          | `7788`
| `controller.replicas`                             | the replicas of csi-azuredisk-controller                   | `2`                                                            |
| `controller.metricsPort`                          | metrics port of csi-azuredisk-controller                   | `29604`                                                        |
| `controller.livenessProbe.healthPort`             | health check port for liveness probe                       | `29602` |
| `controller.runOnMaster`                          | run csi-azuredisk-controller on master node(deprecated on k8s 1.25+)                | `false`                                                        |
| `controller.runOnControlPlane`                    | run controller on control plane node                                                          |`false`                                                           |
| `controller.vmssCacheTTLInSeconds`                | vmss cache TTL in seconds (600 by default)                                |`-1` (use default value)                                                          |
| `controller.vmType`                | type of agent node. available values: `vmss`, `standard`                     |`` (use default value in cloud config)                                                          |
| `controller.logLevel`                             | controller driver log level                                |`5`                                                           |
| `controller.tolerations`                          | controller pod tolerations                                 |                                                              |
| `controller.affinity`                             | controller pod affinity                               | `{}`                                                             |
| `controller.nodeSelector`                         | controller pod node selector                          | `{}`                                                             |
| `controller.labels`                               | controller deployment extra labels                    | `{}`
| `controller.annotations`                          | controller deployment extra annotations               | `{}`
| `controller.podLabels`                            | controller pods extra labels                          | `{}`
| `controller.podAnnotations`                       | controller pods extra annotations                     | `{}`
| `controller.hostNetwork`                          | `hostNetwork` setting on controller driver(could be disabled if controller does not depend on MSI setting)                            | `true`                                                            | `true`, `false`
| `controller.resources.csiProvisioner.limits.memory`   | csi-provisioner memory limits                         | 500Mi                                                          |
| `controller.resources.csiProvisioner.requests.cpu`    | csi-provisioner cpu requests                   | 10m                                                            |
| `controller.resources.csiProvisioner.requests.memory` | csi-provisioner memory requests                | 20Mi                                                           |
| `controller.resources.csiAttacher.limits.memory`      | csi-attacher memory limits                         | 500Mi                                                          |
| `controller.resources.csiAttacher.requests.cpu`       | csi-attacher cpu requests                   | 10m                                                            |
| `controller.resources.csiAttacher.requests.memory`    | csi-attacher memory requests                | 20Mi                                                           |
| `controller.resources.csiResizer.limits.memory`       | csi-resizer memory limits                         | 500Mi                                                          |
| `controller.resources.csiResizer.requests.cpu`        | csi-resizer cpu requests                   | 10m                                                            |
| `controller.resources.csiResizer.requests.memory`     | csi-resizer memory requests                | 20Mi                                                           |
| `controller.resources.csiSnapshotter.limits.memory`   | csi-snapshotter memory limits                         | 400Mi                                                          |
| `controller.resources.csiSnapshotter.requests.cpu`    | csi-snapshotter cpu requests                   | 10m                                                            |
| `controller.resources.csiSnapshotter.requests.memory` | csi-snapshotter memory requests                | 20Mi                                                           |
| `controller.resources.livenessProbe.limits.memory`    | liveness-probe memory limits                          | 100Mi                                                          |
| `controller.resources.livenessProbe.requests.cpu`     | liveness-probe cpu requests                    | 10m                                                            |
| `controller.resources.livenessProbe.requests.memory`  | liveness-probe memory requests                 | 20Mi                                                           |
| `controller.resources.azuredisk.limits.memory`        | azuredisk memory limits                         | 500Mi                                                          |
| `controller.resources.azuredisk.requests.cpu`         | azuredisk cpu requests                   | 10m                                                            |
| `controller.resources.azuredisk.requests.memory`      | azuredisk memory requests                | 20Mi                                                           |
| `node.cloudConfigSecretName`                      | cloud config secret name of node driver                    | `azure-cloud-provider`
| `node.cloudConfigSecretNamespace`                 | cloud config secret namespace of node driver               | `kube-system`
| `node.reservedDataDiskSlotNum`                  | reserved data disk slot number per node by default(`0`)    | `0` |
| `node.supportZone`                                | Whether getting zone info in NodeGetInfo on the node (requires instance metadata support)               | `true`
| `node.getNodeIDFromIMDS`                          | Whether getting NodeID from IMDS on the node (requires instance metadata support)               | `false`
| `node.removeNotReadyTaint`                        | Whether enable the startup taint removal feature            | `true`
| `node.allowEmptyCloudConfig`                      | Whether allow running node driver without cloud config               | `true`
| `node.maxUnavailable`                             | `maxUnavailable` value of driver node daemonset            | `1`
| `node.metricsPort`                          | metrics port of driver node daemonset                   | `29605`                                                        |
| `node.livenessProbe.healthPort`                   | health check port for liveness probe                       | `29603` |
| `node.logLevel`                                   | node driver log level                                      |`5`                                                           |
| `snapshot.enabled`                                | whether enable snapshot feature                            | `false`                                                        |
| `snapshot.image.csiSnapshotter.repository`        | csi-snapshotter container image                               | `/oss/kubernetes-csi/csi-snapshotter`         |
| `snapshot.image.csiSnapshotter.tag`               | csi-snapshotter container image tag                           | `v8.4.0`                                                       |
| `snapshot.image.csiSnapshotter.pullPolicy`        | csi-snapshotter image pull policy                          | `IfNotPresent`                                                 |
| `snapshot.image.csiSnapshotController.repository` | snapshot-controller container image                           | `/oss/kubernetes-csi/snapshot-controller`     |
| `snapshot.image.csiSnapshotController.tag`        | snapshot-controller container image tag                       | `v8.4.0`                                                      |
| `snapshot.image.csiSnapshotController.pullPolicy` | snapshot-controller image pull policy                      | `IfNotPresent`                                                 |
| `snapshot.snapshotController.name`                | snapshot controller name                                   | `csi-snapshot-controller`                                                           |
| `snapshot.snapshotController.replicas`            | the replicas of snapshot-controller                        | `2`                                                            |
| `snapshot.snapshotController.labels`                               | snapshot controller deployment extra labels                    | `{}`
| `snapshot.snapshotController.annotations`                          | snapshot controller deployment extra annotations               | `{}`
| `snapshot.snapshotController.podLabels`                            | snapshot controller pods extra labels                          | `{}`
| `snapshot.snapshotController.podAnnotations`                       | snapshot controller pods extra annotations                     | `{}`
| `snapshot.snapshotController.resources.limits.memory`          | csi-snapshot-controller memory limits                          | 500Mi                                                          |
| `snapshot.snapshotController.resources.requests.cpu`           | csi-snapshot-controller cpu requests                    | 10m                                                            |
| `snapshot.snapshotController.resources.requests.memory`        | csi-snapshot-controller memory requests                 | 20Mi                                                           |
| `linux.enabled`                                   | whether enable linux feature                               | `true`                                                         |
| `linux.dsName`                                    | name of driver daemonset on linux                          |`csi-azuredisk-node`                                                         |
| `linux.kubelet`                                   | configure kubelet directory path on Linux agent node       | `/var/lib/kubelet`                                                |
| `linux.getNodeInfoFromLabels`                     | get node info from node labels instead of IMDS on Linux agent node       | `false`                                                |
| `linux.enableRegistrationProbe`                   | enable [kubelet-registration-probe](https://github.com/kubernetes-csi/node-driver-registrar#health-check-with-an-exec-probe) on Linux driver config     | `true`
| `linux.distro`                                    | configure ssl certificates for different Linux distribution(available values: `debian`, `fedora`)                  | `debian`                                                |
| `linux.tolerations`                               | linux node driver tolerations                              |                                                              |
| `linux.affinity`                                  | linux node pod affinity                                     | `{}`                                                             |
| `linux.nodeSelector`                              | linux node pod node selector                                | `{}`                                                             |
| `linux.hostNetwork`                               | `hostNetwork` setting on linux node driver(could be disabled if perfProfile is `none`)                            | `true`                                                            | `true`, `false`
| `linux.labels`                                    | linux node daemonset extra labels                     | `{}`
| `linux.annotations`                               | linux node daemonset extra annotations                | `{}`
| `linux.otelTracing.enabled`                       | whether enable opentelemetry tracing                  | `false`                                                   |
| `linux.otelTracing.otelServiceName`               | name of the service associated with the produced traces      | `csi-azuredisk-node`                               |
| `linux.otelTracing.otelExporterEndpoint`          | opentelemetry-compatible endpoint where traces are sent      | `"http://localhost:4317"`                          |
| `linux.podLabels`                                 | linux node pods extra labels                          | `{}`
| `linux.podAnnotations`                            | linux node pods extra annotations                     | `{}`
| `linux.resources.livenessProbe.limits.memory`          | liveness-probe memory limits                          | 100Mi                                                          |
| `linux.resources.livenessProbe.requests.cpu`           | liveness-probe cpu requests                    | 10m                                                            |
| `linux.resources.livenessProbe.requests.memory`        | liveness-probe memory requests                 | 20Mi                                                           |
| `linux.resources.nodeDriverRegistrar.limits.memory`    | csi-node-driver-registrar memory limits               | 100Mi                                                          |
| `linux.resources.nodeDriverRegistrar.requests.cpu`     | csi-node-driver-registrar cpu requests         | 10m                                                            |
| `linux.resources.nodeDriverRegistrar.requests.memory`  | csi-node-driver-registrar memory requests      | 20Mi                                                           |
| `linux.resources.azuredisk.limits.memory`              | azuredisk memory limits                         | 600Mi                                                         |
| `linux.resources.azuredisk.requests.cpu`               | azuredisk cpu requests                   | 10m                                                            |
| `linux.resources.azuredisk.requests.memory`            | azuredisk memory requests                | 20Mi                                                           |
| `windows.enabled`                                 | whether enable windows feature                             | `true`                                                        |
| `windows.dsName`                                  | name of driver daemonset on windows                        |`csi-azuredisk-node-win`                                                         |
| `windows.kubelet`                                 | configure kubelet directory path on Windows agent node     | `'C:\var\lib\kubelet'`                                            |
| `windows.getNodeInfoFromLabels`                   | get node info from node labels instead of IMDS on windows agent node       | `false`                                                |
| `windows.enableRegistrationProbe`                 | enable [kubelet-registration-probe](https://github.com/kubernetes-csi/node-driver-registrar#health-check-with-an-exec-probe) on windows driver config     | `true`
| `windows.tolerations`                             | windows node driver tolerations                            |                                                              |
| `windows.affinity`                                | windows node pod affinity                                     | `{}`                                                             |
| `windows.nodeSelector`                            | windows node pod node selector                                | `{}`                                                             |
| `windows.labels`                                  | windows node daemonset extra labels                     | `{}`
| `windows.annotations`                             | windows node daemonset extra annotations                | `{}`
| `windows.otelTracing.enabled`                     | whether enable opentelemetry tracing                    | `false`                                                      |
| `windows.otelTracing.otelServiceName`             | name of the service associated with the produced traces          | `csi-azuredisk-node-win`                            |
| `windows.otelTracing.otelExporterEndpoint`        | opentelemetry-compatible endpoint where traces are sent          | `"http://localhost:4317"`                           |
| `windows.podLabels`                               | windows node pods extra labels                          | `{}`
| `windows.podAnnotations`                          | windows node pods extra annotations                     | `{}`
| `windows.resources.livenessProbe.limits.memory`          | liveness-probe memory limits                          | 150Mi                                                          |
| `windows.resources.livenessProbe.requests.cpu`           | liveness-probe cpu requests                    | 10m                                                            |
| `windows.resources.livenessProbe.requests.memory`        | liveness-probe memory requests                 | 40Mi                                                           |
| `windows.resources.nodeDriverRegistrar.limits.memory`    | csi-node-driver-registrar memory limits               | 150Mi                                                          |
| `windows.resources.nodeDriverRegistrar.requests.cpu`     | csi-node-driver-registrar cpu requests         | 30m                                                            |
| `windows.resources.nodeDriverRegistrar.requests.memory`  | csi-node-driver-registrar memory requests      | 40Mi                                                           |
| `windows.resources.azuredisk.limits.memory`              | azuredisk memory limits                         | 600Mi                                                         |
| `windows.resources.azuredisk.requests.cpu`               | azuredisk cpu requests                   | 10m                                                            |
| `windows.resources.azuredisk.requests.memory`            | azuredisk memory requests                | 40Mi                                                           |
| `windows.useHostProcessContainers`                       | whether deploy driver daemonset with HostProcess containers on windows | true                                                          |
| `cloud`                                           | cloud environment driver is running on                     | `AzurePublicCloud`                                                  |
| `workloadIdentity.clientID` | client ID of workload identity | ''
| `workloadIdentity.tenantID` | [optional] If the AAD application or user-assigned managed identity is not in the same tenant as the cluster then set tenantID with the AAD application or user-assigned managed identity tenant ID | ''
---

## Troubleshooting

- Add `--wait -v=5 --debug` in `helm install` command to get detailed error
- Use `kubectl describe` to get more info
