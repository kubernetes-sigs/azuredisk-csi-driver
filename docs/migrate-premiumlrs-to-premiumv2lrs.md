# Premium_LRS → PremiumV2_LRS Migration Guide

This guide explains how to use the migration scripts in `hack/` to move Azure Disk backed PVCs from Premium_LRS to PremiumV2_LRS. It covers the two supported modes (`inplace` and `dual`), prerequisites, validation steps, safety / rollback, cleanup, and troubleshooting.

---

## 1. Goals & When to Use These Scripts

These scripts automate:
- Snapshotting existing Premium_LRS volumes
- Creating PremiumV2_LRS replacement PVCs / PVs
- (Optionally) staging intermediate CSI objects for in-tree disks
- Applying safety checks, labels, audit logging, and rollback metadata

They are intended for controlled batches (not fire‑and‑forget across an entire large cluster without review).

---

## 2. Modes Overview

| Mode | Script | Summary | Pros | Trade‑offs / Cons | Typical Use |
|------|--------|---------|------|-------------------|-------------|
| In-place | `hack/premium-to-premiumv2-migrator-inplace.sh` | Deletes original PVC (keeping original PV), recreates same name PVC pointing to snapshot and PremiumV2 SC | Same name preserved; minimal object sprawl | Short window where PVC is absent; workload must be quiesced/detached; rollback relies on retained PV | Smaller batches, controlled maintenance windows |
| Dual (pv1→pv2) | `hack/premium-to-premiumv2-migrator-dualpvc.sh` | Creates intermediate CSI PV/PVC (if source was in-tree), snapshots, creates a *pv2* PVC (suffix), monitors migration events | Keeps original PVC around longer (reduced disruption); clearer staged artifacts | More objects (intermediate PV/PVC + target); higher cleanup burden; naming complexity | Migration where minimizing initial disruption matters or need visibility before switch |

Recommendation:
1. Pilot on a tiny subset using `inplace` (simpler) in a non-prod namespace.
2. If operational constraints demand minimal rename churn or extra observation time, use `dual` for broader rollout.
3. Always label PVCs explicitly to opt them in (staged adoption).

---

## 3. Key Scripts & Shared Library

- In-place runner: `hack/premium-to-premiumv2-migrator-inplace.sh`
- Dual runner: `hack/premium-to-premiumv2-migrator-dualpvc.sh`
- Shared logic: `hack/lib-premiumv2-migration-common.sh`

The common library provides:
- RBAC preflight
- Snapshot class creation (`ensure_snapshot_class`)
- StorageClass variant creation (`<sourceSC>-pv1`, `<sourceSC>-pv2`)
- Intermediate PV/PVC creation for in-tree disks
- Migration event parsing (`SKUMigration*`)
- Cleanup report & audit logging
- Base64 backup and rollback metadata (in-place mode)

---

## 4. Label-Driven Selection

PVCs are selected by (default):
```
disk.csi.azure.com/pv2migration=true
```
Environment variable:
```
MIGRATION_LABEL="disk.csi.azure.com/pv2migration=true"
```
Change the label (or add additional selectors externally) to control scope. Only labeled, *Bound* PVCs under the size threshold are processed.

---

## 5. Environment Variables (Key Tunables)

| Variable | Default | Meaning / Guidance |
|----------|---------|--------------------|
| `MIG_SUFFIX` | `csi` | Suffix for intermediate & pv2 naming (`<pvc>-<suffix>[-pv2]`) – keep stable. |
| `MAX_PVCS` | `50` | Upper bound per run; script truncates beyond this to avoid huge batches. |
| `MAX_PVC_CAPACITY_GIB` | `2048` | Skip PVCs at or above this (safety / PremiumV2 size comfort). |
| `WAIT_FOR_WORKLOAD` | `true` | If true, tries to ensure detachment before migration (in-place more critical). |
| `WORKLOAD_DETACH_TIMEOUT_MINUTES` | `0` | >0 to enforce a max wait for volume detach. |
| `BIND_TIMEOUT_SECONDS` | `60` | Wait for new pv2 PVC binding. |
| `MONITOR_TIMEOUT_MINUTES` | `300` | Global migration monitor upper bound. |
| `MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES` | `10` | Force a migration-inprogress label on original PV if events lag (in both modes). |
| `BACKUP_ORIGINAL_PVC` | `true` | In-place only: store raw YAML (under `PVC_BACKUP_DIR`). |
| `PVC_BACKUP_DIR` | `pvc-backups` | Backup directory root. |
| `ROLLBACK_ON_TIMEOUT` | `true` (if set by you) | In-place: attempt rollback automatically on bind timeout / monitor timeout. |
| `SNAPSHOT_CLASS` | `csi-azuredisk-vsc` | Snapshot class name (created if missing). |
| `SNAPSHOT_MAX_AGE_SECONDS` | `7200` | Reuse snapshot if younger unless stale logic triggers. |
| `SNAPSHOT_RECREATE_ON_STALE` | `false` | If `true`, stale snapshot is deleted and recreated. |
| `MIGRATION_LABEL` | see above | PVC selection. |
| `AUDIT_ENABLE` | `true` | Enable audit log lines. |
| `AUDIT_LOG_FILE` | `pv1-pv2-migration-audit.log` | Rolling append log file. |

(See top of `lib-premiumv2-migration-common.sh` for the complete list.)

---

## 6. Prerequisites & Validation Checklist

Before running:
1. RBAC: Ensure your principal can `get/list/create/patch/delete` PV/PVC/Snapshot/SC as required. Script will abort if critical verbs fail.
2. Quota: Check PremiumV2 disk quotas in target subscription/region (script does NOT enforce).
3. StorageClasses: Confirm original SC(s) are Premium_LRS (cachingMode=none, no unsupported encryption combos).
4. Workload readiness: Plan for pods referencing target PVCs to be idle / safe to pause if using in-place.
5. Snapshot CRDs: Ensure `VolumeSnapshot` CRDs installed (the script creates a class if absent).
6. Label small test set:
   ```
   kubectl label pvc data-app-a -n team-a disk.csi.azure.com/pv2migration=true
   ```
7. Dry run *logic* (syntax & preflight only):
   ```
   bash -n hack/premium-to-premiumv2-migrator-inplace.sh
   bash -n hack/premium-to-premiumv2-migrator-dualpvc.sh
   ```
8. Optional: Run with a deliberately empty label selector to validate preflight (set `MIGRATION_LABEL="doesnotexist=true"` temporarily).

---

## 7. Running the Scripts

Change to repository root or `hack/` directory.

In-place example:
```bash
cd hack
# Limit to first few PVCs, raise verbosity by tee'ing output
MAX_PVCS=5 MIG_SUFFIX=csi \
  ./premium-to-premiumv2-migrator-inplace.sh 2>&1 | tee run-inplace-$(date +%Y%m%d-%H%M%S).log
```

Dual example:
```bash
cd hack
MAX_PVCS=5 MIG_SUFFIX=csi \
  ./premium-to-premiumv2-migrator-dualpvc.sh 2>&1 | tee run-dual-$(date +%Y%m%d-%H%M%S).log
```

Important runtime phases (both):
1. Pre-req scan (size, SC parameters, binding).
2. RBAC preflight.
3. StorageClass variant creation (`<source>-pv1`, `<source>-pv2`).
4. Snapshot creation or reuse.
5. PVC/PV creation (intermediate for in-tree in dual; immediate replacement in inplace).
6. Bind wait & event monitoring (`SKUMigrationStarted/Progress/Completed`).
7. Labeling original PVC (`migration-done=true`) on completion.
8. Summary + cleanup report + audit summary.

---

## 8. Rollback (In-place Mode)

Each migrated PVC stores:
- Base64 annotation with sanitized pre-migration spec (`rollback-pvc-yaml`)
- Original PV name annotation

Automatic rollback triggers:
- Bind timeout (rc=2) if `ROLLBACK_ON_TIMEOUT=true`
- Monitor timeout per PVC (also loops through for rollback)

### Manual rollback steps

Primary (annotation-based) method:
```bash
# 1. Fetch encoded sanitized spec from annotation
enc=$(kubectl get pvc mypvc -n ns -o jsonpath="{.metadata.annotations['disk.csi.azure.com/rollback-pvc-yaml']}")
# 2. Decode to a file
echo "$enc" | base64 -d > original.yaml

# 3. Delete current pv2 PVC (it references the PremiumV2 volume)
kubectl delete pvc mypvc -n ns

# 4. Clear claimRef on original PV (name stored in annotation)
origpv=$(kubectl get pvc mypvc -n ns -o jsonpath="{.metadata.annotations['disk.csi.azure.com/rollback-orig-pv']}")
kubectl patch pv "$origpv" -p '{"spec":{"claimRef":null}}'

# 5. Recreate original PVC from saved spec
kubectl apply -f original.yaml
```

#### Alternative if annotation is missing (e.g., pv2 PVC already removed or annotations pruned)

If the pv2 PVC (with rollback annotations) was deleted before you captured the encoded spec, you can fall back to the raw backup taken when `BACKUP_ORIGINAL_PVC=true`.

1. Locate the most recent backup file:
   ```
   ls -1 pvc-backups/<namespace>/ | grep '^<pvcName>-.*\.yaml$' | sort | tail -n1
   ```
   Example:
   ```
   latest_backup=$(ls -1 pvc-backups/ns-example/ | grep '^mypvc-.*\.yaml$' | sort | tail -n1)
   cp "pvc-backups/ns-example/$latest_backup" restore.yaml
   ```

2. Inspect & sanitize if needed (the backup is the full original object; it may still contain fields you don’t want to apply directly in rare cluster/version mismatches):
   - Remove `status:` block (if present).
   - Ensure metadata does NOT include: `resourceVersion`, `uid`, `creationTimestamp`, `managedFields`, `finalizers`.
   Quick one-liner to strip common runtime fields:
   ```bash
   yq 'del(.status, .metadata.uid, .metadata.resourceVersion, .metadata.managedFields, .metadata.creationTimestamp, .metadata.finalizers)' restore.yaml > restore.clean.yaml
   mv restore.clean.yaml restore.yaml
   ```

3. Get the original PV name from the backup spec (or from audit log lines):
   ```bash
   origpv=$(yq -r '.spec.volumeName // ""' restore.yaml)
   [ -z "$origpv" ] && echo "Could not determine original PV name" && exit 1
   ```

4. Ensure the PV reclaim policy is still `Retain` (script should have patched it earlier):
   ```bash
   kubectl get pv "$origpv" -o jsonpath='{.spec.persistentVolumeReclaimPolicy}'; echo
   ```

5. Clear the `claimRef` on the original PV so it can rebind:
   ```bash
   kubectl patch pv "$origpv" -p '{"spec":{"claimRef":null}}'
   ```

6. (Optional) Double-check no pv2 PVC with the same name still exists:
   ```bash
   kubectl get pvc mypvc -n ns && echo "A PVC named mypvc still exists; delete it first" && exit 1 || true
   ```

7. Recreate the original PVC:
   ```bash
   kubectl apply -f restore.yaml
   ```

8. Wait for binding:
   ```bash
   kubectl wait --for=jsonpath='{.status.phase}=Bound' pvc/mypvc -n ns --timeout=5m
   ```

9. Validate pod/workload mounts (if you restart workloads):
   ```bash
   kubectl describe pvc mypvc -n ns | grep -E 'Volume|Status'
   ```

Notes & cautions:
- If multiple backups exist, always choose the latest timestamped file unless you have a reason to revert further back.
- If the original PV was manually modified post-migration (uncommon), verify it still points to the original disk resource.
- Audit log file (`pv1-pv2-migration-audit.log`) can help correlate the PV name and timing if the backup spec is ambiguous.

Validation after restore:
```bash
# Confirm PVC bound to original PV (not a PremiumV2 one)
kubectl get pvc mypvc -n ns -o jsonpath='{.spec.volumeName}'; echo
kubectl get pv "$origpv" -o jsonpath='{.spec.csi.driver}'; echo  # likely empty for in-tree
kubectl get pv "$origpv" -o jsonpath='{.spec.azureDisk.diskURI}'; echo
```

If you also plan to retry the migration later:
- Re-apply the migration label to the restored PVC.
- Ensure the reclaim policy is still `Retain`.
- Remove any stale snapshot (or keep it if you want faster retry and it’s recent).

---

## 9. Interpreting Output

Key log prefixes:
- `[OK]` – success milestones
- `[WARN]` – transient issues, retries, or manual review needed
- `[ERROR]` – abort conditions

Events:
- The script inspects `kubectl get events` for `SKUMigration*` reasons to drive progress/state.

Audit log (`pv1-pv2-migration-audit.log`):
- Pipe-delimited lines: `timestamp|action|kind|namespace|name|revertCommand|extra`
- At the end, a "best-effort revert command summary" is printed for quick manual cleanup / rollback.

Backups (in-place mode):
- Raw PVC YAMLs stored under `pvc-backups/<namespace>/`.
- Keep this directory until you are fully confident in the migration; then archive or delete.

---

## 10. Cleanup Report & Post-Migration Tasks

Final section (`print_migration_cleanup_report`) highlights:
- Intermediate PV/PVC candidates (dual mode)
- Snapshots safe to remove
- Released PremiumV2 PVs that still reference the claimRef (especially leftover post rollback or naming transitions)
- Original PV references (in dual) that may no longer be needed once you fully switch workloads

Actions to consider after verifying data integrity:
```bash
# Delete intermediate artifacts (dual mode example)
kubectl delete pvc <pvc>-csi -n <ns>       # if listed & unused
kubectl delete pv  <pv>-csi                # intermediate PV
kubectl delete volumesnapshot ss-<pv>-csi-pv # snapshot if not needed

# Delete released PVs (after ensuring data & rollback not required)
kubectl delete pv <releasedPremiumV2PV>
```

Ensure workloads are successfully using the new PremiumV2 PVC:
```bash
kubectl describe pvc mypvc -n ns | grep -i "StorageClass"
kubectl get pv $(kubectl get pvc mypvc -n ns -o jsonpath='{.spec.volumeName}') -o jsonpath='{.spec.csi.volumeAttributes.skuName}' ; echo
```
Expect `PremiumV2_LRS` (or `skuname` attribute).

---

## 10.1 Original Premium_LRS Disk Lifecycle & Safe Deletion

During migration the scripts intentionally set or preserve `persistentVolumeReclaimPolicy: Retain` on the *original* Premium_LRS PV. This ensures:
- Rollback remains possible (data & PV object remain).
- The underlying managed disk in Azure is **not** deleted automatically when the PVC is deleted or replaced.

Implications:
- After you confirm migration success and no rollback need, those original Premium_LRS disks continue to incur cost until explicitly removed.
- Simply deleting the *PVC* (in dual mode, or an old intermediate PVC) does NOT delete a retained PV’s backing disk.
- Deleting a PV with reclaimPolicy=Retain only removes the Kubernetes PV object; the Azure managed disk still survives (becomes an “unattached” disk visible in your resource group).

Recommended deletion workflow (when you are 100% certain rollback is unnecessary):

1. Identify the original PV(s):
   ```bash
   # For a migrated PVC
   kubectl get pvc mypvc -n ns -o jsonpath='{.metadata.annotations["disk.csi.azure.com/rollback-orig-pv"]}'; echo
   # Or from audit log or cleanup report
   ```

2. (Optional) Final verification:
   - Mount / read data from the new PremiumV2 PVC.
   - Confirm application-level integrity checks (db consistency, file checksums).

3. Decide whether you want Kubernetes to clean up the disk automatically or to delete manually:
   - Automatic deletion path:
     a. Patch reclaim policy to Delete.
     b. Delete the PV (Kubernetes will then ask the in-tree/CSI provisioner to release & remove the Azure disk—verify it actually does for your scenario; in-tree azureDisk with Retain→Delete patch + PV deletion should delete the managed disk).
   - Manual deletion path:
     a. Delete the PV (still Retain).
     b. Locate and delete the managed disk via Azure CLI / Portal.

4. Automatic deletion path commands:
   ```bash
   PV=orig-pv-name
   # Patch reclaim policy
   kubectl patch pv "$PV" -p '{"spec":{"persistentVolumeReclaimPolicy":"Delete"}}'
   # Double-check
   kubectl get pv "$PV" -o jsonpath='{.spec.persistentVolumeReclaimPolicy}'; echo
   # Delete PV (this should trigger disk deletion)
   kubectl delete pv "$PV"
   ```

5. Manual deletion path commands:
   ```bash
   PV=orig-pv-name
   # Delete PV object but leave disk (policy still Retain)
   kubectl delete pv "$PV"
   # Find disk URI (from prior audit log or:
   #   kubectl get pv $PV -o jsonpath='{.spec.azureDisk.diskURI}'
   # If you noted it earlier, delete with Azure CLI:
   az disk delete --ids "<diskURI>" --yes
   ```

6. Post-deletion validation:
   ```bash
   # PV gone
   kubectl get pv "$PV" || echo "PV deleted"
   # (If automatic) Ensure disk no longer appears:
   az disk show --ids "<diskURI>" || echo "Disk deleted"
   ```

Cautions:
- Never patch to Delete *before* you are certain rollback is unnecessary.
- If you batch-delete PVs after mass migration, maintain an inventory (audit log + cleanup report) so you can reconcile against Azure resource group disks and ensure no unexpected survivors or accidental deletions.
- For Released PVs (phase=Released, reclaimPolicy=Retain) that refer to a PremiumV2 volume you’ve decided to discard:
  - Same procedure applies: patch to Delete then delete PV, or delete PV + disk manually.

Quick identification of retained original PVs older than N days (example 7):
```bash
kubectl get pv -o json \
  | jq -r '
      [.items[]
        | select(.spec.persistentVolumeReclaimPolicy=="Retain")
        | select(.status.phase=="Released" or .status.phase=="Available")
        | {name:.metadata.name, creation:.metadata.creationTimestamp}
      ]
      | map(select(((now - ( .creation | fromdateiso8601 ))/86400) > 7))
      | .[]
      | "\(.name)\t\(.creation)"'
```

Summary:
- Migration scripts keep you safe by retaining source disks.
- You must perform an explicit, audited cleanup pass to avoid ongoing Premium_LRS cost.
- Choose Delete vs manual removal consciously, and record what was deleted (append to your audit log or change tracking system).

---

## 11. Recommended Validation Before Scaling Up

1. Single test PVC end-to-end (snapshot + bind + event completion + cleanup).
2. Run read/write workload (e.g., fill a file, checksum before & after).
3. Confirm no unexpected resizing or mode changes.
4. Validate rollback path (simulate a forced rollback on a dummy PVC).
5. Check storage account / disk inventory (ensure no orphaned or unexpectedly deleted disks).
6. Measure migration time per PVC to forecast batch durations.

---

## 12. Scaling Strategy

- Start with `MAX_PVCS=5` (pilot)
- Increase to 20–30 (during low traffic)
- Always review cleanup report before next batch
- Keep a gap (e.g., 15–30 minutes) between batches to allow asynchronous controller reconciliation and quota feedback.

---

## 13. Troubleshooting

| Symptom | Likely Cause | Action |
|---------|--------------|--------|
| Syntax error near `)` | Hidden character or earlier unclosed heredoc | Run `bash -n` and retype suspicious line; normalize line endings |
| Snapshot never ready | Snapshot class or CSI driver issues | `kubectl describe volumesnapshot` and underlying `VolumeSnapshotContent` |
| PVC stuck Pending | Missing `*-pv2` StorageClass or quota | Verify SC creation logs and `kubectl describe pvc` |
| No `SKUMigration*` events | Controller not emitting or watch delay | Force in-progress label (script auto after threshold) |
| Released PV leftovers | Rollback or partial batch | Confirm not needed → delete PV |
| Rollback failed to rebind | claimRef not cleared or PV reclaimPolicy=Delete | Ensure reclaimPolicy changed to Retain earlier |

---

## 14. Safety & Data Integrity Notes

- The script relies on *snapshots*; ensure snapshot storage is regionally redundant as per policy.
- Retain raw PVC backups and audit logs until a post-migration verification window closes.
- Consider taking an application-layer backup (database dump, etc.) before large waves.

---

## 15. Example End-to-End (In-Place, Small Batch)

```bash
# Label a target PVC
kubectl label pvc data-app-a -n team-a disk.csi.azure.com/pv2migration=true

# Run migration
cd hack
MAX_PVCS=1 BIND_TIMEOUT_SECONDS=120 MONITOR_TIMEOUT_MINUTES=60 \
  ./premium-to-premiumv2-migrator-inplace.sh | tee mig-inplace-a.log

# Inspect cleanup report in output
# Verify migration:
kubectl get pvc data-app-a -n team-a -o wide
kubectl describe pv $(kubectl get pvc data-app-a -n team-a -o jsonpath='{.spec.volumeName}') | grep -i sku
```

---

## 16. After Everything Looks Good

- Archive `pv1-pv2-migration-audit.log`
- Archive or prune `pvc-backups/`
- Remove migration label from PVCs (optional):
  ```
  kubectl label pvc data-app-a -n team-a disk.csi.azure.com/pv2migration-
  ```
- Enforce a policy (OPA / Kyverno) to prefer PremiumV2 SCs for new claims.

---

## 17. Limitations / Non-Goals

- Does not auto-resize or convert AccessModes.
- Does not verify disk-level performance metrics.
- Does not handle non-Premium_LRS source SKUs (skips them).
- Does not auto-delete original PVs; leaves operator in control.

---

## 18. Contributing / Extending

Ideas:
- Add a dry-run (`NO_MUTATE=true`) that stops before creations.
- Metrics export (JSON summary).
- Parallelization with rate limits.

Open a PR if you extend; keep safety-first defaults.

---

## 19. Quick Variable Cheat Sheet

```
# Common overrides:
export MAX_PVCS=10
export MIG_SUFFIX=csi
export MONITOR_TIMEOUT_MINUTES=180
export BIND_TIMEOUT_SECONDS=120
export WORKLOAD_DETACH_TIMEOUT_MINUTES=15
export BACKUP_ORIGINAL_PVC=true
export ROLLBACK_ON_TIMEOUT=true
```

---

## 20. Final Checklist (Per Batch)

1. Label set? (Only intended PVCs show up)
2. `bash -n` passes
3. Run script → watch logs until summary
4. Review cleanup report
5. Verify data & app workload on PremiumV2
6. Cleanup intermediate / snapshot artifacts as appropriate
7. Archive audit + backups
8. Proceed to next batch

---

Happy migrating! Review each summary carefully—intentional operator review is a built-in safety step, not an inefficiency.