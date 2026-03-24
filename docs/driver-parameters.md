# Driver Parameters

> Parameter names are case-insensitive.

## Table of Contents

- [Required Permissions](#required-permissions)
- [Dynamic Provisioning](#dynamic-provisioning)
- [Static Provisioning](#static-provisioning-bring-your-own-azure-disk)
- [VolumeSnapshotClass](#volumesnapshotclass)
- [Tips](#tips)

## Required Permissions

<details><summary>Click to expand required permissions for CSI driver controller</summary>

**Always required:**

```
Microsoft.Compute/disks/read
Microsoft.Compute/disks/write
Microsoft.Compute/disks/delete
Microsoft.Compute/diskEncryptionSets/read
Microsoft.Compute/snapshots/read
Microsoft.Compute/snapshots/write
Microsoft.Compute/snapshots/delete
Microsoft.Compute/virtualMachines/write
Microsoft.Compute/virtualMachines/read
Microsoft.Compute/virtualMachineScaleSets/virtualMachines/write
Microsoft.Compute/virtualMachineScaleSets/virtualMachines/read
Microsoft.Compute/virtualMachineScaleSets/read
Microsoft.Compute/locations/operations/read
Microsoft.Compute/locations/DiskOperations/read
Microsoft.Resources/subscriptions/resourceGroups/read
Microsoft.Resources/subscriptions/resourceGroups/*/read
```

**Conditionally required** (only when the VM or VMSS have additional resources configured):

```
Microsoft.Compute/disks/beginGetAccess/action
Microsoft.KeyVault/vaults/deploy/action
Microsoft.ManagedIdentity/userAssignedIdentities/assign/action
Microsoft.Network/applicationGateways/backendAddressPools/join/action
Microsoft.Network/applicationSecurityGroups/joinIpConfiguration/action
Microsoft.Network/loadBalancers/backendAddressPools/join/action
Microsoft.Network/loadBalancers/inboundNatPools/join/action
Microsoft.Network/loadBalancers/probes/join/action
Microsoft.Network/networkInterfaces/join/action
Microsoft.Network/networkSecurityGroups/join/action
Microsoft.Network/publicIPPrefixes/join/action
Microsoft.Network/virtualNetworks/subnets/join/action
```

</details>

## Dynamic Provisioning

> Get an [example StorageClass](../deploy/example/storageclass-azuredisk-csi.yaml).

### Disk Type & Caching

| Name | Meaning | Available Value | Mandatory | Default value |
|---|---|---|---|---|
| skuName | Azure disk storage account type (alias: `storageAccountType`) | `Standard_LRS`, `Premium_LRS`, `StandardSSD_LRS`, `UltraSSD_LRS`, `Premium_ZRS`, `StandardSSD_ZRS`, `PremiumV2_LRS` | No | `StandardSSD_LRS` |
| kind | Managed or unmanaged (blob-based) disk | `managed` (`dedicated`, `shared` are deprecated) | No | `managed` |
| cachingMode | [Azure Data Disk Host Cache Setting](https://learn.microsoft.com/en-us/azure/virtual-machines/premium-storage-performance#disk-caching) | `None`, `ReadOnly`, `ReadWrite` | No | `ReadOnly` |
| fsType | File system type | `ext4`, `ext3`, `ext2`, `xfs`, `btrfs` on Linux, `ntfs` on Windows | No | `ext4` on Linux, `ntfs` on Windows |

> **Note:**
> - [PremiumV2_LRS](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-deploy-premium-v2) and [UltraSSD_LRS](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-enable-ultra-ssd) only support `None` caching mode.
> - `ReadWrite` caching mode is deprecated.

### Performance

| Name | Meaning | Available Value | Mandatory | Default value |
|---|---|---|---|---|
| DiskIOPSReadWrite | [UltraSSD](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-types#ultra-disks) / [PremiumV2_LRS](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-types#premium-ssd-v2) disk IOPS capability | Integer | No | `500` for UltraSSD |
| DiskMBpsReadWrite | [UltraSSD](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-types#ultra-disks) / [PremiumV2_LRS](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-types#premium-ssd-v2) disk throughput capability (MBps) | Integer | No | `100` for UltraSSD |
| LogicalSectorSize | Logical sector size in bytes for Ultra disk. Supported values are 512 and 4096. | `512`, `4096` | No | `4096` |
| enableBursting | [Enable on-demand bursting](https://learn.microsoft.com/en-us/azure/virtual-machines/disk-bursting) beyond the provisioned performance target of the disk. Only applies to Premium disks with size > 512 GiB. Ultra and shared disks are not supported. | `true`, `false` | No | `false` |
| enablePerformancePlus | [Enable performance plus](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-enable-performance). Only applies to Premium SSD, Standard SSD, and HDD with disk size > 512 GiB. | `true`, `false` | No | `false` |
| perfProfile | [Block device performance tuning using perfProfiles](./perf-profiles.md) | `none`, `basic`, `advanced` | No | `none` |
| writeAcceleratorEnabled | [Write Accelerator on Azure Disks](https://learn.microsoft.com/en-us/azure/virtual-machines/how-to-enable-write-accelerator) | `true`, `false` | No | `false` |

### Location & Resource

| Name | Meaning | Available Value | Mandatory | Default value |
|---|---|---|---|---|
| location | Azure region in which the disk will be created. Region name should only contain lowercase letters or digits. | `eastus2`, `westus`, etc. | No | Same region as the k8s cluster |
| resourceGroup | Resource group in which the disk will be created | Existing resource group name | No | Same resource group as the k8s cluster |
| subscriptionID | Azure subscription ID in which the disk will be created | Azure subscription ID | No | If not empty, `resourceGroup` must be provided |
| tags | Azure disk [tags](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/tag-resources) | Tag format: `key1=val1,key2=val2` | No | `""` |

### Encryption & Network Security

| Name | Meaning | Available Value | Mandatory | Default value |
|---|---|---|---|---|
| diskEncryptionSetID | Resource ID of the disk encryption set for [enabling encryption at rest](https://learn.microsoft.com/en-us/azure/virtual-machines/disk-encryption) | `/subscriptions/{subs-id}/resourceGroups/{rg-name}/providers/Microsoft.Compute/diskEncryptionSets/{diskEncryptionSet-name}` | No | `""` |
| diskEncryptionType | Encryption type of the disk encryption set | `EncryptionAtRestWithCustomerKey`, `EncryptionAtRestWithPlatformAndCustomerKeys` | No | `EncryptionAtRestWithCustomerKey` |
| networkAccessPolicy | Prevent generating the SAS URI for a disk or a snapshot | `AllowAll`, `DenyAll`, `AllowPrivate` | No | `AllowAll` |
| publicNetworkAccess | Enable or disable public access to the underlying data of a disk, even when NetworkAccessPolicy is set to `AllowAll` | `Enabled`, `Disabled` | No | `Enabled` |
| diskAccessID | ARM ID of the [DiskAccess](https://aka.ms/disksprivatelinksdoc) resource for using private endpoints on disks | Resource ID string | No | `""` |

### Advanced

| Name | Meaning | Available Value | Mandatory | Default value |
|---|---|---|---|---|
| attachDiskInitialDelay | Initial delay in milliseconds for batch disk attach/detach. A larger value can reduce ARM throttling. | Integer (ms) | No | `1000` |
| useragent | User agent used for [customer usage attribution](https://learn.microsoft.com/en-us/azure/marketplace/azure-partner-customer-usage-attribution) | String | No | Generated as `driverName/driverVersion compiler/version (OS-ARCH)` |

### Dynamically Provisioned Disk Info

- **Disk name format** (example): `pvc-e132d37f-9e8f-434a-b599-15a4ab211b39`
- **Default tags** (example):

  ```yaml
  k8s-azure-created-by: kubernetes-azure-dd
  kubernetes.io-created-for-pv-name: pvc-e132d37f-9e8f-434a-b599-15a4ab211b39
  kubernetes.io-created-for-pvc-name: pvc-azuredisk
  kubernetes.io-created-for-pvc-namespace: default
  ```

## Static Provisioning (bring your own Azure Disk)

> Get an [example PV](../deploy/example/pv-azuredisk-csi.yaml).

| Name | Meaning | Available Value | Mandatory | Default value |
|---|---|---|---|---|
| volumeHandle | Azure disk URI | `/subscriptions/{sub-id}/resourcegroups/{group-name}/providers/microsoft.compute/disks/{disk-id}` | Yes | N/A |
| volumeAttributes.fsType | File system type | `ext4`, `ext3`, `ext2`, `xfs`, `btrfs` on Linux, `ntfs` on Windows | No | `ext4` on Linux, `ntfs` on Windows |
| volumeAttributes.partition | Partition number of the existing disk (only supported on Linux) | `1`, `2`, `3` | No | empty (no partition) — make sure partition format is like `-part1` |
| volumeAttributes.cachingMode | [Disk host cache setting](https://learn.microsoft.com/en-us/azure/virtual-machines/premium-storage-performance#disk-caching) | `None`, `ReadOnly`, `ReadWrite` | No | `ReadOnly` |
| volumeAttributes.attachDiskInitialDelay | Initial delay in milliseconds for batch disk attach/detach. A larger value can reduce ARM throttling. | Integer (ms) | No | `1000` |

## `VolumeSnapshotClass`

| Name | Meaning | Available Value | Mandatory | Default value |
|---|---|---|---|---|
| resourceGroup | Resource group where the snapshots will be stored | Existing resource group name | No | Same resource group as source Azure disk |
| incremental | Take [full or incremental snapshot](https://learn.microsoft.com/en-us/azure/virtual-machines/incremental-snapshots) | `true`, `false` | No | `true` |
| dataAccessAuthMode | [Data access authentication mode](https://learn.microsoft.com/en-us/rest/api/compute/disks/create-or-update?tabs=HTTP#dataaccessauthmode) when creating a snapshot | `None`, `AzureActiveDirectory` | No | `None` |
| tags | Azure disk [tags](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/tag-resources) | Tag format: `key1=val1,key2=val2` | No | `""` |
| userAgent | User agent used for [customer usage attribution](https://learn.microsoft.com/en-us/azure/marketplace/azure-partner-customer-usage-attribution) | String | No | Generated as `driverName/driverVersion compiler/version (OS-ARCH)` |
| subscriptionID | Azure subscription ID for the snapshot | Azure subscription ID | No | If not empty, `resourceGroup` must be provided and `incremental` must be set to `false` |
| location | Azure region in which the snapshot will be created. Region name should only contain lowercase letters or digits. | `eastus2`, `westus`, etc. | No | Same region as the k8s cluster |
| instantAccessDurationMinutes | Enable [instant access snapshots](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-instant-access-snapshots) for PremiumV2 or UltraSSD disks | `60` to `300` | No | |

## Tips

- If there are CVEs in the `livenessprobe` and `csi-node-driver-registrar` sidecar images, you can run the following to change the `imagePullPolicy` to `Always` for both sidecar containers. This will cause the CSI driver to restart and pull the latest patched images:

  ```sh
  kubectl edit ds -n kube-system csi-azuredisk-node
  ```
