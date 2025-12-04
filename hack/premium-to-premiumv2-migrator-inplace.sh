#!/usr/bin/env bash
# shellcheck source=./lib-premiumv2-migration-common.sh
set -euo pipefail
IFS=$'\n\t'

SCRIPT_START_TS="$(date +'%Y-%m-%dT%H:%M:%S')"
SCRIPT_START_EPOCH="$(date +%s)"
MODE=inplace

# Declarations
declare -a MIG_PVCS
declare -a PREREQ_ISSUES
declare -a CONFLICT_ISSUES
declare -a ROLLBACK_FAILURES
declare -a NON_DETACHED_PVCS        # PVCs skipped because workload still attached
declare -A NON_DETACHED_SET         # membership map ns|pvc -> 1

# Explicit zeroing (defensive; avoids 'unbound variable' under set -u even if declarations are edited later)
MIG_PVCS=()
PREREQ_ISSUES=()
CONFLICT_ISSUES=()
ROLLBACK_FAILURES=()
NON_DETACHED_PVCS=()
NON_DETACHED_SET=()

cleanup_on_error() {
  finalize_audit_summary "$SCRIPT_START_TS" "$SCRIPT_START_EPOCH" || true
  err "Script failed (exit=$rc); cleanup_on_error ran."
}
trap 'rc=$?; if [ $rc -ne 0 ]; then cleanup_on_error $rc; fi' EXIT

# Helper: safe length (avoids accidental nounset trip if future refactor removes a declare)
safe_array_len() {
  # usage: safe_array_len arrayName
  local name="$1"
  declare -p "$name" &>/dev/null || { echo 0; return; }
  # indirect expansion
  eval "echo \${#${name}[@]}"
}

# Zonal-aware helper lib
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ZONAL_HELPER_LIB="${SCRIPT_DIR}/premium-to-premiumv2-zonal-aware-helper.sh"
[[ -f "$ZONAL_HELPER_LIB" ]] || { echo "[ERROR] Missing lib: $ZONAL_HELPER_LIB" >&2; exit 1; }
# shellcheck disable=SC1090
. "$ZONAL_HELPER_LIB"

# Run RBAC preflight (mode=inplace)
MODE=$MODE migration_rbac_check || { err "Aborting due to insufficient permissions."; exit 1; }

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
MODE=$MODE process_pvcs_for_zone_preparation

# ---------------- Pre-Requisite Validation (mirrors dual script) ----------------
print_combined_validation_report_and_exit_if_needed() {
  local prereq_count
  prereq_count=$(safe_array_len PREREQ_ISSUES)
  local conflict_count
  conflict_count=$(safe_array_len CONFLICT_ISSUES)
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
create_unique_storage_classes

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

  if [[ -z "$spec_doc" ]]; then
    warn "Empty rollback spec"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-empty-spec" "N/A" "reason=emptySpec"
    ROLLBACK_FAILURES+=("${ns}/${pvc}:empty-spec")
    return 1
  fi

  # Delete pv2 PVC (best-effort)
  encoded_spec=$(get_pvc_encoded_json "$pvc" "$ns")
  if ! kcmd delete pvc "$pvc" -n "$ns" --wait=true; then
    warn "Rollback delete failed (continuing) for $ns/$pvc"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-delete-failed" "N/A" "reason=deleteError"
    # Continue – apply may still succeed if object was already gone
  else
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-delete" "kubectl create -f <(echo \"$encoded_spec\" | base64 --decode) " "inplace=true reason=rollbackreplace"
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
declare -A PVC_SNAPSHOTS
for ENTRY in "${MIG_PVCS[@]}"; do
  pvc_ns="${ENTRY%%|*}"
  pvc="${ENTRY##*|}"

  if check_premiumv2_lrs "$pvc_ns" "$pvc"; then
    info "PVC $pvc_ns/$pvc not PremiumV2_LRS -> skip"
    continue
  fi

  DONE_LABEL=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" 2>/dev/null || true)
  [[ "$DONE_LABEL" == "$MIGRATION_DONE_LABEL_VALUE" ]] && { info "Already migrated $pvc_ns/$pvc"; continue; }

  pv=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
  [[ -z "$pv" ]] && { warn "PVC $pvc_ns/$pvc not bound; skip"; continue; }

  if [[ "$WAIT_FOR_WORKLOAD" == "true" ]]; then
    attachments=$(kcmd get volumeattachment -o jsonpath="{range .items[?(@.spec.source.persistentVolumeName=='$pv')]}{.metadata.name}{'\n'}{end}" 2>/dev/null || true)
    if [[ -n "$attachments" ]]; then
      if ! wait_for_workload_detach "$pv" "$pvc" "$pvc_ns"; then
        warn "Workload still attached -> deferring migration of $pvc_ns/$pvc (no labels changed)"
        NON_DETACHED_PVCS+=("${pvc_ns}|${pvc}")
        NON_DETACHED_SET["${pvc_ns}|${pvc}"]=1
        continue
      fi
    fi
  fi

  ensure_reclaim_policy_retain "$pv"

  mode=$(kcmd get pv "$pv" -o jsonpath='{.spec.volumeMode}' 2>/dev/null || true)
  sc=$(get_sc_of_pvc "$pvc" "$pvc_ns")
  size=$(kcmd get pv "$pv" -o jsonpath='{.spec.capacity.storage}' 2>/dev/null || true)
  diskuri=$(kcmd get pv "$pv" -o jsonpath='{.spec.azureDisk.diskURI}' 2>/dev/null || true)
  scpv2="$(name_pv2_sc "$sc")"

  csi_driver=$(kcmd get pv "$pv" -o jsonpath='{.spec.csi.driver}' 2>/dev/null || true)
  if [[ -n "$diskuri" ]]; then
    if [[ -z "$sc" || -z "$size" ]]; then warn "Missing sc/size for in-tree $pvc_ns/$pvc"; continue; fi
    scpv1="$(name_pv1_sc "$sc")"
    kcmd get sc "${scpv1}" >/dev/null 2>&1 || { warn "Missing ${scpv1}"; continue; }
    create_csi_pv_pvc "$pvc" "$pvc_ns" "$pv" "$size" "$mode" "${scpv1}" "$diskuri" || { warn "Failed to create CSI PV/PVC for $pvc_ns/$pvc"; continue; }
    snapshot_source_pvc="$(name_csi_pvc "$pvc")"
  else
    [[ "$csi_driver" != "disk.csi.azure.com" ]] && { warn "Unknown PV driver for $pv"; continue; }
    if [[ -z "$scpv2" || -z "$size" ]]; then warn "Missing sc/size for CSI $pvc_ns/$pvc"; continue; fi
    kcmd get sc "${scpv2}" >/dev/null 2>&1 || { warn "Missing ${scpv2}"; continue; }
    snapshot_source_pvc="$pvc"
  fi

  snapshot="$(name_snapshot "$pv")"
  create_snapshot "$snapshot" "$snapshot_source_pvc" "$pvc_ns" "$pv" || { warn "Snapshot failed $pvc_ns/$pvc"; continue; }

  PVC_SNAPSHOTS+=("${pvc_ns}|${snapshot}|${pvc}")
done

wait_for_snapshots_ready

# ------------- Main Migration Loop -------------
SOURCE_SNAPSHOTS=("${!PVC_SNAPSHOTS[@]}")
for ENTRY in "${SOURCE_SNAPSHOTS[@]}"; do
  IFS='|' read -r pvc_ns snapshot pvc <<< "$ENTRY"
  pv=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)

  mode=$(kcmd get pv "$pv" -o jsonpath='{.spec.volumeMode}' 2>/dev/null || true)
  sc=$(get_sc_of_pvc "$pvc" "$pvc_ns")
  size=$(kcmd get pv "$pv" -o jsonpath='{.spec.capacity.storage}' 2>/dev/null || true)
  diskuri=$(kcmd get pv "$pv" -o jsonpath='{.spec.azureDisk.diskURI}' 2>/dev/null || true)
  scpv2="$(name_pv2_sc "$sc")"

  run_without_errexit create_pvc_from_snapshot "$pvc" "$pvc_ns" "$pv" "$size" "$mode" "$scpv2" "$pvc" "$snapshot"
  rc=$LAST_RUN_WITHOUT_ERREXIT_RC
  if [[ $rc -eq 0 ]]; then
    ok "PV2 creation success $pvc_ns/$pvc"
    audit_add "PersistentVolumeClaim" "$pvc" "$pvc_ns" "migrated" "kubectl describe pvc $pvc -n $pvc_ns" "mode=inplace"
  elif [[ $rc -eq 2 ]]; then
    warn "Timeout $pvc_ns/$pvc"
    audit_add "PersistentVolumeClaim" "$pvc" "$pvc_ns" "migrate-timeout" "kubectl describe pvc $pvc -n $pvc_ns" "mode=inplace"
    if [[ "$ROLLBACK_ON_TIMEOUT" == "true" ]]; then
      rollback_inplace "$pvc_ns" "$pvc" || true
    fi
  else
    warn "Migration failure rc=$rc $pvc_ns/$pvc"
    audit_add "PersistentVolumeClaim" "$pvc" "$pvc_ns" "migrate-failed" "kubectl describe pvc $pvc -n $pvc_ns" "mode=inplace rc=$rc"
    if [[ "$ROLLBACK_ON_TIMEOUT" == "true" ]]; then
      rollback_inplace "$pvc_ns" "$pvc" || true
    fi
  fi
done

# ------------- Monitoring Loop -------------
deadline=$(( $(date +%s) + MONITOR_TIMEOUT_MINUTES*60 ))
info "Monitoring migrations (timeout ${MONITOR_TIMEOUT_MINUTES}m)..."

while true; do
  ALL_DONE=true
  for ENTRY in "${MIG_PVCS[@]}"; do
    pvc_ns="${ENTRY%%|*}" pvc="${ENTRY##*|}"

    # Skip monitoring for PVCs we never started due to attachment
    if [[ ${NON_DETACHED_SET["${pvc_ns}|${pvc}"]+x} ]]; then
      continue
    fi

    lbl=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" || true)
    if [[ "$lbl" == "$MIGRATION_DONE_LABEL_VALUE" || "$lbl" == "$MIGRATION_DONE_LABEL_VALUE_FALSE" ]]; then
      continue
    fi

    STATUS=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    [[ "$STATUS" != "Bound" ]] && ALL_DONE=false
    reason=$(extract_event_reason "$pvc_ns" "$pvc")
    case "$reason" in
      SKUMigrationCompleted)
        mark_source_done "$pvc_ns" "$pvc"
        ok "Completed $pvc_ns/$pvc"
        continue
        ;;
      SKUMigrationProgress|SKUMigrationStarted)
        mark_source_in_progress "$pvc_ns" "$pvc"
        info "$reason $pvc_ns/$pvc"; ALL_DONE=false ;;
      ReasonSKUMigrationTimeout)
        rollback_inplace "$pvc_ns" "$pvc" || true
        mark_source_notdone "$pvc_ns" "$pvc"
        warn "$reason $pvc_ns/$pvc"; ALL_DONE=false ;;
      "")
        info "No migration events yet for $pvc_ns/$pvc"; ALL_DONE=false ;;
    esac
    if [[ "$STATUS" == "Bound" && -z "$reason" && $MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES -gt 0 ]]; then
      orig_pv=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
      [[ -z "$orig_pv" ]] && continue
      inprog_val=$(kcmd get pv "$orig_pv" -o go-template="{{ index .metadata.labels \"${MIGRATION_INPROGRESS_LABEL_KEY}\" }}" 2>/dev/null || true)
      if [[ "$inprog_val" != "$MIGRATION_INPROGRESS_LABEL_VALUE" ]]; then
        cts=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o jsonpath='{.metadata.creationTimestamp}' 2>/dev/null || true)
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
      # Ignore non-detached deferred PVCs
      if [[ ${NON_DETACHED_SET["${pvc_ns}|${pvc}"]+x} ]]; then
        continue
      fi
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
if (( ${#ROLLBACK_FAILURES[@]} > 0 )); then
  echo
  warn "Rollback failures:"
  printf '  - %s\n' "${ROLLBACK_FAILURES[@]}"
fi

for entry in "${MIG_PVCS[@]}"; do
  ns="${entry%%|*}" pvc="${entry##*|}"
  if [[ ${NON_DETACHED_SET["${ns}|${pvc}"]+x} ]]; then
    echo "  - ${ns}/${pvc} deferred (workload still attached)"
    continue
  fi
  lbl=$(kcmd get pvc "$pvc" -n "$ns" -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" 2>/dev/null || true)
  if [[ "$lbl" == "$MIGRATION_DONE_LABEL_VALUE" ]]; then
    echo "  ✓ $ns/$pvc migrated"
  else
    echo "  • $ns/$pvc NOT completed"
  fi
done

MODE=$MODE print_migration_cleanup_report
finalize_audit_summary "$SCRIPT_START_TS" "$SCRIPT_START_EPOCH"

ok "Script finished."