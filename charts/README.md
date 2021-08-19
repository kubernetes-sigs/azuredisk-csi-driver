# Install CSI driver with Helm 3

## Prerequisites
 - [install Helm](https://helm.sh/docs/intro/quickstart/#install-helm)

### Tips
 - make controller only run on master node: `--set controller.runOnMaster=true`
 - enable `fsGroupPolicy` on a k8s 1.20+ cluster: `--set feature.enableFSGroupPolicy=true`
 - set replica of controller as `1`: `--set controller.replicas=1`
 - specify different cloud config secret for the driver:
   - `--set controller.cloudConfigSecretName`
   - `--set controller.cloudConfigSecretNamesapce`
   - `--set node.cloudConfigSecretName`
   - `--set node.cloudConfigSecretNamesapce`

## install latest version
```console
helm repo add azuredisk-csi-driver https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/charts
helm install azuredisk-csi-driver azuredisk-csi-driver/azuredisk-csi-driver --namespace kube-system
```

### install a specific version
```console
helm repo add azuredisk-csi-driver https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/charts
helm install azuredisk-csi-driver azuredisk-csi-driver/azuredisk-csi-driver --namespace kube-system --version v1.5.1
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
helm install azuredisk-csi-driver azuredisk-csi-driver/azuredisk-csi-driver --namespace kube-system --set driver.name="disk2.csi.azure.com" --set controller.name="csi-azuredisk2-controller" --set rbac.name=azuredisk2 --set serviceAccount.controller=csi-azuredisk2-controller-sa --set serviceAccount.node=csi-azuredisk2-node-sa --set linux.dsName=csi-azuredisk2-node --set windows.dsName=csi-azuredisk2-node-win --set node.livenessProbe.healthPort=39705
```

### search for all available chart versions
```console
helm search repo -l azuredisk-csi-driver
```

## uninstall CSI driver
```console
helm uninstall azuredisk-csi-driver -n kube-system
```

## latest chart configuration

The following table lists the configurable parameters of the latest Azure Disk CSI Driver chart and default values.

| Parameter                                         | Description                                                | Default                                                      |
| ------------------------------------------------- | ---------------------------------------------------------- | ------------------------------------------------------------ |
| `driver.name`                                     | alternative driver name                        | `disk.csi.azure.com` |
| `driver.customUserAgent`                          | custom userAgent               | `` |
| `driver.userAgentSuffix`                          | userAgent suffix               | `` |
| `driver.volumeAttachLimit`                        | maximum number of attachable volumes per node maximum number is defined according to node instance type by default(`-1`)                        | `-1` |
| `feature.enableFSGroupPolicy`                     | enable `fsGroupPolicy` on a k8s 1.20+ cluster           | `false`                      |
| `image.azuredisk.repository`                      | azuredisk-csi-driver docker image                          | `mcr.microsoft.com/k8s/csi/azuredisk-csi`                      |
| `image.azuredisk.tag`                             | azuredisk-csi-driver docker image tag                      | `latest`                                                       |
| `image.azuredisk.pullPolicy`                      | azuredisk-csi-driver image pull policy                     | `IfNotPresent`                                                 |
| `image.csiProvisioner.repository`                 | csi-provisioner docker image                               | `mcr.microsoft.com/oss/kubernetes-csi/csi-provisioner`         |
| `image.csiProvisioner.tag`                        | csi-provisioner docker image tag                           | `v1.5.0`                                                       |
| `image.csiProvisioner.pullPolicy`                 | csi-provisioner image pull policy                          | `IfNotPresent`                                                 |
| `image.csiAttacher.repository`                    | csi-attacher docker image                                  | `mcr.microsoft.com/oss/kubernetes-csi/csi-attacher`            |
| `image.csiAttacher.tag`                           | csi-attacher docker image tag                              | `v2.2.0`                                                       |
| `image.csiAttacher.pullPolicy`                    | csi-attacher image pull policy                             | `IfNotPresent`                                                 |
| `image.csiResizer.repository`                     | csi-resizer docker image                                   | `mcr.microsoft.com/oss/kubernetes-csi/csi-resizer`             |
| `image.csiResizer.tag`                            | csi-resizer docker image tag                               | `v0.5.0`                                                       |
| `image.csiResizer.pullPolicy`                     | csi-resizer image pull policy                              | `IfNotPresent`                                                 |
| `image.livenessProbe.repository`                  | liveness-probe docker image                                | `mcr.microsoft.com/oss/kubernetes-csi/livenessprobe`           |
| `image.livenessProbe.tag`                         | liveness-probe docker image tag                            | `v2.3.0`                                                       |
| `image.livenessProbe.pullPolicy`                  | liveness-probe image pull policy                           | `IfNotPresent`                                                 |
| `image.nodeDriverRegistrar.repository`            | csi-node-driver-registrar docker image                     | `mcr.microsoft.com/oss/kubernetes-csi/csi-node-driver-registrar` |
| `image.nodeDriverRegistrar.tag`                   | csi-node-driver-registrar docker image tag                 | `v2.2.0`                                                       |
| `image.nodeDriverRegistrar.pullPolicy`            | csi-node-driver-registrar image pull policy                | `IfNotPresent`                                                 |
| `imagePullSecrets`                                | Specify docker-registry secret names as an array           | [] (does not add image pull secrets to deployed pods)        |                                       |
| `serviceAccount.create`                           | whether create service account of csi-azuredisk-controller, csi-azuredisk-node, and snapshot-controller| true                                                    |
| `serviceAccount.controller`                       | name of service account for csi-azuredisk-controller       | `csi-azuredisk-controller-sa`                                  |
| `serviceAccount.node`                             | name of service account for csi-azuredisk-node             | `csi-azuredisk-node-sa`                                        |
| `serviceAccount.snapshotController`               | name of service account for csi-snapshot-controller        | `csi-snapshot-controller-sa`                                   |
| `rbac.create`                                     | whether create rbac of csi-azuredisk-controller            | `true`                                                         |
| `rbac.name`                                       | driver name in rbac role                | `true`                                                         |
| `controller.name`                                 | name of driver deployment               | `csi-azuredisk-controller`
| `controller.cloudConfigSecretName`                | cloud config secret name of controller driver               | `azure-cloud-provider`
| `controller.cloudConfigSecretNamespace`           | cloud config secret namespace of controller driver          | `kube-system`
| `controller.replicas`                             | the replicas of csi-azuredisk-controller                   | `2`                                                            |
| `controller.metricsPort`                          | metrics port of csi-azuredisk-controller                   | `29604`                                                        |
| `controller.livenessProbe.healthPort `            | health check port for liveness probe                   | `29602` |
| `controller.runOnMaster`                          | run csi-azuredisk-controller on master node                | `false`                                                        |
| `controller.logLevel`                             | controller driver log level                                                          |`5`                                                           |
| `controller.tolerations`                          | controller pod tolerations                            |                                                              |
| `controller.hostNetwork`                          | `hostNetwork` setting on controller driver(could be disabled if controller does not depend on MSI setting)                            | `true`                                                            | `true`, `false`
| `node.cloudConfigSecretName`                      | cloud config secret name of node driver               | `azure-cloud-provider`
| `node.cloudConfigSecretNamespace`                 | cloud config secret namespace of node driver          | `kube-system`
| `node.maxUnavailable`                             | `maxUnavailable` value of driver node daemonset                            | `1`
| `node.metricsPort`                                | metrics port of csi-azuredisk-node                         |`29605`                                                        |
| `node.livenessProbe.healthPort `                  | health check port for liveness probe                   | `29603` |
| `node.logLevel`                                   | node driver log level                                                          |`5`                                                           |
| `snapshot.apiVersion`                             | when using Snapshot, specify `ga` for K8s >= 1.20            | `beta`                                                        |
| `snapshot.enabled`                                | whether enable snapshot feature                            | `false`                                                        |
| `snapshot.image.csiSnapshotter.repository`        | csi-snapshotter docker image                               | `mcr.microsoft.com/oss/kubernetes-csi/csi-snapshotter`         |
| `snapshot.image.csiSnapshotter.tag`               | csi-snapshotter docker image tag                           | `v3.0.3`                                                       |
| `snapshot.image.csiSnapshotter.pullPolicy`        | csi-snapshotter image pull policy                          | `IfNotPresent`                                                 |
| `snapshot.image.csiSnapshotController.repository` | snapshot-controller docker image                           | `mcr.microsoft.com/oss/kubernetes-csi/snapshot-controller`     |
| `snapshot.image.csiSnapshotController.tag`        | snapshot-controller docker image tag                       | `v3.0.3`                                                      |
| `snapshot.image.csiSnapshotController.pullPolicy` | snapshot-controller image pull policy                      | `IfNotPresent`                                                 |
| `snapshot.snapshotController.name`                | snapshot controller name | `csi-snapshot-controller`                                                           |
| `snapshot.snapshotController.replicas`            | the replicas of snapshot-controller                        | `1`                                                            |
| `linux.enabled`                                   | whether enable linux feature                               | `true`                                                         |
| `linux.dsName`                                    | name of driver daemonset on linux                             |`csi-azuredisk-node`                                                         |
| `linux.kubelet`                                   | configure kubelet directory path on Linux agent node                  | `/var/lib/kubelet`                                                |
| `linux.distro`                                    | configure ssl certificates for different Linux distribution(available values: `debian`, `fedora`)                  | `debian`                                                |
| `linux.tolerations`                               | linux node driver tolerations                            |                                                              |
| `linux.hostNetwork`                               | `hostNetwork` setting on linux node driver(could be disabled if perfProfile is `none`)                            | `true`                                                            | `true`, `false`
| `windows.enabled`                                 | whether enable windows feature                             | `true`                                                        |
| `windows.dsName`                                  | name of driver daemonset on windows                             |`csi-azuredisk-node-win`                                                         |
| `windows.kubelet`                                 | configure kubelet directory path on Windows agent node                | `'C:\var\lib\kubelet'`                                            |
| `windows.image.livenessProbe.repository`          | windows liveness-probe docker image                        | `mcr.microsoft.com/oss/kubernetes-csi/livenessprobe`           |
| `windows.image.livenessProbe.tag`                 | windows liveness-probe docker image tag                    | `v2.3.0`                           |
| `windows.image.livenessProbe.pullPolicy`          | windows liveness-probe image pull policy                   | `IfNotPresent`                                                 |
| `windows.image.nodeDriverRegistrar.repository`    | windows csi-node-driver-registrar docker image             | `mcr.microsoft.com/oss/kubernetes-csi/csi-node-driver-registrar` |
| `windows.image.nodeDriverRegistrar.tag`           | windows csi-node-driver-registrar docker image tag         | `v2.3.0`                          |
| `windows.image.nodeDriverRegistrar.pullPolicy`    | windows csi-node-driver-registrar image pull policy        | `IfNotPresent`                                                 |
| `windows.tolerations`                             | windows node driver tolerations                            |                                                              |
| `cloud`                                           | cloud environment driver is running on             | `AzurePublicCloud`                                                  |

## troubleshooting
 - Add `--wait -v=5 --debug` in `helm install` command to get detailed error
 - Use `kubectl describe` to get more info
