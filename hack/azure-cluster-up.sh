#!/bin/bash
# Copyright 2021 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

set -euo pipefail

#
# Define common functions.
#
printhelp() {
  cat <<EOF
azure-cluster-up.sh: Creates a Kubernetes cluster in the Azure cloud.

Options:
  -l, --location     [Required] : The region.
  -s, --subscription [Required] : The subscription identifier.
  -a, --node-resource-group     : The nodepool resource group name. Defaults
                                  to <name>-nodepool-rg
  -b, --enable-bastion          : If present, enable Azure Bastion for access
                                  to the cluster nodes.
  -c, --client-id string        : The service principal identifier. Default to
                                  <name>-sp.
  -e, --client-tenant string    : The client tenant. Required if --client-id
                                  is used.
  -n, --name string             : The Kubernetes cluster DNS name. Defaults to
                                  k8s-<git-commit>-<template>.
  -o, --output string           : The output directory. Defaults to
                                  ./_output/<name>.
  -p, --client-secret string    : The service principal password.  Required if
                                  --client-id is used.
  -r, --resource-group string   : The resource group name. Defaults to 
                                  <name>-rg
  -t, --template string         : The cluster template name or URL. Defaults
                                  to single-az.
  -v, --k8s-version string      : The Kubernetes version. Defaults to latest.
EOF
}

echoerr() {
  printf "%s\n\n" "$*" >&2
}

trap_push() {
  local SIGNAL="${2:?Signal required}"
  HANDLERS="$( trap -p "${SIGNAL}" | cut -f2 -d \' )";

  # shellcheck disable=SC2064
  trap "${1:?Handler required}${HANDLERS:+;}${HANDLERS}" "${SIGNAL}"
}

retry() {
  local MAX_ATTEMPTS=$1
  local SLEEP_INTERVAL=$2
  shift 2

  local ATTEMPT=1
  while true;
  do
    # shellcheck disable=SC2015
    ("$@") && break || true

    echoerr "Command '$1' failed $ATTEMPT of $MAX_ATTEMPTS attempts."

    ATTEMPT=$((ATTEMPT + 1))
    if [[ $ATTEMPT -ge $MAX_ATTEMPTS ]]; then
      return 1
    fi

    echoerr "Retry after $SLEEP_INTERVAL seconds..."
    sleep "$SLEEP_INTERVAL"
  done
}

choose() {
  echo "${1:RANDOM%${#1}:1} $RANDOM"
}

genpwd() {
  PWD_SYMBOLS='~!@#$%^&*_-+=`|\(){}[]:;<>.?/'
  PWD_DIGITS='0123456789'
  PWD_LOWERCASE='abcdefghijklmnopqrstuvwxyz'
  PWD_UPPERCASE='ABCDEFGHIJKLMNOPQRSTUVWXYZ'
  PWD_ALL_CHARS="${PWD_DIGITS}${PWD_LOWERCASE}${PWD_UPPERCASE}${PWD_SYMBOLS}"

  {
      if [[ $1 -lt 4 ]]; then
          echoerr "ERROR: Password length must be at least 4"
          exit 1
      fi
      choose "${PWD_SYMBOLS}"
      choose "${PWD_DIGITS}"
      choose "${PWD_LOWERCASE}"
      choose "${PWD_UPPERCASE}"

      # shellcheck disable=SC2034
      for i in $( seq 4 "$1" )
      do
          choose "${PWD_ALL_CHARS}"
      done

  } | sort -R | awk '{printf "%s",$1}'
  echo ""
}

print_cleanup_command() {
  cat <<EOF
To delete the cluster, run the following command:

$ $1
EOF
}

#
# Process the command line arguments.
#
unset AZURE_CLIENT_ID
unset AZURE_CLIENT_SECRET
unset AZURE_CLUSTER_DNS_NAME
unset AZURE_CLUSTER_TEMPLATE
unset AZURE_K8S_VERSION
unset AZURE_LOCATION
unset AZURE_CLUSTER_RESOURCE_GROUP
unset AZURE_NODEPOOL_RESOURCE_GROUP
unset AZURE_SUBSCRIPTION_ID
unset AZURE_TENANT_ID
unset ENABLE_AZURE_BASTION
unset OUTPUT_DIR
POSITIONAL=()

while [[ $# -gt 0 ]]
do
  ARG="$1"
  case $ARG in
    -a|--node-resource-group)
      AZURE_NODEPOOL_RESOURCE_GROUP="$2"
      shift 2 # skip the option arguments
      ;;
      
    -b|--enable-bastion)
      ENABLE_AZURE_BASTION="true"
      shift
      ;;

    -c|--client-id)
      AZURE_CLIENT_ID="$2"
      shift 2 # skip the option arguments
      ;;

    -d|--debug)
      set -x
      shift
      ;;

    -e|--client-tenant)
      AZURE_TENANT_ID="$2"
      shift 2 # skip the option arguments
      ;;
    
    -l|--location)
      AZURE_LOCATION="$2"
      shift 2 # skip the option arguments
      ;;

    -n|--name)
      AZURE_CLUSTER_DNS_NAME="$2"
      shift 2 # skip the option arguments
      ;;

    -o|--output)
      OUTPUT_DIR="$2"
      shift 2 # skip the option arguments
      ;;
    
    -p|--client-secret)
      AZURE_CLIENT_SECRET="$2"
      shift 2 # skip the option arguments
      ;;

    -r|--resource-group)
      AZURE_CLUSTER_RESOURCE_GROUP="$2"
      shift 2 # skip the option arguments
      ;;

    -s|--subscription)
      AZURE_SUBSCRIPTION_ID="$2"
      shift 2 # skip the option arguments
      ;;

    -t|--template)
      AZURE_CLUSTER_TEMPLATE="$2"
      shift 2 # skip the option arguments
      ;;

    -v|--k8s-version)
      AZURE_K8S_VERSION="$2"
      shift 2 # skip the option arguments
      ;;

    -?|--help)
      printhelp
      exit 1
      ;;

    *)
      POSITIONAL+=("$1")
      shift
      ;;
  esac
done
set -- "${POSITIONAL[@]}" # restore positional parameters

#
# Validate command-line arguments and initialize variables.
#
if [[ ${#POSITIONAL[@]} -ne 0 ]]; then
  echoerr "ERROR: Unknown positional parameters - ${POSITIONAL[*]}"
  printhelp
  exit 1
fi

if [[ -z ${AZURE_SUBSCRIPTION_ID:-} ]]; then
  echoerr "ERROR: The --subscription option is required."
  printhelp
  exit 1
fi

if [[ -z ${AZURE_LOCATION:-} ]]; then
  echoerr "ERROR: The --location option is required."
  printhelp
  exit 1
fi

if [[ -n ${AZURE_CLIENT_ID:-} ]]; then
  if [[ -z ${AZURE_CLIENT_SECRET:-} ]]; then
    echoerr "ERROR: The --client-secret option is required when --client-id is used."
    printhelp
    exit 1
  fi
  if [[ -z ${AZURE_TENANT_ID:-} ]]; then
    echoerr "ERROR: The --client-tenant option is required when --client-id is used."
    printhelp
    exit 1
  fi
fi

if [[ -z ${AZURE_CLUSTER_TEMPLATE:-} ]]; then
  AZURE_CLUSTER_TEMPLATE="single-az"
fi

IS_AZURE_CLUSTER_TEMPLATE_URI=$([[ "$AZURE_CLUSTER_TEMPLATE" =~ (https://|http://) ]]; echo $(( $? == 0 )))
IS_WINDOWS_CLUSTER=$([[ "$AZURE_CLUSTER_TEMPLATE" =~ windows|.*-windows|.*/windows-testing/.* ]]; echo $(( $? == 0 )))

GIT_ROOT=$(git rev-parse --show-toplevel)
GIT_COMMIT=$(git rev-parse --short HEAD)

if [[ ${IS_AZURE_CLUSTER_TEMPLATE_URI} -eq 0 ]]; then
  AZURE_CLUSTER_TEMPLATE_ROOT=$GIT_ROOT/test/e2e/manifest
  AZURE_CLUSTER_TEMPLATE_FILE=${AZURE_CLUSTER_TEMPLATE_ROOT}/${AZURE_CLUSTER_TEMPLATE}.json

  if [[ ! -f "$AZURE_CLUSTER_TEMPLATE_FILE" ]]; then
    AZURE_CLUSTER_VALID_TEMPLATES=$(find "$AZURE_CLUSTER_TEMPLATE_ROOT" -maxdepth 1 -iname '*.json' -printf "%P\n" | awk '{split($1,f,"."); printf (NR>1?", ":"") f[1]}')
    echoerr "ERROR: The template '$AZURE_CLUSTER_TEMPLATE' is not known. Select one of the following values: $AZURE_CLUSTER_VALID_TEMPLATES."
    printhelp
    exit 1
  fi
else
  AZURE_CLUSTER_TEMPLATE_FILE=$(mktemp -t "aks-engine-model-XXX.json")
  curl -sSfL "$AZURE_CLUSTER_TEMPLATE" -o "$AZURE_CLUSTER_TEMPLATE_FILE"
  AZURE_CLUSTER_TEMPLATE=$(basename -s ".json" "$AZURE_CLUSTER_TEMPLATE" | sed "s/_/-/g" )
fi

if [[ -z ${AZURE_CLUSTER_DNS_NAME:-} ]]; then
  CLUSTER_PREFIX=$(whoami)
  if [[ ${CLUSTER_PREFIX:-root} == "root" ]]; then
    CLUSTER_PREFIX=k8s
  fi
  # Generate the cluster name, replacing "windows" with "win" to keep ARM from preventing
  # creation of DNS name containing a trademarked named.
  AZURE_CLUSTER_DNS_NAME=$(basename "$(mktemp -t "${CLUSTER_PREFIX}-${AZURE_CLUSTER_TEMPLATE}-${GIT_COMMIT}-XXX")" | sed "s/windows/win/g")
fi

if [[ -z "${AZURE_CLUSTER_RESOURCE_GROUP:-}" ]]; then
  AZURE_CLUSTER_RESOURCE_GROUP="$AZURE_CLUSTER_DNS_NAME-rg"
fi

if [[ -z "${AZURE_NODEPOOL_RESOURCE_GROUP:-}" ]]; then
  AZURE_NODEPOOL_RESOURCE_GROUP="$AZURE_CLUSTER_DNS_NAME-nodepool-rg"
fi

AZURE_SUBSCRIPTION_URI="/subscriptions/${AZURE_SUBSCRIPTION_ID}"

if [[ -z ${OUTPUT_DIR:-} ]]; then
  OUTPUT_DIR="$GIT_ROOT/_output/$AZURE_CLUSTER_DNS_NAME"
fi

# Ensure the output directory exists.
mkdir -p "$OUTPUT_DIR"

#
# Install required tools if necessary.
#
if [[ -z "$(command -v az)" ]]; then
  echo "Installing Azure CLI..."
  curl -sSfL https://aka.ms/InstallAzureCLIDeb | sudo bash
fi

AZURE_CLI_AKS_PREVIEW_EXTENSION_VERSION="$(az extension show --name aks-preview --query version --output tsv || true)"
if [[ -z "$AZURE_CLI_AKS_PREVIEW_EXTENSION_VERSION" ]]; then
  echo "Installing AKS Preview extension..."
  az extension add --name aks-preview > /dev/null
else
  AZURE_CLI_AKS_PREVIEW_CURRENT_VERSION="$(az extension list-available --query "[?name=='aks-preview'].version | [0]" --output tsv || true)"
  if [[ "$AZURE_CLI_AKS_PREVIEW_EXTENSION_VERSION" != "$AZURE_CLI_AKS_PREVIEW_CURRENT_VERSION" ]]; then
    echo "Upgrading AKS Preview extension..."
    az extension update --name aks-preview > /dev/null
  fi
fi

#
# Login to Azure, and set up the service principal, resource group and cluster.
#
AZURE_ACTIVE_SUBSCRIPTION_ID=$(az account list --query="[?isDefault].id | [0]" --output=tsv || true)
if [[ -z $AZURE_ACTIVE_SUBSCRIPTION_ID ]]; then
  echo "Logging in to Azure..."
  az login 1> /dev/null
fi

if [[ "$AZURE_SUBSCRIPTION_ID" != "$AZURE_ACTIVE_SUBSCRIPTION_ID" ]]; then
  echo "Setting active subscription to $AZURE_SUBSCRIPTION_ID..."
  az account set --subscription "${AZURE_SUBSCRIPTION_ID}" 1> /dev/null
fi

if [[ "$(az group exists --resource-group "$AZURE_CLUSTER_RESOURCE_GROUP")" != "true" ]]; then
  echo "Creating cluster resource group $AZURE_CLUSTER_RESOURCE_GROUP..."
  az group create --name "$AZURE_CLUSTER_RESOURCE_GROUP" --location "$AZURE_LOCATION" 1> /dev/null
fi

CLEANUP_FILE="$OUTPUT_DIR/cluster-down.sh"
>"$CLEANUP_FILE" cat <<EOF
set -euo pipefail
AZURE_ACTIVE_SUBSCRIPTION_ID=\$(az account list --query="[?isDefault].id | [0]" --output=tsv)
if [[ -z \$AZURE_ACTIVE_SUBSCRIPTION_ID ]]; then
  echo "Logging in to Azure..."
  az login 1> /dev/null
fi
set -x
az group delete --subscription="$AZURE_SUBSCRIPTION_ID" --resource-group="$AZURE_CLUSTER_RESOURCE_GROUP" --yes
EOF
chmod +x "$CLEANUP_FILE"

trap_push "print_cleanup_command \"${CLEANUP_FILE}\"" exit

#
# Create an Azure Key Vault to hold the cluster service principal credentials
#
AZURE_KEYVAULT_NAME="$AZURE_CLUSTER_DNS_NAME-kv"
if [[ -z "$( (az keyvault show --name "$AZURE_KEYVAULT_NAME" --resource-group "$AZURE_CLUSTER_RESOURCE_GROUP" --output tsv --query name || true) 2> /dev/null)" ]]; then
  echo "Creating KeyVault $AZURE_KEYVAULT_NAME..."
  az keyvault create --name "$AZURE_KEYVAULT_NAME" --resource-group "$AZURE_CLUSTER_RESOURCE_GROUP" --location "$AZURE_LOCATION" 1> /dev/null
fi

#
# Create a user-assigned managed identity for the AKS cluster.
#
AZURE_MANAGED_IDENTITY_NAME="$AZURE_CLUSTER_DNS_NAME"
AZURE_MANAGED_IDENTITY_ID="$( (az identity show --name "$AZURE_MANAGED_IDENTITY_NAME" --resource-group "$AZURE_CLUSTER_RESOURCE_GROUP" --output tsv --query id || true) 2> /dev/null)"
if [[ -z "$AZURE_MANAGED_IDENTITY_ID" ]]; then
  AZURE_MANAGED_IDENTITY_ID="$(az identity create --name "$AZURE_MANAGED_IDENTITY_NAME" --resource-group "$AZURE_CLUSTER_RESOURCE_GROUP" --location "$AZURE_LOCATION" --output tsv --query id)"
fi

#
# If no service principal was specified, create a new one or use the one from a previous
# run of this script.
#
if [[ -z ${AZURE_CLIENT_ID:-} ]]; then
  AZURE_CLIENT_NAME=${AZURE_CLUSTER_DNS_NAME}-sp
  AZURE_CLIENT_TENANT_NAME="${AZURE_CLIENT_NAME}-tenant-id"
  AZURE_CLIENT_ID_NAME="${AZURE_CLIENT_NAME}-client-id"
  AZURE_CLIENT_SECRET_NAME="${AZURE_CLIENT_NAME}-secret"

  AZURE_CLIENT_ID="$( (az keyvault secret show --vault-name "$AZURE_KEYVAULT_NAME" --name "$AZURE_CLIENT_ID_NAME" --output tsv --query value || true) 2> /dev/null)"
  AZURE_CLIENT_SECRET="$( (az keyvault secret show --vault-name "$AZURE_KEYVAULT_NAME" --name "$AZURE_CLIENT_SECRET_NAME" --output tsv --query value || true) 2> /dev/null)"
  AZURE_TENANT_ID="$( (az keyvault secret show --vault-name "$AZURE_KEYVAULT_NAME" --name "$AZURE_CLIENT_TENANT_NAME" --output tsv --query value || true) 2> /dev/null)"

  if [[ -n "$AZURE_CLIENT_ID" ]] && [[ -n "$AZURE_CLIENT_SECRET" ]] &&  [[ -n "$AZURE_TENANT_ID" ]]; then
    echo "Using existing service principal $AZURE_CLIENT_NAME..."

    echo "Assigning $AZURE_CLIENT_ID to \"Owner\" role of $AZURE_CLUSTER_RESOURCE_GROUP..."
    az role assignment create \
        --assignee="$AZURE_CLIENT_ID" \
        --role=Contributor \
        --scope="$AZURE_SUBSCRIPTION_URI" 1> /dev/null
  else
    echo "Creating service principal $AZURE_CLIENT_NAME..."
    mapfile -t AZURE_CLIENT_INFO < <(az ad sp create-for-rbac \
      --name="http://$AZURE_CLIENT_NAME" \
      --role="Contributor" \
      --scopes="$AZURE_SUBSCRIPTION_URI" \
      --output=tsv \
      --query="[tenant,appId,password]")
    AZURE_TENANT_ID=${AZURE_CLIENT_INFO[0]}
    AZURE_CLIENT_ID=${AZURE_CLIENT_INFO[1]}
    AZURE_CLIENT_SECRET=${AZURE_CLIENT_INFO[2]}
    unset AZURE_CLIENT_INFO

    echo "Storing service principal credentials in KeyVault $AZURE_KEYVAULT_NAME..."
    az keyvault secret set --vault-name "$AZURE_KEYVAULT_NAME" --name "$AZURE_CLIENT_TENANT_NAME" --value "$AZURE_TENANT_ID" 1> /dev/null
    az keyvault secret set --vault-name "$AZURE_KEYVAULT_NAME" --name "$AZURE_CLIENT_ID_NAME" --value "$AZURE_CLIENT_ID" 1> /dev/null
    az keyvault secret set --vault-name "$AZURE_KEYVAULT_NAME" --name "$AZURE_CLIENT_SECRET_NAME" --value "$AZURE_CLIENT_SECRET" 1> /dev/null
  fi

  # Add removal of the service principal to the cleanup script.
  >>"$CLEANUP_FILE" cat <<EOF
az role assignment delete --assignee "$AZURE_CLIENT_ID" --role Contributor --scope "$AZURE_SUBSCRIPTION_URI" --yes
az ad sp delete --id="$AZURE_CLIENT_ID"
EOF
fi

if [[ $IS_WINDOWS_CLUSTER -ne 0 ]]; then
  AZURE_WIN_ADMIN_SECRET_NAME="$AZURE_CLUSTER_DNS_NAME-winadm"
  AZURE_WIN_ADMIN_SECRET=$(az keyvault secret show --vault-name "$AZURE_KEYVAULT_NAME" --name "$AZURE_WIN_ADMIN_SECRET_NAME" || true)

  if [[ -n "$AZURE_WIN_ADMIN_SECRET" ]]; then
    echo "Using existing Windows administrator password..."
  else
    echo "Creating Windows administrator password..."
    AZURE_WIN_ADMIN_SECRET="$(genpwd 16)"
    az keyvault secret set --vault-name "$AZURE_KEYVAULT_NAME" --name "$AZURE_WIN_ADMIN_SECRET_NAME" --value "$AZURE_WIN_ADMIN_SECRET" 1> /dev/null
  fi
fi

#
# Create the Azure Kubernetes Service cluster from the aks-engine template.
#
echo "Creating cluster $AZURE_CLUSTER_DNS_NAME and system nodepool..."
MASTER_NAME=$(cat "$AZURE_CLUSTER_TEMPLATE_FILE" | jq --raw-output '.properties.masterProfile.name // "systempool"')
MASTER_NODE_COUNT=$(cat "$AZURE_CLUSTER_TEMPLATE_FILE" | jq --raw-output '.properties.masterProfile.count // 1')
MASTER_NODE_SKU=$(cat "$AZURE_CLUSTER_TEMPLATE_FILE" | jq --raw-output '.properties.masterProfile.vmSize // "Standard_D2s_v3"')
AGENTPOOL_NAME=$(cat "$AZURE_CLUSTER_TEMPLATE_FILE" | jq --raw-output '.properties.agentPoolProfiles[0].name // "agentpool"')
AGENTPOOL_NODE_COUNT=$(cat "$AZURE_CLUSTER_TEMPLATE_FILE" | jq --raw-output '.properties.agentPoolProfiles[0].count // 3')
AGENTPOOL_NODE_SKU=$(cat "$AZURE_CLUSTER_TEMPLATE_FILE" | jq --raw-output '.properties.agentPoolProfiles[0].vmSize // "Standard_D2s_v3"')
AGENTPOOL_OS_TYPE=$(cat "$AZURE_CLUSTER_TEMPLATE_FILE" | jq --raw-output '.properties.agentPoolProfiles[0].osType // "Linux"')
mapfile -t AGENTPOOL_ZONES < <(cat "$AZURE_CLUSTER_TEMPLATE_FILE" | jq --raw-output '.properties.agentPoolProfiles[0].availabilityZones // [] | .[]')
WINDOWS_PROFILE_ADMIN_USER=$(cat "$AZURE_CLUSTER_TEMPLATE_FILE" | jq --raw-output '.properties.windowsProfile.adminUsername // "azureuser"')

AKS_CREATE_OPTIONS=(
  --name "$AZURE_CLUSTER_DNS_NAME" \
  --location "$AZURE_LOCATION" \
  --resource-group "$AZURE_CLUSTER_RESOURCE_GROUP" \
  --dns-name-prefix "$AZURE_CLUSTER_DNS_NAME" \
  --generate-ssh-keys \
  --enable-managed-identity \
  --assign-identity "$AZURE_MANAGED_IDENTITY_ID" \
  --assign-kubelet-identity "$AZURE_MANAGED_IDENTITY_ID" \
  --node-resource-group "$AZURE_NODEPOOL_RESOURCE_GROUP" \
  --nodepool-name "$MASTER_NAME" \
  --node-count "$MASTER_NODE_COUNT" \
  --node-vm-size "$MASTER_NODE_SKU" \
  --disable-disk-driver \
  --disable-file-driver \
  --disable-snapshot-controller \
  --network-plugin azure \
  --yes
  )

if [[ $IS_WINDOWS_CLUSTER -ne 0 ]]; then
  AKS_CREATE_OPTIONS+=(
    --windows-admin-username "$WINDOWS_PROFILE_ADMIN_USER" \
    --windows-admin-password "$AZURE_WIN_ADMIN_SECRET"
  )
fi

if [[ -n "${AZURE_K8S_VERSION:-}" ]]; then
  AKS_CREATE_OPTIONS+=(--kubernetes-version "$AZURE_K8S_VERSION")
fi

az aks create "${AKS_CREATE_OPTIONS[@]}" > /dev/null

echo "Creating agent nodepool..."
AGENTPOOL_ADD_OPTIONS=(
  --cluster-name "$AZURE_CLUSTER_DNS_NAME" \
  --resource-group "$AZURE_CLUSTER_RESOURCE_GROUP" \
  --nodepool-name "$AGENTPOOL_NAME" \
  --node-count "$AGENTPOOL_NODE_COUNT" \
  --node-vm-size "$AGENTPOOL_NODE_SKU" \
  --mode "User" \
  --os-type "$AGENTPOOL_OS_TYPE"
  )

if [[ -n "${AZURE_K8S_VERSION:-}" ]]; then
  AGENTPOOL_ADD_OPTIONS+=(--kubernetes-version "$AZURE_K8S_VERSION")
fi

if [[ ${#AGENTPOOL_ZONES[@]} -ne 0 ]]; then
  AGENTPOOL_ADD_OPTIONS+=(--zones "${AGENTPOOL_ZONES[@]}")
fi

az aks nodepool add "${AGENTPOOL_ADD_OPTIONS[@]}" > /dev/null

echo "Getting the cluster credentials..."
az aks get-credentials \
  --name "$AZURE_CLUSTER_DNS_NAME" \
  --resource-group "$AZURE_CLUSTER_RESOURCE_GROUP" \
  --overwrite-existing  > /dev/null
>>"$CLEANUP_FILE" cat <<EOF
kubectl config delete-context "$AZURE_CLUSTER_DNS_NAME"
EOF

echo "Tainting the system nodes..."
kubectl taint nodes --selector "kubernetes.azure.com/mode=system" "CriticalAddonsOnly=true:NoSchedule"

# Set up Bastion access if requested
if [[ ${ENABLE_AZURE_BASTION:-false} == "true" ]]; then
  "${GIT_ROOT}/hack/enable-azure-bastion.sh" \
    --subscription "$AZURE_SUBSCRIPTION_ID" \
    --resource-group "$AZURE_NODEPOOL_RESOURCE_GROUP" \
    --location "$AZURE_LOCATION" \
    --name "$AZURE_CLUSTER_DNS_NAME"
fi

#
# Create Helm chart overrides file.
#
HELM_VALUES_FILE="$OUTPUT_DIR/csi-azuredisk-overrides.yaml"
>"$HELM_VALUES_FILE" cat <<EOF
controller:
  replicas: 1
  affinity:
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
        - matchExpressions:
          - key: kubernetes.azure.com/mode
            operator: In
            values:
            - system
  tolerations:
    - key: "CriticalAddonsOnly"
      operator: "Exists"
      effect: "NoSchedule"
snapshot:
  snapshotController:
    replicas: 1
schedulerExtender:
  replicas: 1
  affinity:
    nodeAffinity:
      requiredDuringSchedulingIgnoredDuringExecution:
        nodeSelectorTerms:
        - matchExpressions:
          - key: kubernetes.azure.com/mode
            operator: In
            values:
            - system
  tolerations:
    - key: "CriticalAddonsOnly"
      operator: "Exists"
      effect: "NoSchedule"
linux:
  tolerations: []
EOF

#
# Create setup and clean-up shell scripts.
#
SETUP_FILE="$OUTPUT_DIR/e2e-setup.sh"
>"$SETUP_FILE" cat <<EOF
export ARTIFACTS="$OUTPUT_DIR/artifacts"
export AZURE_TENANT_ID="$AZURE_TENANT_ID"
export AZURE_SUBSCRIPTION_ID="$AZURE_SUBSCRIPTION_ID"
export AZURE_CLIENT_ID="$AZURE_CLIENT_ID"
export AZURE_CLIENT_SECRET="$AZURE_CLIENT_SECRET"
export AZURE_RESOURCE_GROUP="$AZURE_NODEPOOL_RESOURCE_GROUP"
export AZURE_LOCATION="$AZURE_LOCATION"
export EXTRA_HELM_OPTIONS="--values $HELM_VALUES_FILE"

az aks get-credentials \
  --name "$AZURE_CLUSTER_DNS_NAME" \
  --resource-group "$AZURE_CLUSTER_RESOURCE_GROUP" \
  --overwrite-existing
EOF

if [[ ${IS_WINDOWS_CLUSTER} -ne 0 ]]; then
  >>"$SETUP_FILE" echo "export TEST_WINDOWS=\"true\""
fi

chmod +x "$SETUP_FILE"

mkdir -p "$OUTPUT_DIR/artifacts"

cat <<EOF
To use the $AZURE_CLUSTER_DNS_NAME cluster, run the following command:

$ az aks get-credentials \
  --name "$AZURE_CLUSTER_DNS_NAME" \
  --resource-group "$AZURE_CLUSTER_RESOURCE_GROUP" \
  --overwrite-existing

To setup for e2e tests, run the following command:

$ source "$SETUP_FILE"

EOF