# Install azuredisk CSI driver on a kubernetes cluster
```
kubectl apply -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/crd-csi-driver-registry.yaml
kubectl apply -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/crd-csi-node-info.yaml
kubectl apply -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/rbac-csi-attacher.yaml
kubectl apply -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/rbac-csi-driver-registrar.yaml
kubectl apply -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/rbac-csi-provisioner.yaml
kubectl apply -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/rbac-csi-snapshotter.yaml
kubectl apply -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/csi-azuredisk-provisioner.yaml
kubectl apply -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/csi-azuredisk-attacher.yaml
kubectl apply -f https://raw.githubusercontent.com/andyzhangx/azuredisk-csi-driver/master/deploy/azuredisk-csi-driver.yaml
```

 - check pods status:
```
watch kubectl get po -o wide
```
example output:
```
NAME                          READY   STATUS    RESTARTS   AGE   IP            NODE                     
csi-azuredisk-attacher-0      1/1     Running   0          22h   10.240.0.61   k8s-agentpool-17181929-1
csi-azuredisk-g2ksx           2/2     Running   0          21h   10.240.0.4    k8s-agentpool-17181929-0
csi-azuredisk-nqxn9           2/2     Running   0          21h   10.240.0.35   k8s-agentpool-17181929-1
csi-azuredisk-provisioner-0   1/1     Running   0          22h   10.240.0.39   k8s-agentpool-17181929-1
```

 - clean up azure file CSI driver
```
kubectl delete ds csi-azuredisk
kubectl delete sts csi-azuredisk-provisioner
kubectl delete sts csi-azuredisk-attacher
```
