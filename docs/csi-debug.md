## CSI driver debug tips
### case#1: disk create/delete/attach/detach/snapshot/restore failed
> This step is not applicable if you are using [managed CSI driver on AKS](https://docs.microsoft.com/en-us/azure/aks/csi-storage-drivers).
 - find csi driver controller pod
> There could be multiple controller pods (only one pod is the leader), if there are no helpful logs, try to get logs from the leader controller pod.
```console
kubectl get po -o wide -n kube-system | grep csi-azuredisk-controller
```
<pre>
NAME                                           READY   STATUS    RESTARTS   AGE     IP             NODE
csi-azuredisk-controller-56bfddd689-dh5tk      5/5     Running   0          35s     10.240.0.19    k8s-agentpool-22533604-0
csi-azuredisk-controller-56bfddd689-sl4ll      5/5     Running   0          35s     10.240.0.23    k8s-agentpool-22533604-1
</pre>

 - get pod description and logs
```console
kubectl describe pod csi-azuredisk-controller-56bfddd689-dh5tk -n kube-system > csi-azuredisk-controller-description.log
kubectl logs csi-azuredisk-controller-56bfddd689-dh5tk -c azuredisk -n kube-system > csi-azuredisk-controller.log
```

### case#2: volume mount/unmount failed
 - locate csi driver pod that does the actual volume mount/unmount
```console
kubectl get po -o wide -n kube-system | grep csi-azuredisk-node
```
<pre>
NAME                                           READY   STATUS    RESTARTS   AGE     IP             NODE
csi-azuredisk-node-cvgbs                       3/3     Running   0          7m4s    10.240.0.35    k8s-agentpool-22533604-1
csi-azuredisk-node-dr4s4                       3/3     Running   0          7m4s    10.240.0.4     k8s-agentpool-22533604-0
</pre>

 - get pod description and logs
```console
kubectl describe pod csi-azuredisk-node-cvgbs -n kube-system > csi-azuredisk-node-description.log
kubectl logs csi-azuredisk-node-cvgbs -c azuredisk -n kube-system > csi-azuredisk-node.log
```

 - check disk mount inside driver
```console
kubectl exec -it csi-azuredisk-node-j796x -n kube-system -c azuredisk -- mount | grep sd
```
<pre>
/dev/sdc on /var/lib/kubelet/plugins/kubernetes.io/csi/pv/pvc-e4c14592-2a79-423e-846f-4b25fe393d6c/globalmount type ext4 (rw,relatime)
/dev/sdc on /var/lib/kubelet/pods/75351f5a-b2ce-4fab-bb90-250aaa010298/volumes/kubernetes.io~csi/pvc-e4c14592-2a79-423e-846f-4b25fe393d6c/mount type ext4 (rw,relatime)
</pre>

 - check domain name resolution issue inside driver
```console
apt update && apt install curl -y
curl https://apiserver-fqdn -k -v 2>&1
```

 - get cloud config file(`azure.json`) on Linux node
```console
kubectl exec -it csi-azuredisk-node-dx94w -n kube-system -c azuredisk -- cat /etc/kubernetes/azure.json
```

 - get cloud config file(`azure.json`) on Windows node
```console
kubectl exec -it csi-azuredisk-node-win-xxxxx -n kube-system -c azuredisk cmd
type c:\k\azure.json
```

 - get Windows csi-proxy logs inside driver
```console
kubectl exec -it csi-azuredisk-node-win-xxxxx -n kube-system -c azuredisk cmd
type c:\k\csi-proxy.err.log
```

#### Update driver version quickly by editing driver deployment directly
 - update controller deployment
```console
kubectl edit deployment csi-azuredisk-controller -n kube-system
```
 - update daemonset deployment
```console
kubectl edit ds csi-azuredisk-node -n kube-system
```
change below deployment config, e.g.
```console
        image: mcr.microsoft.com/k8s/csi/azuredisk-csi:v1.8.0
        imagePullPolicy: Always
```

#### Links
 - [Errors when mounting Azure disk volumes](https://docs.microsoft.com/en-us/troubleshoot/azure/azure-kubernetes/fail-to-mount-azure-disk-volume)
