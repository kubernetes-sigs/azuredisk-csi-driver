#!/usr/bin/env bash
# shellcheck source=./lib-premiumv2-migration-common.sh
set -euo pipefail
IFS=$'\n\t'

SCRIPT_START_TS="$(date +'%Y-%m-%dT%H:%M:%S')"
SCRIPT_START_EPOCH="$(date +%s)"
MODE=dual

# Declarations
declare -a MIG_PVCS
declare -a PREREQ_ISSUES
declare -a CONFLICT_ISSUES
declare -a PV2_CREATE_FAILURES
declare -a PV2_BIND_TIMEOUTS
declare -a NON_DETACHED_PVCS          # PVCs we skipped because workloads still attached
declare -A NON_DETACHED_SET           # Fast membership check: key = "ns|pvc"

MIG_PVCS=()
PREREQ_ISSUES=()
CONFLICT_ISSUES=()
PV2_CREATE_FAILURES=()
PV2_BIND_TIMEOUTS=()
NON_DETACHED_PVCS=()
NON_DETACHED_SET=()

cleanup_on_error() {
  finalize_audit_summary "$SCRIPT_START_TS" "$SCRIPT_START_EPOCH" || true
  err "Script failed (exit=$rc); cleanup_on_error ran."
}
trap 'rc=$?; if [ $rc -ne 0 ]; then cleanup_on_error $rc; fi' EXIT

# (after array zeroing, before ensure_no_foreign_conflicts)
safe_array_len() {
  local name="$1"
  # If array not declared (future refactor), return 0 instead of tripping set -u
  declare -p "$name" &>/dev/null || { echo 0; return; }
  eval "echo \${#${name}[@]}"
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
. "${SCRIPT_DIR}/lib-premiumv2-migration-common.sh"

# Run RBAC preflight (mode=dual)
MODE=$MODE migration_rbac_check || { err "Aborting due to insufficient permissions."; exit 1; }

info "Migration label: $MIGRATION_LABEL"
info "Target snapshot class: $SNAPSHOT_CLASS"
info "Max PVCs: $MAX_PVCS  MIG_SUFFIX=$MIG_SUFFIX  WAIT_FOR_WORKLOAD=$WAIT_FOR_WORKLOAD"

# Snapshot, SC variant, workload detach, event helpers now in lib
ensure_snapshot_class   # from common lib

# ---------------- Discover Tagged PVCs ----------------
populate_pvcs

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
    local pv2_pvc pv diskuri int_pvc int_pv snap
    pv2_pvc="$(name_pv2_pvc "$pvc")"
    if kcmd get pvc "$pv2_pvc" -n "$ns" >/dev/null 2>&1; then
      if ! kcmd get pvc "$pv2_pvc" -n "$ns" -o json | jq -e \
           --arg k "$CREATED_BY_LABEL_KEY" --arg v "$MIGRATION_TOOL_ID" \
           '.metadata.labels[$k]==$v' >/dev/null; then
        CONFLICT_ISSUES+=("PVC/$ns/$pv2_pvc (pv2) missing label")
      fi
    fi
    pv=$(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
    [[ -z "$pv" ]] && continue
    diskuri=$(kcmd get pv "$pv" -o jsonpath='{.spec.azureDisk.diskURI}' 2>/dev/null || true)
    if [[ -n "$diskuri" ]]; then
      int_pvc="$(name_csi_pvc "$pvc")"
      int_pv="$(name_csi_pv "$pv")"
      if kcmd get pvc "$int_pvc" -n "$ns" >/dev/null 2>&1; then
        kcmd get pvc "$int_pvc" -n "$ns" -o json | jq -e \
           --arg k "$CREATED_BY_LABEL_KEY" --arg v "$MIGRATION_TOOL_ID" \
           '.metadata.labels[$k]==$v' >/dev/null || \
           CONFLICT_ISSUES+=("PVC/$ns/$int_pvc (intermediate) missing label")
      fi
      if kcmd get pv "$int_pv" >/dev/null 2>&1; then
        kcmd get pv "$int_pv" -o json | jq -e \
           --arg k "$CREATED_BY_LABEL_KEY" --arg v "$MIGRATION_TOOL_ID" \
           '.metadata.labels[$k]==$v' >/dev/null || \
           CONFLICT_ISSUES+=("PV/$int_pv (intermediate) missing label")
      fi
    fi
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

# ------------- Pre-Req & Conflicts -------------
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
      if ! wait_for_workload_detach "$pv" "$pvc" "$pvc_ns"; then
        warn "Workload still attached -> deferring migration for $pvc_ns/$pvc (recording in NON_DETACHED_PVCS; no labels changed)."
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
    create_csi_pv_pvc "$pvc" "$pvc_ns" "$pv" "$size" "$mode" "${scpv1}" "$diskuri"
    snapshot_source_pvc="$(name_csi_pvc "$pvc")"
  else
    [[ "$csi_driver" != "disk.csi.azure.com" ]] && { warn "Unknown PV driver for $pv"; continue; }
    if [[ -z "$scpv2" || -z "$size" ]]; then warn "Missing sc/size for CSI $pvc_ns/$pvc"; continue; fi
    kcmd get sc "${scpv2}" >/dev/null 2>&1 || { warn "Missing ${scpv2}"; continue; }
    snapshot_source_pvc="$pvc"
  fi

  pv2_pvc="$(name_pv2_pvc "$pvc")"
  snapshot="$(name_snapshot "$pv")"
  ensure_snapshot "$snapshot" "$snapshot_source_pvc" "$pvc_ns" "$pv" || { warn "Snapshot failed $pvc_ns/$pvc"; continue; }
  run_without_errexit create_pvc_from_snapshot "$pvc" "$pvc_ns" "$pv" "$size" "$mode" "$scpv2" "$pv2_pvc" "$snapshot"
  case "$LAST_RUN_WITHOUT_ERREXIT_RC" in
    0) ;; # success
    1) PV2_CREATE_FAILURES+=("${pvc_ns}/${pvc}") ;;
    2) PV2_BIND_TIMEOUTS+=("${pvc_ns}/${pvc}") ;;
  esac
done

# ------------- Monitoring Loop -------------
deadline=$(( $(date +%s) + MONITOR_TIMEOUT_MINUTES*60 ))
info "Monitoring migrations (timeout ${MONITOR_TIMEOUT_MINUTES}m)..."

while true; do
  ALL_DONE=true
  for ENTRY in "${MIG_PVCS[@]}"; do
    pvc_ns="${ENTRY%%|*}" pvc="${ENTRY##*|}" pv2_pvc="$(name_pv2_pvc "$pvc")"

    # Skip monitoring entirely for PVCs we never started due to attachment
    if [[ ${NON_DETACHED_SET["${pvc_ns}|${pvc}"]+x} ]]; then
      continue
    fi

    lbl=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" || true)
    if [[ "$lbl" == "$MIGRATION_DONE_LABEL_VALUE" ]]; then
      continue
    fi

    STATUS=$(kcmd get pvc "$pv2_pvc" -n "$pvc_ns" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    [[ "$STATUS" != "Bound" ]] && ALL_DONE=false
    reason=$(extract_event_reason "$pvc_ns" "$pv2_pvc")
    case "$reason" in
      SKUMigrationCompleted)
        mark_source_done "$pvc_ns" "$pvc"
        ok "Completed $pvc_ns/$pvc"
        continue
        ;;
      SKUMigrationProgress|SKUMigrationStarted)
        info "$reason $pvc_ns/$pv2_pvc"; ALL_DONE=false ;;
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
      # Ignore non-detached deferred PVCs
      if [[ ${NON_DETACHED_SET["${pvc_ns}|${pvc}"]+x} ]]; then
        continue
      fi
      kcmd get pvc "$pvc" -n "$pvc_ns" -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" | \
        grep -q "^${MIGRATION_DONE_LABEL_VALUE}\$" && continue
      orig_pv=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
      [[ -z "$orig_pv" ]] && continue
      inprog_val=$(kcmd get pv "$orig_pv" -o go-template="{{ index .metadata.labels \"${MIGRATION_INPROGRESS_LABEL_KEY}\" }}" 2>/dev/null || true)
      if [[ "$inprog_val" != "$MIGRATION_INPROGRESS_LABEL_VALUE" ]]; then
        warn "Timeout fallback: labeling PV $orig_pv with ${MIGRATION_INPROGRESS_LABEL_KEY}=${MIGRATION_INPROGRESS_LABEL_VALUE}"
        kcmd label pv "$orig_pv" "${MIGRATION_INPROGRESS_LABEL_KEY}=${MIGRATION_INPROGRESS_LABEL_VALUE}" --overwrite
        audit_add "PersistentVolume" "$orig_pv" "" "label" \
          "kubectl label pv $orig_pv ${MIGRATION_INPROGRESS_LABEL_KEY}-" \
          "forced=true reason=monitorTimeout"
      fi
    done
    break
  fi
  sleep "$POLL_INTERVAL"
done

# ------------- Summary & Audit -------------
echo
info "Summary:"
if (( ${#PV2_CREATE_FAILURES[@]} > 0 )); then
  echo
  warn "pv2 PVC creation failures (no resource or create error):"
  printf '  - %s\n' "${PV2_CREATE_FAILURES[@]}"
fi
if (( ${#PV2_BIND_TIMEOUTS[@]} > 0 )); then
  echo
  warn "pv2 PVC bind timeouts:"
  printf '  - %s\n' "${PV2_BIND_TIMEOUTS[@]}"
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