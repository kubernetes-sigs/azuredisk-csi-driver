# Raw Block Volume Example

1. Specify `volumeMode` as `Block` in PVC
> `volumeMode` is `Filesystem` by default

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pvc-azuredisk
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
  volumeMode: Block
  storageClassName: managed-csi
```

2. Specify `volumeDevices`, `devicePath` in Pod Spec

```yaml
kind: Pod
apiVersion: v1
metadata:
  name: nginx-azuredisk
spec:
  nodeSelector:
    kubernetes.io/os: linux
  containers:
  - image: nginx
    name: nginx-azuredisk
    volumeDevices:
    - name: azuredisk
      devicePath: /dev/sdx
  volumes:
  - name: azuredisk
    persistentVolumeClaim:
      claimName: pvc-azuredisk
```

3. Check block device in pod

```console
# kubectl exec -it nginx-azuredisk bash
root@nginx-azuredisk:/# dd if=/dev/zero of=/dev/sdx bs=1024k count=100
100+0 records in
100+0 records out
104857600 bytes (105 MB, 100 MiB) copied, 0.0566293 s, 1.9 GB/s
```
