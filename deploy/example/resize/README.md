# Volume online resize

Azure Disk CSI Driver now supports resizing **attached** disk from v1.11

Note:
 - Pod restart on agent node is not necessary from v1.11.

## Prerequisite
#### 1. follow this [guide](https://docs.microsoft.com/en-us/azure/virtual-machines/linux/expand-disks#expand-an-azure-managed-disk) to register `LiveResize` feature

#### 2. make sure `allowVolumeExpansion: true` is set in storage class.

<details>

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: managed-csi
provisioner: disk.csi.azure.com
allowVolumeExpansion: true
parameters:
  skuName: StandardSSD_LRS
```

</details>

## Example

1. create a pod with disk PV mount.

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/statefulset.yaml
```

2. check the filesystem size in the container.

```console
$ kubectl exec -it statefulset-azuredisk-0 -- df -h /mnt/azuredisk
Filesystem      Size  Used Avail Use% Mounted on
/dev/sdc        9.8G   37M  9.8G   1% /mnt/azuredisk
```

3. expand pvc by increasing the value of `spec.resources.requests.storage`.

```console
kubectl patch pvc persistent-storage-statefulset-azuredisk-0 --type merge --patch '{"spec": {"resources": {"requests": {"storage": "15Gi"}}}}'
```

4. check pvc size after around 2 minutes.

```console
$ kubectl get pvc persistent-storage-statefulset-azuredisk-0
NAME                                         STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS
persistent-storage-statefulset-azuredisk-0   Bound    pvc-84903bd6-f4da-44f3-b3f7-9b8b59f55b6b   15Gi       RWO            disk.csi.azure.com
```

5. verify the filesystem size.

```console
$ kubectl exec -it statefulset-azuredisk-0 -- df -h /mnt/azuredisk
Filesystem      Size  Used Avail Use% Mounted on
/dev/sdc        15G   41M   15G   1% /mnt/azuredisk
```
