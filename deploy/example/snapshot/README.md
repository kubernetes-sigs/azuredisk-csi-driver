# Snapshot Example

## Prerequisite
 - Enable Volume Snapshot Data Source Feature Gate

Volume snapshot is an alpha feature since Kubernetes v1.12(beta in v1.17), feature gate [`VolumeSnapshotDataSource`](https://github.com/kubernetes/kubernetes/blob/bb7bad49f54b682a9ec2d6c82824673acc33c64c/pkg/features/kube_features.go#L354-L359) must be enabled before v1.17, refer to [Snapshot & Restore Feature](https://kubernetes-csi.github.io/docs/snapshot-restore-feature.html) for more details.

## Create a Source PVC

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```

### Check the Source PVC

```console
$ kubectl exec nginx-azuredisk -- ls /mnt/disk
lost+found
outfile
```

## Create a Snapshot of the Source PVC

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/snapshot/storageclass-azuredisk-snapshot.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/snapshot/azuredisk-volume-snapshot.yaml
```
### Check the Creation Status

```console
$ kubectl describe volumesnapshot azuredisk-volume-snapshot
Name:         azuredisk-volume-snapshot
Namespace:    default
Labels:       <none>
Annotations:  <none>
API Version:  snapshot.storage.k8s.io/v1beta1
Kind:         VolumeSnapshot
Metadata:
  Creation Timestamp:  2020-01-22T09:53:18Z
  Finalizers:
    snapshot.storage.kubernetes.io/volumesnapshot-as-source-protection
    snapshot.storage.kubernetes.io/volumesnapshot-bound-protection
  Generation:        1
  Resource Version:  5279
  Self Link:         /apis/snapshot.storage.k8s.io/v1beta1/namespaces/default/volumesnapshots/azuredisk-volume-snapshot
  UID:               fc094296-3056-4732-be0e-c694187c77bd
Spec:
  Source:
    Persistent Volume Claim Name:  pvc-azuredisk
  Volume Snapshot Class Name:      csi-azuredisk-vsc
Status:
  Bound Volume Snapshot Content Name:  snapcontent-fc094296-3056-4732-be0e-c694187c77bd
  Creation Time:                       2020-01-22T09:53:18Z
  Ready To Use:                        true
  Restore Size:                        10Gi
Events:                                <none>
```

## Restore the Snapshot into a New PVC

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/snapshot/pvc-azuredisk-snapshot-restored.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/snapshot/nginx-pod-restored-snapshot.yaml
```

### Check Sample Data

```console
$ kubectl exec nginx-restored -- ls /mnt/disk
lost+found
outfile
```
