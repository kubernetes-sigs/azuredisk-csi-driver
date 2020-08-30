# [Volume Limits support](https://kubernetes-csi.github.io/docs/volume-limits.html)

[Volume Limits](https://kubernetes-csi.github.io/docs/volume-limits.html) is supported with this driver.

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
I0829 02:13:15.165405       1 utils.go:108] GRPC call: /csi.v1.Node/NodeGetInfo
I0829 02:13:15.165427       1 utils.go:109] GRPC request: {}
I0829 02:13:15.170350       1 azure_zones.go:76] Availability zone is not enabled for the node, falling back to fault domain
I0829 02:13:15.170378       1 nodeserver.go:281] got a matching size in getMaxDataDiskCount, VM Size: STANDARD_DS2_V2, MaxDataDiskCount: 8
I0829 02:13:15.170386       1 utils.go:114] GRPC response: {"accessible_topology":{"segments":{"topology.disk.csi.azure.com/zone":""}},"max_volumes_per_node":8,"node_id":"aks-nodepool1-75219208-1"}
```
