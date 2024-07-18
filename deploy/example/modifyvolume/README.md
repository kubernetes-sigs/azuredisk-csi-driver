# Volume Modification
## Prerequisites
Volume modification only work on a cluster with the `VolumeAttributesClass` feature enabled. To use this feature, it should:
- `VolumeAttributesClass` feature gate on `kube-apiserver` (consult your Kubernetes distro's documentation)
- `storage.k8s.io/v1alpha1` enabled in `kube-apiserver` via [`runtime-config`](https://kubernetes.io/docs/tasks/administer-cluster/enable-disable-api/) (consult your Kubernetes distro's documentation)
- `VolumeAttributesClass` feature gate on `kube-controller-manager` (consult your Kubernetes distro's documentation)
- `VolumeAttributesClass` feature gate on `csi-provisioner` container in csi-azuredisk-controller
- `VolumeAttributesClass` feature gate on `csi-resizer` container in csi-azuredisk-controller

For more information, see the [Kubernetes documentation for the feature](https://kubernetes.io/docs/concepts/storage/volume-attributes-classes/).

## Parameters
Users can specify the following modification parameters:
- `skuName`: to update the disk type(skuName is not allowed to change from or to UltraSSD_LRS or PremiumV2_LRS disk type, more details on [Change the disk type of an Azure managed disk](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-convert-types?tabs=azure-powershell))
- `DiskIOPSReadWrite`: to update the IOPS
- `DiskMBpsReadWrite`: to update the throughput

## Usage

### Create an example Pod, PVC and StorageClass
```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/modifyvolume/storageclass-azuredisk-csi-premiumv2.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```

### Wait for the PVC in Bound state and the pod in Running state
```console
kubectl get pvc pvc-azuredisk
NAME            STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS        VOLUMEATTRIBUTESCLASS   AGE
pvc-azuredisk   Bound    pvc-e2a5c302-0b48-49a5-bde7-5c0528c7a06f   10Gi       RWO            managed-csi         <unset>                 17m

kubectl get pod nginx-azuredisk
NAME              READY   STATUS              RESTARTS   AGE
nginx-azuredisk   1/1     Running             0          20s
```

### Create VolumeAttributesClass
```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/modifyvolume/volumeattributesclass.yaml
kubectl get volumeattributesclass premium2-disk-class
NAME                  DRIVERNAME           AGE
premium2-disk-class   disk.csi.azure.com   4s
```

### Edit the PVC to point to the VolumeAttributesClass
```console
kubectl patch pvc pvc-azuredisk --patch '{"spec": {"volumeAttributesClassName": "premium2-disk-class"}}'
```

### Wait for the VolumeAttributesClass to apply to the volume
```console
kubectl get pvc pvc-azuredisk
NAME            STATUS   VOLUME                                     CAPACITY   ACCESS MODES   STORAGECLASS        VOLUMEATTRIBUTESCLASS   AGE
pvc-azuredisk   Bound    pvc-e2a5c302-0b48-49a5-bde7-5c0528c7a06f   10Gi       RWO            managed-csi         premium2-disk-class     20m

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