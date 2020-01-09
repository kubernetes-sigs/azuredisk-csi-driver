# Azure disk CSI driver development guide

## How to build this project
 - Clone repo
```
$ mkdir -p $GOPATH/src/sigs.k8s.io/
$ git clone https://github.com/kubernetes-sigs/azuredisk-csi-driver $GOPATH/src/sigs.k8s.io/azuredisk-csi-driver
```

 - Build CSI driver
```
$ cd $GOPATH/src/sigs.k8s.io/azuredisk-csi-driver
$ make azuredisk
```

 - Run verification before sending PR
```
$ make verify
```

## How to test CSI driver in local environment

Install `csc` tool according to https://github.com/rexray/gocsi/tree/master/csc
```
$ mkdir -p $GOPATH/src/github.com/rexray
$ cd $GOPATH/src/github.com/rexray
$ git clone https://github.com/rexray/gocsi.git
$ cd gocsi/csc
$ make build
```

#### Start CSI driver locally
```
$ cd $GOPATH/src/sigs.k8s.io/azuredisk-csi-driver
$ ./_output/azurediskplugin --endpoint tcp://127.0.0.1:10000 --nodeid CSINode -v=5 &
```
> Before running CSI driver, create "/etc/kubernetes/azure.json" file under testing server(it's better copy `azure.json` file from a k8s cluster with service principle configured correctly) and set `AZURE_CREDENTIAL_FILE` as following:
```
export set AZURE_CREDENTIAL_FILE=/etc/kubernetes/azure.json
```

#### 1. Get plugin info
```
$ csc identity plugin-info --endpoint tcp://127.0.0.1:10000
"disk.csi.azure.com"    "v0.5.0"
```

#### 2. Create an azure disk volume
```
$ csc controller new --endpoint tcp://127.0.0.1:10000 --cap 1,block CSIVolumeName  --req-bytes 2147483648 --params skuname=Standard_LRS,kind=managed
"/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"        2147483648      "kind"="managed"        "skuname"="Standard_LRS"
```

#### 3. Attach an azure disk volume to a node
```
$ csc controller publish --endpoint tcp://127.0.0.1:10000 --node-id k8s-agentpool-17181929-0 --cap 1,block "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 4. Stage an azure disk volume on a node (format and mount disk to a staging path)
```
$ csc node stage --endpoint tcp://127.0.0.1:10000 --cap 1,block --staging-target-path=/tmp/staging-path --pub-info devicePath="0" "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 5. Publish an azure disk volume on a node (bind mount the volume from staging to target path)
```
$ csc node publish --endpoint tcp://127.0.0.1:10000 --cap 1,block --staging-target-path=/tmp/staging-path --target-path=/tmp/publish-path "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 6. Unpublish an azure disk volume on a node
```
$ csc node unpublish --endpoint tcp://127.0.0.1:10000 --target-path=/tmp/publish-path "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 7. Unstage an azure disk volume on a node
```
$ csc node unstage --endpoint tcp://127.0.0.1:10000 --staging-target-path=/tmp/staging-path "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 8. Detach an azure disk volume from a node
```
$ csc controller unpublish --endpoint tcp://127.0.0.1:10000 --node-id k8s-agentpool-17181929-0 "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 9. Delete an azure disk volume
```
$ csc controller del --endpoint tcp://127.0.0.1:10000 "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 10. Validate volume capabilities
```
$ csc controller validate-volume-capabilities --endpoint tcp://127.0.0.1:10000 --cap 1,block CSIVolumeID
CSIVolumeID  true
```

#### 11. Get NodeID
```
$ csc node get-info --endpoint tcp://127.0.0.1:10000
CSINode
```

#### 12. Create snapshot
```
$ csc controller create-snapshot snapshot-name --endpoint tcp://127.0.0.1:10000 --source-volume "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 13. Delete snapshot
```
$ csc controller delete-snapshot snapshot-name --endpoint tcp://127.0.0.1:10000
```

#### 14. List snapshot
```
$ csc controller list-snapshots --endpoint tcp://127.0.0.1:10000
```

## How to test CSI driver in a Kubernetes cluster

 - Build driver image and push image to dockerhub
```
# run `docker login` first
export REGISTRY=<dockerhub-alias>
make azuredisk-container
make push-latest
```

 - Replace `mcr.microsoft.com/k8s/csi/azuredisk-csi:latest` in [`csi-azuredisk-controller.yaml`](https://github.com/kubernetes-sigs/azuredisk-csi-driver/blob/master/deploy/csi-azuredisk-controller.yaml) and [`csi-azuredisk-node.yaml`](https://github.com/kubernetes-sigs/azuredisk-csi-driver/blob/master/deploy/csi-azuredisk-node.yaml) with above dockerhub image urls and then follow [install CSI driver master version](https://github.com/kubernetes-sigs/azuredisk-csi-driver/blob/master/docs/install-csi-driver-master.md)
 ```
wget -O csi-azuredisk-controller.yaml https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-controller.yaml
# edit csi-azuredisk-controller.yaml
kubectl apply -f csi-azuredisk-controller.yaml

wget -O csi-azuredisk-node.yaml https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/csi-azuredisk-node.yaml
# edit csi-azuredisk-node.yaml
kubectl apply -f csi-azuredisk-node.yaml
 ```
