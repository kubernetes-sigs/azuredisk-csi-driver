# CSI on Windows example

## Feature Status: Alpha

CSI on Windows support is an alpha feature since Kubernetes v1.18, refer to [Windows-CSI-Support](https://github.com/kubernetes/enhancements/blob/master/keps/sig-windows/20190714-windows-csi-support.md) for more details.

## Prerequisite
- Install CSI-Proxy on Windows Node

[csi-proxy installation](https://github.com/Azure/aks-engine/blob/master/docs/topics/csi-proxy-windows.md) is supported with [aks-engine v0.48.0](https://github.com/Azure/aks-engine/releases/tag/v0.48.0).

## Install CSI Driver

Follow the [instructions](https://github.com/kubernetes-sigs/azuredisk-csi-driver/blob/master/docs/install-csi-driver-master.md) to install windows driver.

## Deploy a Windows pod with PVC mount

### Create StorageClass

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
```

### Create Windows pod

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/windows/statefulset.yaml
```

### enter pod container to do validation

```console
$ kubectl exec -it aspnet-azuredisk-0 -- cmd
Microsoft Windows [Version 10.0.17763.1098]
(c) 2018 Microsoft Corporation. All rights reserved.

C:\inetpub\wwwroot>cd c:\mnt\azuredisk

c:\mnt\azuredisk>echo hello > 20200325

c:\mnt\azuredisk>dir
 Volume in drive C has no label.
 Volume Serial Number is DE36-B78A

 Directory of c:\mnt\azuredisk

03/25/2020  06:33 AM                 8 20200325
               1 File(s)              8 bytes
               0 Dir(s)  107,268,366,336 bytes free
```
In the above example, there is a `c:\mnt\azuredisk` directory mounted as NTFS filesystem.
