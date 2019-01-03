# azuredisk CSI driver for Kubernetes (Alpha)

**WARNING**: This driver is in ALPHA currently. Do NOT use this driver in a production environment in its current state.

 - supported Kubernetes version: v1.12.0 or later version
 - supported agent OS: Linux

## About
This driver allows Kubernetes to use [azure disk](https://azure.microsoft.com/en-us/services/storage/disks/) volume, csi plugin name: `disk.csi.azure.com`

## `disk.csi.azure.com` driver parameters
 > storage class `disk.csi.azure.com` parameters are compatable with built-in [azuredisk](https://kubernetes.io/docs/concepts/storage/volumes/#azuredisk) plugin
 
Name | Meaning | Available Values(or example) | Mandatory | Notes
--- | --- | --- | --- | ---
skuName | azure disk storage account type (alias: `storageAccountType`)| `Standard_LRS`, `Premium_LRS`, `StandardSSD_LRS`, `UltraSSD_LRS` | No | if empty, `Standard_LRS` is the default value)
kind | managed or unmanaged(blob based) disk | `managed` (`dedicated`, `shared` are deprecated since it's using unmanaged disk) | No | if empty, `managed` is the default value)
storageAccount | specify the storage account name in which azure disk will be created | STORAGE_ACCOUNT_NAME | No | if empty, driver find a suitable storage account that matches `skuName` in the same resource group
location | specify the location in which azure disk will be created | `eastus`, `westus`, etc. | No | if empty, driver will use the same location name as current k8s cluster
resourceGroup | specify the resource group in which azure disk will be created | RG_NAME | No | if empty, driver will use the same resource group name as current k8s cluster
DiskIOPSReadWrite | [UltraSSD disk](https://docs.microsoft.com/en-us/azure/virtual-machines/linux/disks-ultra-ssd) IOPS Capability |  | No | if empty, `500` is the default value
DiskMBpsReadWrite | [UltraSSD disk](https://docs.microsoft.com/en-us/azure/virtual-machines/linux/disks-ultra-ssd) Throughput Capability |  | No | if empty, `100` is the default value

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

#### Example#2: Azuredisk Static Provisioning(use an existing azure disk)
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
Filesystem      Size  Used Avail Use% Mounted on
overlay          30G   15G   15G  52% /
...
/dev/sdc        9.8G   37M  9.8G   1% /mnt/azuredisk
...
```
In the above example, there is a `/mnt/azuredisk` directory mounted as disk filesystem.

## Kubernetes Development
Please refer to [development guide](./docs/csi-dev.md)


### Links
 - [Kubernetes CSI Documentation](https://kubernetes-csi.github.io/docs/Home.html)
 - [Analysis of the CSI Spec](https://blog.thecodeteam.com/2017/11/03/analysis-csi-spec/)
 - [CSI Drivers](https://github.com/kubernetes-csi/drivers)
 - [Container Storage Interface (CSI) Specification](https://github.com/container-storage-interface/spec)
