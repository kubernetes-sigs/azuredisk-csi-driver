#!/usr/bin/env bash
# Copyright 2025 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# shellcheck source=./lib-premiumv2-migration-common.sh
# shellcheck source=./premium-to-premiumv2-zonal-aware-helper.sh

set -euo pipefail
IFS=$'\n\t'

SCRIPT_START_TS="$(date +'%Y-%m-%dT%H:%M:%S')"
SCRIPT_START_EPOCH="$(date +%s)"
MODE=attrclass

# Environment overrides specific to this mode (others inherited from lib):
ATTR_CLASS_NAME="${ATTR_CLASS_NAME:-azuredisk-premiumv2}"
ATTR_CLASS_API_VERSION="${ATTR_CLASS_API_VERSION:-storage.k8s.io/v1}"  # Update to v1 when GA.
TARGET_SKU="${TARGET_SKU:-PremiumV2_LRS}"
ATTR_CLASS_FORCE_RECREATE="${ATTR_CLASS_FORCE_RECREATE:-false}"
PV_POLL_INTERVAL_SECONDS="${PV_POLL_INTERVAL_SECONDS:-10}"
SKU_UPDATE_TIMEOUT_MINUTES="${SKU_UPDATE_TIMEOUT_MINUTES:-60}"
CSI_BASELINE_SC="${CSI_BASELINE_SC:-}"

# Declarations (parity)
declare -a MIG_PVCS
declare -a PREREQ_ISSUES
declare -a CONFLICT_ISSUES
declare -a NON_DETACHED_PVCS        # PVCs skipped because workload still attached
declare -A NON_DETACHED_SET         # membership map ns|pvc -> 1
PREREQ_ISSUES=()
CONFLICT_ISSUES=()
MIG_PVCS=()
NON_DETACHED_PVCS=()
NON_DETACHED_SET=()

cleanup_on_error() {
  local exit_code="$1"
  finalize_audit_summary "$SCRIPT_START_TS" "$SCRIPT_START_EPOCH" || true
  err "Script failed (exit=$exit_code); cleanup_on_error ran."
}
trap 'rc=$?; if [ $rc -ne 0 ]; then cleanup_on_error $rc; fi' EXIT

# (after array zeroing, before ensure_no_foreign_conflicts)
safe_array_len() {
  local name="$1"
  # If array not declared (future refactor), return 0 instead of tripping set -u
  declare -p "$name" &>/dev/null || { echo 0; return; }
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
info "ATTR_CLASS_NAME=${ATTR_CLASS_NAME} TARGET_SKU=${TARGET_SKU}"
info "Max PVCs: $MAX_PVCS  MIG_SUFFIX=$MIG_SUFFIX  WAIT_FOR_WORKLOAD=$WAIT_FOR_WORKLOAD"

# Snapshot, VAC, SC variant, workload detach, event helpers now in lib
ensure_snapshot_class   # from common lib
ensure_volume_attributes_class

# ensure_no_foreign_conflicts is now provided by lib-premiumv2-migration-common.sh
# It uses batch cache for optimized lookups and supports MODE=attrclass

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

# ---------------- Discover Tagged PVCs ----------------
MODE=$MODE process_pvcs_for_zone_preparation

# ------------- Collect Unique Source SCs -------------
create_unique_storage_classes

migrate_pvc_attributes_class() {
  local ns="$1" pvc="$2"
  info "Processing PVC $ns/$pvc"
  local pv
  pv="$(get_pv_of_pvc "$ns" "$pvc")"
  if [[ -z "$pv" ]]; then
    warn "Skip $ns/$pvc (no PV yet)"
    return
  fi
  
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
declare -a PVC_SNAPSHOTS
declare -a INTERMEDIATE_PVCS
for ENTRY in "${MIG_PVCS[@]}"; do
  pvc_ns="${ENTRY%%|*}"
  pvc="${ENTRY##*|}"
  
  # Use cached PVC JSON for label check and PV lookup
  pvc_json=$(get_cached_pvc_json "$pvc_ns" "$pvc")
  lbl=$(echo "$pvc_json" | jq -r --arg key "$MIGRATION_DONE_LABEL_KEY" '.metadata.labels[$key] // empty')
  [[ "$lbl" == "$MIGRATION_DONE_LABEL_VALUE" ]] && { info "Already migrated $pvc_ns/$pvc"; continue; }

  pv=$(echo "$pvc_json" | jq -r '.spec.volumeName // empty')
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

  # Use cached PV JSON and extract all needed fields
  pv_json=$(get_cached_pv_json "$pv")
  mode=$(echo "$pv_json" | jq -r '.spec.volumeMode // "Filesystem"')
  size=$(echo "$pv_json" | jq -r '.spec.capacity.storage // empty')
  diskuri=$(echo "$pv_json" | jq -r '.spec.azureDisk.diskURI // empty')
  fstype=$(echo "$pv_json" | jq -r '.spec.azureDisk.fsType // empty')
  csi_driver=$(echo "$pv_json" | jq -r '.spec.csi.driver // empty')

  if [[ -n "$diskuri" ]]; then
    sc=$(get_sc_of_pvc "$pvc" "$pvc_ns")
    if [[ -z "$sc" || -z "$size" ]]; then warn "Missing sc/size for in-tree $pvc_ns/$pvc"; continue; fi
    scpv1="$(name_pv1_sc "$sc")"
    kcmd get sc "${scpv1}" >/dev/null 2>&1 || { warn "Missing ${scpv1}"; continue; }
    create_csi_pv_pvc "$pvc" "$pvc_ns" "$pv" "$size" "$mode" "${scpv1}" "$diskuri" true "$fstype" || { warn "Failed to create CSI PV/PVC for $pvc_ns/$pvc"; continue; }
    INTERMEDIATE_PVCS+=("${pvc_ns}|${pvc}")
  else
    [[ "$csi_driver" != "disk.csi.azure.com" ]] && { warn "Unknown PV driver for $pv"; continue; }
  fi

  snapshot="$(name_snapshot "$pv")"
  PVC_SNAPSHOTS+=("${pvc_ns}|${snapshot}|${pvc}") 
done

for ENTRY in "${INTERMEDIATE_PVCS[@]}"; do
  IFS='|' read -r ns pvc <<< "$ENTRY"
  if wait_pvc_bound "$ns" "$pvc" "$BIND_TIMEOUT_SECONDS"; then
    ok "PVC $ns/$pvc bound"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "bound" "kubectl describe pvc $pvc -n $ns" "csi=true"
    continue
  fi

  warn "Intermediate PVC $ns/$pvc not bound within timeout (${BIND_TIMEOUT_SECONDS}s)"
  audit_add "PersistentVolumeClaim" "$pvc" "$ns" "bind-timeout" "kubectl describe pvc $pvc -n $ns" "csi=true timeout=${BIND_TIMEOUT_SECONDS}s"
  warn "Failed to create CSI PV/PVC for $ns/$pvc"
done

for ENTRY in "${PVC_SNAPSHOTS[@]}"; do
  IFS='|' read -r pvc_ns snapshot pvc <<< "$ENTRY"
  create_snapshot "$snapshot" "$pvc" "$pvc_ns" || { warn "Snapshot failed $pvc_ns/$pvc"; continue; }
done

wait_for_snapshots_ready

SOURCE_SNAPSHOTS=("${PVC_SNAPSHOTS[@]}")
for ENTRY in "${SOURCE_SNAPSHOTS[@]}"; do
  IFS='|' read -r ns snapshot pvc <<< "$ENTRY"
  migrate_pvc_attributes_class "$ns" "$pvc"
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

    # Fetch PVC JSON once per iteration and extract needed fields
    pvc_json=$(kcmd get pvc "$pvc" -n "$ns" -o json 2>/dev/null || true)
    [[ -z "$pvc_json" ]] && { warn "Could not fetch PVC $ns/$pvc"; continue; }

    lbl=$(echo "$pvc_json" | jq -r --arg key "$MIGRATION_DONE_LABEL_KEY" '.metadata.labels[$key] // empty')
    if [[ "$lbl" == "$MIGRATION_DONE_LABEL_VALUE" || "$lbl" == "$MIGRATION_DONE_LABEL_VALUE_FALSE" ]]; then
      continue
    fi

    pv=$(echo "$pvc_json" | jq -r '.spec.volumeName // empty')
    if [[ -z "$pv" ]]; then
      warn "PVC lost PV binding? $ns/$pvc"
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
        mark_source_in_progress "$ns" "$pvc"
        info "$reason $ns/$pvc"
        ALL_DONE=false
        ;;
      ReasonSKUMigrationTimeout)
        mark_source_notdone "$ns" "$pvc"
        warn "$reason $ns/$pv2_pvc"
        ALL_DONE=false
        ;;
      "")
        info "No migration events yet for $ns/$pvc"
        ALL_DONE=false
        ;;
    esac

    # Force in-progress label on PV after threshold if no events
    if [[ -n "$pv" && $MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES -gt 0 ]]; then
      # Fetch PV JSON once and extract needed fields
      pv_json=$(kcmd get pv "$pv" -o json 2>/dev/null || true)
      [[ -z "$pv_json" ]] && continue

      inprog=$(echo "$pv_json" | jq -r --arg key "$MIGRATION_INPROGRESS_LABEL_KEY" '.metadata.labels[$key] // empty')
      if [[ "$inprog" != "$MIGRATION_INPROGRESS_LABEL_VALUE" ]]; then
        cts=$(echo "$pvc_json" | jq -r '.metadata.creationTimestamp // empty')
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
