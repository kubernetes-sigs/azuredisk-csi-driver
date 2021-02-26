```console
# kubectl describe pvc persistent-storage-statefulset-azuredisk-0
Name:          persistent-storage-statefulset-azuredisk-0
Namespace:     default
StorageClass:  default
Status:        Bound
Volume:        pvc-612523ec-66c7-4fc1-8986-5884f99ead77
Labels:        app=nginx
Annotations:   pv.kubernetes.io/bind-completed: yes
               pv.kubernetes.io/bound-by-controller: yes
               volume.beta.kubernetes.io/storage-class: default
               volume.beta.kubernetes.io/storage-provisioner: kubernetes.io/azure-disk
               volume.kubernetes.io/storage-resizer: kubernetes.io/azure-disk
Finalizers:    [kubernetes.io/pvc-protection]
Capacity:      30Gi
Access Modes:  RWO
VolumeMode:    Filesystem
Mounted By:    statefulset-azuredisk-0
Events:
  Type    Reason                      Age                  From                                        Message
  ----    ------                      ----                 ----                                        -------
  Normal  FileSystemResizeSuccessful  13s (x2 over 3d12h)  kubelet, aks-nodepool1-15423606-vmss000000  MountVolume.NodeExpandVolume succeeded for volume "pvc-612523ec-66c7-4fc1-8986-5884f99ead77"
  
  
# kubectl describe pvc persistent-storage-statefulset-azuredisk-nonroot-0
Name:          persistent-storage-statefulset-azuredisk-nonroot-0
Namespace:     default
StorageClass:  managed-csi
Status:        Bound
Volume:        pvc-2e033853-f502-4a1c-94f5-05ee91946117
Labels:        app=nginx
Annotations:   pv.kubernetes.io/bind-completed: yes
               pv.kubernetes.io/bound-by-controller: yes
               volume.beta.kubernetes.io/storage-class: managed-csi
               volume.beta.kubernetes.io/storage-provisioner: disk.csi.azure.com
Finalizers:    [kubernetes.io/pvc-protection]
Capacity:      10Gi
Access Modes:  RWO
VolumeMode:    Filesystem
Mounted By:    statefulset-azuredisk-nonroot-0
Conditions:
  Type                      Status  LastProbeTime                     LastTransitionTime                Reason  Message
  ----                      ------  -----------------                 ------------------                ------  -------
  FileSystemResizePending   True    Mon, 01 Jan 0001 00:00:00 +0000   Thu, 04 Feb 2021 14:15:03 +0000           Waiting for user to (re-)start a pod to finish file system resize of volume on node.
Events:
  Type     Reason                    Age    From                                                                                       Message
  ----     ------                    ----   ----                                                                                       -------
  Normal   ExternalProvisioning      2m28s  persistentvolume-controller                                                                waiting for a volume to be created, either by external provisioner "disk.csi.azure.com" or manually created by system administrator
  Normal   Provisioning              2m28s  disk.csi.azure.com_aks-nodepool1-15423606-vmss000000_536e29ad-91fb-4f74-852e-83c4209e6e96  External provisioner is provisioning volume for claim "default/persistent-storage-statefulset-azuredisk-nonroot-0"
  Normal   ProvisioningSucceeded     2m25s  disk.csi.azure.com_aks-nodepool1-15423606-vmss000000_536e29ad-91fb-4f74-852e-83c4209e6e96  Successfully provisioned volume pvc-2e033853-f502-4a1c-94f5-05ee91946117
  Warning  ExternalExpanding         34s    volume_expand                                                                              Ignoring the PVC: didn't find a plugin capable of expanding the volume; waiting for an external controller to process this PVC.
  Normal   Resizing                  34s    external-resizer disk.csi.azure.com                                                        External resizer is resizing volume pvc-2e033853-f502-4a1c-94f5-05ee91946117
  Normal   FileSystemResizeRequired  32s    external-resizer disk.csi.azure.com                                                        Require file system resize of volume on node


Events:
  Type     Reason                      Age   From                                        Message
  ----     ------                      ----  ----                                        -------
  Warning  ExternalExpanding           60m   volume_expand                               Ignoring the PVC: didn't find a plugin capable of expanding the volume; waiting for an external controller to process this PVC.
  Normal   Resizing                    60m   external-resizer disk.csi.azure.com         External resizer is resizing volume pvc-2e033853-f502-4a1c-94f5-05ee91946117
  Normal   FileSystemResizeRequired    60m   external-resizer disk.csi.azure.com         Require file system resize of volume on node
  Normal   FileSystemResizeSuccessful  22s   kubelet, aks-nodepool1-15423606-vmss000000  MountVolume.NodeExpandVolume succeeded for volume "pvc-2e033853-f502-4a1c-94f5-05ee91946117"
``
