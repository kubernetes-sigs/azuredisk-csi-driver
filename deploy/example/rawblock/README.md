# Raw Block Volume Example

1. User requests a PVC with `volumeMode = Block`

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
  storageClassName: disk.csi.azure.com
```

2. Then requests a Pod with `volumeDevices` and specifies `devicePath`

```yaml
kind: Pod
apiVersion: v1
metadata:
  name: nginx-azuredisk
spec:
  nodeSelector:
    beta.kubernetes.io/os: linux
  containers:
  - image: nginx
    name: nginx-azuredisk
    command:
    - "/bin/sh"
    - "-c"
    - while true; do echo $(date) >> /mnt/azuredisk/outfile; sleep 1; done
    volumeDevices:
    - name: azuredisk01
      devicePath: /dev/xvda
  volumes:
  - name: azuredisk01
    persistentVolumeClaim:
      claimName: pvc-azuredisk
```

3. Finally the Block PV is passed to the Pod as /dev/xvda (or any user defined devicePath) 

```
# kubectl exec -it nginx-azuredisk bash
root@nginx-azuredisk:/# dd if=/dev/zero of=/dev/xvda bs=1024k count=100
100+0 records in
100+0 records out
104857600 bytes (105 MB, 100 MiB) copied, 0.0566293 s, 1.9 GB/s
```