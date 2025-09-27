#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

SCRIPT_START_TS="$(date +'%Y-%m-%dT%H:%M:%S')"
SCRIPT_START_EPOCH="$(date +%s)"
MODE=dual

# Declarations
declare -a MIG_PVCS
declare -a PREREQ_ISSUES
declare -a CONFLICT_ISSUES
declare -A SC_SET
declare -a PV2_CREATE_FAILURES
declare -a PV2_BIND_TIMEOUTS

MIG_PVCS=()
PREREQ_ISSUES=()
CONFLICT_ISSUES=()
PV2_CREATE_FAILURES=()
PV2_BIND_TIMEOUTS=()

# (after array zeroing, before ensure_no_foreign_conflicts)
safe_array_len() {
  local name="$1"
  # If array not declared (future refactor), return 0 instead of tripping set -u
  declare -p "$name" &>/dev/null || { echo 0; return; }
  eval "echo \${#$name[@]}"
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
. "${SCRIPT_DIR}/lib-premiumv2-migration-common.sh"

# Run RBAC preflight (mode=dual)
MIGRATION_MODE=dual migration_rbac_check || { err "Aborting due to insufficient permissions."; exit 1; }

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

ensure_pv2_pvc() {
  local pvc="$1" ns="$2" pv="$3" size="$4" mode="$5" sc="$6"
  local pv2_pvc snapshot
  pv2_pvc="$(name_pv2_pvc "$pvc")"
  snapshot="$(name_snapshot "$pv")"
  if kcmd get pvc "$pv2_pvc" -n "$ns" >/dev/null 2>&1; then
    info "PV2 PVC $ns/$pv2_pvc exists"
    return
  fi
  info "Creating PV2 PVC $ns/$pv2_pvc"
  if ! kapply_retry <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $pv2_pvc
  namespace: $ns
  labels:
    ${CREATED_BY_LABEL_KEY}: ${MIGRATION_TOOL_ID}
    ${MIGRATION_LABEL_KEY}: "${MIGRATION_LABEL_VALUE}"
spec:
  accessModes:
  - ReadWriteOnce
  volumeMode: $mode
  storageClassName: ${sc}-pv2
  resources:
    requests:
      storage: $size
  dataSource:
    apiGroup: snapshot.storage.k8s.io
    kind: VolumeSnapshot
    name: $snapshot
EOF
  then
    audit_add "PersistentVolumeClaim" "$pv2_pvc" "$ns" "create-failed" "N/A" "pv2=true sc=${sc}-pv2 snapshot=${snapshot} reason=applyFailure"
    return 1
  else
    audit_add "PersistentVolumeClaim" "$pv2_pvc" "$ns" "create" "kubectl delete pvc $pv2_pvc -n $ns" "pv2=true sc=${sc}-pv2 snapshot=${snapshot}"
  fi

  if wait_pvc_bound "$ns" "$pv2_pvc" "$BIND_TIMEOUT_SECONDS"; then
    ok "PVC $ns/$pv2_pvc bound PremiumV2"
    audit_add "PersistentVolumeClaim" "$pv2_pvc" "$ns" "bound" "kubectl describe pvc $pv2_pvc -n $ns" "pv2=true"
    return 0
  fi

  warn "PVC $ns/$pv2_pvc not bound within timeout (${BIND_TIMEOUT_SECONDS}s)"
  audit_add "PersistentVolumeClaim" "$pv2_pvc" "$ns" "bind-timeout" "kubectl describe pvc $pv2_pvc -n $ns" "pv2=true timeout=${BIND_TIMEOUT_SECONDS}s"
  return 2
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
create_pv2_unique_storage_classes

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

  ensure_snapshot "$snapshot_source_pvc" "$pvc_ns" "$pv" || { \
    warn "Snapshot failed $pvc_ns/$pvc"; \
    audit_add "PersistentVolumeClaim" "$pvc" "$pvc_ns" "snapshot-failed" "kubectl describe pvc $pvc -n $pvc_ns" ""; \
    continue; }
  run_without_errexit ensure_pv2_pvc "$pvc" "$pvc_ns" "$pv" "$size" "$mode" "$sc"
  rc=$?
  case "$rc" in
    0) ;; # success
    1) PV2_CREATE_FAILURES+=("${pvc_ns}/${pvc}") ;;
    2) PV2_BIND_TIMEOUTS+=("${pvc_ns}/${pvc}") ;;
  esac
done

# ------------- Monitoring Loop -------------
deadline=$(( $(date +%s) + MONITOR_TIMEOUT_MINUTES * 60 ))
info "Monitoring migrations (timeout ${MONITOR_TIMEOUT_MINUTES}m)..."

while true; do
  ALL_DONE=true
  for ENTRY in "${MIG_PVCS[@]}"; do
    pvc_ns="${ENTRY%%|*}" pvc="${ENTRY##*|}" pv2_pvc="$(name_pv2_pvc "$pvc")"
    if kcmd get pvc "$pvc" -n "$pvc_ns" -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" | grep -q "^${MIGRATION_DONE_LABEL_VALUE}\$"; then
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
for entry in "${MIG_PVCS[@]}"; do
  ns="${entry%%|*}" pvc="${entry##*|}"
  lbl=$(kcmd get pvc "$pvc" -n "$ns" -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" 2>/dev/null || true)
  if [[ "$lbl" == "$MIGRATION_DONE_LABEL_VALUE" ]]; then
    echo "  ✓ $ns/$pvc migrated"
  else
    echo "  • $ns/$pvc NOT completed"
  fi
done

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

MIGRATION_MODE=dual print_migration_cleanup_report
finalize_audit_summary "$SCRIPT_START_TS" "$SCRIPT_START_EPOCH"
ok "Script finished."