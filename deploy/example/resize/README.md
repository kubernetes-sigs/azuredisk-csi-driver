# Volume Resizing Example

Currently, Azure Disk CSI Driver only supports resizing unattached disk.

## Example

1. Set `allowVolumeExpansion` field as true in the storage class.

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

4. Delete the pod.

```console
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```

5. Expand the pvc by increasing the field `spec.resources.requests.storage`.

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

6. Check the pvc and pv size.

```console
$ k get pvc pvc-azuredisk
NAME            STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS         AGE
pvc-azuredisk   Bound    pvc-84903bd6-f4da-44f3-b3f7-9b8b59f55b6b   10Gi       RWO            disk.csi.azure.com   3m11s

$ k get pv pvc-84903bd6-f4da-44f3-b3f7-9b8b59f55b6b
NAME                                       CAPACITY   ACCESS MODES   RECLAIM POLICY   STATUS   CLAIM                   STORAGECLASS         REASON   AGE
pvc-84903bd6-f4da-44f3-b3f7-9b8b59f55b6b   15Gi       RWO            Delete           Bound    default/pvc-azuredisk   disk.csi.azure.com            4m2s
```

After the pvc re-attaches to the container, the size will be updated.

7. Create the new pod.

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```

8. Verify the filesystem size.

```console
$ kubectl get pvc pvc-azuredisk
NAME            STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS         AGE
pvc-azuredisk   Bound    pvc-84903bd6-f4da-44f3-b3f7-9b8b59f55b6b   15Gi       RWO            disk.csi.azure.com   7m

$ kubectl exec -it nginx-azuredisk -- df -h /mnt/azuredisk
Filesystem      Size  Used Avail Use% Mounted on
/devhost/sdc     15G   41M   15G   1% /mnt/azuredisk
```
