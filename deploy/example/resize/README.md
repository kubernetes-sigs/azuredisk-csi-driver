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

#### successful volume expansion events example
<details>
 
```console
$ kubectl get describe persistent-storage-statefulset-azuredisk-0
Events:
  Type     Reason                      Age                   From                                                                                       Message
  ----     ------                      ----                  ----                                                                                       -------
  Normal   WaitForFirstConsumer        35m (x2 over 35m)     persistentvolume-controller                                                                waiting for first consumer to be created before binding
  Normal   ExternalProvisioning        35m                   persistentvolume-controller                                                                waiting for a volume to be created, either by external provisioner "disk.csi.azure.com" or manually created by system administrator
  Normal   Provisioning                35m                   disk.csi.azure.com_aks-agentpool-32806483-vmss000001_010c423f-e720-4b9f-89fa-07b246c920cb  External provisioner is provisioning volume for claim "default/persistent-storage-statefulset-azuredisk-0"
  Normal   ProvisioningSucceeded       35m                   disk.csi.azure.com_aks-agentpool-32806483-vmss000001_010c423f-e720-4b9f-89fa-07b246c920cb  Successfully provisioned volume pvc-65a8b677-4490-4066-9446-c4067188acab
  Warning  ExternalExpanding           77s (x3 over 33m)     volume_expand                                                                              Ignoring the PVC: didn't find a plugin capable of expanding the volume; waiting for an external controller to process this PVC.
  Normal   Resizing                    77s                   external-resizer disk.csi.azure.com                                                        External resizer is resizing volume pvc-65a8b677-4490-4066-9446-c4067188acab
  Normal   FileSystemResizeRequired    44s                   external-resizer disk.csi.azure.com                                                        Require file system resize of volume on node
  Normal   FileSystemResizeSuccessful  6s (x3 over 29m)      kubelet                                                                                    MountVolume.NodeExpandVolume succeeded for volume "pvc-65a8b677-4490-4066-9446-c4067188acab"
```

</details>
