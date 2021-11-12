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
 - switch to `mcr.azk8s.cn` repository in Azure China: `--set image.baseRepo=mcr.azk8s.cn`

## install latest version
```console
helm repo add azuredisk-csi-driver https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/charts
helm install azuredisk-csi-driver azuredisk-csi-driver/azuredisk-csi-driver --namespace kube-system
```

### install a specific version
```console
helm repo add azuredisk-csi-driver https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/charts
helm install azuredisk-csi-driver azuredisk-csi-driver/azuredisk-csi-driver --namespace kube-system --version v1.8.0
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
| `driver.name`                                     | alternative driver name                                    | `disk.csi.azure.com` |
| `driver.customUserAgent`                          | custom userAgent                                           | `` |
| `driver.userAgentSuffix`                          | userAgent suffix                                           | `OSS-helm` |
| `driver.volumeAttachLimit`                        | maximum number of attachable volumes per node maximum number is defined according to node instance type by default(`-1`)                        | `-1` |
| `feature.enableFSGroupPolicy`                     | enable `fsGroupPolicy` on a k8s 1.20+ cluster              | `false`                      |
| `image.baseRepo`                                  | base repository of driver images                           | `mcr.microsoft.com`                      |
| `image.azuredisk.repository`                      | azuredisk-csi-driver docker image                          | `/k8s/csi/azuredisk-csi`                      |
| `image.azuredisk.tag`                             | azuredisk-csi-driver docker image tag                      | `latest`                                                       |
| `image.azuredisk.pullPolicy`                      | azuredisk-csi-driver image pull policy                     | `IfNotPresent`                                                 |
| `image.csiProvisioner.repository`                 | csi-provisioner docker image                               | `/oss/kubernetes-csi/csi-provisioner`         |
| `image.csiProvisioner.tag`                        | csi-provisioner docker image tag                           | `v2.2.2`                                                       |
| `image.csiProvisioner.pullPolicy`                 | csi-provisioner image pull policy                          | `IfNotPresent`                                                 |
| `image.csiAttacher.repository`                    | csi-attacher docker image                                  | `/oss/kubernetes-csi/csi-attacher`            |
| `image.csiAttacher.tag`                           | csi-attacher docker image tag                              | `v3.3.0`                                                       |
| `image.csiAttacher.pullPolicy`                    | csi-attacher image pull policy                             | `IfNotPresent`                                                 |
| `image.csiResizer.repository`                     | csi-resizer docker image                                   | `/oss/kubernetes-csi/csi-resizer`             |
| `image.csiResizer.tag`                            | csi-resizer docker image tag                               | `v1.3.0`                                                       |
| `image.csiResizer.pullPolicy`                     | csi-resizer image pull policy                              | `IfNotPresent`                                                 |
| `image.livenessProbe.repository`                  | liveness-probe docker image                                | `/oss/kubernetes-csi/livenessprobe`           |
| `image.livenessProbe.tag`                         | liveness-probe docker image tag                            | `v2.5.0`                                                       |
| `image.livenessProbe.pullPolicy`                  | liveness-probe image pull policy                           | `IfNotPresent`                                                 |
| `image.nodeDriverRegistrar.repository`            | csi-node-driver-registrar docker image                     | `/oss/kubernetes-csi/csi-node-driver-registrar` |
| `image.nodeDriverRegistrar.tag`                   | csi-node-driver-registrar docker image tag                 | `v2.4.0`                                                       |
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
| `controller.replicas`                             | the replicas of csi-azuredisk-controller                   | `2`                                                            |
| `controller.metricsPort`                          | metrics port of csi-azuredisk-controller                   | `29604`                                                        |
| `controller.livenessProbe.healthPort `            | health check port for liveness probe                       | `29602` |
| `controller.runOnMaster`                          | run csi-azuredisk-controller on master node                | `false`                                                        |
| `controller.logLevel`                             | controller driver log level                                |`5`                                                           |
| `controller.tolerations`                          | controller pod tolerations                                 |                                                              |
| `controller.hostNetwork`                          | `hostNetwork` setting on controller driver(could be disabled if controller does not depend on MSI setting)                            | `true`                                                            | `true`, `false`
| `controller.resources.csiProvisioner.limits.cpu`      | csi-provisioner cpu limits                            | 200m                                                           |
| `controller.resources.csiProvisioner.limits.memory`   | csi-provisioner memory limits                         | 500Mi                                                          |
| `controller.resources.csiProvisioner.requests.cpu`    | csi-provisioner cpu requests limits                   | 10m                                                            |
| `controller.resources.csiProvisioner.requests.memory` | csi-provisioner memory requests limits                | 20Mi                                                           |
| `controller.resources.csiAttacher.limits.cpu`         | csi-attacher cpu limits                            | 200m                                                           |
| `controller.resources.csiAttacher.limits.memory`      | csi-attacher memory limits                         | 500Mi                                                          |
| `controller.resources.csiAttacher.requests.cpu`       | csi-attacher cpu requests limits                   | 10m                                                            |
| `controller.resources.csiAttacher.requests.memory`    | csi-attacher memory requests limits                | 20Mi                                                           |
| `controller.resources.csiResizer.limits.cpu`          | csi-resizer cpu limits                            | 200m                                                           |
| `controller.resources.csiResizer.limits.memory`       | csi-resizer memory limits                         | 500Mi                                                          |
| `controller.resources.csiResizer.requests.cpu`        | csi-resizer cpu requests limits                   | 10m                                                            |
| `controller.resources.csiResizer.requests.memory`     | csi-resizer memory requests limits                | 20Mi                                                           |
| `controller.resources.csiSnapshotter.limits.cpu`      | csi-snapshotter cpu limits                            | 200m                                                           |
| `controller.resources.csiSnapshotter.limits.memory`   | csi-snapshotter memory limits                         | 500Mi                                                          |
| `controller.resources.csiSnapshotter.requests.cpu`    | csi-snapshotter cpu requests limits                   | 10m                                                            |
| `controller.resources.csiSnapshotter.requests.memory` | csi-snapshotter memory requests limits                | 20Mi                                                           |
| `controller.resources.livenessProbe.limits.cpu`       | liveness-probe cpu limits                             | 100m                                                           |
| `controller.resources.livenessProbe.limits.memory`    | liveness-probe memory limits                          | 100Mi                                                          |
| `controller.resources.livenessProbe.requests.cpu`     | liveness-probe cpu requests limits                    | 10m                                                            |
| `controller.resources.livenessProbe.requests.memory`  | liveness-probe memory requests limits                 | 20Mi                                                           |
| `controller.resources.azuredisk.limits.cpu`           | azuredisk cpu limits                            | 300m                                                           |
| `controller.resources.azuredisk.limits.memory`        | azuredisk memory limits                         | 500Mi                                                          |
| `controller.resources.azuredisk.requests.cpu`         | azuredisk cpu requests limits                   | 10m                                                            |
| `controller.resources.azuredisk.requests.memory`      | azuredisk memory requests limits                | 20Mi                                                           |
| `node.cloudConfigSecretName`                      | cloud config secret name of node driver                    | `azure-cloud-provider`
| `node.cloudConfigSecretNamespace`                 | cloud config secret namespace of node driver               | `kube-system`
| `node.allowEmptyCloudConfig`                      | Whether allow running node driver without cloud config               | `true`
| `node.maxUnavailable`                             | `maxUnavailable` value of driver node daemonset            | `1`
| `node.metricsPort`                                | metrics port of csi-azuredisk-node                         |`29605`                                                        |
| `node.livenessProbe.healthPort `                  | health check port for liveness probe                       | `29603` |
| `node.logLevel`                                   | node driver log level                                      |`5`                                                           |
| `snapshot.apiVersion`                             | when using Snapshot, specify `ga` for K8s >= 1.20          | `beta`                                                        |
| `snapshot.enabled`                                | whether enable snapshot feature                            | `false`                                                        |
| `snapshot.image.csiSnapshotter.repository`        | csi-snapshotter docker image                               | `/oss/kubernetes-csi/csi-snapshotter`         |
| `snapshot.image.csiSnapshotter.tag`               | csi-snapshotter docker image tag                           | `v3.0.3`                                                       |
| `snapshot.image.csiSnapshotter.pullPolicy`        | csi-snapshotter image pull policy                          | `IfNotPresent`                                                 |
| `snapshot.image.csiSnapshotController.repository` | snapshot-controller docker image                           | `/oss/kubernetes-csi/snapshot-controller`     |
| `snapshot.image.csiSnapshotController.tag`        | snapshot-controller docker image tag                       | `v3.0.3`                                                      |
| `snapshot.image.csiSnapshotController.pullPolicy` | snapshot-controller image pull policy                      | `IfNotPresent`                                                 |
| `snapshot.snapshotController.name`                | snapshot controller name                                   | `csi-snapshot-controller`                                                           |
| `snapshot.snapshotController.replicas`            | the replicas of snapshot-controller                        | `2`                                                            |
| `snapshot.snapshotController.resources.limits.cpu`             | csi-snapshot-controller cpu limits                             | 200m                                                           |
| `snapshot.snapshotController.resources.limits.memory`          | csi-snapshot-controller memory limits                          | 100Mi                                                          |
| `snapshot.snapshotController.resources.requests.cpu`           | csi-snapshot-controller cpu requests limits                    | 10m                                                            |
| `snapshot.snapshotController.resources.requests.memory`        | csi-snapshot-controller memory requests limits                 | 20Mi                                                           |
| `linux.enabled`                                   | whether enable linux feature                               | `true`                                                         |
| `linux.dsName`                                    | name of driver daemonset on linux                          |`csi-azuredisk-node`                                                         |
| `linux.kubelet`                                   | configure kubelet directory path on Linux agent node       | `/var/lib/kubelet`                                                |
| `linux.distro`                                    | configure ssl certificates for different Linux distribution(available values: `debian`, `fedora`)                  | `debian`                                                |
| `linux.tolerations`                               | linux node driver tolerations                              |                                                              |
| `linux.hostNetwork`                               | `hostNetwork` setting on linux node driver(could be disabled if perfProfile is `none`)                            | `true`                                                            | `true`, `false`
| `linux.resources.livenessProbe.limits.cpu`             | liveness-probe cpu limits                             | 100m                                                           |
| `linux.resources.livenessProbe.limits.memory`          | liveness-probe memory limits                          | 100Mi                                                          |
| `linux.resources.livenessProbe.requests.cpu`           | liveness-probe cpu requests limits                    | 10m                                                            |
| `linux.resources.livenessProbe.requests.memory`        | liveness-probe memory requests limits                 | 20Mi                                                           |
| `linux.resources.nodeDriverRegistrar.limits.cpu`       | csi-node-driver-registrar cpu limits                  | 200m                                                           |
| `linux.resources.nodeDriverRegistrar.limits.memory`    | csi-node-driver-registrar memory limits               | 100Mi                                                          |
| `linux.resources.nodeDriverRegistrar.requests.cpu`     | csi-node-driver-registrar cpu requests limits         | 10m                                                            |
| `linux.resources.nodeDriverRegistrar.requests.memory`  | csi-node-driver-registrar memory requests limits      | 20Mi                                                           |
| `linux.resources.azuredisk.limits.cpu`                 | azuredisk cpu limits                            | 200m                                                            |
| `linux.resources.azuredisk.limits.memory`              | azuredisk memory limits                         | 200Mi                                                         |
| `linux.resources.azuredisk.requests.cpu`               | azuredisk cpu requests limits                   | 10m                                                            |
| `linux.resources.azuredisk.requests.memory`            | azuredisk memory requests limits                | 20Mi                                                           |
| `windows.enabled`                                 | whether enable windows feature                             | `true`                                                        |
| `windows.dsName`                                  | name of driver daemonset on windows                        |`csi-azuredisk-node-win`                                                         |
| `windows.kubelet`                                 | configure kubelet directory path on Windows agent node     | `'C:\var\lib\kubelet'`                                            |
| `windows.tolerations`                             | windows node driver tolerations                            |                                                              |
| `windows.resources.livenessProbe.limits.cpu`             | liveness-probe cpu limits                             | 200m                                                           |
| `windows.resources.livenessProbe.limits.memory`          | liveness-probe memory limits                          | 200Mi                                                          |
| `windows.resources.livenessProbe.requests.cpu`           | liveness-probe cpu requests limits                    | 10m                                                            |
| `windows.resources.livenessProbe.requests.memory`        | liveness-probe memory requests limits                 | 20Mi                                                           |
| `windows.resources.nodeDriverRegistrar.limits.cpu`       | csi-node-driver-registrar cpu limits                  | 200m                                                           |
| `windows.resources.nodeDriverRegistrar.limits.memory`    | csi-node-driver-registrar memory limits               | 200Mi                                                          |
| `windows.resources.nodeDriverRegistrar.requests.cpu`     | csi-node-driver-registrar cpu requests limits         | 10m                                                            |
| `windows.resources.nodeDriverRegistrar.requests.memory`  | csi-node-driver-registrar memory requests limits      | 20Mi                                                           |
| `windows.resources.azuredisk.limits.cpu`                 | azuredisk cpu limits                            | 400m                                                            |
| `windows.resources.azuredisk.limits.memory`              | azuredisk memory limits                         | 400Mi                                                         |
| `windows.resources.azuredisk.requests.cpu`               | azuredisk cpu requests limits                   | 10m                                                            |
| `windows.resources.azuredisk.requests.memory`            | azuredisk memory requests limits                | 20Mi                                                           |
| `cloud`                                           | cloud environment driver is running on                     | `AzurePublicCloud`                                                  |

## troubleshooting
 - Add `--wait -v=5 --debug` in `helm install` command to get detailed error
 - Use `kubectl describe` to get more info
