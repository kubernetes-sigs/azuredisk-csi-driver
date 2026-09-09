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

## Limitations

- Do not use this StorageClass on clusters or node pools that are not QAD-enabled.
- Do not make the preview StorageClass the cluster default.
- Mixed QAD-capable and incapable node pools require platform-provided scheduling
  constraints; the StorageClass parameter alone does not constrain pod placement.
- Workload Identity for node-side WireServer requests is unsupported unless the
  QAD service explicitly accepts federated workload tokens. A VM/VMSS-associated
  managed identity is the recommended node identity for the initial preview.
