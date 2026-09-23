# Kata Containers mounts

- Feature stage: Alpha
- Default: Disabled
- Feature gate: `KataMount`

> **Note:** This feature gate can be safely enabled starting with the
> 1.36.1 release, once reliable Kata Containers runtime detection has
> been added. Enabling it in earlier releases is not recommended.

When using the Azure Disk CSI driver with Kata Containers, by default
the runtime shares the host-mounted filesystem with the pod VM with
virtio-fs.

Instead the driver can pass the block device directly to the pod VM with
virtio-blk to be mounted inside the pod VM for improved performance.

## How to enable

First enable Kata mounts with the Helm flag:

```helm
driver.featureGates.KataMount=true
```

This passes the following option to the node driver component:

```text
--feature-gates=KataMount=true
```

Before starting the node driver, annotate each node that will use direct volumes:

```yaml
azure.csi.disk/kata-mount: direct-volume
```

Finally, select direct volumes for pods by setting the following annotation on
their Kata Containers RuntimeClass:

```yaml
azure.csi.disk/kata-mount: direct-volume
```

## Example

```yaml
---
kind: RuntimeClass
apiVersion: node.k8s.io/v1
metadata:
  name: kata
  annotations:
    azure.csi.disk/kata-mount: "direct-volume"
handler: kata
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: pvc-azuredisk
spec:
  accessModes:
    - ReadWriteOncePod
  resources:
    requests:
      storage: 10Gi
  storageClassName: managed-csi
---
apiVersion: v1
kind: Pod
metadata:
  name: kata-azuredisk
spec:
  runtimeClassName: kata
  containers:
    - name: busybox
      image: busybox:latest
      command: ["sleep", "infinity"]
      volumeMounts:
        - name: azuredisk
          mountPath: /mnt/azuredisk
  volumes:
    - name: azuredisk
      persistentVolumeClaim:
        claimName: pvc-azuredisk
```

## Limitations

 * Virtio-blk requires exclusive access to the Azure Disk and the volume
   needs to have the `ReadWriteOncePod` access mode (instead of
   `ReadWriteOnce`). Otherwise the driver will return the error `volume
   needs ReadWriteOncePod access mode with Kata`.
 * `NodeGetVolumeStats` and `NodeExpandVolume` are not yet supported
   and the driver will return an error.
