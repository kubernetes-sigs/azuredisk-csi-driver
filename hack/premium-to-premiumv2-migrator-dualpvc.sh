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

# Zonal-aware helper lib
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ZONAL_HELPER_LIB="${SCRIPT_DIR}/premium-to-premiumv2-zonal-aware-helper.sh"
[[ -f "$ZONAL_HELPER_LIB" ]] || { echo "[ERROR] Missing lib: $ZONAL_HELPER_LIB" >&2; exit 1; }
# shellcheck disable=SC1090
. "$ZONAL_HELPER_LIB"

# Run RBAC preflight (mode=dual)
MODE=$MODE migration_rbac_check || { err "Aborting due to insufficient permissions."; exit 1; }

info "Migration label: $MIGRATION_LABEL"
info "Target snapshot class: $SNAPSHOT_CLASS"
info "Max PVCs: $MAX_PVCS  MIG_SUFFIX=$MIG_SUFFIX  WAIT_FOR_WORKLOAD=$WAIT_FOR_WORKLOAD"

# Snapshot, SC variant, workload detach, event helpers now in lib
ensure_snapshot_class   # from common lib

# ---------------- Discover Tagged PVCs ----------------
MODE=$MODE process_pvcs_for_zone_preparation

# ensure_no_foreign_conflicts is now provided by lib-premiumv2-migration-common.sh
# It uses batch cache for optimized lookups and supports MODE=dual

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
declare -a PVC_SNAPSHOTS
declare -a INTERMEDIATE_PVCS
for ENTRY in "${MIG_PVCS[@]}"; do
  pvc_ns="${ENTRY%%|*}"
  pvc="${ENTRY##*|}"

  if ! check_premium_lrs "$pvc" "$pvc_ns"; then
    info "PVC $pvc_ns/$pvc not Premium_LRS -> skip"
    continue
  fi

  # Use cached PVC JSON for label check and PV lookup
  pvc_json=$(get_cached_pvc_json "$pvc_ns" "$pvc")
  if [[ -z "$pvc_json" ]]; then
    warn "PVC $pvc_ns/$pvc not yet available"; continue
  fi
  DONE_LABEL=$(echo "$pvc_json" | jq -r --arg key "$MIGRATION_DONE_LABEL_KEY" '.metadata.labels[$key] // empty')
  [[ "$DONE_LABEL" == "$MIGRATION_DONE_LABEL_VALUE" ]] && { info "Already migrated $pvc_ns/$pvc"; continue; }

  pv=$(echo "$pvc_json" | jq -r '.spec.volumeName // empty')
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

  # Use cached PV JSON and extract all needed fields
  pv_json=$(get_cached_pv_json "$pv")
  mode=$(echo "$pv_json" | jq -r '.spec.volumeMode // "Filesystem"')
  size=$(echo "$pv_json" | jq -r '.spec.capacity.storage // empty')
  diskuri=$(echo "$pv_json" | jq -r '.spec.azureDisk.diskURI // empty')
  fstype=$(echo "$pv_json" | jq -r '.spec.azureDisk.fsType // empty')
  csi_driver=$(echo "$pv_json" | jq -r '.spec.csi.driver // empty')
  sc=$(get_sc_of_pvc "$pvc" "$pvc_ns")

  if [[ -n "$diskuri" ]]; then
    if [[ -z "$sc" || -z "$size" ]]; then warn "Missing sc/size for in-tree $pvc_ns/$pvc"; continue; fi
    scpv1="$(name_pv1_sc "$sc")"
    create_csi_pv_pvc "$pvc" "$pvc_ns" "$pv" "$size" "$mode" "${scpv1}" "$diskuri" false "$fstype" || { warn "Failed to create CSI PV/PVC for $pvc_ns/$pvc"; continue; }
    snapshot_source_pvc="$(name_csi_pvc "$pvc")"
    INTERMEDIATE_PVCS+=("${pvc_ns}|${snapshot_source_pvc}")
  else
    [[ "$csi_driver" != "disk.csi.azure.com" ]] && { warn "Unknown PV driver for $pv"; continue; }
    scpv2="$(name_pv2_sc "$sc")"
    if [[ -z "$scpv2" || -z "$size" ]]; then warn "Missing sc/size for CSI $pvc_ns/$pvc"; continue; fi
    kcmd get sc "${scpv2}" >/dev/null 2>&1 || { warn "Missing ${scpv2}"; continue; }
    snapshot_source_pvc="$pvc"
  fi

  snapshot="$(name_snapshot "$pv")"
  PVC_SNAPSHOTS+=("${pvc_ns}|${snapshot}|${pvc}|${snapshot_source_pvc}")
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
  IFS='|' read -r pvc_ns snapshot pvc snapshot_source_pvc <<< "$ENTRY"
  create_snapshot "$snapshot" "$snapshot_source_pvc" "$pvc_ns" || { warn "Snapshot failed $pvc_ns/$pvc"; continue; }
done

wait_for_snapshots_ready

SOURCE_SNAPSHOTS=("${PVC_SNAPSHOTS[@]}")
for ENTRY in "${SOURCE_SNAPSHOTS[@]}"; do
  IFS='|' read -r pvc_ns snapshot pvc snapshot_source_pvc <<< "$ENTRY"

  # Use cached PVC JSON to get PV name
  pvc_json=$(get_cached_pvc_json "$pvc_ns" "$pvc")
  pv=$(echo "$pvc_json" | jq -r '.spec.volumeName // empty')

  # Use cached PV JSON and extract all needed fields
  pv_json=$(get_cached_pv_json "$pv")
  size=$(echo "$pv_json" | jq -r '.spec.capacity.storage // empty')
  mode=$(echo "$pv_json" | jq -r '.spec.volumeMode // "Filesystem"')
  sc=$(get_sc_of_pvc "$pvc" "$pvc_ns")
  scpv2="$(name_pv2_sc "$sc")"
  pv2_pvc="$(name_pv2_pvc "$pvc")"

  run_without_errexit create_pvc_from_snapshot "$pvc" "$pvc_ns" "$pv" "$size" "$mode" "$scpv2" "$pv2_pvc" "$snapshot"
  case "$LAST_RUN_WITHOUT_ERREXIT_RC" in
    0) ;; # success
    *) PV2_CREATE_FAILURES+=("${pvc_ns}/${pvc}") ;;
  esac
done

SOURCE_SNAPSHOTS=("${PVC_SNAPSHOTS[@]}")
for ENTRY in "${SOURCE_SNAPSHOTS[@]}"; do
  IFS='|' read -r pvc_ns snapshot pvc snapshot_source_pvc <<< "$ENTRY"
  pv2_pvc="$(name_pv2_pvc "$pvc")"

  # Get storage class of the source PVC from cached JSON for accurate auditing
  pvc_json=$(get_cached_pvc_json "$pvc_ns" "$pv2_pvc")
  sc=$(echo "$pvc_json" | jq -r '.spec.storageClassName // empty')

  if wait_pvc_bound "$pvc_ns" "$pv2_pvc" "$BIND_TIMEOUT_SECONDS"; then
    ok "PVC $pvc_ns/$pv2_pvc bound"
    audit_add "PersistentVolumeClaim" "$pv2_pvc" "$pvc_ns" "bound" "kubectl describe pvc $pv2_pvc -n $pvc_ns" "sc=${sc}"
    continue
  fi

  warn "PVC $pvc_ns/$pv2_pvc not bound within timeout (${BIND_TIMEOUT_SECONDS}s)"
  audit_add "PersistentVolumeClaim" "$pv2_pvc" "$pvc_ns" "bind-timeout" "kubectl describe pvc $pv2_pvc -n $pvc_ns" "sc=${sc} timeout=${BIND_TIMEOUT_SECONDS}s"
  PV2_BIND_TIMEOUTS+=("${pvc_ns}/${pv2_pvc}")
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

    # Fetch source PVC JSON once per iteration and extract needed fields
    src_pvc_json=$(kcmd get pvc "$pvc" -n "$pvc_ns" -o json 2>/dev/null || true)
    [[ -z "$src_pvc_json" ]] && { warn "Could not fetch PVC $pvc_ns/$pvc"; continue; }

    lbl=$(echo "$src_pvc_json" | jq -r --arg key "$MIGRATION_DONE_LABEL_KEY" '.metadata.labels[$key] // empty')
    if [[ "$lbl" == "$MIGRATION_DONE_LABEL_VALUE" || "$lbl" == "$MIGRATION_DONE_LABEL_VALUE_FALSE" ]]; then
      continue
    fi

    # Fetch pv2 PVC JSON once per iteration
    pv2_pvc_json=$(kcmd get pvc "$pv2_pvc" -n "$pvc_ns" -o json 2>/dev/null || true)
    if [[ -z "$pv2_pvc_json" ]]; then
      info "pv2 PVC $pvc_ns/$pv2_pvc not yet available"; ALL_DONE=false
      continue
    fi
    STATUS=$(echo "$pv2_pvc_json" | jq -r '.status.phase // empty')
    [[ "$STATUS" != "Bound" ]] && ALL_DONE=false
    reason=$(extract_event_reason "$pvc_ns" "$pv2_pvc")
    case "$reason" in
      SKUMigrationCompleted)
        mark_source_done "$pvc_ns" "$pvc"
        ok "Completed $pvc_ns/$pvc"
        continue
        ;;
      SKUMigrationProgress|SKUMigrationStarted)
        mark_source_in_progress "$pvc_ns" "$pvc"
        info "$reason $pvc_ns/$pv2_pvc"; ALL_DONE=false ;;
      ReasonSKUMigrationTimeout)
        mark_source_notdone "$pvc_ns" "$pvc"
        warn "$reason $pvc_ns/$pv2_pvc"; ALL_DONE=false ;;
      "")
        info "No migration events yet for $pvc_ns/$pv2_pvc"; ALL_DONE=false ;;
    esac
    if [[ "$STATUS" == "Bound" && -z "$reason" && $MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES -gt 0 ]]; then
      orig_pv=$(echo "$src_pvc_json" | jq -r '.spec.volumeName // empty')
      [[ -z "$orig_pv" ]] && continue

      # Fetch PV JSON once and extract needed fields
      pv_json=$(kcmd get pv "$orig_pv" -o json 2>/dev/null || true)
      [[ -z "$pv_json" ]] && continue

      inprog_val=$(echo "$pv_json" | jq -r --arg key "$MIGRATION_INPROGRESS_LABEL_KEY" '.metadata.labels[$key] // empty')
      if [[ "$inprog_val" != "$MIGRATION_INPROGRESS_LABEL_VALUE" ]]; then
        cts=$(echo "$pv2_pvc_json" | jq -r '.metadata.creationTimestamp // empty')
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
