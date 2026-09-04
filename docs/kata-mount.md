# Kata Containers mounts

When using the Azure Disk CSI driver with Kata Containers, by default
the runtime shares the host-mounted filesystem with the pod VM with
virtio-fs.

Instead the driver can pass the block device directly to the pod VM with
virtio-blk to be mounted inside the pod VM for improved performance.

This virtio-blk integration can enabled by setting the following
annotation on your Kata Containers RuntimeClass:

```yaml
azure.csi.disk/kata-mount: direct-volume
```

## Limitations

 * Virtio-blk requires exclusive access to the Azure Disk and the volume
   needs to have the `ReadWriteOncePod` access mode. Otherwise the
   driver will return the error `volume needs ReadWriteOncePod access
   mode with Kata`.
 * `NodeGetVolumeStats` and `NodeExpandVolume` are not yet supported
   and the driver will return an error.
