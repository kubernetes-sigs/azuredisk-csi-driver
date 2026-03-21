# Azure Disk CSI Driver Usage Example

> Refer to [driver parameters](../../docs/driver-parameters.md) for more detailed usage.

## Dynamic Provisioning

### 1. Create a StorageClass

> See [`storageclass-azuredisk-csi.yaml`](storageclass-azuredisk-csi.yaml) for the full YAML spec.

```console
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
```

### 2. Create a StatefulSet with Azure Disk mount

> See [`statefulset.yaml`](statefulset.yaml) for the full YAML spec.

```console
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/statefulset.yaml
```

Verify the volume is mounted:

```console
kubectl exec -it statefulset-azuredisk-0 -- df -h
```

<pre>
Filesystem      Size  Used Avail Use% Mounted on
...
/dev/sdc         98G   62M   98G   1% /mnt/azuredisk
...
</pre>

---

## Static Provisioning (use an existing Azure Disk)

> Make sure the identity used by the driver has access to the existing Azure Disk.

### 1. Create PV/PVC bound with an existing Azure Disk

Create an Azure Disk CSI PV — download [`pv-azuredisk-csi.yaml`](pv-azuredisk-csi.yaml) and edit `volumeHandle` to match your disk resource ID:

```console
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/pv-azuredisk-csi.yaml
```

Create a PVC — see [`pvc-azuredisk-csi-static.yaml`](pvc-azuredisk-csi-static.yaml):

```console
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi-static.yaml
```

Make sure the PVC is created and in `Bound` status:

```console
kubectl describe pvc pvc-azuredisk
```

### 2. Create a pod with PVC mount

> See [`nginx-pod-azuredisk.yaml`](nginx-pod-azuredisk.yaml) for the full YAML spec.

```console
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```

Verify the volume is mounted:

```console
kubectl exec -it nginx-azuredisk -- df -h
```

<pre>
Filesystem      Size  Used Avail Use% Mounted on
...
/dev/sdc         98G   62M   98G   1% /mnt/azuredisk
...
</pre>

In the above example, `/mnt/azuredisk` is mounted as an ext4 filesystem.

---

## More Examples

- [Windows](../example/windows/)
- [Snapshot](../example/snapshot/)
- [Volume Resize](../example/resize/)
- [Raw Block Volume](../example/rawblock/)
- [Shared Disk](../example/sharedisk/)
- [Volume Cloning](../example/cloning/)
- [fsGroup](../example/fsgroup/)
- [Topology/Zone](../example/topology/)
- [Volume Limits](../example/volumelimits/)
