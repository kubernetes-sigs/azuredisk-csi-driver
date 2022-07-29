# Azure disk CSI driver development guide

## How to build this project
 - Clone repo
```console
$ mkdir -p $GOPATH/src/sigs.k8s.io/
$ git clone https://github.com/kubernetes-sigs/azuredisk-csi-driver $GOPATH/src/sigs.k8s.io/azuredisk-csi-driver
```

 - Build CSI driver
```console
$ cd $GOPATH/src/sigs.k8s.io/azuredisk-csi-driver
$ make azuredisk
```

 - Build CSI driver v2

Development of the V2 driver is currently ongoing in the `main_v2` branch.

```console
$ cd $GOPATH/src/sigs.k8s.io/azuredisk-csi-driver
$ git checkout main_v2
$ BUILD_V2=true make azuredisk
```

 - Run verification before sending PR
```console
$ make verify
```

 - If there is config file changed under `charts` directory, run following command to update chart file
```console
helm package charts/latest/azuredisk-csi-driver -d charts/latest/
```

## How to test CSI driver in local environment
- Install `csc` tool according to https://github.com/rexray/gocsi/tree/master/csc
```console
$ mkdir -p $GOPATH/src/github.com/rexray
$ cd $GOPATH/src/github.com/rexray
$ git clone https://github.com/rexray/gocsi.git
$ cd gocsi/csc
$ make build
```

#### Start CSI driver locally
```console
$ cd $GOPATH/src/sigs.k8s.io/azuredisk-csi-driver
$ ./_output/amd64/azurediskplugin --endpoint tcp://127.0.0.1:10000 --nodeid CSINode -v=5 &
```
> Before running CSI driver, create "/etc/kubernetes/azure.json" file under testing server(it's better copy `azure.json` file from a k8s cluster with service principle configured correctly) and set `AZURE_CREDENTIAL_FILE` as following:
```console
export set AZURE_CREDENTIAL_FILE=/etc/kubernetes/azure.json
```

#### 1. Get plugin info
```console
$ csc identity plugin-info --endpoint tcp://127.0.0.1:10000
"disk.csi.azure.com"    "v0.5.0"
```

#### 2. Create an azure disk volume
```console
$ csc controller new --endpoint tcp://127.0.0.1:10000 --cap 1,block CSIVolumeName  --req-bytes 2147483648 --params skuname=Standard_LRS,kind=managed
"/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"        2147483648      "kind"="managed"        "skuname"="Standard_LRS"
```

#### 3. Attach an Azure disk volume to a node
```console
$ csc controller publish --endpoint tcp://127.0.0.1:10000 --node-id k8s-agentpool-17181929-0 --cap 1,block "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 4. Stage an Azure disk volume on a node (format and mount disk to a staging path)
```console
$ csc node stage --endpoint tcp://127.0.0.1:10000 --cap 1,block --staging-target-path=/tmp/staging-path --pub-info devicePath="0" "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 5. Publish an Azure disk volume on a node (bind mount the volume from staging to target path)
```console
$ csc node publish --endpoint tcp://127.0.0.1:10000 --cap 1,block --staging-target-path=/tmp/staging-path --target-path=/tmp/publish-path "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 6. Unpublish an Azure disk volume on a node
```console
$ csc node unpublish --endpoint tcp://127.0.0.1:10000 --target-path=/tmp/publish-path "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 7. Unstage an Azure disk volume on a node
```console
$ csc node unstage --endpoint tcp://127.0.0.1:10000 --staging-target-path=/tmp/staging-path "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 8. Detach an Azure disk volume from a node
```console
$ csc controller unpublish --endpoint tcp://127.0.0.1:10000 --node-id k8s-agentpool-17181929-0 "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 9. Delete an Azure disk volume
```console
$ csc controller del --endpoint tcp://127.0.0.1:10000 "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 10. Validate volume capabilities
```console
$ csc controller validate-volume-capabilities --endpoint tcp://127.0.0.1:10000 --cap 1,block CSIVolumeID
CSIVolumeID  true
```

#### 11. Get NodeID
```console
$ csc node get-info --endpoint tcp://127.0.0.1:10000
CSINode
```

#### 12. Create snapshot
```console
$ csc controller create-snapshot snapshot-name --endpoint tcp://127.0.0.1:10000 --source-volume "/subscriptions/b9d2281e-dcd5-4dfd-9a97-xxx/resourceGroups/xxx/providers/Microsoft.Compute/disks/pvc-disk-dynamic-398b838f-0432-11e9-9978-000d3a00df41"
```

#### 13. Delete snapshot
```console
$ csc controller delete-snapshot snapshot-name --endpoint tcp://127.0.0.1:10000
```

#### 14. List snapshot
```console
$ csc controller list-snapshots --endpoint tcp://127.0.0.1:10000
```

## How to test CSI driver in a Kubernetes cluster
 - Build driver image and push image to dockerhub
```console
# run `docker login` first
export REGISTRY=<dockerhub-alias>
export IMAGE_VERSION=latest
# build linux, windows images
make container-all
# create a manifest list for the images above
make push-manifest
```
 - For the V2 driver, set the BUILD_V2 environment variable before building the images. You will also need to build the scheduler extender image as well.
```console
export REGISTRY=<dockerhub-alias>
export IMAGE_VERSION=latest-v2
export BUILD_V2=true
# checkout the V2 development branch
git checkout main_v2
# build linux, windows images
make container-all
# create a manifest list for the images above
make push-manifest
# build scheduler extender image
make azdiskschedulerextender-all
# create a manifest list for the scheduler extender images
make push-manifest-azdiskschedulerextender
```

 - Install your private build using Helm.
```console
curl https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 | bash
helm install azuredisk-csi-driver charts/latest/azuredisk-csi-driver \
  --namespace kube-system \
  --set image.azuredisk.repository=$REGISTRY/azuredisk-csi-driver \
  --set image.azuredisk.tag=$IMAGE_VERSION \
  --set image.azuredisk.pullPolicy=Always
```

 - Install your private build of the V2 driver using Helm.
```console
curl https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 | bash
helm install azuredisk-csi-driver charts/latest-v2/azuredisk-csi-driver \
  --namespace kube-system \
  --set image.azuredisk.repository=$REGISTRY/azuredisk-csi-driver \
  --set image.azuredisk.tag=$IMAGE_VERSION \
  --set image.azuredisk.pullPolicy=Always \
  --set image.schedulerExtender.repository=$REGISTRY/azuredisk-csi-driver \
  --set image.schedulerExtender.tag=$IMAGE_VERSION \
  --set image.schedulerExtender.pullPolicy=Always
```

### How to update Azure cloud provider library
 - get latest version of [github.com/kubernetes/legacy-cloud-providers](https://github.com/kubernetes/legacy-cloud-providers/tree/master/azure)
> in following example, `20200619215319-3e8d72e51d7d` is the git version
```console
# git clone https://github.com/kubernetes/legacy-cloud-providers.git
# cd ~/go/src/github.com/kubernetes/legacy-cloud-providers
# TZ=UTC
# git --no-pager show \
  --quiet \
  --abbrev=12 \
  --date='format-local:%Y%m%d%H%M%S' \
  --format="%cd-%h"
20200619215319-3e8d72e51d7d
```

 - update `go.mod`
```console
export GO111MODULE=on
#edit go.mod. add the necessary vendors in `replace`
go mod tidy
go mod vendor
```

### How to update chart index

```console
helm repo index charts --url=https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/charts
```
