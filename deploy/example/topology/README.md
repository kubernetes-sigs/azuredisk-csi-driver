# Topology(Availability Zone)

Topology is a GA feature since Kubernetes v1.17, refer to [CSI Topology Feature](https://kubernetes-csi.github.io/docs/topology.html) for more details.

### Check node topology on the agent node

In below example, there are two nodes with topology label: `topology.disk.csi.azure.com/zone=eastus2-1`, which means these two nodes are both in zone 1.
```console
$ kubectl get no --show-labels | grep topo
k8s-agentpool-83483713-vmss000000   Ready    agent    62d   v1.16.2   ...topology.disk.csi.azure.com/zone=eastus2-1
k8s-agentpool-83483713-vmss000001   Ready    agent    62d   v1.16.2   ...topology.disk.csi.azure.com/zone=eastus2-1
```
> if node is in non-zone, topology label would be `topology.disk.csi.azure.com/zone=`

### Tips

Use `volumeBindingMode: WaitForFirstConsumer` in storage class, pods would be scheduled on the agent node first, and then disk PVs would be created with the same zone as the agent node.

#### ZRS disk support
 - available version: v1.5.0+

ZRS(`Premium_ZRS`, `StandardSSD_ZRS`) disk could be scheduled on the zone and non-zone agent node, without the restriction that disk volume should be co-located in the same zone as a given node. ZRS disk is supported only in a few regions, more details about [Zone-redundant storage for managed disks](https://docs.microsoft.com/en-us/azure/virtual-machines/disks-redundancy).

 - Use following storage class with ZRS disk support
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: managed-csi-zrs
provisioner: disk.csi.azure.com
parameters:
  skuName: StandardSSD_ZRS
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
```

#### Links
 - [Azure Availability Zones](https://github.com/kubernetes-sigs/cloud-provider-azure/blob/master/docs/using-availability-zones.md)
 - [Allowed Topologies](https://kubernetes.io/docs/concepts/storage/storage-classes/#allowed-topologies)
 - [Use volume cloning to create a new ZRS disk from LRS disk](../cloning#use-volume-cloning-to-create-a-new-disk-with-different-sku)
