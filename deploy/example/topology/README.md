# Topology(Availability Zone) example

Topology is a beta feature since Kubernetes v1.14, refer to [CSI Topology Feature](https://kubernetes-csi.github.io/docs/topology.html) for more details.

### Check node topology after driver installation

In below example, there are two nodes with topology label: `topology.disk.csi.azure.com/zone=eastus2-1`

```console
$ kubectl get no --show-labels | grep topo
k8s-agentpool-83483713-vmss000000   Ready    agent    62d   v1.16.2   ...topology.disk.csi.azure.com/zone=eastus2-1
k8s-agentpool-83483713-vmss000001   Ready    agent    62d   v1.16.2   ...topology.disk.csi.azure.com/zone=eastus2-1
```

### Create an azure disk storage class with topology support

```console
$ wget -O storageclass-azuredisk-csi-topology.yaml https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi-topology.yaml
# edit `topology.disk.csi.azure.com/zone` values with above node topology label value
$ kubectl apply -f storageclass-azuredisk-csi-topology.yaml
```
 > make sure `volumeBindingMode` is set as `WaitForFirstConsumer`

### Follow azure disk dynamic provisioning

Continue from step `Create an azuredisk CSI PVC`, refer to [Basic usage](../deploy/example/e2e_usage.md)
