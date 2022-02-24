# CSI driver on Windows

## Feature Status: Beta
Refer to [Windows-CSI-Support](https://github.com/kubernetes/enhancements/tree/master/keps/sig-windows/1122-windows-csi-support) for more details.

## Prerequisite
- [Install CSI-Proxy on Windows Node](https://github.com/Azure/aks-engine/blob/master/docs/topics/csi-proxy-windows.md)

## Deploy a Windows pod with PVC mount
### Create Storage Class
```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
```

### Create Windows pod
```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/windows/statefulset.yaml
```

#### enter pod container to do validation
```console
# kubectl exec -it busybox-azuredisk-0 cmd
Microsoft Windows [Version 10.0.17763.1098]
(c) 2018 Microsoft Corporation. All rights reserved.

C:\>cd c:\mnt\azuredisk

c:\mnt\azuredisk>dir
 Volume in drive C has no label.
 Volume Serial Number is C820-6BEE

 Directory of c:\mnt\azuredisk

05/31/2020  12:41 PM               528 data.txt
               1 File(s)            528 bytes
               0 Dir(s)  107,268,366,336 bytes free

c:\mnt\azuredisk>cat data.txt
2020-05-31 12:40:59Z
2020-05-31 12:41:00Z
2020-05-31 12:41:01Z
2020-05-31 12:41:02Z
```
In the above example, there is a `c:\mnt\azuredisk` directory mounted as `NTFS` filesystem.
