# Topology(Availability Zone)

Topology is a beta feature since Kubernetes v1.14, refer to [CSI Topology Feature](https://kubernetes-csi.github.io/docs/topology.html) for more details.

### Check node topology after driver installation

In below example, there are two nodes with topology label: `topology.disk.csi.azure.com/zone=eastus2-1`
```console
$ kubectl get no --show-labels | grep topo
k8s-agentpool-83483713-vmss000000   Ready    agent    62d   v1.16.2   ...topology.disk.csi.azure.com/zone=eastus2-1
k8s-agentpool-83483713-vmss000001   Ready    agent    62d   v1.16.2   ...topology.disk.csi.azure.com/zone=eastus2-1
```

### Use following storage class with topology support
```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: disk.csi.azure.com
provisioner: disk.csi.azure.com
parameters:
  skuname: StandardSSD_LRS  # alias: storageaccounttype, available values: Standard_LRS, Premium_LRS, StandardSSD_LRS, UltraSSD_LRS
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer  # make sure `volumeBindingMode` is set as `WaitForFirstConsumer`
allowedTopologies:
  - matchLabelExpressions:
      - key: topology.disk.csi.azure.com/zone
        values:
          - eastus2-1
          - eastus2-2
```

### Follow azure disk dynamic provisioning

Continue from step `Create an azuredisk CSI PVC`, refer to [Basic usage](../e2e_usage.md)

#### Links
 - [Azure Availability Zones](https://github.com/kubernetes-sigs/cloud-provider-azure/blob/master/docs/using-availability-zones.md)
