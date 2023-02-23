## Install open source CSI driver on AKS cluster

> When CSI driver is enabled, built-in in-tree driver(`kubernetes.io/azure-disk`) should not be used any more since there is potential race condition when both in-tree and CSI drivers are working.

 - Prerequisites

AKS cluster is created with user assigned identity(with naming rule [`AKS Cluster Name-agentpool`](https://docs.microsoft.com/en-us/azure/aks/use-managed-identity#summary-of-managed-identities)) on agent node pool by default, make sure that identity has `Contributor` role on node resource group, follow below instruction to set up `Contributor` role on node resource group
![image](https://user-images.githubusercontent.com/4178417/120978367-f68f0a00-c7a6-11eb-8e87-89247d1ddc0b.png)

<details><summary>least privilege for CSI driver controller</summary>
<pre>
"Microsoft.Resources/subscriptions/resourceGroups/read",
"Microsoft.Compute/disks/*",
"Microsoft.Compute/snapshots/*",
"Microsoft.Compute/virtualMachines/*/read",
"Microsoft.Compute/virtualMachineScaleSets/virtualMachines/write",
"Microsoft.Compute/virtualMachineScaleSets/*/read",
"Microsoft.Compute/virtualMachineScaleSets/read" 
</pre>
</details>

 - Install CSI driver

install latest **released** CSI driver version, following guide [here](./install-azuredisk-csi-driver.md)

 - Set up new storage classes
```console
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
```
 > follow guide [here](https://github.com/Azure/AKS/issues/118#issuecomment-708257760) to replace built-in storage classes on AKS
