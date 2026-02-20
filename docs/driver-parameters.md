# Driver Parameters
> parameter names are case-insensitive

<details><summary>required permissions for CSI driver controller</summary>
<pre>
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
# Those are needed only when the virtualMachine or virtualMachineScaleSets have these additional resources configured:
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
</pre>
</details>

## Dynamic Provisioning

> get an [example](../deploy/example/storageclass-azuredisk-csi.yaml)

Name | Meaning | Available Value | Mandatory | Default value
--- | --- | --- | --- | ---
skuName | azure disk storage account type (alias: `storageAccountType`)| `Standard_LRS`, `Premium_LRS`, `StandardSSD_LRS`, `UltraSSD_LRS`, `Premium_ZRS`, `StandardSSD_ZRS`, `PremiumV2_LRS`<br>(Note: [PremiumV2_LRS](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-deploy-premium-v2) and [UltraSSD_LRS](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-enable-ultra-ssd) only support `None` caching mode) | No | `StandardSSD_LRS`
kind | managed or unmanaged(blob based) disk | `managed` (`dedicated`, `shared` are deprecated) | No | `managed`
fsType | File System Type | `ext4`, `ext3`, `ext2`, `xfs`, `btrfs` on Linux, `ntfs` on Windows | No | `ext4` on Linux, `ntfs` on Windows
cachingMode | [Azure Data Disk Host Cache Setting](https://docs.microsoft.com/en-us/azure/virtual-machines/windows/premium-storage-performance#disk-caching) | `None`, `ReadOnly`, `ReadWrite`<br>(`ReadWrite` caching mode is deprecated, [PremiumV2_LRS](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-deploy-premium-v2) and [UltraSSD_LRS](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-enable-ultra-ssd) only support `None` caching mode) | No | `ReadOnly`
location | specify Azure region in which Azure disk will be created, region name should only have lower-case letter or digit number. | `eastus2`, `westus`, etc. | No | if empty, driver will use the same region name as current k8s cluster
resourceGroup | specify the resource group in which azure disk will be created | existing resource group name | No | if empty, driver will use the same resource group name as current k8s cluster
DiskIOPSReadWrite | [UltraSSD](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-types#ultra-disks), [PremiumV2_LRS](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-types#premium-ssd-v2-preview) disk IOPS capability |  | No | `500` for UltraSSD
DiskMBpsReadWrite | [UltraSSD](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-types#ultra-disks), [PremiumV2_LRS](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-types#premium-ssd-v2-preview) disk throughput capability |  | No | `100` for UltraSSD
LogicalSectorSize | Logical sector size in bytes for Ultra disk. Supported values are 512 ad 4096. 4096 is the default. | `512`, `4096` | No | `4096`
tags | azure disk [tags](https://docs.microsoft.com/en-us/azure/azure-resource-manager/management/tag-resources) | tag format: `key1=val1,key2=val2` | No | ""
diskEncryptionSetID | ResourceId of the disk encryption set to use for [enabling encryption at rest](https://docs.microsoft.com/en-us/azure/virtual-machines/windows/disk-encryption) | format: `/subscriptions/{subs-id}/resourceGroups/{rg-name}/providers/Microsoft.Compute/diskEncryptionSets/{diskEncryptionSet-name}` | No | ""
diskEncryptionType | encryption type of the disk encryption set | `EncryptionAtRestWithCustomerKey`(by default), `EncryptionAtRestWithPlatformAndCustomerKeys` | No | ""
writeAcceleratorEnabled | [Write Accelerator on Azure Disks](https://docs.microsoft.com/azure/virtual-machines/windows/how-to-enable-write-accelerator) | `true`, `false` | No | ""
perfProfile | [Block device performance tuning using perfProfiles](./perf-profiles.md) | `none`, `basic`, `advanced` | No | `none`
networkAccessPolicy | NetworkAccessPolicy property to prevent anybody from generating the SAS URI for a disk or a snapshot | `AllowAll`, `DenyAll`, `AllowPrivate` | No | `AllowAll`
publicNetworkAccess | Enabling or disabling public access to the underlying data of a disk on the internet, even when the NetworkAccessPolicy is set to `AllowAll` | `Enabled`, `Disabled` | No | `Enabled`
diskAccessID | ARM id of the [DiskAccess](https://aka.ms/disksprivatelinksdoc) resource for using private endpoints on disks | | No  | ``
enableBursting | [enable on-demand bursting](https://docs.microsoft.com/en-us/azure/virtual-machines/disk-bursting) beyond the provisioned performance target of the disk. On-demand bursting only be applied to Premium disk, disk size > 512GB, Ultra & shared disk is not supported. Bursting is disabled by default. | `true`, `false` | No | `false`
enablePerformancePlus | [enabling performance plus](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-enable-performance), this setting only applies to Premium SSD, Standard SSD and HDD with disk size > 512GB. | `true`, `false` | No | `false`
attachDiskInitialDelay | setting a large number for the initial delay in milliseconds for batch disk attach/detach could reduce the number of operations and ARM throttling |  | No | `1000`
useragent | User agent used for [customer usage attribution](https://docs.microsoft.com/en-us/azure/marketplace/azure-partner-customer-usage-attribution)| | No  | Generated Useragent formatted `driverName/driverVersion compiler/version (OS-ARCH)`
subscriptionID | specify Azure subscription ID in which Azure disk will be created  | Azure subscription ID | No | if not empty, `resourceGroup` must be provided

- disk created by dynamic provisioning
  - disk name format (example): `pvc-e132d37f-9e8f-434a-b599-15a4ab211b39`
  - tags format (example):

    ```yaml
    k8s-azure-created-by: kubernetes-azure-dd
    kubernetes.io-created-for-pv-name: pvc-e132d37f-9e8f-434a-b599-15a4ab211b39
    kubernetes.io-created-for-pvc-name: pvc-azuredisk
    kubernetes.io-created-for-pvc-namespace: default
    ```

## Static Provisioning (bring your own Azure Disk)

> get an [example](../deploy/example/pv-azuredisk-csi.yaml)

Name | Meaning | Available Value | Mandatory | Default value
--- | --- | --- | --- | ---
volumeHandle| Azure disk URI | /subscriptions/{sub-id}/resourcegroups/{group-name}/providers/microsoft.compute/disks/{disk-id} | Yes | N/A
volumeAttributes.fsType | File System Type | `ext4`, `ext3`, `ext2`, `xfs`, `btrfs` on Linux, `ntfs` on Windows | No | `ext4` on Linux, `ntfs` on Windows
volumeAttributes.partition | partition num of the existing disk (only supported on Linux) | `1`, `2`, `3` | No | empty(no partition) </br>- make sure partition format is like `-part1`
volumeAttributes.cachingMode | [disk host cache setting](https://docs.microsoft.com/en-us/azure/virtual-machines/windows/premium-storage-performance#disk-caching)| `None`, `ReadOnly`, `ReadWrite` | No  | `ReadOnly`
volumeAttributes.attachDiskInitialDelay | setting a large number for the initial delay in milliseconds for batch disk attach/detach could reduce the number of operations and ARM throttling |  | No | `1000`

## `VolumeSnapshotClass`

Name | Meaning | Available Value | Mandatory | Default value
--- | --- | --- | --- | ---
resourceGroup | resource group where the snapshots of the disks will be stored | EXISTING RESOURCE GROUP | No | If not specified, snapshot will be stored in the same resource group as source Azure disk
incremental | take [full or incremental snapshot](https://docs.microsoft.com/en-us/azure/virtual-machines/windows/incremental-snapshots) | `true`, `false` | No | `true`
dataAccessAuthMode | [enable data access authentication mode when creating a snapshot](https://learn.microsoft.com/en-us/rest/api/compute/disks/create-or-update?tabs=HTTP#dataaccessauthmode) | `None`, `AzureActiveDirectory` | No | `None`
tags | azure disk [tags](https://docs.microsoft.com/en-us/azure/azure-resource-manager/management/tag-resources) | tag format: 'key1=val1,key2=val2' | No | ""
userAgent | User agent used for [customer usage attribution](https://docs.microsoft.com/en-us/azure/marketplace/azure-partner-customer-usage-attribution) | | No  | Generated Useragent formatted `driverName/driverVersion compiler/version (OS-ARCH)`
subscriptionID | specify Azure subscription ID in which Azure disk will be created  | Azure subscription ID | No | if not empty, `resourceGroup` must be provided, `incremental` must set as `false`
location | specify Azure region in which Azure disk snapshot will be created, region name should only have lower-case letter or digit number. | `eastus2`, `westus`, etc. | No | if empty, driver will use the same region name as current k8s cluster
instantAccessDurationMinutes | enable [instant access snapshots](https://learn.microsoft.com/en-us/azure/virtual-machines/disks-instant-access-snapshots) for PremiumV2 or UltraSSD disks | `60` to `300` | No |

### Tips
  - If there are CVEs in the `livenessprobe` and `csi-node-driver-registrar` sidecar images, you can run `kubectl edit ds -n kube-system csi-azuredisk-node` to change the `imagePullPolicy` to `Always` for both sidecar containers. This will cause the CSI driver to restart and pull the latest patched images, thereby resolving the CVEs in these sidecar components.
