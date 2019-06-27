# Install azuredisk CSI driver on a kubernetes cluster

If you have already installed Helm, you can also use it to install azuredisk CSI driver. Please see [Installation with Helm](../charts/README.md).

## Installation with kubectl

```
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/crd-csi-driver-registry.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/crd-csi-node-info.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-controller.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-node.yaml
```

- check pods status:

```
watch kubectl get po -o wide -n kube-system | grep csi-azuredisk
```

example output:

```
NAME                                   READY   STATUS    RESTARTS   AGE    IP            NODE
csi-azuredisk-controller-0             6/6     Running   3          109s   10.244.0.40   aks-nodepool1-35570981-0   
csi-azuredisk-node-dxtxp               3/3     Running   3          7d2h   10.240.0.5    aks-nodepool1-35570981-0

```

- clean up azure disk CSI driver

```
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/crd-csi-driver-registry.yaml
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/crd-csi-node-info.yaml
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-controller.yaml
kubectl delete -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-node.yaml
```
