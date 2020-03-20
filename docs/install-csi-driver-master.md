## Install azuredisk CSI driver development version on a Kubernetes cluster

### Installation with kubectl

#### Linux

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-driver.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/crd-csi-node-info.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/rbac-csi-azuredisk-controller.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-controller.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-node.yaml

# skip below yaml configurations if snapshot feature(only available from v1.17.0) will not be used
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/crd-csi-snapshot.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/rbac-csi-snapshot-controller.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-snapshot-controller.yaml
```

#### Windows

Windows CSI driver requires [CSI Proxy](https://github.com/kubernetes-csi/csi-proxy) to be installed on the system. Please use the 
instructions in the [aks-engine](https://github.com/Azure/aks-engine/blob/master/docs/topics/csi-proxy-windows.md) repo to create 
a cluster with CSI proxy installed. 

> Note: Windows CSI drivers are in alpha state.

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-driver.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/crd-csi-node-info.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/rbac-csi-azuredisk-controller.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-controller.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-node-windows.yaml
```

### Check pods status:

```console
kubectl -n kube-system get pod -o wide --watch -l app=csi-azuredisk-controller
kubectl -n kube-system get pod -o wide --watch -l app=csi-azuredisk-node
```

example output:

```console
NAME                                            READY   STATUS    RESTARTS   AGE     IP             NODE
csi-azuredisk-controller-56bfddd689-dh5tk       6/6     Running   0          35s     10.240.0.19    k8s-agentpool-22533604-0
csi-azuredisk-controller-56bfddd689-7s8yg       6/6     Running   0          35s     10.240.0.29    k8s-agentpool-22533604-1
csi-snapshot-controller-84db6dbbb-stzwr         6/6     Running   0          41s     10.240.0.17    k8s-agentpool-22533604-0
csi-azuredisk-node-cvgbs                        3/3     Running   0          7m4s    10.240.0.35    k8s-agentpool-22533604-1
csi-azuredisk-node-dr4s4                        3/3     Running   0          7m4s    10.240.0.4     k8s-agentpool-22533604-0
```

### Clean up azure disk CSI driver

```console
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-controller.yaml
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-node.yaml
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-driver.yaml
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/crd-csi-node-info.yaml
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/rbac-csi-azuredisk-controller.yaml
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-snapshot-controller.yaml
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/rbac-csi-snapshot-controller.yaml
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/crd-csi-snapshot.yaml
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-node-windows.yaml
```
