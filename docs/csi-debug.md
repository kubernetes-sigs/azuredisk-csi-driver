## CSI driver debug tips
### Condition#1: disk create/delete/attach/detach failed
 - locate csi driver pod
```
$ kubectl get po -o wide -n kube-system | grep csi
csi-azuredisk-attacher-0                        1/1     Running   0          23m     10.240.0.45    k8s-agentpool-17181929-1   <none>
csi-azuredisk-bctp5                             2/2     Running   0          23m     10.240.0.35    k8s-agentpool-17181929-1   <none>
csi-azuredisk-provisioner-0                     1/1     Running   0          23m     10.240.0.43    k8s-agentpool-17181929-1   <none>
csi-azuredisk-wwpxc                             2/2     Running   0          23m     10.240.0.4     k8s-agentpool-17181929-0   <none>
```
In above example, `csi-azuredisk-provisioner-0` is running on `k8s-agentpool-17181929-1`, so `csi-azuredisk-bctp5` which is running on same node is the backend csi driver intact with `csi-azuredisk-provisioner-0`
 - get csi driver logs
```
$ kubectl logs csi-azuredisk-provisioner-0 -n kube-system
$ kubectl logs csi-azuredisk-bctp5 -c azuredisk -n kube-system
```

### Condition#2: disk mount/unmount failed
 - locate csi driver pod
If the pod is running on `k8s-agentpool-17181929-0`, then `csi-azuredisk-wwpxc` which is running on same node is the backend csi driver handling mount/unmount

 - get csi driver logs
```
$ kubectl logs csi-azuredisk-wwpxc -c azuredisk -n kube-system
```
