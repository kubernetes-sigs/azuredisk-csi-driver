# Azure Disk Snapshot feature

- Snapshot feature is beta since Kubernetes v1.17.0, refer to [Snapshot & Restore Feature](https://kubernetes-csi.github.io/docs/snapshot-restore-feature.html) for more details.

## Introduction
This driver supports both [full and incremental snapshot functionalities](https://docs.microsoft.com/en-us/azure/virtual-machines/windows/incremental-snapshots), user could set `incremental` in `VolumeSnapshotClass` to control whether create full or incremental(by default) snapshot(refer to [VolumeSnapshotClass](../../../docs/driver-parameters.md#volumesnapshotclass) for detailed parameters description):

```
apiVersion: snapshot.storage.k8s.io/v1beta1
kind: VolumeSnapshotClass
metadata:
  name: csi-azuredisk-vsc
driver: disk.csi.azure.com
deletionPolicy: Delete
parameters:
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
                {"apiVersion":"snapshot.storage.k8s.io/v1beta1","kind":"VolumeSnapshot","metadata":{"annotations":{},"name":"azuredisk-volume-snapshot","n...
API Version:  snapshot.storage.k8s.io/v1beta1
Kind:         VolumeSnapshot
Metadata:
  Creation Timestamp:  2020-02-04T13:59:54Z
  Finalizers:
    snapshot.storage.kubernetes.io/volumesnapshot-as-source-protection
    snapshot.storage.kubernetes.io/volumesnapshot-bound-protection
  Generation:        1
  Resource Version:  48044
  Self Link:         /apis/snapshot.storage.k8s.io/v1beta1/namespaces/default/volumesnapshots/azuredisk-volume-snapshot
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

#### Links
 - [CSI Snapshotter](https://github.com/kubernetes-csi/external-snapshotter)
