#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

SCRIPT_START_TS="$(date +'%Y-%m-%dT%H:%M:%S')"
SCRIPT_START_EPOCH="$(date +%s)"
MODE=inplace

# In-place rollback keys
ROLLBACK_PVC_YAML_ANN="${ROLLBACK_PVC_YAML_ANN:-disk.csi.azure.com/rollback-pvc-yaml}"
ROLLBACK_ORIG_PV_ANN="${ROLLBACK_ORIG_PV_ANN:-disk.csi.azure.com/rollback-orig-pv}"

# Declarations
declare -a MIG_PVCS
declare -a PREREQ_ISSUES
declare -a CONFLICT_ISSUES
declare -A SC_SET
declare -a ROLLBACK_FAILURES

# Explicit zeroing (defensive; avoids 'unbound variable' under set -u even if declarations are edited later)
MIG_PVCS=()
PREREQ_ISSUES=()
CONFLICT_ISSUES=()
ROLLBACK_FAILURES=()

# Helper: safe length (avoids accidental nounset trip if future refactor removes a declare)
safe_array_len() {
  # usage: safe_array_len arrayName
  local name="$1"
  declare -p "$name" &>/dev/null || { echo 0; return; }
  # indirect expansion
  eval "echo \${#$name[@]}"
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
. "${SCRIPT_DIR}/lib-premiumv2-migration-common.sh"

# Run RBAC preflight (mode=inplace)
MIGRATION_MODE=inplace migration_rbac_check || { err "Aborting due to insufficient permissions."; exit 1; }

info "Migration label: $MIGRATION_LABEL"
info "Target snapshot class: $SNAPSHOT_CLASS"
info "Max PVCs: $MAX_PVCS  MIG_SUFFIX=$MIG_SUFFIX  WAIT_FOR_WORKLOAD=$WAIT_FOR_WORKLOAD"

# Snapshot, SC variant, workload detach, event helpers now in lib
ensure_snapshot_class   # from common lib

# --- Conflict / prerequisite logic reused only here (kept local) ---
ensure_no_foreign_conflicts() {
  info "Checking for pre-existing conflicting objects not created by this tool..."
  if kcmd get volumesnapshotclass "$SNAPSHOT_CLASS" >/dev/null 2>&1; then
    if ! kcmd get volumesnapshotclass "$SNAPSHOT_CLASS" -o json | jq -e \
         --arg k "$CREATED_BY_LABEL_KEY" --arg v "$MIGRATION_TOOL_ID" \
         '.metadata.labels[$k]==$v' >/dev/null; then
      CONFLICT_ISSUES+=("VolumeSnapshotClass/$SNAPSHOT_CLASS (missing label)")
    fi
  fi
  for ENTRY in "${MIG_PVCS[@]}"; do
    local ns="${ENTRY%%|*}" pvc="${ENTRY##*|}"
    local pv snap
    pv=$(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
    [[ -z "$pv" ]] && continue
    snap="$(name_snapshot "$pv")"
    if kcmd get volumesnapshot "$snap" -n "$ns" >/dev/null 2>&1; then
      kcmd get volumesnapshot "$snap" -n "$ns" -o json | jq -e \
         --arg k "$CREATED_BY_LABEL_KEY" --arg v "$MIGRATION_TOOL_ID" \
         '.metadata.labels[$k]==$v' >/dev/null || \
         CONFLICT_ISSUES+=("VolumeSnapshot/$ns/$snap missing label")
    fi
  done
  if (( $(safe_array_len CONFLICT_ISSUES) > 0 )); then
    err "Conflict check failed ($(safe_array_len CONFLICT_ISSUES) items)."
    printf '  - %s\n' "${CONFLICT_ISSUES[@]}"
    exit 1
  fi
  ok "No conflicting pre-existing objects detected."
}

# ---------------- Discover Tagged PVCs ----------------
populate_pvcs

# ---------------- Pre-Requisite Validation (mirrors dual script) ----------------
print_combined_validation_report_and_exit_if_needed() {
  local prereq_count=$(safe_array_len PREREQ_ISSUES)
  local conflict_count=$(safe_array_len CONFLICT_ISSUES)
  if (( prereq_count==0 && conflict_count==0 )); then
    ok "Pre-req & conflict checks passed."
    return
  fi
  echo
  err "Pre-run validation failed:"
  (( prereq_count>0 )) && { echo "  Pre-requisite issues ($prereq_count):"; printf '    - %s\n' "${PREREQ_ISSUES[@]}"; }
  (( conflict_count>0 )) && { echo "  Naming conflicts ($conflict_count):"; printf '    - %s\n' "${CONFLICT_ISSUES[@]}"; }
  echo
  err "Resolve issues and re-run. (No mutations performed.)"
  exit 1
}

run_prerequisites_and_conflicts() {
  run_prerequisites_checks
  ensure_no_foreign_conflicts
  print_combined_validation_report_and_exit_if_needed
}

run_prerequisites_and_conflicts

# ------------- Collect Unique Source SCs -------------
create_pv2_unique_storage_classes

create_pv2_inplace() {
  local ns="$1" pvc="$2"

  # Capture and sanitize original PVC definition (remove status & runtime metadata).
  # We keep:
  #   apiVersion, kind
  #   metadata: name, namespace, labels, filtered annotations
  #   spec (including volumeName for re-binding on rollback)
  # Removed metadata fields: resourceVersion, uid, generation, creationTimestamp, managedFields, finalizers.
  # Removed annotations with infra/runtime prefixes: pv.kubernetes.io/, volume.kubernetes.io/, kubectl.kubernetes.io/, control-plane., and common bind helpers.
  local orig_json
  orig_json=$(kcmd get pvc "$pvc" -n "$ns" -o json | jq '{
      apiVersion,
      kind,
      metadata: {
        name: .metadata.name,
        namespace: .metadata.namespace,
        labels: (.metadata.labels // {}),
        annotations: (
          (.metadata.annotations // {})
          | with_entries(
              select(
                .key
                | test("^(pv\\.kubernetes\\.io/|volume\\.kubernetes\\.io/|kubectl\\.kubernetes\\.io/|control-plane\\.|kubernetes\\.io/created-by)$")
                | not
              )
            )
        )
      },
      spec
    }')

  # Extract fields needed for pv2 PVC creation (from sanitized JSON, not raw kubectl yaml)
  local sc size mode
  sc=$(echo "$orig_json" | jq -r '.spec.storageClassName // empty')
  size=$(echo "$orig_json" | jq -r '.spec.resources.requests.storage // empty')
  mode=$(echo "$orig_json" | jq -r '.spec.volumeMode // "Filesystem"')

  # Fail early if unexpectedly missing critical spec details
  [[ -z "$sc" || -z "$size" ]] && { err "Sanitized original PVC missing storageClass or size (sc='$sc' size='$size')"; return 1; }

  local pv_before
  pv_before=$(get_pv_of_pvc "$ns" "$pvc")
  [[ -z "$pv_before" ]] && { err "Original PVC has no bound PV (cannot proceed)"; return 1; }
  local snap="$(name_snapshot "$pv_before")"

  ensure_reclaim_policy_retain "$pv_before"

  # -------- Raw PVC backup (full YAML) prior to deletion --------
  if [[ "$BACKUP_ORIGINAL_PVC" == "true" ]]; then
    local ts backup_ns_dir backup_file tmp_file
    ts="$(date +%Y%m%d-%H%M%S)"
    backup_ns_dir="${PVC_BACKUP_DIR}/${ns}"
    mkdir -p "$backup_ns_dir"
    tmp_file="${backup_ns_dir}/${pvc}-${ts}.yaml.tmp"
    backup_file="${backup_ns_dir}/${pvc}-${ts}.yaml"
    info "Backing up original PVC $ns/$pvc to $backup_file"
    if ! kcmd get pvc "$pvc" -n "$ns" -o yaml >"$tmp_file" 2>/dev/null; then
      err "Failed to fetch PVC $ns/$pvc for backup; skipping migration of this PVC."
      audit_add "PersistentVolumeClaim" "$pvc" "$ns" "backup-failed" "kubectl get pvc $pvc -n $ns -o yaml" "dest=$backup_file reason=kubectlError"
      return 1
    fi
    if [[ ! -s "$tmp_file" ]] || ! grep -q '^kind: *PersistentVolumeClaim' "$tmp_file"; then
      err "Backup validation failed for $ns/$pvc (empty or malformed); skipping migration."
      rm -f "$tmp_file"
      audit_add "PersistentVolumeClaim" "$pvc" "$ns" "backup-invalid" "kubectl get pvc $pvc -n $ns -o yaml" "dest=$backup_file reason=validation"
      return 1
    fi
    mv "$tmp_file" "$backup_file"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "delete" "kubectl create -f $backup_file" "pv2=false sc=${sc}"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "backup" "rm -f $backup_file" "dest=$backup_file size=$(wc -c <"$backup_file")B"
  else
    info "BACKUP_ORIGINAL_PVC=false -> skipping raw PVC backup for $ns/$pvc"
  fi
  # ---------------------------------------------------------------

  info "Deleting original PVC $ns/$pvc (PV retained for rollback)"
  kcmd delete pvc "$pvc" -n "$ns" --wait=true 2>/dev/null
  audit_add "PersistentVolumeClaim" "$pvc" "$ns" "delete" "N/A" "stage=replace retainedPV=${pv_before}"

  # Derive pv2 StorageClass (we expect <originalSC>-pv2 exists from earlier variant creation)
  local pv2_sc="${sc%-pv1}-pv2"

  # Base64 encode sanitized JSON for rollback annotation
  local encoded_spec
  encoded_spec=$(printf '%s' "$orig_json" | jq 'del(.status) | {
      apiVersion,
      kind,
      metadata: {
        name: .metadata.name,
        namespace: .metadata.namespace,
        labels: (.metadata.labels // {}),
        annotations: (
          (.metadata.annotations // {})
          | with_entries(
              select(
                .key
                | test("^(pv\\.kubernetes\\.io/|volume\\.kubernetes\\.io/|kubectl\\.kubernetes\\.io/|control-plane\\.|kubernetes\\.io/created-by)$")
                | not
              )
            )
        )
      },
      spec
    }' | b64e)

  info "Creating PV2 replacement PVC $ns/$pvc (SC=$pv2_sc snapshot=$snap)"
  if ! kapply_retry <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: ${pvc}
  namespace: ${ns}
  labels:
    ${CREATED_BY_LABEL_KEY}: ${MIGRATION_TOOL_ID}
    ${MIGRATION_LABEL_KEY}: "${MIGRATION_LABEL_VALUE}"
  annotations:
    ${ROLLBACK_PVC_YAML_ANN}: "${encoded_spec}"
    ${ROLLBACK_ORIG_PV_ANN}: "${pv_before}"
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: ${size}
  volumeMode: ${mode}
  storageClassName: ${pv2_sc}
  dataSource:
    name: ${snap}
    kind: VolumeSnapshot
    apiGroup: snapshot.storage.k8s.io
EOF
  then
    err "Failed to create PV2 PVC ${ns}/${pvc}"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "create-failed" "kubectl get pvc $pvc -n $ns -o yaml" "pv2=true sc=${pv2_sc} snapshot=${snap} rollbackPV=${pv_before} reason=applyError"
    return 1
  else
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "create" "kubectl delete pvc $pvc -n $ns" "pv2=true sc=${pv2_sc} snapshot=${snap} rollbackPV=${pv_before}"
  fi

  if wait_pvc_bound "$ns" "$pvc" "$BIND_TIMEOUT_SECONDS"; then
    ok "PVC $ns/$pvc bound PremiumV2"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "bound" "kubectl describe pvc $pvc -n $ns" "pv2=true"
    return 0
  fi

  warn "PVC $ns/$pvc not bound within timeout (${BIND_TIMEOUT_SECONDS}s)"
  audit_add "PersistentVolumeClaim" "$pvc" "$ns" "bind-timeout" "kubectl describe pvc $pvc -n $ns" "pv2=true timeout=${BIND_TIMEOUT_SECONDS}s"
  return 2
}

rollback_inplace() {
  local ns="$1" pvc="$2"
  warn "Rollback for $ns/$pvc"
  local encoded
  encoded=$(kcmd get pvc "$pvc" -n "$ns" -o go-template="{{ index .metadata.annotations \"${ROLLBACK_PVC_YAML_ANN}\" }}" 2>/dev/null || true)
  if [[ -z "$encoded" ]]; then
    warn "No rollback annotation"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-skip" "N/A" "reason=noAnnotation"
    ROLLBACK_FAILURES+=("${ns}/${pvc}:no-annotation")
    return 1
  fi
  local spec_doc
  spec_doc=$(printf '%s' "$encoded" | b64d)

  # Delete pv2 PVC (best-effort)
  if ! kcmd delete pvc "$pvc" -n "$ns" --wait=true; then
    warn "Rollback delete failed (continuing) for $ns/$pvc"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-delete-failed" "N/A" "reason=deleteError"
    # Continue – apply may still succeed if object was already gone
  else
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-delete" "N/A" ""
  fi

  # Clear the claimRef on the original PV to allow re-binding
  local orig_pv
  orig_pv=$(printf '%s' "$spec_doc" | jq -r '.spec.volumeName // empty')
  if [[ -z "$orig_pv" ]]; then
    warn "Rollback missing original PV name in spec"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-missing-pv" "N/A" "reason=missingPV"
    ROLLBACK_FAILURES+=("${ns}/${pvc}:missing-pv")
    return 1
  fi
  if ! kcmd patch pv "$orig_pv" -p '{"spec":{"claimRef":null}}' >/dev/null 2>&1; then
    warn "Rollback clear claimRef failed for PV $orig_pv"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-clear-claimref-failed" "kubectl describe pv $orig_pv" "reason=patchError"
    ROLLBACK_FAILURES+=("${ns}/${pvc}:clear-claimref-failed")
    return 1
  fi

  # Recreate original PVC using retrying apply
  if ! printf '%s\n' "$spec_doc" | kapply_retry; then
    err "Rollback apply failed for $ns/$pvc"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-restore-failed" "kubectl get pvc $pvc -n $ns -o yaml" "original=true reason=applyError"
    ROLLBACK_FAILURES+=("${ns}/${pvc}:apply-failed")
    return 1
  fi
  audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-restore" "kubectl delete pvc $pvc -n $ns" "original=true"

  if wait_pvc_bound "$ns" "$pvc" 600; then
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-bound" "kubectl describe pvc $pvc -n $ns" ""
    return 0
  else
    warn "Rollback PVC $ns/$pvc not yet bound (600s timeout)"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-bind-timeout" "kubectl describe pvc $pvc -n $ns" "timeout=600s"
    ROLLBACK_FAILURES+=("${ns}/${pvc}:bind-timeout")
    return 1
  fi
}

# ------------- Main Migration Loop -------------
for ENTRY in "${MIG_PVCS[@]}"; do
  pvc_ns="${ENTRY%%|*}"
  pvc="${ENTRY##*|}"

  if ! check_premium_lrs "$pvc" "$pvc_ns"; then
    info "PVC $pvc_ns/$pvc not Premium_LRS -> skip"
    continue
  fi

  DONE_LABEL=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" 2>/dev/null || true)
  [[ "$DONE_LABEL" == "$MIGRATION_DONE_LABEL_VALUE" ]] && { info "Already migrated $pvc_ns/$pvc"; continue; }

  pv=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
  [[ -z "$pv" ]] && { warn "PVC $pvc_ns/$pvc not bound; skip"; continue; }

  if [[ "$WAIT_FOR_WORKLOAD" == "true" ]]; then
    attachments=$(kcmd get volumeattachment -o jsonpath="{range .items[?(@.spec.source.persistentVolumeName=='$pv')]}{.metadata.name}{'\n'}{end}" 2>/dev/null || true)
    if [[ -n "$attachments" ]]; then
      wait_for_workload_detach "$pv" "$pvc" "$pvc_ns" || { warn "Still attached -> skip $pvc_ns/$pvc"; continue; }
    fi
  fi

  ensure_reclaim_policy_retain "$pv"

  mode=$(kcmd get pv "$pv" -o jsonpath='{.spec.volumeMode}' 2>/dev/null || true)
  sc=$(kcmd get pv "$pv" -o jsonpath='{.spec.storageClassName}' 2>/dev/null || true)
  size=$(kcmd get pv "$pv" -o jsonpath='{.spec.capacity.storage}' 2>/dev/null || true)
  diskuri=$(kcmd get pv "$pv" -o jsonpath='{.spec.azureDisk.diskURI}' 2>/dev/null || true)

  csi_driver=$(kcmd get pv "$pv" -o jsonpath='{.spec.csi.driver}' 2>/dev/null || true)
  if [[ -n "$diskuri" ]]; then
    if [[ -z "$sc" || -z "$size" ]]; then warn "Missing sc/size for in-tree $pvc_ns/$pvc"; continue; fi
    kcmd get sc "${sc}-pv1" >/dev/null 2>&1 || { warn "Missing ${sc}-pv1"; continue; }
    kcmd get sc "${sc}-pv2" >/dev/null 2>&1 || { warn "Missing ${sc}-pv2"; continue; }
    create_csi_pv_pvc "$pvc" "$pvc_ns" "$pv" "$size" "$mode" "$sc" "$diskuri"
    snapshot_source_pvc="$(name_csi_pvc "$pvc")"
  else
    [[ "$csi_driver" != "disk.csi.azure.com" ]] && { warn "Unknown PV driver for $pv"; continue; }
    if [[ -z "$sc" || -z "$size" ]]; then warn "Missing sc/size for CSI $pvc_ns/$pvc"; continue; fi
    kcmd get sc "${sc}-pv2" >/dev/null 2>&1 || { warn "Missing ${sc}-pv2"; continue; }
    snapshot_source_pvc="$pvc"
  fi

  ensure_snapshot "$snapshot_source_pvc" "$pvc_ns" "$pv" || { warn "Snapshot failed $pvc_ns/$pvc"; audit_add "PersistentVolumeClaim" "$pvc" "$pvc_ns" "snapshot-failed" "kubectl describe pvc $pvc -n $pvc_ns" ""; continue; }
  run_without_errexit create_pv2_inplace "$pvc_ns" "$pvc"
  rc=$?
  if [[ $rc -eq 0 ]]; then
    ok "PV2 creation success $pvc_ns/$pvc"
    audit_add "PersistentVolumeClaim" "$pvc" "$pvc_ns" "migrated" "kubectl describe pvc $pvc -n $pvc_ns" "mode=inplace"
  elif [[ $rc -eq 2 ]]; then
    warn "Timeout $pvc_ns/$pvc"
    audit_add "PersistentVolumeClaim" "$pvc" "$pvc_ns" "migrate-timeout" "kubectl describe pvc $pvc -n $pvc_ns" "mode=inplace"
    [[ "$ROLLBACK_ON_TIMEOUT" == "true" ]] && rollback_inplace "$pvc_ns" "$pvc" || true
  else
    warn "Migration failure rc=$rc $pvc_ns/$pvc"
    audit_add "PersistentVolumeClaim" "$pvc" "$pvc_ns" "migrate-failed" "kubectl describe pvc $pvc -n $pvc_ns" "mode=inplace rc=$rc"
    [[ "$ROLLBACK_ON_TIMEOUT" == "true" ]] && rollback_inplace "$pvc_ns" "$pvc" || true
  fi
done

# ------------- Monitoring Loop -------------
deadline=$(( $(date +%s) + MONITOR_TIMEOUT_MINUTES*60 ))
info "Monitoring migrations (timeout ${MONITOR_TIMEOUT_MINUTES}m)..."

while true; do
  ALL_DONE=true
  for ENTRY in "${MIG_PVCS[@]}"; do
    pvc_ns="${ENTRY%%|*}" pvc="${ENTRY##*|}" pv2_pvc="$pvc"
    if kcmd get pvc $pvc -n $pvc_ns -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" | grep -q "^${MIGRATION_DONE_LABEL_VALUE}\$"; then
      continue
    fi
    STATUS=$(kcmd get pvc "$pv2_pvc" -n "$pvc_ns" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    [[ "$STATUS" != "Bound" ]] && ALL_DONE=false
    reason=$(extract_event_reason "$pvc_ns" "$pv2_pvc")
    case "$reason" in
      SKUMigrationCompleted)
        mark_source_done "$pvc_ns" "$pvc"
        ok "Completed $pvc_ns/$pvc"
        ;;
      SKUMigrationProgress|SKUMigrationStarted)
        info "$reason $pvc_ns/$pv2_pvc"; ALL_DONE=false ;;
      ReasonSKUMigrationTimeout)
        rollback_inplace "$pvc_ns" "$pv2_pvc" || true
        warn "$reason $pvc_ns/$pv2_pvc"; ALL_DONE=false ;;
      "")
        info "No migration events yet for $pvc_ns/$pv2_pvc"; ALL_DONE=false ;;
    esac
    if [[ "$STATUS" == "Bound" && -z "$reason" && $MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES -gt 0 ]]; then
      orig_pv=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
      [[ -z "$orig_pv" ]] && continue
      inprog_val=$(kcmd get pv "$orig_pv" -o go-template="{{ index .metadata.labels \"${MIGRATION_INPROGRESS_LABEL_KEY}\" }}" 2>/dev/null || true)
      if [[ "$inprog_val" != "$MIGRATION_INPROGRESS_LABEL_VALUE" ]]; then
        cts=$(kcmd get pvc "$pv2_pvc" -n "$pvc_ns" -o jsonpath='{.metadata.creationTimestamp}' 2>/dev/null || true)
        if [[ -n "$cts" ]]; then
          creation_epoch=$(date -d "$cts" +%s 2>/dev/null || echo 0)
          now_epoch=$(date +%s)
            if (( now_epoch - creation_epoch >= MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES * 60 )); then
              warn "Forcing ${MIGRATION_INPROGRESS_LABEL_KEY}=${MIGRATION_INPROGRESS_LABEL_VALUE} on PV $orig_pv (no events)"
              kcmd label pv "$orig_pv" "${MIGRATION_INPROGRESS_LABEL_KEY}=${MIGRATION_INPROGRESS_LABEL_VALUE}" --overwrite
              audit_add "PersistentVolume" "$orig_pv" "" "label" \
                "kubectl label pv $orig_pv ${MIGRATION_INPROGRESS_LABEL_KEY}-" \
                "forced=true reason=noEventsAfter${MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES}m"
            fi
        fi
      fi
    fi
  done
  if [[ "$ALL_DONE" == "true" ]]; then
    ok "All processed PVCs migrated or already labeled."
    break
  fi
  if (( $(date +%s) >= deadline )); then
    warn "Monitor timeout reached."
    for ENTRY in "${MIG_PVCS[@]}"; do
      pvc_ns="${ENTRY%%|*}" pvc="${ENTRY##*|}"
      kcmd get pvc "$pvc" -n "$pvc_ns" -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" | \
        grep -q "^${MIGRATION_DONE_LABEL_VALUE}\$" && continue
      warn "Timeout rollback $pvc_ns/$pvc"
      rollback_inplace "$pvc_ns" "$pvc" || true
    done
    break
  fi
  sleep "$POLL_INTERVAL"
done

# ------------- Summary & Audit -------------
echo
info "Summary:"
for entry in "${MIG_PVCS[@]}"; do
  ns="${entry%%|*}" pvc="${entry##*|}"
  lbl=$(kcmd get pvc "$pvc" -n "$ns" -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" 2>/dev/null || true)
  if [[ "$lbl" == "$MIGRATION_DONE_LABEL_VALUE" ]]; then
    echo "  ✓ $ns/$pvc migrated"
  else
    echo "  • $ns/$pvc NOT completed"
  fi
done

MIGRATION_MODE=inplace print_migration_cleanup_report
finalize_audit_summary "$SCRIPT_START_TS" "$SCRIPT_START_EPOCH"

if (( ${#ROLLBACK_FAILURES[@]} > 0 )); then
  echo
  warn "Rollback failures:"
  printf '  - %s\n' "${ROLLBACK_FAILURES[@]}"
fi

ok "Script finished."