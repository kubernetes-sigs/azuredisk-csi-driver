# az-analyze

## About
az-analyze is a command line tool that provides a strightforward way to get joint information between Kubernetes resources and custom resources that are used by the driver to perform operations. For example, get the AzVolumes used by a pod or get the AzVolumeAttachments present on a node.

## Installation
```console
$ cd $GOPATH/src/sigs.k8s.io/azuredisk-csi-driver
$ make install-az-analyze
```
## Configuration
Check if any configuration settings need to be updated.
```console
$ cat $GOPATH/src/sigs.k8s.io/azuredisk-csi-driver/cmd/az-analyze/config/az-analyze.yaml
```

## Features
|Command|Description|
|---|---|
|az-analyze get azv|Show all azVolumes with a pod column.
|--pod \<pod-name\>|Show all azVolumes used by a given pod|
|--namespace \<pod-namespace\>|Specify the namespace of the pod. If it's not specified, default namesapce is used.|

|Command|Description|
|---|---|
|az-analyze get azva|Show all azVolumesAttachments with pod, node, and zone as columns|
|--pod \<pod-name\>|Show all azVolumesAttachments that are used by a given pod.|
|--namespace \<pod-namespace\>|Specify the namespace of the pod. If it's not specified, default namesapce is used.|
|--node \<node-name\>|Show all azVolumesAttachments that are present in a given node.|
|--zone \<zone-name\>|Show all azVolumesAttachments that are present in a given zone.|

