# [Volume Limits support](https://kubernetes-csi.github.io/docs/volume-limits.html)

[Volume Limits](https://kubernetes-csi.github.io/docs/volume-limits.html) is supported with this driver. 
> CSI driver would leverage IMDS to query current VM sku on the agent node, if IMDS is available, it would return max data disk num of current VM sku, if IMDS is not available, it would use default max data disk num `16` which may be incorrect for some VM SKUs.

### Get `max_volumes_per_node`

 - following example shows `max_volumes_per_node` is `8` on node `aks-nodepool1-75219208-0`
```console
# kubectl get CSINode node-name -o yaml
apiVersion: storage.k8s.io/v1
kind: CSINode
metadata:
...
spec:
  drivers:
  - allocatable:
      count: 8
    name: disk.csi.azure.com
    nodeID: aks-nodepool1-75219208-0
    topologyKeys:
    - topology.disk.csi.azure.com/zone
```

### Troubleshooting `max_volumes_per_node` setting

 - Get csi-azuredisk-node logs on the node
```
kubectl logs csi-azuredisk-node-s2zrz -n kube-system -c azuredisk
```
 - Check `/csi.v1.Node/NodeGetInfo` logs
```
I0130 02:59:33.996338       1 utils.go:77] GRPC call: /csi.v1.Node/NodeGetInfo
I0130 02:59:33.996366       1 utils.go:78] GRPC request: {}
I0130 02:59:33.996432       1 azure_zones.go:165] Availability zone is not enabled for the node, falling back to fault domain
I0130 02:59:33.996445       1 nodeserver.go:350] NodeGetInfo, nodeName: aks-agentpool-31822535-vmss000006, failureDomain: 0
I0130 02:59:33.996458       1 nodeserver.go:408] got a matching size in getMaxDataDiskCount, VM Size: STANDARD_D4S_V3, MaxDataDiskCount: 8
I0130 02:59:33.996468       1 utils.go:84] GRPC response: {"accessible_topology":{"segments":{"topology.disk.csi.azure.com/zone":""}},"max_volumes_per_node":8,"node_id":"aks-agentpool-31822535-vmss000006"}
```
