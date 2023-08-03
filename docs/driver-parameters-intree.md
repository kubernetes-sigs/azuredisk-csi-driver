## Driver Parameters

 - deprecated in-tree [kubernetes.io/azure-disk](https://kubernetes.io/docs/concepts/storage/volumes/#azuredisk) driver parameters

### Dynamic Provisioning

Name | Meaning | Available Value | Mandatory | Default value
--- | --- | --- | --- | ---
skuName | azure disk storage account type (alias: `storageAccountType`)| `Standard_LRS`, `Premium_LRS`, `StandardSSD_LRS`, `UltraSSD_LRS` | No | `StandardSSD_LRS`
kind | managed or unmanaged(blob based) disk | `managed` (`dedicated`, `shared` are deprecated) | No | `managed`
fsType | File System Type | `ext4`, `ext3`, `ext2`, `xfs`on Linux, `ntfs` on Windows | No | `ext4` on Linux, `ntfs` on Windows
cachingMode | [Azure Data Disk Host Cache Setting](https://docs.microsoft.com/en-us/azure/virtual-machines/windows/premium-storage-performance#disk-caching) | `None`, `ReadOnly`, `ReadWrite` | No | `ReadOnly`
storageAccount | specify the storage account name in which azure disk will be created | STORAGE_ACCOUNT_NAME | No | if empty, driver will find a suitable storage account that matches `skuName` in the same resource group as current k8s cluster
location | specify the Azure location in which azure disk will be created | `eastus`, `westus`, etc. | No | if empty, driver will use the same location name as current k8s cluster
resourceGroup | specify the resource group in which azure disk will be created | existing resource group name | No | if empty, driver will use the same resource group name as current k8s cluster
DiskIOPSReadWrite | [UltraSSD disk](https://docs.microsoft.com/en-us/azure/virtual-machines/linux/disks-ultra-ssd) IOPS Capability (minimum: 2 IOPS/GiB ) | 100~160000 | No | `500`
DiskMBpsReadWrite | [UltraSSD disk](https://docs.microsoft.com/en-us/azure/virtual-machines/linux/disks-ultra-ssd) Throughput Capability(minimum: 0.032/GiB) | 1~2000 | No | `100`
tags | Azure disk [tags](https://docs.microsoft.com/en-us/azure/azure-resource-manager/management/tag-resources), available from k8s v1.19.0 | tag format: `key1=val1,key2=val2` | No | ""
diskEncryptionSetID | ResourceId of the disk encryption set to use for [enabling encryption at rest](https://docs.microsoft.com/en-us/azure/virtual-machines/windows/disk-encryption) | format: `/subscriptions/{subs-id}/resourceGroups/{rg-name}/providers/Microsoft.Compute/diskEncryptionSets/{diskEncryptionSet-name}` | No | ""
writeAcceleratorEnabled | [Write Accelerator on Azure Disks](https://docs.microsoft.com/azure/virtual-machines/windows/how-to-enable-write-accelerator) | `true`, `false` | No | ""

 - disk created by dynamic provisioning
   - disk name format(example): `kubernetes-dynamic-pvc-e132d37f-9e8f-434a-b599-15a4ab211b39`
   - tags format(example):
```
created-by: kubernetes-azure-dd
kubernetes.io-created-for-pv-name: pvc-e132d37f-9e8f-434a-b599-15a4ab211b39
kubernetes.io-created-for-pvc-name: pvc-azuredisk
kubernetes.io-created-for-pvc-namespace: default
```

### Static Provisioning(bring your own disk)

Name | Meaning | Available Value | Mandatory | Default value
--- | --- | --- | --- | ---
diskName | disk name | | Yes |
diskName | disk resource ID | /subscriptions/{sub-id}/resourcegroups/{group-name}/providers/microsoft.compute/disks/{disk-id} | Yes |
fsType | File System Type | `ext4`, `ext3`, `ext2`, `xfs`on Linux, `ntfs` on Windows | No | `ext4` on Linux, `ntfs` on Windows
cachingMode | [Azure Data Disk Host Cache Setting](https://docs.microsoft.com/en-us/azure/virtual-machines/windows/premium-storage-performance#disk-caching) | `None`, `ReadOnly`, `ReadWrite` | No | `ReadOnly`
readOnly | file system read only or not  | `true`, `false` | No | `false`
