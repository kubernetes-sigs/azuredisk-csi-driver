# Node-driven attach/detach

- Feature stage: Alpha
- Default: Disabled
- Feature gate: `NodeDrivenAttachDetach`
- Volume parameter: `attachMode: NodeDriven`

> [!WARNING]
> Node-driven attach/detach is an Alpha feature. It may change incompatibly and
> should be used only on Azure clusters and node pools explicitly enabled for
> the QAD preview. It is not supported for general production use.

## Overview

Node-driven attach/detach changes the Azure Disk attachment architecture. With
this mode, the controller claims and records ownership of the disk, while the
node service performs physical attach and detach through the Azure WireServer
QAD endpoint during CSI node staging and unstaging.

This does not add a CSI protocol capability. Kubernetes continues to use the
standard CSI controller publish/unpublish and node stage/unstage operations.

## Prerequisites

Before enabling this feature:

- the Azure subscription, region, cluster, VM sizes, and node pools must support
  the QAD preview;
- the QAD backend must be enabled for the cluster;
- every eligible node must be able to reach the Azure WireServer endpoint;
- controller and node identities must have the permissions required by the QAD
  preview;
- the same feature-gate value must be configured on the controller and node
  driver components.

For AKS, use the AKS preview enrollment mechanism when available. Do not edit an
AKS-managed CSI Deployment or DaemonSet directly because AKS may reconcile it.

## Enabling the driver capability

The feature is disabled by default. For the Helm chart, set:

```yaml
driver:
  featureGates:
    NodeDrivenAttachDetach: true
```

This passes the following option to the controller and node driver components:

```text
--feature-gates=NodeDrivenAttachDetach=true
```

Enabling the gate only permits adoption of the architecture. It does not move
existing volumes to node-driven attachment and does not make the mode the
default.

## Enabling the mode for a volume

Create a dedicated, non-default StorageClass with:

```yaml
parameters:
  attachMode: NodeDriven
```

See the [node-driven attachment StorageClass example](../deploy/example/storageclass-azuredisk-csi-node-driven.yaml).

A new node-driven volume requires all of the following:

1. the Azure platform capability is ready;
2. the `NodeDrivenAttachDetach` driver feature gate is enabled;
3. the StorageClass requests `attachMode: NodeDriven`.

If the StorageClass requests node-driven attachment while the driver gate is
disabled, provisioning fails with `FailedPrecondition`; the driver does not
silently fall back to controller-driven attachment.

## Existing volumes and rollback

The attachment mode is persisted with the volume. Changing the feature-gate
default or editing a StorageClass does not migrate an existing volume.

Disabling the gate blocks adoption of new node-driven volumes. The driver still
recognizes volumes already marked for QAD so that attach, detach, unclaim, and
delete cleanup are not stranded. Before downgrading to a driver version without
QAD support, detach and delete or migrate all node-driven volumes according to
the AKS preview rollback procedure.

## Manually claiming a static disk

For a **dynamically** provisioned volume, `CreateVolume` claims the disk
automatically: it resolves the owning AKS cluster resource ID, calls the DiskRP
`claimResource` API, and records the returned `blobUrl` and `claimIdentifier` in
the volume context. `ControllerPublishVolume` then persists that metadata onto
the PV.

A **static** (pre-provisioned) disk never goes through `CreateVolume`, so this
auto-claim never runs. To use an existing Azure managed disk with node-driven
attach/detach, reproduce those two steps by hand: claim the disk in Azure to get
its `blobUrl` and `claimIdentifier`, then place that metadata on the PV so the
driver treats the volume as QAD.

### 1. Claim the disk in Azure

Call the DiskRP `claimResource` API on the managed disk, passing the AKS managed
cluster ARM ID as `ownerResourceId`:

```bash
DISK_ID="/subscriptions/<sub>/resourceGroups/<disk-rg>/providers/Microsoft.Compute/disks/<disk-name>"
# ownerResourceId = the AKS managed cluster ARM ID:
OWNER="/subscriptions/<sub>/resourceGroups/<cluster-rg>/providers/Microsoft.ContainerService/managedClusters/<cluster-name>"
TOKEN=$(az account get-access-token --resource https://management.azure.com --query accessToken -o tsv)

curl -sS -X POST \
  "https://management.azure.com${DISK_ID}/claimResource?api-version=2025-01-02" \
  -H "Authorization: Bearer ${TOKEN}" \
  -H "Content-Type: application/json" \
  -d "{\"ownerResourceId\":\"${OWNER}\"}"
```

The call may return `202 Accepted` with a `Location` header to poll; the final
response body contains `properties.blobUrl` and `properties.claimIdentifier`.
Record both values.

### 2. Author the static PV with the QAD metadata

Two things make the driver route the disk through QAD:

- `attachmode: NodeDriven` in `spec.csi.volumeAttributes`, so
  `ControllerPublishVolume` takes the node-driven branch;
- the claim metadata as PV annotations.

```yaml
apiVersion: v1
kind: PersistentVolume
metadata:
  name: static-qad-pv
  annotations:
    azuredisk.csi.azure.com/blob-url: "<blobUrl from step 1>"
    azuredisk.csi.azure.com/claim-identifier: "<claimIdentifier from step 1>"
    # optional; otherwise ControllerPublishVolume seeds it to "0":
    azuredisk.csi.azure.com/attach-sequence: "0"
spec:
  capacity:
    storage: 10Gi
  accessModes: ["ReadWriteOnce"]
  persistentVolumeReclaimPolicy: Retain
  csi:
    driver: disk.csi.azure.com
    volumeHandle: "<DISK_ID from step 1>"
    volumeAttributes:
      attachmode: NodeDriven
```

`volumeAttributes` are immutable, so `attachmode` must be set at PV creation
time. The mutable QAD state (`attach-sequence`, and the claim metadata for a
static PV) lives in annotations, which is why the claim values are supplied
there.

### What the driver does next

1. `ControllerPublishVolume` sees `attachmode=NodeDriven`, looks up the PV by
   disk URI, and calls the QAD annotation seeding logic. Because the claim data
   is on the PV, the driver reads `blob-url`/`claim-identifier` from the
   annotations and seeds `attach-sequence=0`. No controller-side Azure attach is
   performed.
2. `NodeStageVolume` recognizes the volume as QAD (the `attach-sequence`
   annotation is present), reads `blob-url`/`claim-identifier` from the PV
   annotations, increments `attach-sequence`, and performs the physical
   node-driven attach through the WireServer endpoint.

> [!IMPORTANT]
> The `NodeDrivenAttachDetach` feature gate only gates newly created volumes.
> Existing PVs, dynamic or static, that already carry the correct QAD
> configuration (the `attach-sequence` annotation) continue to be serviced
> through the node-driven path even when the gate is disabled.

## Limitations

- Do not use this StorageClass on clusters or node pools that are not QAD-enabled.
- Do not make the preview StorageClass the cluster default.
- Mixed QAD-capable and incapable node pools require platform-provided scheduling
  constraints; the StorageClass parameter alone does not constrain pod placement.
- Workload Identity for node-side WireServer requests is unsupported unless the
  QAD service explicitly accepts federated workload tokens. A VM/VMSS-associated
  managed identity is the recommended node identity for the initial preview.
