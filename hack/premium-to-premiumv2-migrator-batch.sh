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

# Batch wrapper for Premium_LRS -> PremiumV2_LRS migration.
# Adds resumable checkpointing and concurrent execution on top of the
# existing per-mode migration scripts (inplace / dual / attrclass).
#
# Usage:
#   MIGRATION_MODE=inplace ./premium-to-premiumv2-migrator-batch.sh
#
# Key env vars:
#   MIGRATION_MODE        - Required. One of: inplace, dual, attrclass
#   BATCH_SIZE            - PVCs per sub-batch (default: 1)
#   BATCH_CONCURRENCY     - Max parallel batches (default: min(50, num_pvcs))
#   DRY_RUN               - If "true", print plan and exit (default: false)
#   CHECKPOINT_FILE       - Path to checkpoint JSON (default: migration-checkpoint.json)
#   MIGRATION_LABEL       - PVC selector label (default: disk.csi.azure.com/pv2migration=true)
#   NAMESPACE             - If set, scope discovery to this namespace
#   BATCH_LOG_DIR         - Directory for per-batch logs (default: migration-batch-logs)
#
# All other env vars (SNAPSHOT_CLASS, POLL_INTERVAL, etc.) are passed through
# to the underlying migration scripts unchanged.

set -euo pipefail
IFS=$'\n\t'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Source common library for kcmd (kubectl with retries), require_bins, logging, etc.
# shellcheck disable=SC1091
source "${SCRIPT_DIR}/lib-premiumv2-migration-common.sh"

# ==================== Configuration ====================
# These are wrapper-specific vars. Env vars shared with the common lib
# (MIGRATION_LABEL, NAMESPACE, MAX_PVCS, AUDIT_LOG_FILE, PVC_BACKUP_DIR)
# are already set by the lib with defaults. We save the user's original
# MIGRATION_LABEL here — run_batch overrides it per-batch for scoping,
# but discovery and filtering use the original.

MIGRATION_MODE="${MIGRATION_MODE:-}"
BATCH_SIZE="${BATCH_SIZE:-1}"
BATCH_CONCURRENCY="${BATCH_CONCURRENCY:-}"  # Computed later if empty
DRY_RUN="${DRY_RUN:-false}"
CHECKPOINT_FILE="${CHECKPOINT_FILE:-migration-checkpoint.json}"
BATCH_LOG_DIR="${BATCH_LOG_DIR:-migration-batch-logs}"
BATCH_LABEL_KEY="disk.csi.azure.com/pv2migration-batch"
ORIG_MIGRATION_LABEL="${MIGRATION_LABEL}"
CHECKPOINT_FILE="${BATCH_LOG_DIR}/${CHECKPOINT_FILE}"

# ==================== Validation ====================

resolve_mode_script() {
  case "$MIGRATION_MODE" in
    inplace) MODE_SCRIPT="${SCRIPT_DIR}/premium-to-premiumv2-migrator-inplace.sh" ;;
    dual)    MODE_SCRIPT="${SCRIPT_DIR}/premium-to-premiumv2-migrator-dualpvc.sh" ;;
    attrclass) MODE_SCRIPT="${SCRIPT_DIR}/premium-to-premiumv2-migrator-vac.sh" ;;
    *)
      err "Invalid MIGRATION_MODE='${MIGRATION_MODE}'. Must be one of: inplace, dual, attrclass"
      exit 1
      ;;
  esac
  if [[ ! -x "$MODE_SCRIPT" ]]; then
    err "Mode script not found or not executable: $MODE_SCRIPT"
    exit 1
  fi
}

# ==================== Checkpoint Logic ====================

checkpoint_init() {
  if [[ -f "$CHECKPOINT_FILE" ]]; then
    # Validate existing checkpoint
    if ! jq -e '.version' "$CHECKPOINT_FILE" >/dev/null 2>&1; then
      err "Corrupt checkpoint file: $CHECKPOINT_FILE"
      exit 1
    fi
    local ver
    ver=$(jq -r '.version' "$CHECKPOINT_FILE")
    if [[ "$ver" != "1" ]]; then
      err "Unsupported checkpoint version: $ver"
      exit 1
    fi
    info "Loaded checkpoint: $CHECKPOINT_FILE ($(jq '.pvcs | length' "$CHECKPOINT_FILE") PVCs tracked)"
  else
    echo '{"version":1,"pvcs":{}}' | jq '.' > "$CHECKPOINT_FILE"
    info "Created new checkpoint: $CHECKPOINT_FILE"
  fi
}

# Returns status of a PVC from checkpoint. Prints "new" if not present.
checkpoint_get_status() {
  local key="$1"
  local status
  status=$(jq -r --arg k "$key" '.pvcs[$k].status // "new"' "$CHECKPOINT_FILE")
  printf '%s' "$status"
}

# Atomically updates PVC status in checkpoint file.
# Uses flock to serialize concurrent access from parallel batches.
# Optional 3rd arg: pid to store alongside the status.
checkpoint_set_status() {
  local key="$1" status="$2" pid="${3:-}"
  local ts_now
  ts_now="$(date -u +'%Y-%m-%dT%H:%M:%SZ')"
  local lockfile="${CHECKPOINT_FILE}.lock"
  local tmp
  tmp="${CHECKPOINT_FILE}.tmp.${BASHPID:-$$}.$(date +%s%N)"

  local time_field="updated_at"
  if [[ "$status" == "in_progress" ]]; then
    time_field="started_at"
  elif [[ "$status" == "done" || "$status" == "failed" ]]; then
    time_field="finished_at"
  fi

  (
    flock -x 200
    if [[ -n "$pid" ]]; then
      jq --arg k "$key" --arg s "$status" --arg t "$ts_now" --arg tf "$time_field" --arg p "$pid" \
        '.pvcs[$k] = ((.pvcs[$k] // {}) + {"status": $s, ($tf): $t, "pid": ($p | tonumber)})' \
        "$CHECKPOINT_FILE" > "$tmp" && mv "$tmp" "$CHECKPOINT_FILE"
    else
      jq --arg k "$key" --arg s "$status" --arg t "$ts_now" --arg tf "$time_field" \
        '.pvcs[$k] = ((.pvcs[$k] // {}) + {"status": $s, ($tf): $t})' \
        "$CHECKPOINT_FILE" > "$tmp" && mv "$tmp" "$CHECKPOINT_FILE"
    fi
  ) 200>"$lockfile"
}

# Returns the PID stored for a PVC in checkpoint. Empty if not set.
checkpoint_get_pid() {
  local key="$1"
  jq -r --arg k "$key" '.pvcs[$k].pid // empty' "$CHECKPOINT_FILE"
}

# Lists PVC keys not in "done" status.
checkpoint_list_pending() {
  jq -r '.pvcs | to_entries[] | select(.value.status != "done") | .key' "$CHECKPOINT_FILE"
}

# Summary counts from checkpoint.
checkpoint_summary() {
  jq -r '[.pvcs | to_entries[] | .value.status] | group_by(.) | map({(.[0]): length}) | add // {}' "$CHECKPOINT_FILE"
}

# ==================== PVC Discovery ====================

# Get existing batch label for a PVC from ALL_PVCS_JSON (loaded by populate_pvcs).
get_cached_batch_label() {
  local ns="$1" pvc="$2"
  local pvc_json
  pvc_json=$(get_cached_pvc_json "$ns" "$pvc" 2>/dev/null || echo "")
  if [[ -n "$pvc_json" ]]; then
    echo "$pvc_json" | jq -r --arg key "$BATCH_LABEL_KEY" '.metadata.labels[$key] // empty'
  fi
}

# Filter PVCs against checkpoint (skip "done").
filter_eligible_pvcs() {
  local -a all_pvcs=("$@")
  local -a eligible=()
  for entry in "${all_pvcs[@]}"; do
    local ns="${entry%%|*}"
    local pvc="${entry##*|}"
    local key="${ns}/${pvc}"
    local status
    status=$(checkpoint_get_status "$key")
    if [[ "$status" == "done" ]]; then
      continue
    fi
    eligible+=("$entry")
  done
  printf '%s\n' "${eligible[@]}"
}

# ==================== Batch Planning ====================

# Splits PVCs into batches. PVCs that already have a batch label from a
# previous run keep their existing batch ID (no reassignment). Only PVCs
# without a batch label get new IDs assigned in groups of BATCH_SIZE.
# Outputs: batch_index|ns|pvc
plan_batches() {
  local -a pvcs=("$@")

  # Separate PVCs with existing batch IDs from new ones
  local -a existing_lines=()
  local -a new_pvcs=()
  local max_existing=-1

  for entry in "${pvcs[@]}"; do
    local ns="${entry%%|*}"
    local pvc="${entry##*|}"
    local bid
    bid=$(get_cached_batch_label "$ns" "$pvc")
    if [[ -n "$bid" ]]; then
      existing_lines+=("${bid}|${ns}|${pvc}")
      [[ "$bid" =~ ^[0-9]+$ && bid -gt max_existing ]] && max_existing=$bid
    else
      new_pvcs+=("$entry")
    fi
  done

  # Emit existing batch assignments as-is
  if (( ${#existing_lines[@]} > 0 )); then
    printf '%s\n' "${existing_lines[@]}"
  fi

  # Assign new batch IDs starting after the highest existing one
  local next_id=$(( max_existing + 1 ))
  local count=0
  for entry in "${new_pvcs[@]}"; do
    local ns="${entry%%|*}"
    local pvc="${entry##*|}"
    echo "${next_id}|${ns}|${pvc}"
    count=$(( count + 1 ))
    if (( count >= BATCH_SIZE )); then
      next_id=$(( next_id + 1 ))
      count=0
    fi
  done
}

print_plan() {
  local total="$1" eligible_count="$2" num_batches="$3" concurrency="$4"
  echo
  info "=== Migration Batch Plan ==="
  echo "  Mode:             ${MIGRATION_MODE}"
  echo "  Total labeled PVCs: ${total}"
  echo "  Already done:     $(( total - eligible_count ))"
  echo "  Eligible this run:  ${eligible_count}"
  echo "  Batch size:       ${BATCH_SIZE}"
  echo "  Concurrency:      ${concurrency}"
  echo "  Total batches:    ${num_batches}"
  if (( concurrency > 0 )); then
    echo "  Estimated waves:  $(( (num_batches + concurrency - 1) / concurrency ))"
  fi
  echo "  Checkpoint file:  ${CHECKPOINT_FILE}"
  echo "  Batch log dir:    ${BATCH_LOG_DIR}"
  echo
}

# ==================== Preflight ====================

# Pre-create shared cluster-scoped resources (VolumeSnapshotClass, StorageClass
# variants, VolumeAttributesClass) once before launching concurrent batches.
# Sources the common lib and zonal helper directly to call only setup functions.
run_preflight() {
  info "=== Preflight: pre-creating shared cluster resources ==="

  (
    # shellcheck disable=SC1090
    source "${SCRIPT_DIR}/premium-to-premiumv2-zonal-aware-helper.sh"

    ensure_snapshot_class

    export MIGRATION_LABEL="${ORIG_MIGRATION_LABEL}"
    populate_pvcs

    MODE="$MIGRATION_MODE" process_pvcs_for_zone_preparation

    MODE="$MIGRATION_MODE" create_unique_storage_classes

    if [[ "$MIGRATION_MODE" == "attrclass" ]]; then
      ATTR_CLASS_NAME="${ATTR_CLASS_NAME:-azuredisk-premiumv2}"
      ATTR_CLASS_API_VERSION="${ATTR_CLASS_API_VERSION:-storage.k8s.io/v1}"
      TARGET_SKU="${TARGET_SKU:-PremiumV2_LRS}"
      ATTR_CLASS_FORCE_RECREATE="${ATTR_CLASS_FORCE_RECREATE:-false}"
      ensure_volume_attributes_class
    fi

  ) 2>&1 | tee "${BATCH_LOG_DIR}/preflight.log"

  local rc=${PIPESTATUS[0]}
  if (( rc != 0 )); then
    warn "Preflight exited with code ${rc}. Check ${BATCH_LOG_DIR}/preflight.log"
    warn "Shared resources may already exist (continuing)."
  fi
  ok "Preflight complete."
}

# ==================== Concurrent Executor ====================

# Apply temporary batch label to PVCs so the mode script discovers only them.
label_batch_pvcs() {
  local batch_id="$1"
  shift
  local -a entries=("$@")
  for entry in "${entries[@]}"; do
    local ns="${entry%%|*}"
    local pvc="${entry##*|}"
    kcmd label pvc "$pvc" -n "$ns" "${BATCH_LABEL_KEY}=${batch_id}" --overwrite >/dev/null 2>&1 || \
      warn "Failed to label PVC ${ns}/${pvc} with batch=${batch_id}"
  done
}

# Remove temporary batch label from PVCs.
unlabel_batch_pvcs() {
  local batch_id="$1"
  shift
  local -a entries=("$@")
  for entry in "${entries[@]}"; do
    local ns="${entry%%|*}"
    local pvc="${entry##*|}"
    kcmd label pvc "$pvc" -n "$ns" "${BATCH_LABEL_KEY}-" --overwrite >/dev/null 2>&1 || true
  done
}

# Run a single batch: label → invoke mode script → update checkpoint → unlabel.
# This function runs in a subshell (backgrounded).
run_batch() {
  local batch_id="$1"
  shift
  local -a entries=("$@")
  local batch_dir="${BATCH_LOG_DIR}/batch-${batch_id}"
  mkdir -p "$batch_dir" "${batch_dir}/pvc-backups"
  touch "${batch_dir}/audit.log"
  local batch_log="${batch_dir}/batch-${batch_id}.log"
  local rc=0

  # Mark PVCs in-progress with this batch's PID for stale detection on restart
  local batch_pid="${BASHPID:-$$}"
  for entry in "${entries[@]}"; do
    local ns="${entry%%|*}"
    local pvc="${entry##*|}"
    checkpoint_set_status "${ns}/${pvc}" "in_progress" "$batch_pid"
  done

  # Apply scoped label
  label_batch_pvcs "$batch_id" "${entries[@]}"

  # Invoke the existing mode script with scoped label.
  # Each batch gets its own working directory for audit logs, PVC backups, etc.
  # Env vars are set via prefix syntax so they only apply to this subprocess
  # and don't leak back to the wrapper or other batches.
  info "Batch ${batch_id}: starting (${#entries[@]} PVCs) -> ${batch_dir}"
  set +e
  MIGRATION_LABEL="${BATCH_LABEL_KEY}=${batch_id}" \
  MAX_PVCS="${BATCH_SIZE}" \
  AUDIT_ENABLE="${AUDIT_ENABLE:-true}" \
  AUDIT_LOG_FILE="${batch_dir}/audit.log" \
  PVC_BACKUP_DIR="${batch_dir}/pvc-backups" \
  ZONE_MAPPING_FILE="${batch_dir}/disk-zone-mapping.txt" \
    bash -c "cd '${batch_dir}' && bash '${MODE_SCRIPT}'" >>"$batch_log" 2>&1
  rc=$?
  set -e

  # Update checkpoint based on result
  for entry in "${entries[@]}"; do
    local ns="${entry%%|*}"
    local pvc="${entry##*|}"
    if (( rc == 0 )); then
      checkpoint_set_status "${ns}/${pvc}" "done"
    else
      checkpoint_set_status "${ns}/${pvc}" "failed"
    fi
  done

  # Clean up batch label
  unlabel_batch_pvcs "$batch_id" "${entries[@]}"

  if (( rc == 0 )); then
    ok "Batch ${batch_id}: completed successfully"
  else
    warn "Batch ${batch_id}: failed (exit code ${rc}), see ${batch_log}"
  fi
  return $rc
}

# Execute all batches with concurrency control.
execute_batches() {
  local concurrency="$1"; shift
  local -a batch_plan_lines=("$@")

  # Group plan lines by batch index
  local max_batch_idx=-1
  declare -A BATCH_ENTRIES
  for line in "${batch_plan_lines[@]}"; do
    local bidx="${line%%|*}"
    local rest="${line#*|}"
    if [[ -z "${BATCH_ENTRIES[$bidx]:-}" ]]; then
      BATCH_ENTRIES[$bidx]="$rest"
    else
      BATCH_ENTRIES[$bidx]="${BATCH_ENTRIES[$bidx]}"$'\n'"$rest"
    fi
    (( bidx > max_batch_idx )) && max_batch_idx=$bidx
  done

  local total_batches=$(( max_batch_idx + 1 ))
  local completed=0
  local failed=0
  local running=0

  # Track PIDs -> batch_id mapping
  declare -A PID_BATCH

  # Reap finished background jobs, updating counters.
  reap_finished() {
    local pid
    for pid in "${!PID_BATCH[@]}"; do
      if ! kill -0 "$pid" 2>/dev/null; then
        local wrc=0
        # wait only works for child processes; adopted PIDs from a previous
        # wrapper instance are not children of this shell.
        if wait "$pid" 2>/dev/null; then
          wrc=$?
        fi
        (( wrc != 0 )) && failed=$(( failed + 1 ))
        completed=$(( completed + 1 ))
        running=$(( running - 1 ))
        unset "PID_BATCH[$pid]"
      fi
    done
  }

  for (( bidx=0; bidx < total_batches; bidx++ )); do
    # Wait if we're at max concurrency
    while (( running >= concurrency )); do
      sleep 2
      reap_finished
    done

    # Parse entries for this batch
    local -a entries=()
    while IFS= read -r line; do
      [[ -n "$line" ]] && entries+=("$line")
    done <<< "${BATCH_ENTRIES[$bidx]}"

    # Skip if a previous run's process is still alive for these PVCs.
    local stale_pid=""
    for entry in "${entries[@]}"; do
      local ns="${entry%%|*}"
      local pvc="${entry##*|}"
      local stored_pid
      stored_pid=$(checkpoint_get_pid "${ns}/${pvc}")
      if [[ -n "$stored_pid" ]] && kill -0 "$stored_pid" 2>/dev/null; then
        stale_pid="$stored_pid"
        break
      fi
    done
    if [[ -n "$stale_pid" ]]; then
      info "Batch ${bidx}: previous process (pid=${stale_pid}) still running — tracking it."
      PID_BATCH[$stale_pid]=$bidx
      running=$(( running + 1 ))
      continue
    fi

    # Launch batch in background
    run_batch "$bidx" "${entries[@]}" &
    local pid=$!
    PID_BATCH[$pid]=$bidx
    running=$(( running + 1 ))
    info "Launched batch ${bidx}/${total_batches} (pid=${pid}, running=${running}/${concurrency})"
  done

  # Wait for all remaining
  while (( running > 0 )); do
    sleep 2
    reap_finished
  done

  echo
  info "=== Execution Complete ==="
  echo "  Total batches: ${total_batches}"
  echo "  Completed:     ${completed}"
  echo "  Failed:        ${failed}"
}

# ==================== Signal Handling ====================

cleanup_on_signal() {
  echo
  warn "Signal received — saving checkpoint and waiting for active batches..."
  # wait for all children to finish (they each save checkpoint on exit)
  wait 2>/dev/null || true
  info "All active batches finished. Checkpoint saved to: ${CHECKPOINT_FILE}"
  info "Re-run this script to resume from where it left off."
  exit 130
}

# ==================== Main ====================

main() {
  local start_epoch
  start_epoch=$(date +%s)
  # Capture wrapper's own stdout/stderr to wrapper.log (while still showing on terminal)
  mkdir -p "$BATCH_LOG_DIR"
  touch "${BATCH_LOG_DIR}/wrapper.log"
  exec > >(tee -a "${BATCH_LOG_DIR}/wrapper.log") 2>&1
  trap cleanup_on_signal SIGINT SIGTERM

  # Validate
  require_bins
  if [[ -z "$MIGRATION_MODE" ]]; then
    err "MIGRATION_MODE is required. Set to one of: inplace, dual, attrclass"
    exit 1
  fi
  resolve_mode_script

  # Init
  checkpoint_init

  # Discover PVCs using populate_pvcs from common lib.
  # This loads ALL_PVCS_JSON, PVC_JSON_CACHE, PV_JSON_CACHE and fills MIG_PVCS
  # (with size filtering, MAX_PVCS cap, etc.)
  info "Discovering PVCs with label: ${ORIG_MIGRATION_LABEL}"
  MIGRATION_LABEL="${ORIG_MIGRATION_LABEL}"
  populate_pvcs
  local -a all_pvcs=("${MIG_PVCS[@]}")

  info "Found ${#all_pvcs[@]} eligible PVCs (after size/cap filtering)"

  # Filter
  local -a eligible_pvcs=()
  while IFS= read -r line; do
    [[ -n "$line" ]] && eligible_pvcs+=("$line")
  done < <(filter_eligible_pvcs "${all_pvcs[@]}")

  if (( ${#eligible_pvcs[@]} == 0 )); then
    ok "All PVCs already done (per checkpoint). Nothing to do."
    exit 0
  fi

  # Compute concurrency
  local concurrency="${BATCH_CONCURRENCY}"
  if [[ -z "$concurrency" ]]; then
    concurrency=${#eligible_pvcs[@]}
    (( concurrency > 50 )) && concurrency=50
  fi

  # Plan
  local -a batch_plan=()
  while IFS= read -r line; do
    [[ -n "$line" ]] && batch_plan+=("$line")
  done < <(plan_batches "${eligible_pvcs[@]}")

  # Count batches
  local num_batches=0
  if (( ${#batch_plan[@]} > 0 )); then
    local last_idx="${batch_plan[-1]%%|*}"
    num_batches=$(( last_idx + 1 ))
  fi

  print_plan "${#all_pvcs[@]}" "${#eligible_pvcs[@]}" "$num_batches" "$concurrency"

  # Dry run exit
  if [[ "$DRY_RUN" == "true" ]]; then
    info "DRY RUN — listing PVCs per batch:"
    for line in "${batch_plan[@]}"; do
      local bidx="${line%%|*}"
      local rest="${line#*|}"
      local ns="${rest%%|*}"
      local pvc="${rest##*|}"
      echo "  batch ${bidx}: ${ns}/${pvc}"
    done
    echo
    ok "Dry run complete. No mutations performed."
    exit 0
  fi

  # Preflight: create shared resources once before concurrent batches
  run_preflight

  # Execute
  execute_batches "$concurrency" "${batch_plan[@]}"

  # Final summary
  local end_epoch
  end_epoch=$(date +%s)
  local elapsed=$(( end_epoch - start_epoch ))
  echo
  info "=== Final Checkpoint Summary ==="
  checkpoint_summary
  echo
  info "Elapsed: $(( elapsed / 60 ))m $(( elapsed % 60 ))s"
  info "Checkpoint: ${CHECKPOINT_FILE}"
  info "Re-run to resume any failed/pending PVCs."
  ok "Batch migration wrapper finished."
}

main "$@"
