## CSI driver E2E usage example
#### 1. create a pod with csi azuredisk driver mount on linux
##### Option#1: Azuredisk Dynamic Provisioning
 - Create an azuredisk CSI storage class
```
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
```

 - Create a statefulset with Azure Disk mount
```
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/statefulset.yaml
```

##### Option#2: Azuredisk Static Provisioning(use an existing azure disk)
 - Create an azuredisk CSI PV, download `pv-azuredisk-csi.yaml` file and edit `diskName`, `diskURI` in `volumeAttributes`
```
wget https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/pv-azuredisk-csi.yaml
vi pv-azuredisk-csi.yaml
kubectl create -f pv-azuredisk-csi.yaml
```

 - Create an azuredisk CSI PVC which would be bound to the above PV
```
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi-static.yaml
```

#### 2. validate PVC status and create an nginx pod
 - make sure pvc is created and in `Bound` status finally
```
watch kubectl describe pvc pvc-azuredisk
```

 - create a pod with azuredisk CSI PVC
```
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/nginx-pod-azuredisk.yaml
```

#### 3. enter the pod container to do validation
 - watch the status of pod until its Status changed from `Pending` to `Running` and then enter the pod container
```
$ watch kubectl describe po nginx-azuredisk
$ kubectl exec -it nginx-azuredisk -- bash
Filesystem      Size  Used Avail Use% Mounted on
overlay          30G   15G   15G  52% /
...
/devhost/sdc        9.8G   37M  9.8G   1% /mnt/azuredisk
...
```
In the above example, there is a `/mnt/azuredisk` directory mounted as disk filesystem.
