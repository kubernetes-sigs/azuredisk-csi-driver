## CSI driver example
### Azuredisk Dynamic Provisioning
 - Create a CSI storage class
```
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
```

 - Create a statefulset with Azure Disk mount
```
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/statefulset.yaml
```

 - Execute `df -h` command in the container
```
# kubectl exec -it statefulset-azuredisk-0 sh
# df -h
Filesystem      Size  Used Avail Use% Mounted on
...
/dev/sdc         98G   62M   98G   1% /mnt/azuredisk
...
```

### Azuredisk Static Provisioning(use an existing azure disk)
 - Create an azuredisk CSI PV, download `pv-azuredisk-csi.yaml` file and edit `diskName`, `diskURI` in `volumeAttributes`
```
wget https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/pv-azuredisk-csi.yaml
vi pv-azuredisk-csi.yaml
kubectl create -f pv-azuredisk-csi.yaml
```

 - Create a PVC
```
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi-static.yaml
```

 - make sure PVC is created and in `Bound` status after a while
```
kubectl describe pvc pvc-azuredisk
```

 - create a pod with PVC mount
```
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```

 - Execute `df -h` command in the container
```
$ kubectl exec -it nginx-azuredisk -- bash
Filesystem      Size  Used Avail Use% Mounted on
...
/dev/sdc         98G   62M   98G   1% /mnt/azuredisk
...
```
In the above example, there is a `/mnt/azuredisk` directory mounted as disk filesystem.
