# azuredisk CSI driver for Kubernetes
![TravisCI](https://travis-ci.com/andyzhangx/azuredisk-csi-driver.svg?branch=master)
[![Coverage Status](https://coveralls.io/repos/github/andyzhangx/azuredisk-csi-driver/badge.svg?branch=master)](https://coveralls.io/github/andyzhangx/azuredisk-csi-driver?branch=master)
[![FOSSA Status](https://app.fossa.io/api/projects/git%2Bgithub.com%2Fandyzhangx%2Fazuredisk-csi-driver.svg?type=shield)](https://app.fossa.io/projects/git%2Bgithub.com%2Fandyzhangx%2Fazuredisk-csi-driver?ref=badge_shield)

**WARNING**: This driver is in ALPHA currently. Do NOT use this driver in a production environment in its current state.

### About
This driver allows Kubernetes to use [azure disk](https://azure.microsoft.com/en-us/services/storage/disks/) volume, csi plugin name: `disk.csi.azure.com`

### Project Status
Status: Alpha

### Container Images & CSI Compatibility:
|Azure Disk CSI Driver Version  | Image                                              | v0.3.0| v1.0.0 |
|-------------------------------|----------------------------------------------------|-------|--------|
|v0.1.0-alpha                   |mcr.microsoft.com/k8s/csi/azuredisk-csi:v0.1.0-alpha| yes   | no     |
|v0.2.0-alpha                   |mcr.microsoft.com/k8s/csi/azuredisk-csi:v0.2.0-alpha| no    | yes    |
|master branch                  |mcr.microsoft.com/k8s/csi/azuredisk-csi:latest      | no    | yes    |

### Kubernetes Compatibility
| Azure Disk CSI Driver\Kubernetes Version | 1.12 | 1.13+ | 
|------------------------------------------|------|-------|
| v0.1.0-alpha                             | yes  | yes    |
| v0.2.0-alpha                             | no   | yes    |
| master branch                            | no   | yes    |

### Driver parameters
Please refer to [`disk.csi.azure.com` driver parameters](./docs/driver-parameters.md)
 > storage class `disk.csi.azure.com` parameters are compatible with built-in [azuredisk](https://kubernetes.io/docs/concepts/storage/volumes/#azuredisk) plugin

### Prerequisite
 - The driver initialization depends on a [Cloud provider config file](https://github.com/kubernetes/cloud-provider-azure/blob/master/docs/cloud-provider-config.md), usually it's `/etc/kubernetes/azure.json` on all k8s nodes deployed by AKS or aks-engine, here is an [azure.json example](./deploy/example/azure.json)

### Install azuredisk CSI driver on a Kubernetes cluster
Please refer to [install azuredisk csi driver](./docs/install-azuredisk-csi-driver.md)

### E2E Usage example
#### 1. create a pod with csi azuredisk driver mount on linux
##### Option#1: Azuredisk Dynamic Provisioning
 - Create an azuredisk CSI storage class
```
kubectl create -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
```

 - Create an azuredisk CSI PVC
```
kubectl create -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi.yaml
```

##### Option#2: Azuredisk Static Provisioning(use an existing azure disk)
 - Create an azuredisk CSI PV, download `pv-azuredisk-csi.yaml` file and edit `diskName`, `diskURI` in `volumeAttributes`
```
wget https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/example/pv-azuredisk-csi.yaml
vi pv-azuredisk-csi.yaml
kubectl create -f pv-azuredisk-csi.yaml
```

 - Create an azuredisk CSI PVC which would be bound to the above PV
```
kubectl create -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi-static.yaml
```

#### 2. validate PVC status and create an nginx pod
 - make sure pvc is created and in `Bound` status finally
```
watch kubectl describe pvc pvc-azuredisk
```

 - create a pod with azuredisk CSI PVC
```
kubectl create -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```

#### 3. enter the pod container to do validation
 - watch the status of pod until its Status changed from `Pending` to `Running` and then enter the pod container
```
$ watch kubectl describe po nginx-azuredisk
$ kubectl exec -it nginx-azuredisk -- bash
Filesystem      Size  Used Avail Use% Mounted on
overlay          30G   15G   15G  52% /
...
/devhost/sdc        9.8G   37M  9.8G   1% /mnt/azuredisk
...
```
In the above example, there is a `/mnt/azuredisk` directory mounted as disk filesystem.

## Kubernetes Development
Please refer to [development guide](./docs/csi-dev.md)


### Links
 - [Kubernetes CSI Documentation](https://kubernetes-csi.github.io/docs/)
 - [Analysis of the CSI Spec](https://blog.thecodeteam.com/2017/11/03/analysis-csi-spec/)
 - [CSI Drivers](https://github.com/kubernetes-csi/drivers)
 - [Container Storage Interface (CSI) Specification](https://github.com/container-storage-interface/spec)
