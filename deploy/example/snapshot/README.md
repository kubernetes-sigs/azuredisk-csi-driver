# Snapshot feature

> From version 1.26.4, you can take cross-region snapshots by setting the `location` parameter to a different region than the current cluster

- [Use velero to backup & restore Azure disk by snapshot feature](https://velero.io/blog/csi-integration/)

## Introduction
This driver supports both [full and incremental snapshot functionalities](https://docs.microsoft.com/en-us/azure/virtual-machines/disks-incremental-snapshots), you could set `incremental` parameter in `VolumeSnapshotClass` to set full or incremental(by default) snapshot(refer to [VolumeSnapshotClass](../../../docs/driver-parameters.md#volumesnapshotclass) for detailed parameters description):

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: csi-azuredisk-vsc
driver: disk.csi.azure.com
deletionPolicy: Delete
parameters:
  resourceGroup: EXISTING_RESOURCE_GROUP_NAME  # optional, only set this when snapshot is not taken in the same resource group as agent node
  incremental: "true"  # available values: "true"(by default), "false"
```

## Install CSI Driver

Follow the [instructions](https://github.com/kubernetes-sigs/azuredisk-csi-driver/blob/master/docs/install-csi-driver-master.md) to install snapshot driver.

### 1. Create source PVC and an example pod to write data 
```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```
 - Check source PVC
```console
$ kubectl exec nginx-azuredisk -- ls /mnt/azuredisk
lost+found
outfile
```

### 2. Create a snapshot on source PVC
```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/snapshot/storageclass-azuredisk-snapshot.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/snapshot/azuredisk-volume-snapshot.yaml
```
 - Check snapshot Status
```console
$ kubectl describe volumesnapshot azuredisk-volume-snapshot
Name:         azuredisk-volume-snapshot
Namespace:    default
Labels:       <none>
Annotations:  kubectl.kubernetes.io/last-applied-configuration:
                {"apiVersion":"snapshot.storage.k8s.io/v1","kind":"VolumeSnapshot","metadata":{"annotations":{},"name":"azuredisk-volume-snapshot","n...
API Version:  snapshot.storage.k8s.io/v1
Kind:         VolumeSnapshot
Metadata:
  Creation Timestamp:  2020-02-04T13:59:54Z
  Finalizers:
    snapshot.storage.kubernetes.io/volumesnapshot-as-source-protection
    snapshot.storage.kubernetes.io/volumesnapshot-bound-protection
  Generation:        1
  Resource Version:  48044
  Self Link:         /apis/snapshot.storage.k8s.io/v1/namespaces/default/volumesnapshots/azuredisk-volume-snapshot
  UID:               2b0ef334-4112-4c86-8360-079c625d5562
Spec:
  Source:
    Persistent Volume Claim Name:  pvc-azuredisk
  Volume Snapshot Class Name:      csi-azuredisk-vsc
Status:
  Bound Volume Snapshot Content Name:  snapcontent-2b0ef334-4112-4c86-8360-079c625d5562
  Creation Time:                       2020-02-04T14:23:36Z
  Ready To Use:                        true
  Restore Size:                        10Gi
Events:                                <none>
```
> In above example, `snapcontent-2b0ef334-4112-4c86-8360-079c625d5562` is the snapshot name

### 3. Create a new PVC based on snapshot
```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/snapshot/pvc-azuredisk-snapshot-restored.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/snapshot/nginx-pod-restored-snapshot.yaml
```

 - Check data
```console
$ kubectl exec nginx-restored -- ls /mnt/azuredisk
lost+found
outfile
```

### Tips
### Use snapshot feature to create a copy of a disk with a different SKU
> The disk SKU change can be from LRS to ZRS, from standard to premium, or even across zones, however cross-region changes are not supported.

> For information on storage class settings with cross-zone support, please refer to [allowed-topology storage class](../storageclass-azuredisk-csi-allowed-topology.yaml)

 - Before proceeding, ensure that the application is not writing data to the source disk.
 - Take a snapshot of the existing disk PVC.
 - Create a new storage class, such as "new-sku-sc," with the desired SKU name value
 - Create a new PVC with the storage class name "new-sku-sc" based on the snapshot.

#### Links
 - [CSI Snapshotter](https://github.com/kubernetes-csi/external-snapshotter)
 - [Announcing general availability of incremental snapshots of Managed Disks](https://azure.microsoft.com/en-gb/blog/announcing-general-availability-of-incremental-snapshots-of-managed-disks/)
