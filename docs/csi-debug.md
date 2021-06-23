## CSI driver debug tips
### Case#1: disk create/delete/attach/detach/snapshot/restore failed
 - locate csi driver pod
```console
$ kubectl get po -o wide -n kube-system | grep csi-azuredisk-controller
NAME                                           READY   STATUS    RESTARTS   AGE     IP             NODE
csi-azuredisk-controller-56bfddd689-dh5tk      5/5     Running   0          35s     10.240.0.19    k8s-agentpool-22533604-0
csi-azuredisk-controller-56bfddd689-sl4ll      5/5     Running   0          35s     10.240.0.23    k8s-agentpool-22533604-1
```
 - get csi driver logs
```console
$ kubectl logs csi-azuredisk-controller-56bfddd689-dh5tk -c azuredisk -n kube-system > csi-azuredisk-controller.log
```
> note: there could be multiple controller pods, if there are no helpful logs, try to get logs from other controller pods

### Case#2: volume mount/unmount failed
 - locate csi driver pod and make sure which pod do tha actual volume mount/unmount
```console
$ kubectl get po -o wide -n kube-system | grep csi-azuredisk-node
NAME                                           READY   STATUS    RESTARTS   AGE     IP             NODE
csi-azuredisk-node-cvgbs                       3/3     Running   0          7m4s    10.240.0.35    k8s-agentpool-22533604-1
csi-azuredisk-node-dr4s4                       3/3     Running   0          7m4s    10.240.0.4     k8s-agentpool-22533604-0
```

 - get csi driver logs
```console
$ kubectl logs csi-azuredisk-node-cvgbs -c azuredisk -n kube-system > csi-azuredisk-node.log
```

#### Update driver version quickly by editting driver deployment directly
```console
kubectl edit deployment csi-azuredisk-controller -n kube-system
```
and then change below deployment config, e.g.
```console
        image: mcr.microsoft.com/k8s/csi/azuredisk-csi:v1.5.0
        imagePullPolicy: Always
```
