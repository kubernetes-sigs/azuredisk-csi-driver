# Windows Example

## Prerequisite

- Install the CSI-Proxy on the Windows Nodes. Here is a quickstart reference. https://github.com/Azure/aks-engine/blob/master/docs/topics/csi-proxy-windows.md

Windows support is an alpha feature since Kubernetes v1.18, refer to [Windows-CSI-Support](https://github.com/kubernetes/enhancements/blob/master/keps/sig-windows/20190714-windows-csi-support.md) for more details.

## Install AzureDisk CSI Driver

Follow the [instructions](https://github.com/kubernetes-sigs/azuredisk-csi-driver/blob/master/docs/install-csi-driver-master.md#windows) to install the windows version of azuredisk csi driver.

## Deploy a Windows App

### Create the StorageClass and PVC

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/pvc-azuredisk-csi.yaml
```

### Create the AspNet App

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/windows/aspnetapp.yaml
```

>Note that:
>
>Specify the nodeSelector to windows.

### Check the Mounted Disk

```console
kubectl exec -it sample-win-xxx -- powershell
```

After entering the container, use the disk operations to check  `/mnt/azuredisk`
