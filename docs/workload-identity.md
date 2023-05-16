# workload identity support
> Note:
>  - supported version: v1.28.0
>  - workload identity is supported on OpenShift, capz and other self-managed clusters
>  - workload identity is NOT supported on AKS **managed** Azure Disk CSI driver since the driver controller is managed by AKS control plane which is already using [managed identity](https://learn.microsoft.com/en-us/azure/aks/use-managed-identity) by default, it's not necessary to use workload identity for AKS managed Azure Disk CSI driver.

## Prerequisites

Before proceeding with the following steps, please ensure that you have completed the [Workload Identity installation guide](https://azure.github.io/azure-workload-identity/docs/installation.html). After completing the installation, you should have already installed the mutating admission webhook and obtained the OIDC issuer URL for your cluster.

## 1. Set environment variables

```shell
export CLUSTER_NAME="<your cluster name>"
export CLUSTER_RESOURCE_GROUP="<cluster resource group name>"
export LOCATION="<location>"
export OIDC_ISSUER="<your cluster’s OIDC issuer URL>"

# [OPTIONAL] resource group where Azuredisk storage account reside
export AZURE_DISK_RESOURCE_GROUP="<resource group where Azuredisk storage account reside>"

# environment variables for the AAD application
# [OPTIONAL] Only set this if you're using a Azure AD Application as part of this tutorial
export APPLICATION_NAME="<your application name>"

# environment variables for the user-assigned managed identity
# [OPTIONAL] Only set this if you're using a user-assigned managed identity as part of this tutorial
export USER_ASSIGNED_IDENTITY_NAME="<your user-assigned managed identity name>"
export IDENTITY_RESOURCE_GROUP="<resource group where your user-assigned managed identity reside>"

# Azuredisk CSI Driver Service Account and namespace
export SA_LIST=( "csi-azuredisk-controller-sa" "csi-azuredisk-node-sa" )
export NAMESPACE="kube-system"
```

## 2. Create an AAD application or user-assigned managed identity and grant required permissions

```shell
# create an AAD application if you are using Azure AD Application
az ad sp create-for-rbac --name "${APPLICATION_NAME}"
```

```shell
# create a user-assigned managed identity if you are using user-assigned managed identity
az group create -n ${IDENTITY_RESOURCE_GROUP} -l $LOCATION
az identity create --name "${USER_ASSIGNED_IDENTITY_NAME}" --resource-group "${IDENTITY_RESOURCE_GROUP}"
```

Grant required permission to the AAD application or user-assigned managed identity, for simplicity, we just assign Contributor role to the resource group where Azuredisk storage class reside:

 - if you are using Azure AD Application:

```shell
export APPLICATION_CLIENT_ID="$(az ad sp list --display-name "${APPLICATION_NAME}" --query '[0].appId' -otsv)"
export AZURE_DISK_RESOURCE_GROUP_ID="$(az group show -n $AZURE_DISK_RESOURCE_GROUP --query 'id' -otsv)"
az role assignment create --assignee $APPLICATION_CLIENT_ID --role Contributor --scope $AZURE_DISK_RESOURCE_GROUP_ID
```

 - if you are using user-assigned managed identity:

```shell
export USER_ASSIGNED_IDENTITY_OBJECT_ID="$(az identity show --name "${USER_ASSIGNED_IDENTITY_NAME}" --resource-group "${IDENTITY_RESOURCE_GROUP}" --query 'principalId' -otsv)"
export AZURE_DISK_RESOURCE_GROUP_ID="$(az group show -n $AZURE_DISK_RESOURCE_GROUP --query 'id' -otsv)"
az role assignment create --assignee $USER_ASSIGNED_IDENTITY_OBJECT_ID --role Contributor --scope $AZURE_DISK_RESOURCE_GROUP_ID
```

## 3. Establish federated identity credential between the identity and the service account issuer & subject

 - if you are using Azure AD Application:

```shell
# Get the object ID of the AAD application
export APPLICATION_OBJECT_ID="$(az ad app show --id ${APPLICATION_CLIENT_ID} --query id -otsv)"

# Add the federated identity credential:
for SERVICE_ACCOUNT_NAME in "${SA_LIST[@]}"
do
cat <<EOF > params.json
{
  "name": "${SERVICE_ACCOUNT_NAME}",
  "issuer": "${OIDC_ISSUER}",
  "subject": "system:serviceaccount:${NAMESPACE}:${SERVICE_ACCOUNT_NAME}",
  "description": "Kubernetes service account federated credential",
  "audiences": [
    "api://AzureADTokenExchange"
  ]
}
EOF
az ad app federated-credential create --id ${APPLICATION_OBJECT_ID} --parameters @params.json
done
```

 - if you are using user-assigned managed identity:

```shell
for SERVICE_ACCOUNT_NAME in "${SA_LIST[@]}"
do
az identity federated-credential create \
--name "${SERVICE_ACCOUNT_NAME}" \
--identity-name "${USER_ASSIGNED_IDENTITY_NAME}" \
--resource-group "${IDENTITY_RESOURCE_GROUP}" \
--issuer "${OIDC_ISSUER}" \
--subject system:serviceaccount:"${NAMESPACE}":"${SERVICE_ACCOUNT_NAME}"
done
```

## 4. Install CSI driver manually
 > workload identity is NOT supported on AKS **managed** Azure Disk CSI driver
 > if you are using AKS, please disable the managed Azure Disk CSI driver by `--disable-disk-driver` first

 - if you are using Azure AD Application:

```shell
export CLIENT_ID="$(az ad sp list --display-name "${APPLICATION_NAME}" --query '[0].appId' -otsv)"
export TENANT_ID="$(az ad sp list --display-name "${APPLICATION_NAME}" --query '[0].appOwnerOrganizationId' -otsv)"
helm install azuredisk-csi-driver charts/latest/azuredisk-csi-driver \
--namespace $NAMESPACE \
--set workloadIdentity.clientID=$CLIENT_ID \
--set workloadIdentity.tenantID=$TENANT_ID
```

 - if you are using user-assigned managed identity:

```shell
export CLIENT_ID="$(az identity show --name "${USER_ASSIGNED_IDENTITY_NAME}" --resource-group "${IDENTITY_RESOURCE_GROUP}" --query 'clientId' -otsv)"
export TENANT_ID="$(az identity show --name "${USER_ASSIGNED_IDENTITY_NAME}" --resource-group "${IDENTITY_RESOURCE_GROUP}" --query 'tenantId' -otsv)"
helm install azuredisk-csi-driver charts/latest/azuredisk-csi-driver \
--namespace $NAMESPACE \
--set workloadIdentity.clientID=$CLIENT_ID \
--set workloadIdentity.tenantID=$TENANT_ID
```

## 5. Deploy application using CSI driver volume

```shell
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
kubectl create -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/deployment.yaml
```
