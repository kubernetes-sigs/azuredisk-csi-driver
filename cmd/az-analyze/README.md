# az-analyze

## About
az-analyze is a command line tool that provides a strightforward way to get joint information between Kubernetes resources and custom resources that are used by the driver to perform operations. For example, get the AzVolumes used by a pod or get the AzVolumeAttachments present on a node.

## Installation
```console
$ cd $GOPATH/src/sigs.k8s.io/azuredisk-csi-driver
$ make install-az-analyze
```
## Configuration
Create an az-analyze config file in one of the three paths below:
```console
$ cat > /etc/az-analyze.yaml
```
or
```console
$ cat > $HOME/.config/az-analyze.yaml
```
or
```console
$ cat > az-analyze.yaml
```
Write configuration settings and press Ctrl+D to exit the file.
```console
kubeconfig: "" # default is "$HOME/.kube/config"
driverNamespace: "" # default is "azure-disk-csi"
```

## Features
|Command|Description|
|---|---|
|az-analyze get azv|Show all azVolumes with a pod column.|
|az-analyze get azva|Show all azVolumesAttachments with pod, node, and zone as columns|

|Option|Description|
|---|---|
|--pod \<pod-name\>|Show all azVolumes/azVolumesAttachments that are used by a given pod.|
|--node \<node-name\>|Show all azVolumesAttachments that are present in a given node.|
|--zone \<zone-name\>|Show all azVolumesAttachments that are present in a given zone.|
|--namespace \<pod-namespace\>|Specify the namespace of the pod. If it's not specified, default namesapce is used.|
