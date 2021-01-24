# Azure Disk CSI Driver Limitations

## Limitations on Azure Stack
 - Azure Disk IOPS is capped at 2300, please read this [documentation](https://docs.microsoft.com/en-us/azure-stack/user/azure-stack-vm-sizes?view=azs-2008) for details.
 - Azure Stack does not support shared disk, so parameter `maxShares` larger than 1 is not valid in a `StorageClass`.
 - Azure Stack only supports Standard Locally-redundant (Standard_LRS) and Premium Locally-redundant (Premium_LRS) Storage Account types, so only `Standard_LRS` and `Premium_LRS` are valid for parameter `skuName` in a `StorageClass`.
 - Azure Stack does not support incremental disk Snapshot, so only `false` is valid for parameter `incremental` in a `VolumeSnapshotClass`.
 - For Windows agent nodes, you will need to install Windows CSI Proxy, please refer to [Windows CSI Proxy](https://github.com/kubernetes-csi/csi-proxy). To enable the proxy via AKS Engine API model, please refer to [CSI Proxy for Windows](https://github.com/Azure/aks-engine/blob/master/docs/topics/csi-proxy-windows.md).