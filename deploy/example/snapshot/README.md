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
Annotations:  kubectl.kubernetes.io/last-applied-configuration:
                {"apiVersion":"snapshot.storage.k8s.io/v1alpha1","kind":"VolumeSnapshot","metadata":{"annotations":{},"name":"azuredisk-volume-snapshot","...
API Version:  snapshot.storage.k8s.io/v1alpha1
Kind:         VolumeSnapshot
Metadata:
  Creation Timestamp:  2019-08-13T03:39:54Z
  Finalizers:
    snapshot.storage.kubernetes.io/volumesnapshot-protection
  Generation:        5
  Resource Version:  494293
  Self Link:         /apis/snapshot.storage.k8s.io/v1alpha1/namespaces/default/volumesnapshots/azuredisk-volume-snapshot
  UID:               035a5797-bd7c-11e9-a014-aa2a590ff677
Spec:
  Snapshot Class Name:    csi-azuredisk-vsc
  Snapshot Content Name:  snapcontent-035a5797-bd7c-11e9-a014-aa2a590ff677
  Source:
    API Group:  <nil>
    Kind:       PersistentVolumeClaim
    Name:       pvc-azuredisk
Status:
  Creation Time:  2019-08-13T03:39:54Z
  Ready To Use:   true
  Restore Size:   10Gi
Events:           <none>
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
