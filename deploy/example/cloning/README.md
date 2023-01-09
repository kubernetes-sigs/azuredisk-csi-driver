# Volume Cloning Example

## Create a Source PVC

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```

### Check the Source PVC

```console
$ kubectl exec nginx-azuredisk -- ls /mnt/azuredisk
lost+found
outfile
```

## Create a PVC from an existing PVC
>  Make sure application is not writing data to source disk
```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/cloning/pvc-azuredisk-cloning.yaml
```
### Check the Creation Status

```console
$ kubectl describe pvc pvc-azuredisk-cloning
Name:          pvc-azuredisk-cloning
Namespace:     default
StorageClass:  disk.csi.azure.com
Status:        Bound
Volume:        pvc-276b72d5-adc5-45cd-ad67-2a1f8fd6c81b
Labels:        <none>
Annotations:   kubectl.kubernetes.io/last-applied-configuration:
                 {"apiVersion":"v1","kind":"PersistentVolumeClaim","metadata":{"annotations":{},"name":"pvc-azuredisk-cloning","namespace":"default"},"spec...
               pv.kubernetes.io/bind-completed: yes
               pv.kubernetes.io/bound-by-controller: yes
               volume.beta.kubernetes.io/storage-provisioner: disk.csi.azure.com
Finalizers:    [kubernetes.io/pvc-protection]
Capacity:      10Gi
Access Modes:  RWO
VolumeMode:    Filesystem
Mounted By:    <none>
Events:
  Type    Reason                 Age                From                                                                                               Message
  ----    ------                 ----               ----                                                                                               -------
  Normal  Provisioning           30s                disk.csi.azure.com_csi-azuredisk-controller-67f97cbc57-52xpb_dc6c68b9-c45a-4fac-8497-3564fed3a59a  External provisioner is provisioning volume for claim "default/pvc-azuredisk-cloning"
  Normal  ExternalProvisioning   25s (x2 over 30s)  persistentvolume-controller                                                                        waiting for a volume to be created, either by external provisioner "disk.csi.azure.com" or manually created by system administrator
  Normal  ProvisioningSucceeded  20s                disk.csi.azure.com_csi-azuredisk-controller-67f97cbc57-52xpb_dc6c68b9-c45a-4fac-8497-3564fed3a59a  Successfully provisioned volume pvc-276b72d5-adc5-45cd-ad67-2a1f8fd6c81b
```

## Restore the PVC into a Pod

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/cloning/nginx-pod-restored-cloning.yaml
```

### Check Sample Data

```console
$ kubectl exec nginx-restored-cloning -- ls /mnt/azuredisk
lost+found
outfile
```

### Use volume cloning to copy a new disk with different sku
>  disk sku change could be from LRS to ZRS, standard to premium, while it does not support cross region or cross zone

 - Make sure application is not writing data to source disk
 - Delete existing storage class referenced by source disk PVC
 - Create a new storage class with desired `skuName` value
 - Follow steps above to create a new cloned PVC with new sku.
