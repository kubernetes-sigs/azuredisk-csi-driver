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

The feature gate is required to service existing node-driven volumes, not only
to provision new ones. If it is disabled, `ControllerPublishVolume` rejects a
node-driven volume, and the node stage and unstage operations do not look up its
QAD metadata. Deletion also skips the proactive QAD unclaim path.

Keep the gate enabled on both controller and node components until every
node-driven volume has been detached and deleted or migrated, and all required
QAD unclaim cleanup has completed according to the AKS preview rollback
procedure. Disable the gate only after that cleanup, and only then downgrade to
a driver version without QAD support.

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
   node-driven attach through the WireServer endpoint. It returns success once
   the attach is accepted; device discovery and mounting happen in
   `NodePublishVolume` (see [Node staging, publishing, and the LUN](#node-staging-publishing-and-the-lun)).

> [!IMPORTANT]
> Keep the `NodeDrivenAttachDetach` feature gate enabled for the full lifetime
> of every node-driven volume. Existing PV annotations do not bypass the gate;
> disabling it prevents the driver from completing the QAD attach and detach
> paths.

## Node staging, publishing, and the LUN

For QAD volumes the node splits its work so that CSI is the authoritative record
of attach state, independent of when the disk's LUN becomes visible on the node.
The controller-driven (non-QAD) path is unchanged: it still discovers the LUN
from the publish context and formats and mounts during `NodeStageVolume`.

| Operation | QAD volume | Controller-driven volume (unchanged) |
| --- | --- | --- |
| `NodeStageVolume` | Physical attach only. Returns success once WireServer reports the disk attached. Performs no device discovery, format, or mount. | Discovers the LUN, formats, and mounts at the staging path. |
| `NodePublishVolume` | Rediscovers the current LUN from WireServer, waits for the device to enumerate, formats and mounts at the staging path, then bind-mounts to the pod target. | Bind-mounts the staging path to the pod target. |
| `NodeUnstageVolume` | Unmounts the staging path and performs the physical detach. | Unmounts the staging path. |

### Why the LUN is not persisted

The LUN assigned to a QAD disk can differ every time the volume moves to a
different node, so it is never stored on the PV. `NodeStageVolume` records only
that the disk is attached. `NodePublishVolume` re-queries WireServer (the source
of truth) for the current LUN each time it runs, and returns a retryable
`Unavailable` error while the disk state or the guest device is not yet
available. This keeps attach state correct across moves without a stale cached
LUN, and lets `NodeStageVolume` report success even when the LUN is not yet
visible on the node.

Detach does not depend on the LUN: `NodeUnstageVolume` decides whether to detach
from the `attach-sequence` annotation, so teardown is correct even if a LUN was
never persisted or never became visible.

### Caveat: kubelet restart before the first successful publish

Because `NodeStageVolume` returns success without mounting anything at the
staging path, there is a window between a successful attach and the first
successful `NodePublishVolume` where the staging path is not a mount point.

Kubelet reconstructs volume state after a restart by inspecting existing mounts.
If kubelet restarts during that window, reconstruction may not observe a staged
device. This is safe for detach correctness — `NodeUnstageVolume` still detaches
based on the `attach-sequence` annotation, and the QAD attach is idempotent, so
a re-issued `NodeStageVolume` simply re-confirms the existing attach. The disk
cannot be stranded attached. The observable effect is limited to kubelet
re-driving stage/publish for the affected volume.

## Limitations

- Do not use this StorageClass on clusters or node pools that are not QAD-enabled.
- Do not make the preview StorageClass the cluster default.
- Mixed QAD-capable and incapable node pools require platform-provided scheduling
  constraints; the StorageClass parameter alone does not constrain pod placement.
- Workload Identity for node-side WireServer requests is unsupported unless the
  QAD service explicitly accepts federated workload tokens. A VM/VMSS-associated
  managed identity is the recommended node identity for the initial preview.
