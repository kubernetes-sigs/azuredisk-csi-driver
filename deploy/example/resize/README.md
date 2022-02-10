# Volume Resizing Example

Azure Disk CSI Driver now only supports resizing **attached** disk, follow this [guide](https://docs.microsoft.com/en-us/azure/virtual-machines/linux/expand-disks#expand-an-azure-managed-disk) to register `LiveResize` feature.

Note:
 - Azure disk LiveResize feature is supported from v1.10
 - Pod restart is not necessary from v1.11, before that version, pod restart is still required to get resized disk

## Example

1. Make sure `allowVolumeExpansion` field as `true` in the storage class.

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: managed-csi
provisioner: disk.csi.azure.com
allowVolumeExpansion: true
parameters:
  skuName: Standard_LRS
  cachingMode: ReadOnly
reclaimPolicy: Delete
volumeBindingMode: Immediate
```

2. Create storageclass, pvc and pod.

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```

3. Check the filesystem size in the container.

```console
$ kubectl exec -it nginx-azuredisk -- df -h /mnt/azuredisk
Filesystem      Size  Used Avail Use% Mounted on
/devhost/sdc    9.8G   37M  9.8G   1% /mnt/azuredisk
```

4. Expand the pvc by increasing the field `spec.resources.requests.storage`.

```console
$ kubectl edit pvc pvc-azuredisk
...
...
spec:
  resources:
    requests:
      storage: 15Gi
...
...
```

5. Check the pvc and pv size after around 2 minutes.

```console
$ k get pvc pvc-azuredisk
NAME            STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS         AGE
pvc-azuredisk   Bound    pvc-84903bd6-f4da-44f3-b3f7-9b8b59f55b6b   10Gi       RWO            disk.csi.azure.com   3m11s

$ k get pv pvc-84903bd6-f4da-44f3-b3f7-9b8b59f55b6b
NAME                                       CAPACITY   ACCESS MODES   RECLAIM POLICY   STATUS   CLAIM                   STORAGECLASS         REASON   AGE
pvc-84903bd6-f4da-44f3-b3f7-9b8b59f55b6b   15Gi       RWO            Delete           Bound    default/pvc-azuredisk   disk.csi.azure.com            4m2s
```

6. Verify the filesystem size.

```console
$ kubectl get pvc pvc-azuredisk
NAME            STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS         AGE
pvc-azuredisk   Bound    pvc-84903bd6-f4da-44f3-b3f7-9b8b59f55b6b   15Gi       RWO            disk.csi.azure.com   7m

$ kubectl exec -it nginx-azuredisk -- df -h /mnt/azuredisk
Filesystem      Size  Used Avail Use% Mounted on
/devhost/sdc     15G   41M   15G   1% /mnt/azuredisk
```
