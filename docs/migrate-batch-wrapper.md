# Batch Migration Wrapper

The batch wrapper script (`hack/premium-to-premiumv2-migrator-batch.sh`) adds **resumable checkpointing** and **concurrent execution** on top of the existing per-mode migration scripts. It does not modify the underlying inplace, dual, or attrclass scripts.

## When to Use

Use the batch wrapper instead of the per-mode scripts directly when you need:

- **Resumability**: Interrupt and re-run without re-processing completed PVCs
- **Concurrency**: Migrate multiple PVCs in parallel
- **Batch control**: Process PVCs in controlled sub-batches
- **Dry-run planning**: Preview what will be migrated before committing

## Quick Start

```bash
# Label PVCs for migration
kubectl label pvc <pvc-name> -n <namespace> disk.csi.azure.com/pv2migration=true

# Dry run — see what would happen
MIGRATION_MODE=inplace DRY_RUN=true ./hack/premium-to-premiumv2-migrator-batch.sh

# Run migration
MIGRATION_MODE=inplace ./hack/premium-to-premiumv2-migrator-batch.sh

# Resume after interruption (re-run the same command)
MIGRATION_MODE=inplace ./hack/premium-to-premiumv2-migrator-batch.sh
```

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `MIGRATION_MODE` | *(required)* | Migration mode: `inplace`, `dual`, or `attrclass` |
| `BATCH_SIZE` | `1` | Number of PVCs per sub-batch |
| `BATCH_CONCURRENCY` | `min(50, num_pvcs)` | Maximum number of parallel batches |
| `DRY_RUN` | `false` | If `true`, print batch plan and exit |
| `CHECKPOINT_FILE` | `<BATCH_LOG_DIR>/migration-checkpoint.json` | Path to the checkpoint state file (created inside BATCH_LOG_DIR) |
| `MIGRATION_LABEL` | `disk.csi.azure.com/pv2migration=true` | Label selector for PVC discovery |
| `NAMESPACE` | *(all namespaces)* | Scope discovery to a single namespace |
| `BATCH_LOG_DIR` | `migration-batch-logs` | Directory for per-batch log files |

All other environment variables (`SNAPSHOT_CLASS`, `POLL_INTERVAL`, `WAIT_FOR_WORKLOAD`, `ROLLBACK_ON_TIMEOUT`, etc.) are passed through to the underlying migration scripts unchanged.

## How It Works

### 1. Discovery
The wrapper queries the cluster for PVCs matching `MIGRATION_LABEL` and filters out PVCs already marked as `done` in the checkpoint file.

### 2. Batch Planning
Eligible PVCs are split into sub-batches of `BATCH_SIZE` (default 1). Each batch is assigned a unique batch ID.

### 3. Concurrent Execution
Up to `BATCH_CONCURRENCY` batches run in parallel. For each batch:

1. A temporary label (`disk.csi.azure.com/pv2migration-batch=<batch_id>`) is applied to the batch's PVCs
2. The existing mode script is invoked with `MIGRATION_LABEL` set to the batch label
3. On completion, the checkpoint is updated and the temporary label is removed

### 4. Checkpoint & Resume
The checkpoint file (`migration-checkpoint.json`) tracks per-PVC status:

```json
{
  "version": 1,
  "pvcs": {
    "default/my-pvc": {
      "status": "done",
      "finished_at": "2025-06-15T10:05:30Z",
      "pid": 12345
    },
    "prod/data-pvc": {
      "status": "in_progress",
      "started_at": "2025-06-15T10:00:00Z",
      "pid": 12346
    }
  }
}
```

Timestamp fields are set based on status: `started_at` for `in_progress`, `finished_at` for `done`/`failed`. The `pid` field stores the batch subprocess PID for stale process detection on restart.

On re-run, PVCs with status `done` are skipped automatically. PVCs with status `failed` or `in_progress` are retried.

## Examples

### Migrate all labeled PVCs with default concurrency (in-place mode)
```bash
MIGRATION_MODE=inplace ./hack/premium-to-premiumv2-migrator-batch.sh
```

### Migrate using dual-PVC mode with concurrency of 10
```bash
MIGRATION_MODE=dual BATCH_CONCURRENCY=10 ./hack/premium-to-premiumv2-migrator-batch.sh
```

### Migrate in batches of 5 PVCs using VAC mode
```bash
MIGRATION_MODE=attrclass BATCH_SIZE=5 ./hack/premium-to-premiumv2-migrator-batch.sh
```

### Scoped to a single namespace
```bash
MIGRATION_MODE=inplace NAMESPACE=production ./hack/premium-to-premiumv2-migrator-batch.sh
```

### Resume a previous run with a custom checkpoint file
```bash
MIGRATION_MODE=inplace CHECKPOINT_FILE=my-checkpoint.json ./hack/premium-to-premiumv2-migrator-batch.sh
```

## Signal Handling

When interrupted (`Ctrl+C` / `SIGTERM`), the wrapper:
1. Waits for active batch subprocesses to finish
2. Saves checkpoint state
3. Prints resume instructions

Re-run the same command to continue from where it left off.

## Logs

Per-batch logs and artifacts are written to `BATCH_LOG_DIR` (default: `migration-batch-logs/`):
```
migration-batch-logs/
  wrapper.log              # wrapper script stdout/stderr
  preflight.log            # shared resource creation log
  migration-checkpoint.json # checkpoint state
  batch-0/
    batch-0.log            # stdout/stderr from mode script
    audit.log              # audit trail
    pvc-backups/           # PVC YAML backups
  batch-1/
    batch-1.log
    audit.log
    pvc-backups/
  ...
```

## Troubleshooting

**Q: A PVC is stuck as `in_progress` in the checkpoint after a crash.**
A: Re-run the wrapper. PVCs not marked `done` are automatically retried.

**Q: A PVC failed and I want to skip it.**
A: Edit the checkpoint file and change its status to `done`, or remove the migration label from the PVC.

**Q: How do I reset and start over?**
A: Delete the checkpoint file: `rm migration-checkpoint.json`
