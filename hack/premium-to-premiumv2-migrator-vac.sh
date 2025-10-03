#!/usr/bin/env bash
set -euo pipefail
IFS=$'\n\t'

SCRIPT_START_TS="$(date +'%Y-%m-%dT%H:%M:%S')"
SCRIPT_START_EPOCH="$(date +%s)"
MODE=attrclass

# Environment overrides specific to this mode (others inherited from lib):
ATTR_CLASS_NAME="${ATTR_CLASS_NAME:-azuredisk-premiumv2}"
ATTR_CLASS_API_VERSION="${ATTR_CLASS_API_VERSION:-storage.k8s.io/v1beta1}"  # Update to v1 when GA.
TARGET_SKU="${TARGET_SKU:-PremiumV2_LRS}"
ATTR_CLASS_FORCE_RECREATE="${ATTR_CLASS_FORCE_RECREATE:-false}"
PV_POLL_INTERVAL_SECONDS="${PV_POLL_INTERVAL_SECONDS:-10}"
SKU_UPDATE_TIMEOUT_MINUTES="${SKU_UPDATE_TIMEOUT_MINUTES:-60}"
CSI_BASELINE_SC="${CSI_BASELINE_SC:-}"
ROLLBACK_ON_TIMEOUT="${ROLLBACK_ON_TIMEOUT:-false}"

# Declarations (parity)
declare -a MIG_PVCS
declare -a PREREQ_ISSUES
declare -a CONFLICT_ISSUES
declare -A SC_SET
declare -a NON_DETACHED_PVCS        # PVCs skipped because workload still attached
declare -A NON_DETACHED_SET         # membership map ns|pvc -> 1
PREREQ_ISSUES=()
CONFLICT_ISSUES=()
MIG_PVCS=()
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
  eval "echo \${#$name[@]}"
}

# -------- Shared Library --------
SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"
LIB="${SCRIPT_DIR}/lib-premiumv2-migration-common.sh"
[[ -f "$LIB" ]] || { echo "[ERROR] Missing lib: $LIB" >&2; exit 1; }
# shellcheck disable=SC1090
. "$LIB"

# Run RBAC preflight (mode=inplace)
MODE=$MODE migration_rbac_check || { err "Aborting due to insufficient permissions."; exit 1; }

info "Migration label: $MIGRATION_LABEL"
info "Target snapshot class: $SNAPSHOT_CLASS"
info "ATTR_CLASS_NAME=${ATTR_CLASS_NAME} TARGET_SKU=${TARGET_SKU}"
info "Max PVCs: $MAX_PVCS  MIG_SUFFIX=$MIG_SUFFIX  WAIT_FOR_WORKLOAD=$WAIT_FOR_WORKLOAD"

# Snapshot, VAC, SC variant, workload detach, event helpers now in lib
ensure_snapshot_class   # from common lib
ensure_volume_attributes_class

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

  # VolumeAttributesClass ownership check
  if kcmd get volumeattributesclass "${ATTR_CLASS_NAME}" >/dev/null 2>&1; then
    if ! kcmd get volumeattributesclass "${ATTR_CLASS_NAME}" -o json | jq -e \
        --arg k "$CREATED_BY_LABEL_KEY" --arg v "$MIGRATION_TOOL_ID" '.metadata.labels[$k]==$v' >/dev/null; then
      CONFLICT_ISSUES+=("VolumeAttributesClass/${ATTR_CLASS_NAME} (exists without label ${CREATED_BY_LABEL_KEY}=${MIGRATION_TOOL_ID})")
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
    local current_attr
    current_attr=$(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.volumeAttributesClassName}' 2>/dev/null || true)
    if [[ -n "$current_attr" && "$current_attr" != "$ATTR_CLASS_NAME" ]]; then
      CONFLICT_ISSUES+=("PVC/${ns}/${pvc} has volumeAttributesClassName=${current_attr} (expected empty or ${ATTR_CLASS_NAME})")
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
create_unique_storage_classes

migrate_pvc_attrclass() {
  local ns="$1" pvc="$2"
  info "Processing PVC $ns/$pvc"
  local pv
  pv="$(get_pv_of_pvc "$ns" "$pvc")"
  if [[ -z "$pv" ]]; then
    warn "Skip $ns/$pvc (no PV yet)"
    return
  fi

  # Mark in-progress early
  kubectl label pvc "$pvc" -n "$ns" --overwrite "${MIGRATION_INPROGRESS_LABEL_KEY}=${MIGRATION_INPROGRESS_LABEL_VALUE}" >/dev/null 2>&1 || true

  if is_in_tree_pv "$pv"; then
    mode=$(kcmd get pv "$pv" -o jsonpath='{.spec.volumeMode}' 2>/dev/null || true)
    sc=$(kcmd get pv "$pv" -o jsonpath='{.spec.storageClassName}' 2>/dev/null || true)
    diskuri=$(kcmd get pv "$pv" -o jsonpath='{.spec.azureDisk.diskURI}' 2>/dev/null || true)
    scpv1="$(name_pv1_sc "$sc")"

    info "In-tree PV ($pv) -> converting"
    create_csi_pv_pvc "$pvc" "$ns" "$pv" "$size" "$mode" "${scpv1}" "$diskuri" true || {
      err "Failed to create CSI PV/PVC for $ns/$pvc"
      audit_add "PersistentVolumeClaim" "$pvc" "$ns" "convert-failed" "N/A" "phase=intree-create-csi"
      return
    }
    pv="$(get_pv_of_pvc "$ns" "$pvc")"
  else
    backup_pvc "$pvc" "$ns" || {
      warn "PVC backup failed $ns/$pvc"
    }
  fi

  snapshot="$(name_snapshot "$pv")"
  ensure_snapshot "$snapshot" "$pvc" "$pvc_ns" "$pv" || {
    warn "Snapshot failed $pvc_ns/$pvc"; continue;
  }
  ensure_reclaim_policy_retain "$pv"
  ensure_volume_attributes_class

  local cur_attr
  cur_attr="$(kubectl get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.volumeAttributesClassName}' 2>/dev/null || true)"
  if [[ "$cur_attr" != "$ATTR_CLASS_NAME" ]]; then
    info "Patching volumeAttributesClassName=${ATTR_CLASS_NAME}"
    kubectl patch pvc "$pvc" -n "$ns" --type=merge -p "{\"spec\":{\"volumeAttributesClassName\":\"${ATTR_CLASS_NAME}\"}}"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "patch" \
      "kubectl patch pvc $pvc -n $ns --type=json -p '[{\"op\":\"remove\",\"path\":\"/spec/volumeAttributesClassName\"}]'" \
      "attrclass=${ATTR_CLASS_NAME}"
  else
    info "PVC already references ${ATTR_CLASS_NAME}"
  fi

  # CHANGED (monitor handles completion): do NOT block here waiting for sku update.
  audit_add "PersistentVolumeClaim" "$pvc" "$ns" "attrclass-applied" "kubectl describe pvc $pvc -n $ns" "pv=$pv targetSku=${TARGET_SKU}"
}


info "Candidate PVCs:"
printf '  %s\n' "${MIG_PVCS[@]}"

# -------- Initial Mutation Pass (apply attr class / conversion) = migration loop --------
for ENTRY in "${MIG_PVCS[@]}"; do
  pvc_ns="${ENTRY%%|*}"
  pvc="${ENTRY##*|}"
  lbl=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" 2>/dev/null || true)
  [[ "$lbl" == "$MIGRATION_DONE_LABEL_VALUE" ]] && { info "Already migrated $pvc_ns/$pvc"; continue; }

  pv="$(get_pv_of_pvc "$pvc_ns" "$pvc")"
  if [[ -z "$pv" ]]; then
    warn "PVC lost PV binding? $pvc_ns/$pvc. Skipping migration for now."
    continue
  fi

  check_premiumv2_lrs "$pvc_ns" "$pvc" && {
    ok "Already PremiumV2: $pvc_ns/$pvc"
    continue
  }

  if [[ "$WAIT_FOR_WORKLOAD" == "true" ]]; then
    attachments=$(kcmd get volumeattachment -o jsonpath="{range .items[?(@.spec.source.persistentVolumeName=='$pv')]}{.metadata.name}{'\n'}{end}" 2>/dev/null || true)
    if [[ -n "$attachments" ]]; then
      if ! wait_for_workload_detach "$pv" "$pvc" "$pvc_ns"; then
        warn "Workload still attached -> deferring migration for $pvc_ns/$pvc (no labels changed)"
        NON_DETACHED_PVCS+=("${pvc_ns}|${pvc}")
        NON_DETACHED_SET["${pvc_ns}|${pvc}"]=1
        continue
      fi
    fi
  fi

  ensure_reclaim_policy_retain "$pv"

  mode=$(kcmd get pv "$pv" -o jsonpath='{.spec.volumeMode}' 2>/dev/null || true)
  sc=$(kcmd get pv "$pv" -o jsonpath='{.spec.storageClassName}' 2>/dev/null || true)
  size=$(kcmd get pv "$pv" -o jsonpath='{.spec.capacity.storage}' 2>/dev/null || true)
  diskuri=$(kcmd get pv "$pv" -o jsonpath='{.spec.azureDisk.diskURI}' 2>/dev/null || true)

  migrate_pvc_attrclass "$pvc_ns" "$pvc"
done

# -------- Monitoring Loop (events + sku poll) --------
# MONITOR LOOP ADDED
deadline=$(( $(date +%s) + MONITOR_TIMEOUT_MINUTES * 60 ))
info "Monitoring attrclass migrations (timeout ${MONITOR_TIMEOUT_MINUTES}m)..."
monitor_start_epoch=$(date +%s)

while true; do
  ALL_DONE=true
  for ENTRY in "${MIG_PVCS[@]}"; do
    ns="${ENTRY%%|*}" pvc="${ENTRY##*|}"

    # Skip monitoring for deferred PVCs (never mutated)
    if [[ ${NON_DETACHED_SET["${ns}|${pvc}"]+x} ]]; then
      continue
    fi

    # Already done?
    lbl=$(kcmd get pvc "$pvc" -n "$ns" -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" || true)
    if [[ "$lbl" == "$MIGRATION_DONE_LABEL_VALUE" ]]; then
      continue
    fi

    pv="$(get_pv_of_pvc "$ns" "$pvc")"
    if [[ -z "$pv" ]]; then
      warn "PVC lost PV binding? $ns/$pvc"
      continue
    fi

    # Fast path: sku already updated
    if ! check_premiumv2_lrs "$ns" "$pvc"; then
      kubectl label pvc "$pvc" -n "$ns" "${MIGRATION_INPROGRESS_LABEL_KEY}-" 2>/dev/null 2>&1 || true
      continue
    fi

    reason=$(extract_event_reason "$ns" "$pvc")
    case "$reason" in
      SKUMigrationCompleted)
        mark_source_done "$ns" "$pvc"
        ok "Completed (event) $ns/$pvc"
        continue
        ;;
      SKUMigrationProgress|SKUMigrationStarted)
        info "$reason $ns/$pvc"
        ALL_DONE=false
        ;;
      ReasonSKUMigrationTimeout)
        warn "Controller timeout event for $ns/$pvc"
        if [[ "$ROLLBACK_ON_TIMEOUT" == "true" ]]; then
          warn "Attempting best-effort rollback (remove attr class)"
          kubectl patch pvc "$pvc" -n "$ns" --type=json -p '[{"op":"remove","path":"/spec/volumeAttributesClassName"}]' >/dev/null 2>&1 || true
          audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-attrclass-remove" "N/A" "reason=controller-timeout"
        fi
        ALL_DONE=false
        ;;
      "")
        info "No migration events yet for $ns/$pvc"
        ALL_DONE=false
        ;;
    esac

    # Force in-progress label on PV after threshold if no events
    if [[ -n "$pv" && $MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES -gt 0 ]]; then
      inprog=$(kcmd get pv "$pv" -o go-template="{{ index .metadata.labels \"${MIGRATION_INPROGRESS_LABEL_KEY}\" }}" 2>/dev/null || true)
      if [[ "$inprog" != "$MIGRATION_INPROGRESS_LABEL_VALUE" ]]; then
        cts=$(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.metadata.creationTimestamp}' 2>/dev/null || true)
        if [[ -n "$cts" ]]; then
          creation_epoch=$(date -d "$cts" +%s 2>/dev/null || echo 0)
          now_epoch=$(date +%s)
          if (( now_epoch - creation_epoch >= MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES * 60 )); then
            if (( now_epoch - monitor_start_epoch < MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES * 60 )); then
              # Avoid false positive for csi volumes modified to pv2
              continue
            fi
            warn "Forcing in-progress label on PV $pv (no events after ${MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES}m)"
            kcmd label pv "$pv" "${MIGRATION_INPROGRESS_LABEL_KEY}=${MIGRATION_INPROGRESS_LABEL_VALUE}" --overwrite
            audit_add "PersistentVolume" "$pv" "" "label" \
              "kubectl label pv $pv ${MIGRATION_INPROGRESS_LABEL_KEY}-" \
              "forced=true reason=noEventsAfter${MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES}m"
          fi
        fi
      fi
    fi
  done

  if [[ "$ALL_DONE" == "true" ]]; then
    info "All migrations completed."
    break
  fi

  if (( $(date +%s) > deadline )); then
    err "Monitoring timeout reached"
    break
  fi

  sleep "$POLL_INTERVAL"
done

# ------------- Summary -------------
echo
info "Summary:"
for ENTRY in "${MIG_PVCS[@]}"; do
  ns="${ENTRY%%|*}" pvc="${ENTRY##*|}"
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

info "Cleanup / leftover report:"
MODE=$MODE print_migration_cleanup_report

finalize_audit_summary "$SCRIPT_START_TS" "$SCRIPT_START_EPOCH"

ok "AttrClass migration script finished."