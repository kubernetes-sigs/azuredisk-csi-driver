# ModifyVolume feature example
 - Feature Status: GA from Kubernetes 1.34
 - supported from Azure Disk CSI driver v1.30.2

To learn more about this feature, please refer to the [Kubernetes documentation for VolumeAttributesClass feature](https://kubernetes.io/docs/concepts/storage/volume-attributes-classes/).

## Supported parameters in `VolumeAttributesClass`
- `DiskIOPSReadWrite`: disk IOPS
- `DiskMBpsReadWrite`: disk throughput
- `skuName`:  disk type
> Changing the `skuName` to or from UltraSSD_LRS is not permitted. For additional information, please consult the following resource [Change the disk type of an Azure managed disk](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-convert-types)

Here is an example of the `VolumeAttributesClass` used to update the disk IOPS and throughput on an Azure PremiumV2 disk:

```yaml
apiVersion: storage.k8s.io/v1
kind: VolumeAttributesClass
metadata:
  name: premium2-disk-class
driverName: disk.csi.azure.com
parameters:
  DiskIOPSReadWrite: "5000"
  DiskMBpsReadWrite: "1200"
```

## Usage

### Create a StorageClass, PVC and pod
> The following example creates a PremiumV2_LRS disk

```bash
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/modifyvolume/storageclass-azuredisk-csi-premiumv2.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/modifyvolume/pvc-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```

### Wait until the PVC reaches the Bound state and the pod is in the Running state.
```bash
kubectl get pvc pvc-azuredisk
NAME            STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS        VOLUMEATTRIBUTESCLASS   AGE
pvc-azuredisk   Bound    pvc-e2a5c302-0b48-49a5-bde7-5c0528c7a06f   10Gi       RWO            managed-csi         <unset>                 17m

kubectl get pod nginx-azuredisk
NAME              READY   STATUS              RESTARTS   AGE
nginx-azuredisk   1/1     Running             0          20s
```

### Create a new VolumeAttributesClass
```bash
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/modifyvolume/volumeattributesclass.yaml
kubectl get volumeattributesclass premium2-disk-class
NAME                  DRIVERNAME           AGE
premium2-disk-class   disk.csi.azure.com   4s
```

### Update the existing PVC to refer to the newly created VolumeAttributesClass
```bash
kubectl patch pvc pvc-azuredisk --patch '{"spec": {"volumeAttributesClassName": "premium2-disk-class"}}'
```

### Wait until the VolumeAttributesClass is applied to the volume
```bash
kubectl describe pvc pvc-azuredisk
Name:          pvc-azuredisk
Namespace:     default
StorageClass:  managed-csi-prev2
Status:        Bound
Volume:        pvc-e2a5c302-0b48-49a5-bde7-5c0528c7a06f
Labels:        <none>
Annotations:   pv.kubernetes.io/bind-completed: yes
               pv.kubernetes.io/bound-by-controller: yes
               volume.beta.kubernetes.io/storage-provisioner: disk.csi.azure.com
               volume.kubernetes.io/selected-node: aks-agentpool-17390711-vmss000000
               volume.kubernetes.io/storage-provisioner: disk.csi.azure.com
Finalizers:    [kubernetes.io/pvc-protection]
Capacity:      10Gi
Access Modes:  RWO
VolumeMode:    Filesystem
Used By:       nginx-azuredisk
Events:
  Type    Reason                  Age                From                                                                                       Message
  ----    ------                  ----               ----                                                                                       -------
  Normal  WaitForFirstConsumer    21m                persistentvolume-controller                                                                waiting for first consumer to be created before binding
  Normal  Provisioning            21m                disk.csi.azure.com_aks-agentpool-17390711-vmss000000_c36f4e97-171f-46c7-ba4a-c8567bb41452  External provisioner is provisioning volume for claim "default/pvc-azuredisk"
  Normal  ExternalProvisioning    21m (x2 over 21m)  persistentvolume-controller                                                                Waiting for a volume to be created either by the external provisioner 'disk.csi.azure.com' or manually by the system administrator. If volume creation is delayed, please verify that the provisioner is running and correctly registered.
  Normal  ProvisioningSucceeded   21m                disk.csi.azure.com_aks-agentpool-17390711-vmss000000_c36f4e97-171f-46c7-ba4a-c8567bb41452  Successfully provisioned volume pvc-e2a5c302-0b48-49a5-bde7-5c0528c7a06f
  Normal  VolumeModify            18s                external-resizer disk.csi.azure.com                                                        external resizer is modifying volume pvc-azuredisk with vac premium2-disk-class
  Normal  VolumeModifySuccessful  15s                external-resizer disk.csi.azure.com                                                        external resizer modified volume pvc-azuredisk with vac premium2-disk-class successfully
```
