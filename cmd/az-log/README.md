# az-log

## About
az-log is a command line tool to fetch and parse appropriate logs from the driver plugins to track the operation workflow end to end and bring better insight of driver behaviors.

## Installation
```console
$ cd $GOPATH/src/sigs.k8s.io/azuredisk-csi-driver
$ make install-az-log
```

## Configuration
Check if any configuration settings need to be updated.
```console
$ cat $GOPATH/src/sigs.k8s.io/azuredisk-csi-driver/cmd/az-log/config/az-log.yaml
```

## Features

### Source Options
|Command|Description|
|---|---|
|az-log get controller |Fetch and output logs from the leader controller plugin.|
|az-log get node \<node-name\> |Fetch and output logs from the node plugin on the given node.|
|az-log get file \<file-name\> |Fetch and output logs from a *.log file.|
|az-log get pod \<pod-name\>/\<container-name\> |Fetch and output logs from a specific container and plugin running for the driver.|

### Retrieval Options
|Flag|Description|
|---|---|
|--follow |Specify if logs should be streamed. Can't be used with `get file`.|
|--previous |Print logs for previous container in a pod if it exists as well as for current container. Can't be used with `get file`.|

### Query Options
|Flag|Description|
|---|---|
|--volume \<volume-names\> |Filter out logs linked to the given volumes. Multiple arguments should be separated by comma.|
|--node \<node-names\> |Filter out logs linked to the given nodes. Multiple arguments should be separated by comma.|
|--request-id \<request-ids\> |Filter out logs containing the given request-ids. Multiple arguments should be separated by comma.|
|--since |Only return logs newer than a relative duration like 5s, 2m, or 3h. Only one of since-time / since may be used.|
|--since-time |Only return logs after a specific date (RFC3339 or Klog's format). Only one of since-time / since may be used.|
