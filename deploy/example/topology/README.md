# Topology(Availability Zone)

Topology is a GA feature since Kubernetes v1.17, refer to [CSI Topology Feature](https://kubernetes-csi.github.io/docs/topology.html) for more details.

### Check node topology after driver installation

In below example, there are two nodes with topology label: `topology.disk.csi.azure.com/zone=eastus2-1`
```console
$ kubectl get no --show-labels | grep topo
k8s-agentpool-83483713-vmss000000   Ready    agent    62d   v1.16.2   ...topology.disk.csi.azure.com/zone=eastus2-1
k8s-agentpool-83483713-vmss000001   Ready    agent    62d   v1.16.2   ...topology.disk.csi.azure.com/zone=eastus2-1
```
> if node is in non-zone, topology label `topology.disk.csi.azure.com/zone` would be empty

### Use following storage class with topology support
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: managed-csi
provisioner: disk.csi.azure.com
parameters:
  skuName: StandardSSD_LRS  # available values: Standard_LRS, Premium_LRS, StandardSSD_LRS, UltraSSD_LRS, Premium_ZRS, StandardSSD_ZRS
reclaimPolicy: Delete
volumeBindingMode: Immediate
```

### Follow azure disk dynamic provisioning

Continue step `Create an azuredisk CSI PVC`, refer to [Basic usage](../e2e_usage.md)

#### ZRS disk support
 - available version: v1.5.0+

ZRS(`Premium_ZRS`, `StandardSSD_ZRS`) disk could be scheduled on all zone and non-zone agent nodes, without the restriction that disk volume should be co-located in the same zone as a given node.

 - More details about [Zone-redundant storage for managed disks](https://docs.microsoft.com/en-us/azure/virtual-machines/disks-redundancy#zone-redundant-storage-for-managed-disks-preview)

#### Links
 - [Azure Availability Zones](https://github.com/kubernetes-sigs/cloud-provider-azure/blob/master/docs/using-availability-zones.md)
 - [Allowed Topologies](https://kubernetes.io/docs/concepts/storage/storage-classes/#allowed-topologies)
