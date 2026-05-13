# Snapshot Feature

> Starting from version 1.26.4, you can take cross-region snapshots by setting the `location` parameter to a different region than the current cluster.

- [Use Velero to back up & restore Azure disks using the snapshot feature](https://velero.io/blog/csi-integration/)

## Introduction

This driver supports both [full and incremental snapshot functionalities](https://docs.microsoft.com/en-us/azure/virtual-machines/disks-incremental-snapshots). You can set the `incremental` parameter in the `VolumeSnapshotClass` to choose full or incremental (default) snapshots. Refer to [VolumeSnapshotClass](../../../docs/driver-parameters.md#volumesnapshotclass) for detailed parameter descriptions.

```yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: csi-azuredisk-vsc
driver: disk.csi.azure.com
deletionPolicy: Delete
parameters:
  resourceGroup: EXISTING_RESOURCE_GROUP_NAME  # optional, only set this when snapshot is not taken in the same resource group as agent node
  incremental: "true"  # available values: "true" (default), "false"
```

## Install CSI Driver

Follow the [instructions](https://github.com/kubernetes-sigs/azuredisk-csi-driver/blob/master/docs/install-csi-driver-master.md) to install the snapshot driver.

## Example Workflow

### 1. Create a source PVC and an example pod to write data

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```

Verify data on the source PVC:

```console
$ kubectl exec nginx-azuredisk -- ls /mnt/azuredisk
lost+found
outfile
```

### 2. Create a snapshot of the source PVC

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/snapshot/storageclass-azuredisk-snapshot.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/snapshot/azuredisk-volume-snapshot.yaml
```

Check snapshot status:

```console
$ kubectl describe volumesnapshot azuredisk-volume-snapshot
Name:         azuredisk-volume-snapshot
Namespace:    default
...
Status:
  Bound Volume Snapshot Content Name:  snapcontent-2b0ef334-4112-4c86-8360-079c625d5562
  Creation Time:                       2020-02-04T14:23:36Z
  Ready To Use:                        true
  Restore Size:                        10Gi
```

> In the example above, `snapcontent-2b0ef334-4112-4c86-8360-079c625d5562` is the snapshot name.

### 3. Create a new PVC from the snapshot

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/snapshot/pvc-azuredisk-snapshot-restored.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/snapshot/nginx-pod-restored-snapshot.yaml
```

Verify restored data:

```console
$ kubectl exec nginx-restored -- ls /mnt/azuredisk
lost+found
outfile
```

## Use Snapshot to Create a Copy of a Disk with a Different SKU

You can use snapshots to change the disk SKU (e.g., from LRS to ZRS, or from Standard to Premium) and even move across zones.

> **Note:** Changing the disk SKU across regions is not supported.

For storage class settings with cross-zone support, refer to the [allowed-topology storage class](../storageclass-azuredisk-csi-allowed-topology.yaml).

Steps:
1. Ensure the application is **not writing data** to the source disk.
2. Take a snapshot of the existing disk PVC (see step 2 above).
3. Create a new StorageClass (e.g., `new-sku-sc`) with the desired SKU.
4. Create a new PVC with `storageClassName: new-sku-sc` referencing the snapshot as the data source.

## Links

- [CSI Snapshotter](https://github.com/kubernetes-csi/external-snapshotter)
- [Announcing general availability of incremental snapshots of Managed Disks](https://azure.microsoft.com/en-gb/blog/announcing-general-availability-of-incremental-snapshots-of-managed-disks/)
