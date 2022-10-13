## Expand Azure disk PVC with downtime
> Online resize is already supported from Azure Disk CSI driver v1.11, for details, go to [Volume Expansion](../../deploy/example/resize)

#### If you have not registered `LiveResize` feature, Azure disk could not be expanded when it's in "attached" state, we could only resize azure disk when it's in "unattached" state, this page will show you how to achieve this.

**How to use azure disk size grow feature**
 - In the beginning, pls make sure the Azure disk PVC is created by `disk.csi.azure.com` or `kubernetes.io/azure-disk` storage class with `allowVolumeExpansion: true` (default is false)
```yaml
kind: StorageClass
apiVersion: storage.k8s.io/v1
metadata:
  name: hdd
provisioner: disk.csi.azure.com
parameters:
  skuname: Standard_LRS
allowVolumeExpansion: true
```
 - Before run `kubectl edit pvc pvc-azuredisk` operation, pls make sure this PVC is not mounted by any pod, otherwise there would be resize error. There are a few ways to achieve this, wait a few minutes for the PVC disk detached from the node after below operation:
   - option#1: change the replica count to 0, this will terminate the pod and detach the disk
   - option#2: cordon all nodes and then delete the original pod, this will make the pod in pending state

**Make sure the only pod is terminated from the agent node and disk is in unattached state, otherwise may hit `VolumeResizeFailed` when edit disk PVC**

Now run `kubectl edit pvc pvc-azuredisk` to change azuredisk PVC size(`spec.resources.requests.storage`) from 6GB to 10GB
  
```yaml
# Please edit the object below. Lines beginning with a '#' will be ignored,
# and an empty file will abort the edit. If an error occurs while saving this file will be
# reopened with the relevant failures.
#
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  annotations:
...
...
  name: pvc-azuredisk
...
spec:
  accessModes:
  - ReadWriteOnce
  resources:
    requests:
      storage: 6Gi
  storageClassName: hdd
  volumeMode: Filesystem
  volumeName: pvc-d2d00dd9-6185-11e8-a6c3-000d3a0643a8
status:
  accessModes:
  - ReadWriteOnce
  capacity:
    storage: 6Gi
  conditions:
  - lastProbeTime: null
    lastTransitionTime: 2018-05-27T08:14:34Z
    message: Waiting for user to (re-)start a pod to finish file system resize of
      volume on node.
    status: "True"
    type: FileSystemResizePending
  phase: Bound
```

 - After resized, run `kubectl describe pvc pvc-azuredisk` to check PVC status:
```console
$ kubectl describe pvc pvc-azuredisk
Name:          pvc-azuredisk
Namespace:     default
StorageClass:  hdd
Status:        Bound
...
Capacity:      5Gi
Access Modes:  RWO
Conditions:
  Type                      Status  LastProbeTime                     LastTransitionTime                Reason  Message
  ----                      ------  -----------------                 ------------------                ------  -------
  FileSystemResizePending   True    Mon, 01 Jan 0001 00:00:00 +0000   Wed, 29 Aug 2018 02:29:52 +0000           Waiting for user to (re-)start a pod to finish file system resize of volume on node.
Events:
  Type       Reason                 Age    From                         Message
  ----       ------                 ----   ----                         -------
  Normal     ProvisioningSucceeded  3m57s  persistentvolume-controller  Successfully provisioned volume pvc-d7d250c1-ab32-11e8-bfaf-000d3a4e76db using kubernetes.io/azure-disk
Mounted By:  <none>
```

 - Create a pod mounting with this PVC, you will get
```console
kubectl exec -it nginx-azuredisk -- bash
```
```
# df -h
Filesystem      Size  Used Avail Use% Mounted on
...
/dev/sdf        9.8G   16M  9.3G   1% /mnt/disk
...
```


 - Workaround
 
 If disk size does not change inside agent node while its size is actually expanded by Azure, run following command on agent node to workaround:
 ```console
 resize2fs /dev/sdX
 ```
 
 example output could be like following:
 ```console
 resize2fs 1.44.1 (24-Mar-2018)
Filesystem at /dev/sdd is mounted on /var/lib/kubelet/plugins/kubernetes.io/azure-disk/mounts/m398871401; on-line resizing required
old_desc_blocks = 13, new_desc_blocks = 32
The filesystem on /dev/sdd is now 67108864 (4k) blocks long.
```

After resizing successfully, deployment would have the expanded disk size without restart.
