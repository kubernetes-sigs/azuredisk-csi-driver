# azuredisk CSI driver for Kubernetes (Alpha)

**WARNING**: This driver is in ALPHA currently. Do NOT use this driver in a production environment in its current state.

 - supported Kubernetes version: v1.12.0 or later version
 - supported agent OS: Linux

## About
This driver allows Kubernetes to use [azure disk](https://docs.microsoft.com/en-us/azure/storage/files/storage-files-introduction) volume, csi plugin name: `disk.csi.azure.com`

## `disk.csi.azure.com` driver parameters
 > storage class `disk.csi.azure.com` parameters are compatable with built-in [azuredisk](https://kubernetes.io/docs/concepts/storage/volumes/#azuredisk) plugin
 
Name | Meaning | Example | Mandatory | Notes
--- | --- | --- | --- | ---
skuName | azure disk storage account type | `Standard_LRS`, `Standard_GRS`, `Standard_RAGRS` | No | if empty, `Standard_LRS` is the default value)
storageAccount | specify the storage account name in which azure disk share will be created | STORAGE_ACCOUNT_NAME | No | if empty, driver find a suitable storage account that matches `skuName` in the same resource group
location | specify the location in which azure disk share will be created | `eastus`, `westus`, etc. | No | if empty, driver will use the same location name as current k8s cluster
resourceGroup | specify the resource group in which azure disk share will be created | RG_NAME | No | if empty, driver will use the same resource group name as current k8s cluster

## Prerequisite
 - To ensure that all necessary features are enabled, set the following feature gate flags to true:
```
--feature-gates=CSIPersistentVolume=true,MountPropagation=true,VolumeSnapshotDataSource=true,KubeletPluginsWatcher=true,CSINodeInfo=true,CSIDriverRegistry=true
```
CSIPersistentVolume is enabled by default in v1.10. MountPropagation is enabled by default in v1.10. VolumeSnapshotDataSource is a new alpha feature in v1.12. KubeletPluginsWatcher is enabled by default in v1.12. CSINodeInfo and CSIDriverRegistry are new alpha features in v1.12.

 - An [Cloud provider config file](https://github.com/kubernetes/cloud-provider-azure/blob/master/docs/cloud-provider-config.md) should already exist on all agent nodes
 > usually it's `/etc/kubernetes/azure.json` deployed by AKS or acs-engine, and supports both `service principal` and `msi`

### Install azuredisk CSI driver on a kubernetes cluster
Please refer to [install azuredisk csi driver](https://github.com/andyzhangx/azuredisk-csi-driver/blob/master/docs/install-azuredisk-csi-driver.md)

## Example
### 1. create a pod with csi azuredisk driver mount on linux
#### Example#1: Azuredisk Dynamic Provisioning
 - Create an azuredisk CSI storage class
```
kubectl create -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
```

 - Create an azuredisk CSI PVC
```
kubectl create -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi.yaml
```

#### Example#2: Azuredisk Static Provisioning(use an existing azure disk share)
 - Use `kubectl create secret` to create `azure-secret` with existing storage account name and key
```
kubectl create secret generic azure-secret --from-literal accountname=NAME --from-literal accountkey="KEY" --type=Opaque
```

 - Create an azuredisk CSI PV, download `pv-azuredisk-csi.yaml` file and edit `sharename` in `volumeAttributes`
```
wget https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/example/pv-azuredisk-csi.yaml
vi pv-azuredisk-csi.yaml
kubectl create -f pv-azuredisk-csi.yaml
```

 - Create an azuredisk CSI PVC which would be bound to the above PV
```
kubectl create -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi-static.yaml
```

### 2. validate PVC status and create an nginx pod
 - make sure pvc is created and in `Bound` status finally
```
watch kubectl describe pvc pvc-azuredisk
```

 - create a pod with azuredisk CSI PVC
```
kubectl create -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```

### 3. enter the pod container to do validation
 - watch the status of pod until its Status changed from `Pending` to `Running` and then enter the pod container
```
$ watch kubectl describe po nginx-azuredisk
$ kubectl exec -it nginx-azuredisk -- bash
root@nginx-azuredisk:/# df -h
Filesystem                                                                                             Size  Used Avail Use% Mounted on
overlay                                                                                                 30G   19G   11G  65% /
tmpfs                                                                                                  3.5G     0  3.5G   0% /dev
...
//f571xxx.file.core.windows.net/pvc-file-dynamic-e2ade9f3-f88b-11e8-8429-000d3a03e7d7  1.0G   64K  1.0G   1% /mnt/azuredisk
...
```
In the above example, there is a `/mnt/azuredisk` directory mounted as dysk filesystem.

## Kubernetes Development
Please refer to [development guide](./docs/csi-dev.md)


### Links
 - [Kubernetes CSI Documentation](https://kubernetes-csi.github.io/docs/Home.html)
 - [Analysis of the CSI Spec](https://blog.thecodeteam.com/2017/11/03/analysis-csi-spec/)
 - [CSI Drivers](https://github.com/kubernetes-csi/drivers)
 - [Container Storage Interface (CSI) Specification](https://github.com/container-storage-interface/spec)
