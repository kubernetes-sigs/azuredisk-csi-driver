#!/usr/bin/env bash
# Common library for Premium_LRS -> PremiumV2_LRS migration helpers
set -euo pipefail
IFS=$'\n\t'

# ---------- Logging / Audit ----------
ts()   { date +'%Y-%m-%dT%H:%M:%S'; }
info() { echo "$(ts) [INFO] $*"; }
warn() { echo "$(ts) [WARN] $*" >&2; }
err()  { echo "$(ts) [ERROR] $*" >&2; }
ok()   { echo "$(ts) [OK] $*"; }

# ----------- Common configurations -----------
MIG_SUFFIX="${MIG_SUFFIX:-csi}"
AUDIT_ENABLE="${AUDIT_ENABLE:-true}"
AUDIT_LOG_FILE="${AUDIT_LOG_FILE:-pv1-pv2-migration-audit.log}"
SNAPSHOT_MAX_AGE_SECONDS="${SNAPSHOT_MAX_AGE_SECONDS:-7200}"
SNAPSHOT_RECREATE_ON_STALE="${SNAPSHOT_RECREATE_ON_STALE:-false}"
SNAPSHOT_CLASS="${SNAPSHOT_CLASS:-csi-azuredisk-vsc}"
MIGRATION_LABEL="${MIGRATION_LABEL:-disk.csi.azure.com/pv2migration=true}"
NAMESPACE="${NAMESPACE:-}"
MAX_PVCS="${MAX_PVCS:-50}"
POLL_INTERVAL="${POLL_INTERVAL:-120}"
WAIT_FOR_WORKLOAD="${WAIT_FOR_WORKLOAD:-true}"
MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES="${MIGRATION_FORCE_INPROGRESS_AFTER_MINUTES:-3}"

# In-place rollback keys
ROLLBACK_PVC_YAML_ANN="${ROLLBACK_PVC_YAML_ANN:-disk.csi.azure.com/rollback-pvc-yaml}"
ROLLBACK_ORIG_PV_ANN="${ROLLBACK_ORIG_PV_ANN:-disk.csi.azure.com/rollback-orig-pv}"

# Maximum PVC size (in GiB) eligible for migration (default 2TiB = 2048GiB). PVCs >= this are skipped.
MAX_PVC_CAPACITY_GIB="${MAX_PVC_CAPACITY_GIB:-2048}"
declare -a AUDIT_LINES=()
MIGRATION_LABEL_KEY="${MIGRATION_LABEL%%=*}"
MIGRATION_LABEL_VALUE="${MIGRATION_LABEL#*=}"
BACKUP_ORIGINAL_PVC="${BACKUP_ORIGINAL_PVC:-true}"
PVC_BACKUP_DIR="${PVC_BACKUP_DIR:-pvc-backups}"

# ---------- Timeouts ----------
BIND_TIMEOUT_SECONDS="${BIND_TIMEOUT_SECONDS:-60}"
MONITOR_TIMEOUT_MINUTES="${MONITOR_TIMEOUT_MINUTES:-300}"
WORKLOAD_DETACH_TIMEOUT_MINUTES="${WORKLOAD_DETACH_TIMEOUT_MINUTES:-5}"
EXIT_ON_WORKLOAD_DETACH_TIMEOUT="${EXIT_ON_WORKLOAD_DETACH_TIMEOUT:-false}"

# ---------- Kubectl retry configurations ----------
KUBECTL_MAX_RETRIES="${KUBECTL_MAX_RETRIES:-5}"
KUBECTL_RETRY_BASE_DELAY="${KUBECTL_RETRY_BASE_DELAY:-2}"
KUBECTL_RETRY_MAX_DELAY="${KUBECTL_RETRY_MAX_DELAY:-30}"
KUBECTL_TRANSIENT_REGEX="${KUBECTL_TRANSIENT_REGEX:-(connection refused|i/o timeout|timeout exceeded|TLS handshake timeout|context deadline exceeded|Service Unavailable|Too Many Requests|EOF|transport is closing|Internal error|no route to host|Connection reset by peer)}"

# ---------- Labels / Annotations ----------
CREATED_BY_LABEL_KEY="${CREATED_BY_LABEL_KEY:-disk.csi.azure.com/created-by}"
MIGRATION_TOOL_ID="${MIGRATION_TOOL_ID:-azuredisk-pv1-to-pv2-migrator}"
MIGRATION_DONE_LABEL_KEY="${MIGRATION_DONE_LABEL_KEY:-disk.csi.azure.com/migration-done}"
MIGRATION_DONE_LABEL_VALUE="${MIGRATION_DONE_LABEL_VALUE:-true}"
MIGRATION_DONE_LABEL_VALUE_FALSE="${MIGRATION_DONE_LABEL_VALUE_FALSE:-false}"
MIGRATION_INPROGRESS_LABEL_KEY="${MIGRATION_INPROGRESS_LABEL_KEY:-disk.csi.azure.com/migration-in-progress}"
MIGRATION_INPROGRESS_LABEL_VALUE="${MIGRATION_INPROGRESS_LABEL_VALUE:-true}"

audit_add() {
  [[ "$AUDIT_ENABLE" != "true" ]] && return 0
  local kind="$1" name="$2" namespace="$3" action="$4" revert="$5" extra="$6"
  local line
  line="$(ts)|${action}|${kind}|${namespace}|${name}|${revert}|${extra}"
  AUDIT_LINES+=("$line")
  if [[ -n "$AUDIT_LOG_FILE" ]]; then
    printf '%s\n' "$line" >>"$AUDIT_LOG_FILE" || true
  fi
}

human_duration() {
  local total=${1:-0}
  local h=$(( total / 3600 ))
  local m=$(( (total % 3600) / 60 ))
  local s=$(( total % 60 ))
  if (( h>0 )); then printf '%dh%02dm%02ds' "$h" "$m" "$s"
  elif (( m>0 )); then printf '%dm%02ds' "$m" "$s"
  else printf '%ds' "$s"; fi
}

finalize_audit_summary() {
  [[ "$AUDIT_ENABLE" != "true" ]] && return 0
  local start_ts="$1" start_epoch="$2"
  local end_ts end_epoch dur_sec
  end_ts="$(date +'%Y-%m-%dT%H:%M:%S')"
  end_epoch="$(date +%s)"
  dur_sec=$(( end_epoch - start_epoch ))
  local dur_fmt
  dur_fmt="$(human_duration "$dur_sec")"
  echo
  info "Run Timing:"
  echo "  Start: ${start_ts}"
  echo "  End:   ${end_ts}"
  echo "  Elapsed: ${dur_fmt} (${dur_sec}s)"
  info "Audit Trail"
  if (( ${#AUDIT_LINES[@]} == 0 )); then
    echo "  (no mutating actions recorded)"
  else
    echo
    info "Best-effort summary to revert if required:"
    local line act revert
    for line in "${AUDIT_LINES[@]}"; do
      IFS='|' read -r _ act _ _ _ revert _ <<<"$line"
      [[ -z "$revert" || "$revert" == "N/A" ]] && continue
      printf '  %s\n' "$revert"
    done
  fi
  if [[ -n "$AUDIT_LOG_FILE" ]]; then
    {
      printf 'RUN_END|%s|durationSeconds=%d|durationHuman=%s\n' "$end_ts" "$dur_sec" "$dur_fmt"
    } >>"$AUDIT_LOG_FILE" 2>/dev/null || true
  fi
}

kubectl_retry() {
  local attempt=1 rc output
  while true; do
    set +e
    output=$(kubectl "$@" 2>&1)
    rc=$?
    set -e
    if [[ $rc -eq 0 ]]; then
      printf '%s' "$output"
      return 0
    fi
    if ! grep -qiE "$KUBECTL_TRANSIENT_REGEX" <<<"$output"; then
      echo "$output" >&2
      return $rc
    fi
    if (( attempt >= KUBECTL_MAX_RETRIES )); then
      warn "kubectl retry exhausted ($attempt): kubectl $*"
      echo "$output" >&2
      return $rc
    fi
    local sleep_time=$(( KUBECTL_RETRY_BASE_DELAY * 2 ** (attempt-1) ))
    (( sleep_time > KUBECTL_RETRY_MAX_DELAY )) && sleep_time=$KUBECTL_RETRY_MAX_DELAY
    warn "kubectl transient (attempt $attempt) -> retry in ${sleep_time}s: $(head -n1 <<<"$output")"
    sleep "$sleep_time"
    attempt=$(( attempt + 1 ))
  done
}

kcmd() {
  kubectl_retry "$@"
}

kapply_retry() {
  local tmp rc attempt=1 out
  tmp="$(mktemp)"
  cat >"$tmp"
  while true; do
    set +e
    out=$(kubectl apply -f "$tmp" 2>&1)
    rc=$?
    set -e
    if [[ $rc -eq 0 ]]; then
      printf '%s\n' "$out"
      rm -f "$tmp"
      return 0
    fi
    if ! grep -qiE "$KUBECTL_TRANSIENT_REGEX" <<<"$out"; then
      echo "$out" >&2
      rm -f "$tmp"
      return $rc
    fi
    if (( attempt >= KUBECTL_MAX_RETRIES )); then
      warn "kubectl apply retry exhausted ($attempt)"
      echo "$out" >&2
      rm -f "$tmp"
      return $rc
    fi
    local sleep_time=$(( KUBECTL_RETRY_BASE_DELAY * 2 ** (attempt-1) ))
    (( sleep_time > KUBECTL_RETRY_MAX_DELAY )) && sleep_time=$KUBECTL_RETRY_MAX_DELAY
    warn "kubectl apply transient (attempt $attempt) -> retry in ${sleep_time}s: $(head -n1 <<<"$out")"
    sleep "$sleep_time"
    attempt=$(( attempt + 1 ))
  done
}

# ---------- Naming ----------
name_csi_pvc() { local pvc="$1"; echo "${pvc}-${MIG_SUFFIX}"; }
name_csi_pv()  { local pv="$1"; echo "${pv}-${MIG_SUFFIX}"; }
name_snapshot(){ local pv="$1"; echo "ss-$(name_csi_pv "$pv")"; }
name_pv2_pvc() { local pvc="$1"; echo "${pvc}-${MIG_SUFFIX}-pv2"; }
name_pv1_sc()  { local sc="$1"; echo "${sc}-pv1"; }
name_pv2_sc()  { local sc="$1"; echo "${sc}-pv2"; }

# ---------- Utilities ----------
require_bins() {
  local missing=()
  for b in kubectl jq base64; do
    command -v "$b" >/dev/null 2>&1 || missing+=("$b")
  done
  if (( ${#missing[@]} > 0 )); then
    err "Missing required binaries: ${missing[*]}"
    exit 1
  fi
}

# Convert a Kubernetes size string to an integer GiB (ceiling for sub-Gi units).
# Supports Ki, Mi, Gi, Ti, Pi (with optional 'i'). Returns 0 if unparseable.
size_to_gi_ceiling() {
  local raw="$1"
  [[ -z "$raw" ]] && { echo 0; return; }
  if [[ "$raw" =~ ^([0-9]+)([KMGTP]i?)$ ]]; then
    local n="${BASH_REMATCH[1]}"
    local u="${BASH_REMATCH[2]}"
    case "${u}" in
      Ki|K) echo 0 ;;  # effectively negligible; treat as 0 Gi
      Mi|M) echo $(( (n + 1023) / 1024 )) ;;
      Gi|G) echo $n ;;
      Ti|T) echo $(( n * 1024 )) ;;
      Pi|P) echo $(( n * 1024 * 1024 )) ;;
      *) echo 0 ;;
    esac
  else
    # Handles plain numbers (assume Gi)
    if [[ "$raw" =~ ^[0-9]+$ ]]; then
      echo "$raw"
    else
      echo 0
    fi
  fi
}

b64e() { base64 -w0; }
b64d() { base64 -d; }

get_pv_of_pvc() {
  local ns="$1" pvc="$2"
  kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true
}

is_in_tree_pv() {
  local pv="$1"
  local diskuri
  diskuri=$(kcmd get pv "$pv" -o jsonpath='{.spec.azureDisk.diskURI}' 2>/dev/null || true)
  [[ -n "$diskuri" ]]
}

is_csi_pv() {
  local pv="$1"
  local drv
  drv=$(kcmd get pv "$pv" -o jsonpath='{.spec.csi.driver}' 2>/dev/null || true)
  [[ "$drv" == "disk.csi.azure.com" ]]
}

ensure_reclaim_policy_retain() {
  local pv="$1"
  local current
  current=$(kcmd get pv "$pv" -o jsonpath='{.spec.persistentVolumeReclaimPolicy}' 2>/dev/null || true)
  [[ -z "$current" ]] && return 0
  if [[ "$current" == "Delete" ]]; then
    info "Patching reclaimPolicy Delete->Retain for PV $pv"
    kcmd patch pv "$pv" -p '{"spec":{"persistentVolumeReclaimPolicy":"Retain"}}' >/dev/null 2>&1 || true
    audit_add "PersistentVolume" "$pv" "" "patch" \
      "kubectl patch pv $pv -p '{\"spec\":{\"persistentVolumeReclaimPolicy\":\"Delete\"}}'" \
      "reclaimPolicy Delete->Retain"
  fi
}

mark_source_done() {
  local pvc_ns="$1" pvc="$2"
  kcmd label pvc "$pvc" -n "$pvc_ns" "${MIGRATION_DONE_LABEL_KEY}=${MIGRATION_DONE_LABEL_VALUE}" --overwrite
  audit_add "PersistentVolumeClaim" "$pvc" "$pvc_ns" "label" \
    "kubectl label pvc $pvc -n $pvc_ns ${MIGRATION_DONE_LABEL_KEY}-" \
    "done=${MIGRATION_DONE_LABEL_VALUE}"
}

remove_migration_done_label() {
  local pvc_ns="$1" pvc="$2"
  kcmd label pvc "$pvc" -n "$pvc_ns" "${MIGRATION_DONE_LABEL_KEY}-" --overwrite
  audit_add "PersistentVolumeClaim" "$pvc" "$pvc_ns" "label-remove" \
    "kubectl label pvc $pvc -n $pvc_ns ${MIGRATION_DONE_LABEL_KEY}=" \
    "remove done label"
}

wait_pvc_bound() {
  local ns="$1" name="$2" timeout="${3:-600}" poll=5 phase
  local end=$(( $(date +%s) + timeout ))
  while (( $(date +%s) < end )); do
    phase=$(kcmd get pvc "$name" -n "$ns" -o jsonpath='{.status.phase}' 2>/dev/null || true)
    [[ "$phase" == "Bound" ]] && return 0
    sleep "$poll"
  done
  return 1
}

get_pvc_encoded_json() {
  local pvc="$1" ns="$2"
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
    # Base64 encode sanitized JSON for rollback annotation
    printf '%s' "$orig_json" | jq 'del(.status) | {
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
      }' | b64e
}

get_pv_encoded_json() {
  local pvc="$1" ns="$2"
  local orig_json
  local pv

  pv=$(get_pv_of_pvc "$ns" "$pvc")

  [[ -z "$pv" ]] && { err "Original PVC has no bound PV (cannot proceed)"; return 1; }

  orig_json=$(kcmd get pv "$pv" -o json | jq '{
      apiVersion,
      kind,
      metadata: {
        name: .metadata.name,
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
    # Base64 encode sanitized JSON for rollback annotation
    printf '%s' "$orig_json" | jq 'del(.status) | {
        apiVersion,
        kind,
        metadata: {
          name: .metadata.name,
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
      }' | b64e
}

backup_pvc() {
  local pvc="$1" ns="$2"
  if [[ "${BACKUP_ORIGINAL_PVC}" == "true" ]]; then
    mkdir -p "${PVC_BACKUP_DIR}/${ns}"
    local stamp tmp_file backup_file sc
    stamp="$(date +%Y%m%d-%H%M%S)"
    tmp_file="${PVC_BACKUP_DIR}/${ns}/${pvc}-${stamp}.yaml.tmp"
    backup_file="${PVC_BACKUP_DIR}/${ns}/${pvc}-${stamp}.yaml"
    sc=$(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.storageClassName}' 2>/dev/null || true)

    if ! kcmd get pvc "$pvc" -n "$ns" -o yaml >"$tmp_file" 2>/dev/null; then
      err "Failed to fetch PVC $ns/$pvc for backup; skipping migration of this PVC."
      audit_add "PersistentVolumeClaim" "$pvc" "$ns" "backup-failed" "kubectl get pvc $pvc -n $ns -o yaml" "dest=$tmp_file reason=kubectlError"
      return 1
    fi
    if [[ ! -s "$tmp_file" ]] || ! grep -q '^kind: *PersistentVolumeClaim' "$tmp_file"; then
      err "Backup validation failed for $ns/$pvc (empty or malformed); skipping migration."
      rm -f "$tmp_file"
      audit_add "PersistentVolumeClaim" "$pvc" "$ns" "backup-invalid" "kubectl get pvc $pvc -n $ns -o yaml" "dest=$backup_file reason=validation"
      return 1
    fi
    mv "$tmp_file" "$backup_file"
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "backup" "rm -f $backup_file" "dest=$backup_file size=$(wc -c <"$backup_file")B sc=${sc}"
  fi
}

create_csi_pv_pvc() {
  local pvc="$1" ns="$2" pv="$3" size="$4" mode="$5" sc="$6" diskURI="$7" inplace="${8:-false}"
  local csi_pv csi_pvc
  local encoded_spec encoded_pv
  local pv_before

  # check if MIGRATION_LABEL_KEY exists on source pvc and its value is MIGRATION_LABEL_VALUE
  if kcmd get pvc "$pvc" -n "$ns" -o json | jq -e --arg key "$MIGRATION_LABEL_KEY" --arg value "$MIGRATION_LABEL_VALUE" '.metadata.labels[$key]==$value' >/dev/null; then
    migration_label_exists=true
  fi

  csi_pv="$(name_csi_pv "$pv")"
  csi_pvc="$(name_csi_pvc "$pvc")"
  if $inplace; then
    csi_pvc="$pvc"
  fi
  if kcmd get pvc "$csi_pvc" -n "$ns" >/dev/null 2>&1; then
    kcmd get pvc "$csi_pvc" -n "$ns" -o json | jq -e --arg key "$CREATED_BY_LABEL_KEY" --arg tool "$MIGRATION_TOOL_ID" '.metadata.labels[$key]==$tool' >/dev/null \
      && { info "Intermediate PVC $ns/$csi_pvc exists"; return; }
    if ! $inplace; then
      warn "Intermediate PVC $ns/$csi_pvc exists but missing label"
      return
    fi
  fi
  encoded_spec=""
  encoded_pv=""
  pv_before=""
  if [[ $inplace == true ]]; then
    encoded_pv=$(get_pv_encoded_json "$pvc" $ns)
    pv_before=$(get_pv_of_pvc "$ns" "$pvc")

    backup_pvc "$pvc" "$ns" || {
      warn "PVC backup failed $ns/$pvc"
    }
    # Base64 encode sanitized JSON for rollback annotation
    encoded_spec=$(get_pvc_encoded_json "$pvc" "$ns")
    ensure_reclaim_policy_retain "$pv"
    if ! kcmd delete pvc "$csi_pvc" -n "$ns" --wait=true >/dev/null 2>&1; then
      warn "Deleted preexisting PVC $ns/$csi_pvc for inplace recreation"
      audit_add "PersistentVolumeClaim" "$csi_pvc" "$ns" "delete" "N/A" "inplace=true reason=preexisting"
      return 1
    fi
    audit_add "PersistentVolumeClaim" "$csi_pvc" "$ns" "delete" "N/A" "inplace=true reason=preexisting"

    if ! kcmd patch pv "$pv" -p '{"spec":{"claimRef":null}}' >/dev/null 2>&1; then
      warn "Clear claimRef failed for PV $pv"
      audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-clear-claimref-failed" "kubectl describe pv $pv" "reason=patchError"
      return 1
    fi
    info "Recreating inplace CSI (from intree) PV/PVC $pv / $csi_pvc -> $csi_pv / $csi_pvc"
  else
    info "Creating intermediate PV/PVC $csi_pv / $csi_pvc"
  fi

  if ! kapply_retry <<EOF
apiVersion: v1
kind: PersistentVolume
metadata:
  name: $csi_pv
  labels:
    ${CREATED_BY_LABEL_KEY}: ${MIGRATION_TOOL_ID}
  annotations:
    pv.kubernetes.io/provisioned-by: disk.csi.azure.com
spec:
  capacity:
    storage: $size
  accessModes:
  - ReadWriteOnce
  claimRef:
    apiVersion: v1
    kind: PersistentVolumeClaim
    name: $csi_pvc
    namespace: $ns
  csi:
    driver: disk.csi.azure.com
    volumeHandle: $diskURI
    volumeAttributes:
      csi.storage.k8s.io/pv/name: $csi_pv
      csi.storage.k8s.io/pvc/name: $csi_pvc
      csi.storage.k8s.io/pvc/namespace: $ns
      requestedsizegib: "$size"
      skuname: Premium_LRS
  persistentVolumeReclaimPolicy: Retain
  storageClassName: $sc
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $csi_pvc
  namespace: $ns
  labels:
    ${CREATED_BY_LABEL_KEY}: ${MIGRATION_TOOL_ID}
$( [[ $inplace == true && $migration_label_exists == true ]] && echo "    ${MIGRATION_LABEL_KEY}: \"${MIGRATION_LABEL_VALUE}\"" )
$( [[ -n $encoded_spec && -n ${ROLLBACK_PVC_YAML_ANN:-} ]] && echo "  annotations:" )
$( [[ -n $encoded_spec && -n ${ROLLBACK_PVC_YAML_ANN:-} ]] && echo "    ${ROLLBACK_PVC_YAML_ANN}: $encoded_spec" )
$( [[ -n $pv_before && -n ${ROLLBACK_ORIG_PV_ANN:-} ]] && echo "    ${ROLLBACK_ORIG_PV_ANN}: $pv_before" )
spec:
  accessModes:
  - ReadWriteOnce
  volumeMode: ${mode}
  resources:
    requests:
      storage: $size
  storageClassName: $sc
  volumeName: $csi_pv
EOF
  then
    audit_add "PersistentVolume" "$csi_pv" "" "create-failed" "N/A" "intermediate=true sourceDiskURI=$diskURI reason=applyFailure"
    audit_add "PersistentVolumeClaim" "$csi_pvc" "$ns" "create-failed" "N/A" "intermediate=true sc=$sc reason=applyFailure"
    return
  else
    audit_add "PersistentVolume" "$csi_pv" "" "create" "kubectl delete pv $csi_pv" "intermediate=true sourceDiskURI=$diskURI"
    audit_add "PersistentVolumeClaim" "$csi_pvc" "$ns" "create" "kubectl delete pvc $csi_pvc -n $ns" "intermediate=true sc=$sc"

    if $inplace; then
      # remove ROLLBACK_PVC_YAML_ANN annotation from pvc
      kcmd annotate pvc "$csi_pvc" -n "$ns" "${ROLLBACK_PVC_YAML_ANN}-" --overwrite >/dev/null 2>&1 || true
      kcmd annotate pvc "$csi_pvc" -n "$ns" "${ROLLBACK_ORIG_PV_ANN}-" --overwrite >/dev/null 2>&1 || true
    fi
  fi

  if wait_pvc_bound "$ns" "$csi_pvc" "$BIND_TIMEOUT_SECONDS"; then
    ok "PVC $ns/$csi_pvc bound"
    audit_add "PersistentVolumeClaim" "$csi_pvc" "$ns" "bound" "kubectl describe pvc $csi_pvc -n $ns" "csi=true"
    return 0
  fi

  warn "Intermediate PVC $ns/$csi_pvc not bound within timeout (${BIND_TIMEOUT_SECONDS}s)"
  audit_add "PersistentVolumeClaim" "$csi_pvc" "$ns" "bind-timeout" "kubectl describe pvc $csi_pvc -n $ns" "csi=true timeout=${BIND_TIMEOUT_SECONDS}s"
  return 2
}

create_pvc_from_snapshot() {
  local pvc="$1" ns="$2" pv="$3" size="$4" mode="$5" sc="$6" destpvc="$7" snapshot="$8"
  local inplace
  local encoded_spec
  local migration_label_exists
  local encoded_spec encoded_pv
  local pv_before

  if [[ "$destpvc" != "$pvc" ]]; then
    inplace=false
  else
    inplace=true
  fi

  # check if MIGRATION_LABEL_KEY exists on source pvc and its value is MIGRATION_LABEL_VALUE
  if kcmd get pvc "$pvc" -n "$ns" -o json | jq -e --arg key "$MIGRATION_LABEL_KEY" --arg value "$MIGRATION_LABEL_VALUE" '.metadata.labels[$key]==$value' >/dev/null; then
    migration_label_exists=true
  fi

  if kcmd get pvc "$destpvc" -n "$ns" >/dev/null 2>&1; then
    kcmd get pvc "$destpvc" -n "$ns" -o json | jq -e --arg key "$CREATED_BY_LABEL_KEY" --arg tool "$MIGRATION_TOOL_ID" '.metadata.labels[$key]==$tool' >/dev/null \
      && { info "PVC $ns/$destpvc exists"; return; }
    if ! $inplace; then
      warn "PVC $ns/$destpvc exists but missing label"
      return
    fi
  fi

  encoded_spec=""
  encoded_pv=""
  pv_before=""
  if [[ "$destpvc" == "$pvc" ]]; then
    inplace=true
    pv_before=$(get_pv_of_pvc "$ns" "$pvc")

    backup_pvc "$pvc" "$ns" || {
        warn "PVC (snapshot creation path) backup failed $ns/$pvc"
    }

    encoded_spec=$(get_pvc_encoded_json "$pvc" "$ns")
    ensure_reclaim_policy_retain "$pv"

    if ! kcmd delete pvc "$pvc" -n "$ns" --wait=true >/dev/null 2>&1; then
      warn "Deleted preexisting PVC $ns/$pvc for inplace recreation"
      audit_add "PersistentVolumeClaim" "$pvc" "$ns" "delete-failed" "N/A" "inplace=true reason=preexisting"
      return 1
    fi
    audit_add "PersistentVolumeClaim" "$pvc" "$ns" "delete" "N/A" "inplace=true reason=preexisting"

    if ! kcmd patch pv "$pv" -p '{"spec":{"claimRef":null}}' >/dev/null 2>&1; then
      warn "Clear claimRef failed for PV $pv"
      audit_add "PersistentVolumeClaim" "$pvc" "$ns" "rollback-clear-claimref-failed" "kubectl describe pv $pv" "reason=patchError"
      return 1
    fi
    info "Recreating inplace CSI (from snapshot) PV/PVC $snapshot -> $pv / $destpvc"
  fi

  info "Creating PVC $ns/$destpvc"
  if ! kapply_retry <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $destpvc
  namespace: $ns
  labels:
    ${CREATED_BY_LABEL_KEY}: ${MIGRATION_TOOL_ID}
$( [[ $inplace == true && $migration_label_exists == true ]] && echo "    ${MIGRATION_LABEL_KEY}: \"${MIGRATION_LABEL_VALUE}\"" )
$( [[ -n $encoded_spec && -n ${ROLLBACK_PVC_YAML_ANN:-} ]] && echo "  annotations:" )
$( [[ -n $encoded_spec && -n ${ROLLBACK_PVC_YAML_ANN:-} ]] && echo "    ${ROLLBACK_PVC_YAML_ANN}: $encoded_spec" )
$( [[ -n $pv_before && -n ${ROLLBACK_ORIG_PV_ANN:-} ]] && echo "    ${ROLLBACK_ORIG_PV_ANN}: $pv_before" )
spec:
  accessModes:
  - ReadWriteOnce
  volumeMode: $mode
  storageClassName: ${sc}
  resources:
    requests:
      storage: $size
  dataSourceRef:
    apiGroup: snapshot.storage.k8s.io
    kind: VolumeSnapshot
    name: $snapshot
EOF
  then
    audit_add "PersistentVolume" "$destpvc" "" "create-failed" "N/A" "intermediate=true reason=applyFailure"
    audit_add "PersistentVolumeClaim" "$destpvc" "$ns" "create-failed" "N/A" "inplace=$inplace sc=$sc reason=applyFailure"
    return 1
  else
    audit_add "PersistentVolumeClaim" "$destpvc" "$ns" "create" "kubectl delete pvc $destpvc -n $ns" "sc=${sc} snapshot=${snapshot}"
    if $inplace; then
      # remove ROLLBACK_PVC_YAML_ANN annotation from pvc
      kcmd annotate pvc "$destpvc" -n "$ns" "${ROLLBACK_PVC_YAML_ANN}-" --overwrite >/dev/null 2>&1 || true
      kcmd annotate pvc "$destpvc" -n "$ns" "${ROLLBACK_ORIG_PV_ANN}-" --overwrite >/dev/null 2>&1 || true
    fi
  fi

  if wait_pvc_bound "$ns" "$destpvc" "$BIND_TIMEOUT_SECONDS"; then
    ok "PVC $ns/$destpvc bound"
    audit_add "PersistentVolumeClaim" "$destpvc" "$ns" "bound" "kubectl describe pvc $destpvc -n $ns" "sc=${sc}"
    return 0
  fi

  warn "PVC $ns/$destpvc not bound within timeout (${BIND_TIMEOUT_SECONDS}s)"
  audit_add "PersistentVolumeClaim" "$destpvc" "$ns" "bind-timeout" "kubectl describe pvc $destpvc -n $ns" "sc=${sc} timeout=${BIND_TIMEOUT_SECONDS}s"
  return 2
}

# ---------- RBAC Check ----------
migration_rbac_check() {
  local RBAC_CHECK="${RBAC_CHECK:-true}"
  local RBAC_FAIL_FAST="${RBAC_FAIL_FAST:-true}"
  local RBAC_EXTRA_VERBOSE="${RBAC_EXTRA_VERBOSE:-false}"
  local NAMESPACE="${NAMESPACE:-}"

  [[ "$RBAC_CHECK" != "true" ]] && { info "RBAC preflight disabled"; return 0; }

  info "Performing RBAC preflight (mode=$MODE)..."

  # Superset cluster verbs (safe for both modes)
  local cluster_checks=(
    "get persistentvolumes"
    "create persistentvolumes"
    "patch persistentvolumes"
    "get storageclasses"
    "create storageclasses"
    "get volumesnapshotclasses.snapshot.storage.k8s.io"
    "create volumesnapshotclasses.snapshot.storage.k8s.io"
    "get volumeattachments.storage.k8s.io"
  )

  if [[ "$MODE" == "attrclass" ]]; then
    cluster_checks+=("get volumeattributesclass")
    cluster_checks+=("create volumeattributesclass")
  fi

  if [[ -z "$NAMESPACE" ]]; then
    cluster_checks+=("list persistentvolumeclaims")
  fi

  local ns_checks=(
    "get persistentvolumeclaims"
    "list persistentvolumeclaims"
    "create persistentvolumeclaims"
    "patch persistentvolumeclaims"
    "get volumesnapshots.snapshot.storage.k8s.io"
    "create volumesnapshots.snapshot.storage.k8s.io"
    "get events"
  )

  # In-place may need delete PVC (for same-name replacement)
  ns_checks+=("delete persistentvolumeclaims")

  local failures=() passed=0 total=0
  rbac_can() {
    local verb="$1" res="$2"
    shift 2 || true
    if kubectl auth can-i "$verb" "$res" "$@" >/dev/null 2>&1; then
      [[ "$RBAC_EXTRA_VERBOSE" == "true" ]] && info "RBAC OK: $verb $res $*"
      return 0
    fi
    return 1
  }

  for entry in "${cluster_checks[@]}"; do
    [[ -z "$entry" ]] && continue
    total=$((total+1)); read -r verb res <<<"$entry"
    if rbac_can "$verb" "$res"; then passed=$((passed+1)); else
      failures+=("cluster: $verb $res"); warn "RBAC missing: cluster: $verb $res"
      [[ "$RBAC_FAIL_FAST" == "true" ]] && { err "RBAC fail-fast."; return 1; }
    fi
  done

  if [[ -n "$NAMESPACE" ]]; then
    for entry in "${ns_checks[@]}"; do
      total=$((total+1)); read -r verb res <<<"$entry"
      if rbac_can "$verb" "$res" -n "$NAMESPACE"; then passed=$((passed+1)); else
        failures+=("namespace:$NAMESPACE: $verb $res"); warn "RBAC missing: namespace:$NAMESPACE: $verb $res"
        [[ "$RBAC_FAIL_FAST" == "true" ]] && { err "RBAC fail-fast."; return 1; }
      fi
    done
  else
    info "Cluster-wide run: ensure namespace-scoped verbs present in each target namespace."
  fi

  if (( ${#failures[@]} > 0 )); then
    err "RBAC preflight failed (${#failures[@]} missing)."
    printf 'Missing:\n'; printf '  %s\n' "${failures[@]}"
    return 1
  fi
  ok "RBAC preflight success ($passed/$total checks)."
  return 0
}

# ------------- Helper Functions -------------
check_premium_lrs() {
  local pvc="$1" ns="$2" sc sku sat
  sc=$(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.storageClassName}' 2>/dev/null || true)
  [[ -z "$sc" ]] && return 1
  sku=$(kcmd get sc "$sc" -o jsonpath='{.parameters.skuName}' 2>/dev/null || true)
  sat=$(kcmd get sc "$sc" -o jsonpath='{.parameters.storageaccounttype}' 2>/dev/null || true)
  [[ -z "$sku" && -z "$sat" ]] && return 1
  { [[ -z "$sku" || "$sku" == "Premium_LRS" ]] && [[ -z "$sat" || "$sat" == "Premium_LRS" ]]; }
}

check_premiumv2_lrs() {
  local ns="$1" pvc="$2"
  local val vac
  vac="$(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.volumeAttributesClassName}' || true)"
  if [[ ! -z "$vac" ]]; then
    val="$(kcmd get volumeattributesclass.storage.k8s.io "$vac" -o jsonpath='{.parameters.skuName}')"
    [[ "$val" == "PremiumV2_LRS" ]] && return 0
  fi
  return 1
}

# ---------- Snapshot Class & StorageClass Variant Helpers ----------
ensure_snapshot_class() {
  if kcmd get volumesnapshotclass "$SNAPSHOT_CLASS" >/dev/null 2>&1; then
    info "VolumeSnapshotClass '$SNAPSHOT_CLASS' exists"
    return
  fi
  info "Creating VolumeSnapshotClass '$SNAPSHOT_CLASS'"
  if ! kapply_retry <<EOF
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: ${SNAPSHOT_CLASS}
  labels:
    ${CREATED_BY_LABEL_KEY}: ${MIGRATION_TOOL_ID}
driver: disk.csi.azure.com
deletionPolicy: Delete
parameters:
  incremental: "true"
EOF
  then
    audit_add "VolumeSnapshotClass" "$SNAPSHOT_CLASS" "" "create-failed" "N/A" "forSnapshots=true reason=applyFailure"
  else
    audit_add "VolumeSnapshotClass" "$SNAPSHOT_CLASS" "" "create" "kubectl delete volumesnapshotclass $SNAPSHOT_CLASS" "forSnapshots=true"
  fi
}

# -------- Mode-Specific Helpers --------
ensure_volume_attributes_class() {
  if kcmd get volumeattributesclass "${ATTR_CLASS_NAME}" >/dev/null 2>&1; then
    if [[ "$ATTR_CLASS_FORCE_RECREATE" == "true" ]]; then
      info "Recreating VolumeAttributesClass ${ATTR_CLASS_NAME}"
      kcmd delete volumeattributesclass "${ATTR_CLASS_NAME}" --wait=true || true
    else
      info "VolumeAttributesClass ${ATTR_CLASS_NAME} present"
      return 0
    fi
  fi
  info "Creating VolumeAttributesClass ${ATTR_CLASS_NAME} (sku=${TARGET_SKU})"
  if ! kapply_retry << EOF
apiVersion: ${ATTR_CLASS_API_VERSION}
kind: VolumeAttributesClass
metadata:
  name: ${ATTR_CLASS_NAME}
  labels:
    ${CREATED_BY_LABEL_KEY}: ${MIGRATION_TOOL_ID}
driverName: disk.csi.azure.com
parameters:
  skuName: ${TARGET_SKU}
EOF
  then
    audit_add "VolumeAttributesClass" "${ATTR_CLASS_NAME}" "" "create-failed" "N/A" "sku=${TARGET_SKU} reason=applyFailure"
    exit 1
  else
    audit_add "VolumeAttributesClass" "${ATTR_CLASS_NAME}" "" "create" "kubectl delete volumeattributesclass ${ATTR_CLASS_NAME}" "sku=${TARGET_SKU}"
  fi
}

apply_storage_class_variant() {
  local orig_name="$1" sc_name="$2" sku="$3"
  if kcmd get sc "$sc_name" >/dev/null 2>&1; then
    info "StorageClass $sc_name exists"
    return
  fi
  local params_json params_filtered allowed_topologies
  params_json=$(kcmd get sc "$orig_name" -o json 2>/dev/null || true)
  [[ -z "$params_json" ]] && { warn "Cannot fetch base SC $orig_name; skipping variant"; return; }
  params_filtered=$(echo "$params_json" | jq -r '
      .parameters
      | to_entries
      | map(select(.key != "skuName"
                   and .key != "storageaccounttype"
                   and .key != "cachingMode"
                   and (.key | test("encryption";"i") | not)))
      | map("  " + .key + ": \"" + (.value|tostring) + "\"")
      | join("\n")
    ')
  
  # Extract allowedTopologies if present
  allowed_topologies_yaml=$(echo "$params_json" | jq -r '
      if .allowedTopologies then
        "allowedTopologies:" +
        (.allowedTopologies | 
         map("\n- " + 
             (.matchLabelExpressions // [] | 
              if length > 0 then
                "matchLabelExpressions:" +
                (map("\n  - key: " + .key + "\n    values: [" + (.values | map("\"" + . + "\"") | join(", ")) + "]") | join(""))
              else ""
              end
             )
         ) | join(""))
      else
        ""
      end
    ')
  
  info "Creating StorageClass $sc_name (skuName=$sku)"
  if ! kapply_retry <<EOF
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: $sc_name
  labels:
    ${CREATED_BY_LABEL_KEY}: ${MIGRATION_TOOL_ID}
provisioner: disk.csi.azure.com
parameters:
  skuName: $sku
  cachingMode: None
$params_filtered
reclaimPolicy: Retain
allowVolumeExpansion: true
volumeBindingMode: Immediate
$allowed_topologies_yaml
EOF
  then
    audit_add "StorageClass" "$sc_name" "" "create-failed" "N/A" "variantOf=${orig_name} sku=${sku} reason=applyFailure"
    return 1
  else
    audit_add "StorageClass" "$sc_name" "" "create" "kubectl delete sc $sc_name" "variantOf=${orig_name} sku=${sku}"
  fi
}

create_variants_for_sources() {
  local sources=("$@")
  for sc in "${sources[@]}"; do
    local sku sat
    sku=$(kcmd get sc "$sc" -o jsonpath='{.parameters.skuName}' 2>/dev/null || true)
    sat=$(kcmd get sc "$sc" -o jsonpath='{.parameters.storageaccounttype}' 2>/dev/null || true)
    if [[ -n "$sku" || -n "$sat" ]]; then
      if ! { [[ -z "$sku" || "$sku" == "Premium_LRS" ]] && [[ -z "$sat" || "$sat" == "Premium_LRS" ]]; }; then
        warn "Source SC $sc not Premium_LRS (skuName=$sku storageaccounttype=$sat) – skipping variants"
        continue
      fi
    fi
    pv2_sc=$(name_pv2_sc "$sc")
    apply_storage_class_variant "$sc" "$pv2_sc" "PremiumV2_LRS"
    pv1_sc=$(name_pv1_sc "$sc")
    apply_storage_class_variant "$sc" "$pv1_sc" "Premium_LRS"
  done
}

# ---------- Workload & Event Helpers ----------
list_pods_using_pvc() {
  local ns="$1" pvc="$2"
  kcmd get pods -n "$ns" \
    --field-selector spec.volumes.persistentVolumeClaim.claimName="$pvc" \
    -o custom-columns='NAME:.metadata.name,PHASE:.status.phase,NODE:.spec.nodeName' 2>/dev/null || true
}

wait_for_workload_detach() {
  local pv="$1" pvc="$2" ns="$3" poll="${POLL_INTERVAL:-60}"
  local WORKLOAD_DETACH_TIMEOUT_MINUTES="${WORKLOAD_DETACH_TIMEOUT_MINUTES:-0}"
  local timeout_deadline=0
  if (( WORKLOAD_DETACH_TIMEOUT_MINUTES > 0 )); then
    timeout_deadline=$(( $(date +%s) + WORKLOAD_DETACH_TIMEOUT_MINUTES * 60 ))
  fi
  info "Waiting for workload detach from PV $pv (PVC $ns/$pvc)"
  while true; do
    local attachments
    attachments=$(kcmd get volumeattachment -o jsonpath="{range .items[?(@.spec.source.persistentVolumeName=='$pv')]}{.metadata.name}{'\n'}{end}" 2>/dev/null || true)
    if [[ -z "$attachments" ]]; then
      ok "No VolumeAttachment for $pv"
      return 0
    fi
    warn "Still attached (VolumeAttachment present) PV=$pv:"
    echo "$attachments" | sed 's/^/  - /'
    echo "Pods referencing PVC:"
    list_pods_using_pvc "$ns" "$pvc" | sed 's/^/  /' || true
    info "Scale down workloads then retrying in ${poll}s..."
    if (( timeout_deadline > 0 )) && (( $(date +%s) >= timeout_deadline )); then
      warn "Detach wait timeout for $ns/$pvc"
      if [[ "$EXIT_ON_WORKLOAD_DETACH_TIMEOUT" == "true" ]]; then
        err "Exiting due to detach wait timeout."
        exit 1
      else
        warn "Continuing despite detach wait timeout."
      fi
      return 1
    fi
    sleep "$poll"
  done
}

extract_event_reason() {
  local ns="$1" pvc_name="$2"
  kcmd get events -n "$ns" -o json 2>/dev/null \
    | jq -r --arg name "$pvc_name" '
      [ .items[]
          | select(.involvedObject.name==$name
                  and (.reason|test("^SKUMigration(Completed|Progress|Started)$")))
          | {reason, ts:(.lastTimestamp // .eventTime // .deprecatedLastTimestamp // .metadata.creationTimestamp // "")}
        ] as $arr
        | if ($arr|length)>0
            then ($arr|sort_by(.ts)|last|.reason)
            else ""
          end'
}

populate_pvcs() {
  if [[ -z "$NAMESPACE" ]]; then
    mapfile -t MIG_PVCS < <(kcmd get pvc --all-namespaces -l "$MIGRATION_LABEL" -o jsonpath='{range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"\n"}{end}')
  else
    mapfile -t MIG_PVCS < <(kcmd get pvc -n "$NAMESPACE" -l "$MIGRATION_LABEL" -o jsonpath='{range .items[*]}{.metadata.namespace}{"|"}{.metadata.name}{"\n"}{end}')
  fi
  total_found=${#MIG_PVCS[@]}
  if (( total_found == 0 )); then
    warn "No PVCs found with label $MIGRATION_LABEL"
    exit 0
  fi
  IFS=$'\n' MIG_PVCS=($(printf '%s\n' "${MIG_PVCS[@]}" | sort))
  if (( total_found > MAX_PVCS )); then
    warn "Found $total_found PVCs; processing only first $MAX_PVCS"
    MIG_PVCS=("${MIG_PVCS[@]:0:MAX_PVCS}")
  fi

  # Size filtering (< MAX_PVC_CAPACITY_GIB)
  local filtered=() skipped_large=0
  for entry in "${MIG_PVCS[@]}"; do
    local ns="${entry%%|*}" pvc="${entry##*|}"
    local pv size gi
    pv=$(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
    if [[ -z "$pv" ]]; then
      warn "Skipping $ns/$pvc (not bound, no PV yet)"
      continue
    fi
    size=$(kcmd get pv "$pv" -o jsonpath='{.spec.capacity.storage}' 2>/dev/null || true)
    gi=$(size_to_gi_ceiling "$size")
    if (( gi >= MAX_PVC_CAPACITY_GIB )); then
      warn "Skipping $ns/$pvc size=$size (~${gi}Gi) >= threshold ${MAX_PVC_CAPACITY_GIB}Gi"
      skipped_large=$((skipped_large+1))
      continue
    fi
    filtered+=("$entry")
  done
  MIG_PVCS=("${filtered[@]}")

  if (( ${#MIG_PVCS[@]} == 0 )); then
    warn "After filtering, no PVCs under ${MAX_PVC_CAPACITY_GIB}Gi remain (skipped_large=$skipped_large)"
    exit 0
  fi

  info "Processing PVCs (< ${MAX_PVC_CAPACITY_GIB}Gi threshold; skipped_large=$skipped_large):"
  printf '  %s\n' "${MIG_PVCS[@]}"
}

create_unique_storage_classes() {
  for entry in "${MIG_PVCS[@]}"; do
    ns="${entry%%|*}" pvc="${entry##*|}"
    sc=$(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.storageClassName}' 2>/dev/null || true)
    [[ -n "$sc" ]] && SC_SET["$sc"]=1
  done
  SOURCE_SCS=("${!SC_SET[@]}")
  info "Source StorageClasses: ${SOURCE_SCS[*]}"
  create_variants_for_sources "${SOURCE_SCS[@]}"
}

# -------- AttrClass Feature Gate Confirmation (additional pre-req) --------
attrclass_feature_gate_confirm() {
  [[ "$MODE" != "attrclass" ]] && return 0

  # Skip entirely if user explicitly wants to skip the question (they may manage this externally)
  if [[ "${ATTRCLASS_SKIP_FEATURE_GATE_PROMPT:-false}" == "true" ]]; then
    info "Skipping VolumeAttributesClass feature-gate prompt (ATTRCLASS_SKIP_FEATURE_GATE_PROMPT=true)."
    return 0
  fi

  # Auto-accept path for CI / automation
  if [[ "${ATTRCLASS_ASSUME_FEATURE_GATES_YES:-false}" == "true" ]]; then
    info "Assuming VolumeAttributesClass feature gates & runtime-config are enabled (ATTRCLASS_ASSUME_FEATURE_GATES_YES=true)."
    return 0
  fi

  local ref_url="https://github.com/kubernetes-sigs/azuredisk-csi-driver/blob/master/deploy/example/modifyvolume/README.md"
  local prompt_msg
  prompt_msg=$'\nThe AttrClass migration mode requires that ALL of the following are already in place:\n\n'
  prompt_msg+=$'- kube-apiserver started with feature gate:   --feature-gates=...,VolumeAttributesClass=true\n'
  prompt_msg+=$'- kube-controller-manager feature gate:       --feature-gates=...,VolumeAttributesClass=true\n'
  prompt_msg+=$'- external-provisioner (Azure Disk CSI) has:  --feature-gates=VolumeAttributesClass=true (if required by its version)\n'
  prompt_msg+=$'- external-resizer (Azure Disk CSI) has:      --feature-gates=VolumeAttributesClass=true (if required)\n'
  prompt_msg+=$'- API version ${ATTR_CLASS_API_VERSION} for VolumeAttributesClass is enabled (runtime-config if still beta), e.g.\n'
  prompt_msg+=$'    --runtime-config=storage.k8s.io/v1beta1=true   (adjust if GA -> storage.k8s.io/v1)\n\n'
  prompt_msg+=$'Confirm ALL of the above are configured cluster-wide? (y/N): '

  local ans=""
  # Try /dev/tty to remain interactive even if piped; fall back to stdin if tty not available
  if [[ -t 0 ]]; then
    read -r -p "$prompt_msg" ans
  elif [[ -r /dev/tty ]]; then
    read -r -p "$prompt_msg" ans < /dev/tty
  else
    warn "Non-interactive session; cannot prompt for VolumeAttributesClass feature gate confirmation."
    PREREQ_ISSUES+=("VolumeAttributesClass feature gates / runtime-config not confirmed (non-interactive and no ATTRCLASS_ASSUME_FEATURE_GATES_YES)")
    PREREQ_ISSUES+=("See reference (Prerequisites): $ref_url")
    return 0
  fi

  case "${ans,,}" in
    y|yes)
      info "Feature gate & runtime-config confirmation accepted."
      ;;
    *)
      err "User did NOT confirm required VolumeAttributesClass feature gates/runtime-config."
      PREREQ_ISSUES+=("Missing or unconfirmed VolumeAttributesClass feature gates / runtime-config (apiserver/controller-manager/provisioner/resizer / apiVersion ${ATTR_CLASS_API_VERSION})")
      PREREQ_ISSUES+=("Enable per (Prerequisites): $ref_url")
      ;;
  esac
}

run_prerequisites_checks() {
  info "Running migration pre-requisites validation..."
  (( ${#MIG_PVCS[@]} > 50 )) && PREREQ_ISSUES+=("Selected PVC count (${#MIG_PVCS[@]}) > recommended batch (50)")
  declare -A _SC_JSON_CACHE
  for ENTRY in "${MIG_PVCS[@]}"; do
    local ns="${ENTRY%%|*}" pvc="${ENTRY##*|}" pv sc size zone sc_json
    pv=$(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
    [[ -z "$pv" ]] && { PREREQ_ISSUES+=("PVC/$ns/$pvc not bound"); continue; }
    sc=$(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.storageClassName}' 2>/dev/null || true)
    size=$(kcmd get pv "$pv" -o jsonpath='{.spec.capacity.storage}' 2>/dev/null || true)
    [[ -z "$sc" ]] && PREREQ_ISSUES+=("PVC/$ns/$pvc missing storageClassName")
    [[ -z "$size" ]] && PREREQ_ISSUES+=("PV/$pv capacity missing")
    if [[ -n "$sc" ]]; then
      if [[ -z "${_SC_JSON_CACHE[$sc]:-}" ]]; then
        _SC_JSON_CACHE[$sc]=$(kcmd get sc "$sc" -o json 2>/dev/null || echo "")
      fi
      sc_json="${_SC_JSON_CACHE[$sc]}"
      if [[ -n "$sc_json" ]]; then
        local cachingMode enableBursting diskEncryptionType logicalSector perfProfile
        cachingMode=$(echo "$sc_json" | jq -r '.parameters.cachingMode // empty')
        enableBursting=$(echo "$sc_json" | jq -r '.parameters.enableBursting // empty')
        diskEncryptionType=$(echo "$sc_json" | jq -r '.parameters.diskEncryptionType // empty')
        logicalSector=$(echo "$sc_json" | jq -r '.parameters.LogicalSectorSize // empty')
        perfProfile=$(echo "$sc_json" | jq -r '.parameters.perfProfile // empty')
        [[ -n "$cachingMode" && "${cachingMode,,}" != "none" ]] && PREREQ_ISSUES+=("SC/$sc cachingMode=$cachingMode (must be none)")
        [[ -n "$enableBursting" && "${enableBursting,,}" != "false" ]] && PREREQ_ISSUES+=("SC/$sc enableBursting=$enableBursting")
        [[ "$diskEncryptionType" == "EncryptionAtRestWithPlatformAndCustomerKeys" ]] && PREREQ_ISSUES+=("SC/$sc double encryption unsupported")
        [[ -n "$logicalSector" && "$logicalSector" != "512" ]] && PREREQ_ISSUES+=("SC/$sc LogicalSectorSize=$logicalSector (expected 512)")
        [[ -n "$perfProfile" && "${perfProfile,,}" != "basic" ]] && PREREQ_ISSUES+=("SC/$sc perfProfile=$perfProfile (must be basic/empty)")
      else
        PREREQ_ISSUES+=("SC/$sc not retrievable")
      fi
    fi
    local intree
    intree=$(kcmd get pv "$pv" -o jsonpath='{.spec.azureDisk.diskURI}' 2>/dev/null || true)
    if [[ -z "$intree" ]]; then
      local drv
      drv=$(kcmd get pv "$pv" -o jsonpath='{.spec.csi.driver}' 2>/dev/null || true)
      [[ "$drv" != "disk.csi.azure.com" ]] && PREREQ_ISSUES+=("PV/$pv unknown provisioning type")
    fi
  done
  attrclass_feature_gate_confirm
  info "NOTE: PremiumV2 quota not auto-checked."
}

# ensure_volume_snapshot <namespace> <source_pvc> <snapshot_name>
# Env/config used (with safe defaults if unset):
#   SNAPSHOT_CLASS (required)
#   SNAPSHOT_MAX_AGE_SECONDS (default 0 = disable age staleness)
#   SNAPSHOT_RECREATE_ON_STALE (default false)
# Behavior:
#   - Reuse existing ready snapshot if not stale
#   - Detect staleness by age or source PVC resourceVersion drift
#   - Optionally recreate if staleness + SNAPSHOT_RECREATE_ON_STALE == true
# Return codes: 0 success/ready, 1 failure (not ready), 2 recreate failed
ensure_volume_snapshot() {
  local ns="$1" source_pvc="$2" snap="$3"

  local max_age="${SNAPSHOT_MAX_AGE_SECONDS:-0}"
  local recreate_on_stale="${SNAPSHOT_RECREATE_ON_STALE:-false}"

  local exists=false stale=false recreated=false reasons=()
  if kcmd get volumesnapshot "$snap" -n "$ns" >/dev/null 2>&1; then
    exists=true
    # Gather current snapshot metadata
    local prev_rv creation_ts creation_epoch now_epoch current_rv
    prev_rv=$(kcmd get volumesnapshot "$snap" -n "$ns" -o jsonpath='{.metadata.annotations.disk\.csi\.azure\.com/source-pvc-rv}' 2>/dev/null || true)
    creation_ts=$(kcmd get volumesnapshot "$snap" -n "$ns" -o jsonpath='{.metadata.creationTimestamp}' 2>/dev/null || true)
    current_rv=$(kcmd get pvc "$source_pvc" -n "$ns" -o jsonpath='{.metadata.resourceVersion}' 2>/dev/null || true)

    if [[ -n "$creation_ts" && $max_age -gt 0 ]]; then
      creation_epoch=$(date -d "$creation_ts" +%s 2>/dev/null || echo 0)
      now_epoch=$(date +%s)
      if (( now_epoch - creation_epoch > max_age )); then
        stale=true; reasons+=("age>${max_age}")
      fi
    fi
    if [[ -n "$prev_rv" && -n "$current_rv" && "$prev_rv" != "$current_rv" ]]; then
      stale=true; reasons+=("resourceVersionChanged")
    fi

    if [[ "$stale" == "true" && "$recreate_on_stale" == "true" ]]; then
      warn "Deleting stale snapshot $ns/$snap (${reasons[*]})"
      kcmd delete volumesnapshot "$snap" -n "$ns" --ignore-not-found
      audit_add "VolumeSnapshot" "$snap" "$ns" "delete" "N/A" "reason=stale(${reasons[*]})"
      # Wait until gone
      for _ in {1..12}; do
        if ! kcmd get volumesnapshot "$snap" -n "$ns" >/dev/null 2>&1; then
          exists=false; recreated=true; break
        fi
        sleep 5
      done
    fi

    if [[ "$exists" == "true" && "$stale" == "false" ]]; then
      local ready
      ready=$(kcmd get volumesnapshot "$snap" -n "$ns" -o jsonpath='{.status.readyToUse}' 2>/dev/null || true)
      if [[ "$ready" == "true" ]]; then
        info "Snapshot $ns/$snap ready (reused)"
        return 0
      fi
      info "Waiting on existing snapshot $ns/$snap"
    fi
  fi

  if [[ "$exists" == "false" ]]; then
    info "Creating snapshot $ns/$snap from $source_pvc"
    local source_rv
    source_rv=$(kcmd get pvc "$source_pvc" -n "$ns" -o jsonpath='{.metadata.resourceVersion}' 2>/dev/null || true)
    if ! kapply_retry <<EOF
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: ${snap}
  namespace: ${ns}
  labels:
    ${CREATED_BY_LABEL_KEY}: ${MIGRATION_TOOL_ID}
  annotations:
    disk.csi.azure.com/source-pvc: "${source_pvc}"
    disk.csi.azure.com/source-pvc-rv: "${source_rv}"
spec:
  volumeSnapshotClassName: ${SNAPSHOT_CLASS}
  source:
    persistentVolumeClaimName: ${source_pvc}
EOF
    then
      audit_add "VolumeSnapshot" "$snap" "$ns" "create-failed" "N/A" "sourcePVC=$source_pvc recreate=$recreated reason=applyFailure"
      return 1
    fi
    local extra="sourcePVC=$source_pvc sourceRV=$source_rv"
    [[ "$recreated" == "true" ]] && extra="$extra recreated=true"
    audit_add "VolumeSnapshot" "$snap" "$ns" "create" "kubectl delete volumesnapshot $snap -n $ns" "$extra"
  fi

  # Wait up to 3 minutes (36 * 5s) – same as dual
  for _ in {1..36}; do
    local ready
    ready=$(kcmd get volumesnapshot "$snap" -n "$ns" -o jsonpath='{.status.readyToUse}' 2>/dev/null || true)
    [[ "$ready" == "true" ]] && { ok "Snapshot $ns/$snap ready"; return 0; }
    sleep 5
  done
  audit_add "VolumeSnapshot" "$snap" "$ns" "not-ready" "N/A" "sourcePVC=$source_pvc recreate=$recreated reason=timeout"
  warn "Snapshot $ns/$snap not ready after timeout"
  return 1
}

# --- Snapshot routines ---
ensure_snapshot() {
  local snap=$1 source_pvc="$2" ns="$3" pv="$4"
  set +e
  ensure_volume_snapshot "$ns" "$source_pvc" "$snap"
  rc=$?
  set -e
  return $rc
}

print_migration_cleanup_report() {
  local mode="${MODE:-dual}"
  local success_header_printed=false
  local failed_header_printed=false
  local any=false

  if (( ${#MIG_PVCS[@]} == 0 )); then
    warn "print_migration_cleanup_report: MIG_PVCS empty (nothing to report)."
    return 0
  fi

  info "Generating migration cleanup / investigation report (mode=${mode})..."

  # Cache Released PVs that (a) still reference a claimRef and (b) are PremiumV2_LRS in CSI volumeAttributes.
  # Output columns (TSV):
  #   namespace  pvcName  pvName  reclaimPolicy  storageClass  capacity  skuName
  local released_pv_lines
  released_pv_lines="$(kcmd get pv -o json 2>/dev/null | jq -r '
      .items[]
      | select(.status.phase=="Released"
               and .spec.claimRef
               and .spec.claimRef.namespace!=null
               and .spec.claimRef.name!=null
               and .spec.csi!=null
               )
      | . as $pv
      | (
          $pv.spec.csi.volumeAttributes.skuName
          // $pv.spec.csi.volumeAttributes.skuname
          // ""
        ) as $sku
      | select($sku=="PremiumV2_LRS")
      | [
          .spec.claimRef.namespace,
          .spec.claimRef.name,
          .metadata.name,
          (.spec.persistentVolumeReclaimPolicy // ""),
          (.spec.storageClassName // ""),
          (.spec.capacity.storage // ""),
          $sku
        ] | @tsv
    ' 2>/dev/null || true)"

  # For attrclass mode only:
  # Identify original in-tree PVs (azureDisk) now phase=Available (claimRef cleared),
  # that have a CSI twin PV (same diskURI) created by this tool (endswith -$MIG_SUFFIX, labeled created-by).
  # Output columns (TSV):
  #   origPV  origSC  origSize  diskURI  twinCsiPV  twinSC
  local available_intree_lines=""
  if [[ "$mode" == "attrclass" ]]; then
    available_intree_lines="$(kcmd get pv -o json 2>/dev/null | jq -r \
      --arg suf "-${MIG_SUFFIX}" \
      --arg tool "${MIGRATION_TOOL_ID}" '
        .items as $all
        | [
            .items[]
            | select(
                .status.phase=="Available"
                and .spec.azureDisk.diskURI!=null
              )
            | . as $orig
            | ($orig.spec.azureDisk.diskURI) as $disk
            | (
                $all[]
                | select(
                    .spec.csi!=null
                    and .spec.csi.volumeHandle==$disk
                    and (.metadata.labels["disk.csi.azure.com/created-by"]==$tool)
                    and (.metadata.name|endswith($suf))
                  )
              ) as $csi
            | select($csi!=null)
            | [
                $orig.metadata.name,
                ($orig.spec.storageClassName // ""),
                ($orig.spec.capacity.storage // ""),
                $disk,
                $csi.metadata.name,
                ($csi.spec.storageClassName // ""),
                ($orig.spec.persistentVolumeReclaimPolicy // "")
              ] | @tsv
          ]
          | .[]
      ' 2>/dev/null || true)"
  fi

  for ENTRY in "${MIG_PVCS[@]}"; do
    local ns="${ENTRY%%|*}" pvc="${ENTRY##*|}"
    local done lbl pv
    lbl=$(kcmd get pvc "$pvc" -n "$ns" -o go-template="{{ index .metadata.labels \"${MIGRATION_DONE_LABEL_KEY}\" }}" 2>/dev/null || true)
    pv=$(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
    [[ "$lbl" == "$MIGRATION_DONE_LABEL_VALUE" ]] && done=true || done=false

    local snap="" int_pvc="" int_pv="" pv2_pvc=""
    if [[ -n "$pv" ]]; then
      snap="$(name_snapshot "$pv")"
      int_pv="$(name_csi_pv "$pv")"
    fi
    if [[ "$mode" == "inplace" || "$mode" == "attrclass" ]]; then
      # get from dataSourceRef.name from spec of PVC
      snap="$(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.spec.dataSourceRef.name}' 2>/dev/null || true)"
    fi
    int_pvc="$(name_csi_pvc "$pvc")"
    pv2_pvc="$(name_pv2_pvc "$pvc")"

    if [[ "$done" == "true" ]]; then
      $success_header_printed || {
        echo
        ok "Cleanup candidates (successful migrations)"
        echo "  (Only resources that still exist and are labeled by this tool are listed)"
        success_header_printed=true
      }
      any=true

      if [[ "$mode" == "dual" ]]; then
        echo "  Source PVC: $ns/$pvc"
        if kcmd get pvc "$int_pvc" -n "$ns" >/dev/null 2>&1 && \
           kcmd get pvc "$int_pvc" -n "$ns" -o json | jq -e --arg k "$CREATED_BY_LABEL_KEY" --arg v "$MIGRATION_TOOL_ID" '.metadata.labels[$k]==$v' >/dev/null; then
          echo "    - delete intermediate PVC: kubectl delete pvc $int_pvc -n $ns"
        fi
        if kcmd get pv "$int_pv" >/dev/null 2>&1 && \
           kcmd get pv "$int_pv" -o json | jq -e --arg k "$CREATED_BY_LABEL_KEY" --arg v "$MIGRATION_TOOL_ID" '.metadata.labels[$k]==$v' >/dev/null; then
          echo "    - delete intermediate PV:  kubectl delete pv $int_pv"
        fi
        if kcmd get pvc "$pv2_pvc" -n "$ns" >/dev/null 2>&1 && \
           kcmd get pvc "$pv2_pvc" -n "$ns" -o json | jq -e --arg k "$CREATED_BY_LABEL_KEY" --arg v "$MIGRATION_TOOL_ID" '.metadata.labels[$k]==$v' >/dev/null; then
          echo "    - (optional) pv2 PVC present: $pv2_pvc (KEEP unless decommissioning)"
        fi
      else
        if kcmd get pvc "$int_pvc" -n "$ns" >/dev/null 2>&1 && \
           kcmd get pvc "$int_pvc" -n "$ns" -o json | jq -e --arg k "$CREATED_BY_LABEL_KEY" --arg v "$MIGRATION_TOOL_ID" '.metadata.labels[$k]==$v' >/dev/null; then
          echo "  Source PVC: $ns/$pvc"
          echo "    - delete intermediate PVC: kubectl delete pvc $int_pvc -n $ns"
        fi
      fi
      if [[ -n "$snap" ]] && kcmd get volumesnapshot "$snap" -n "$ns" >/dev/null 2>&1 && \
         kcmd get volumesnapshot "$snap" -n "$ns" -o json | jq -e --arg k "$CREATED_BY_LABEL_KEY" --arg v "$MIGRATION_TOOL_ID" '.metadata.labels[$k]==$v' >/dev/null; then
        echo "    - delete snapshot:        kubectl delete volumesnapshot $snap -n $ns"
      fi
      if [[ "$mode" == "dual" && -n "$pv" ]]; then
        echo "    - (review) original PV:   $pv"
      fi
    else
      $failed_header_printed || {
        echo
        warn "Artifacts for incomplete/pending migrations"
        echo "  (Review before deletion; may be needed for retry/rollback)"
        failed_header_printed=true
      }
      any=true
      echo "  Incomplete PVC: $ns/$pvc"

      if [[ "$mode" == "dual" ]]; then
        [[ "$(kcmd get pvc "$int_pvc" -n "$ns" -o name 2>/dev/null || true)" ]] && \
          echo "    - intermediate PVC exists: $int_pvc"
        [[ "$(kcmd get pv "$int_pv" -o name 2>/dev/null || true)" ]] && \
          echo "    - intermediate PV exists:  $int_pv"
        [[ "$(kcmd get pvc "$pv2_pvc" -n "$ns" -o name 2>/dev/null || true)" ]] && \
          echo "    - pv2 PVC (target) exists: $pv2_pvc"
      else
        [[ "$(kcmd get pvc "$pvc" -n "$ns" -o name 2>/dev/null || true)" ]] && \
          echo "    - current PVC phase: $(kcmd get pvc "$pvc" -n "$ns" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
      fi
      if [[ -n "$snap" ]] && kcmd get volumesnapshot "$snap" -n "$ns" >/dev/null 2>&1; then
        echo "    - snapshot exists: $snap"
      fi
      [[ -n "$pv" ]] && echo "    - source PV: $pv"
      echo "    - retry guidance: leave artifacts intact; script will reuse them."
    fi

    # PremiumV2 Released PVs referencing this claim (leftover pv2 PVs)
    if [[ -n "$released_pv_lines" ]]; then
      local had_rel=false
      while IFS=$'\t' read -r rns rpvc rpv rreclaim rsc rcap rsku; do
        [[ -z "$rns" ]] && continue
        if [[ "$rns" == "$ns" && "$rpvc" == "$pvc" && "$rpv" != "$pv" ]]; then
          $had_rel || { echo "    - released PremiumV2 PV(s) associated (not currently) with claim:"; had_rel=true; }
          echo "        * $rpv (sku=$rsku reclaim=${rreclaim:-?} sc=${rsc:-?} size=${rcap:-?})"
          echo "            inspect: kubectl describe pv $rpv"
          echo "            delete : kubectl delete pv $rpv   # after verifying data & rollback success"
        fi
      done <<< "$released_pv_lines"
    fi
  done

  # AttrClass extra: list original in-tree PVs now Available with CSI twin
  if [[ "$mode" == "attrclass" && -n "$available_intree_lines" ]]; then
    echo
    ok "Original in-tree PVs (phase=Available) with CSI replacement present"
    echo "  (These are the original in-tree PVs; after verifying successful migration & no rollback need, you may delete them.)"
    while IFS=$'\t' read -r origPV origSC origSize diskURI twinPV twinSC origRECLAIM; do
      [[ -z "$origPV" ]] && continue
      echo "    - $origPV (sc=${origSC:-?} size=${origSize:-?}) diskURI=$diskURI"
      echo "        twin CSI PV: $twinPV (sc=${twinSC:-?})"
      echo "        inspect (Reclaim: $origRECLAIM) : kubectl describe pv $origPV"
      echo "        delete : kubectl delete pv $origPV   # non-destructive if reclaimPolicy=Retain"
    done <<< "$available_intree_lines"
  fi

  if [[ "$any" == "false" ]]; then
    info "No PVC entries to report."
  else
    echo
    info "Report complete. (No actions performed.)"
  fi
}

run_without_errexit() {
    local errexit_was_set=false
    [[ $- == *e* ]] && errexit_was_set=true

    set +e
    "$@"
    local rc=$?

    $errexit_was_set && set -e
    return $rc
}

require_bins