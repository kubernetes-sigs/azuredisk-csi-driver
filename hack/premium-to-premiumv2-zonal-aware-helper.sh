#!/usr/bin/env bash
# shellcheck source=./lib-premiumv2-migration-common.sh

# Zone-aware migration helper for Azure Disk CSI Driver
# This script ensures PremiumV2 LRS disks are created in correct zones
set -euo pipefail
IFS=$'\n\t'

# Source the common migration library
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR}/lib-premiumv2-migration-common.sh"

# ---------- Zone-aware Configuration ----------
ZONE_MAPPING_FILE="${ZONE_MAPPING_FILE:-disk-zone-mapping.txt}"

# ---------- Zone Detection Functions ----------

# Parse the zone mapping file
# Format: <ArmId>=<zone> (e.g., /subscriptions/.../disks/mydisk=uksouth-1)
load_zone_mapping() {
    local mapping_file="$1"
    declare -g -A DISK_ZONE_MAP
    
    if [[ ! -f "$mapping_file" ]]; then
        warn "Zone mapping file not found: $mapping_file"
        return 1
    fi
    
    info "Loading zone mapping from: $mapping_file"
    local line_count=0
    while IFS='=' read -r arm_id zone || [[ -n "$arm_id" ]]; do
        [[ -z "$arm_id" || "$arm_id" =~ ^[[:space:]]*# ]] && continue  # Skip empty lines and comments
        [[ -z "$zone" ]] && { warn "Invalid mapping line: $arm_id (missing zone)"; continue; }
        
        # Normalize ARM ID (remove leading/trailing whitespace)
        arm_id=$(echo "$arm_id" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')
        zone=$(echo "$zone" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')
        
        DISK_ZONE_MAP["$arm_id"]="$zone"
        line_count=$((line_count + 1))
    done < "$mapping_file"
    
    info "Loaded $line_count disk-to-zone mappings"
    return 0
}

# Extract zone from StorageClass allowedTopologies
# Returns the zone if exactly one is found, empty string otherwise
extract_zone_from_storageclass() {
    local sc_name="$1"
    local sc_json zones_json zone_count
    
    sc_json=$(kcmd get sc "$sc_name" -o json 2>/dev/null || true)
    [[ -z "$sc_json" ]] && return 1
    
    zones_json=$(echo "$sc_json" | jq -r '
        .allowedTopologies // [] 
        | map(.matchLabelExpressions // [])
        | flatten
        | map(.values // [])
        | flatten
        | unique
    ')
    
    zone_count=$(echo "$zones_json" | jq 'length')
    
    if [[ "$zone_count" == "1" ]]; then
        echo "$zones_json" | jq -r '.[0]'
        return 0
    elif [[ "$zone_count" == "0" ]]; then
        info "No zone constraints in StorageClass $sc_name, checking PV nodeAffinity"
        return 1
    else
        info "Multiple zones in StorageClass $sc_name, need to determine specific zone"
        return 2
    fi
}

# Extract zone from PV nodeAffinity
# Returns the zone if exactly one matching zone expression is found
# Only considers recognized zone keys to avoid accidentally pulling region or other topology labels.
extract_zone_from_pv_nodeaffinity() {
    local pv_name="$1"
    local pv_json zone_count
    
    pv_json=$(kcmd get pv "$pv_name" -o json 2>/dev/null || true)
    [[ -z "$pv_json" ]] && return 1
    
    # Extract zones from nodeAffinity restricted to zone keys
    local zones_json
    zones_json=$(echo "$pv_json" | jq -r '
        .spec.nodeAffinity.required.nodeSelectorTerms // []
        | map(.matchExpressions // [])
        | flatten
        | map(select(.key == "topology.disk.csi.azure.com/zone"))
        | map(.values // [])
        | flatten
        | unique
    ')
    
    zone_count=$(echo "$zones_json" | jq 'length')
    
    if [[ "$zone_count" == "1" ]]; then
        echo "$zones_json" | jq -r '.[0]'
        return 0
    else
        return 1
    fi
}

# Get disk URI from PV (handles both in-tree and CSI)
get_disk_uri_from_pv() {
    local pv_name="$1"
    local disk_uri
    
    # Try in-tree first
    disk_uri=$(kcmd get pv "$pv_name" -o jsonpath='{.spec.azureDisk.diskURI}' 2>/dev/null || true)
    
    if [[ -z "$disk_uri" ]]; then
        # Try CSI
        disk_uri=$(kcmd get pv "$pv_name" -o jsonpath='{.spec.csi.volumeHandle}' 2>/dev/null || true)
    fi
    
    echo "$disk_uri"
}

# Determine zone for a PVC (updated priority: PV nodeAffinity first)
# Exit codes:
#   0 -> zone from PV nodeAffinity
#   1 -> zone from StorageClass allowedTopologies
#   2 -> zone from mapping file
#   3 -> failed to determine zone
determine_zone_for_pvc() {
    local pvc_name="$1" pvc_ns="$2"
    local sc_name pv_name zone disk_uri rc
    
    sc_name=$(kcmd get pvc "$pvc_name" -n "$pvc_ns" -o jsonpath='{.spec.storageClassName}' 2>/dev/null || true)
    pv_name=$(kcmd get pvc "$pvc_name" -n "$pvc_ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
    
    [[ -z "$sc_name" ]] && { warn "PVC $pvc_ns/$pvc_name has no storageClassName"; return 3; }
    [[ -z "$pv_name" ]] && { warn "PVC $pvc_ns/$pvc_name has no bound PV"; return 3; }
    
    # Priority Step 1: PV nodeAffinity (most authoritative actual placement)
    zone=$(extract_zone_from_pv_nodeaffinity "$pv_name"); rc=$?
    if [[ $rc -eq 0 && -n "$zone" ]]; then
        printf '%s\n' "$zone"
        return 0
    fi
    
    # Priority Step 2: StorageClass allowedTopologies (design intent)
    zone=$(extract_zone_from_storageclass "$sc_name"); rc=$?
    if [[ $rc -eq 0 && -n "$zone" ]]; then
        printf '%s\n' "$zone"
        return 1
    fi
    
    # Priority Step 3: Mapping file fallback
    disk_uri=$(get_disk_uri_from_pv "$pv_name")
    disk_uri=$(echo "$disk_uri" | sed 's/^[[:space:]]*//;s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')
    if [[ -n "$disk_uri" && -n "${DISK_ZONE_MAP[$disk_uri]:-}" ]]; then
        zone="${DISK_ZONE_MAP[$disk_uri]}"
        echo "$zone"
        return 2
    fi
    
    warn "Unable to determine zone for PVC $pvc_ns/$pvc_name (SC: $sc_name, PV: $pv_name, Disk: ${disk_uri:-unknown})"
    return 3
}

# Create zone-specific StorageClass with comprehensive property preservation
# Returns the name of the created/existing StorageClass
create_zone_specific_storageclass() {
    local orig_sc="$1" zone="$2" sku="${3:-Premium_LRS}" zone_sc_name=${4:-"${orig_sc}-${zone}"}
    
    if kcmd get sc "$zone_sc_name" >/dev/null 2>&1; then
        # Check if this StorageClass was created by our migration script
        local created_by existing_sku existing_zone
        created_by=$(kcmd get sc "$zone_sc_name" -o jsonpath="{.metadata.labels.${CREATED_BY_LABEL_KEY//./\\.}}" 2>/dev/null || true)
        
        if [[ "$created_by" == "$MIGRATION_TOOL_ID" ]]; then
            # Verify it matches our expected configuration
            existing_sku=$(kcmd get sc "$zone_sc_name" -o jsonpath='{.parameters.skuName}' 2>/dev/null || true)
            existing_zone=$(extract_zone_from_storageclass "$zone_sc_name")

            if [[ "$existing_sku" == "$sku" && "$existing_zone" == "$zone" ]]; then
                info "Zone-specific StorageClass $zone_sc_name already exists (created by migration script, sku=$existing_sku, zone=$existing_zone)"
                return 0
            else
                warn "Zone-specific StorageClass $zone_sc_name exists but has different configuration:"
                warn "  Expected: sku=$sku, zone=$zone"
                warn "  Existing: sku=$existing_sku, zone=$existing_zone"
                warn "  Recreating to match expected configuration..."
                
                # Delete and recreate with correct configuration
                if ! kcmd delete sc "$zone_sc_name" --wait=true 2>/dev/null; then
                    err "Failed to delete existing zone StorageClass $zone_sc_name for recreation"
                    return 1
                fi
                audit_add "StorageClass" "$zone_sc_name" "" "delete" "N/A" "reason=mismatchedConfig expectedSku=$sku expectedZone=$zone existingSku=$existing_sku existingZone=$existing_zone"
                info "Deleted existing zone StorageClass $zone_sc_name with mismatched configuration"
            fi
        else
            # StorageClass exists but wasn't created by our script
            if [[ -z "$created_by" ]]; then
                err "Zone-specific StorageClass $zone_sc_name already exists but was not created by migration script"
                err "  Missing label: ${CREATED_BY_LABEL_KEY}=${MIGRATION_TOOL_ID}"
            else
                err "Zone-specific StorageClass $zone_sc_name already exists but was created by different tool: $created_by"
            fi
            err "  Please rename or delete the existing StorageClass, or choose a different zone naming scheme"
            err "  Current StorageClass details:"
            kcmd describe sc "$zone_sc_name" | head -20 >&2 || true
            return 1
        fi
    fi
    
    local params_json
    params_json=$(kcmd get sc "$orig_sc" -o json 2>/dev/null || true)
    if [[ -z "$params_json" ]]; then
        err "Cannot fetch base StorageClass $orig_sc"
        return 1
    fi
    
    # Validate that we have a valid StorageClass
    local sc_kind
    sc_kind=$(echo "$params_json" | jq -r '.kind // ""')
    if [[ "$sc_kind" != "StorageClass" ]]; then
        err "Invalid StorageClass data for $orig_sc"
        return 1
    fi
    
    info "Analyzing StorageClass $orig_sc for zone-specific variant creation (zone=$zone, sku=$sku)"
    
    # Extract all components with detailed preservation
    local orig_labels orig_annotations params_filtered provisioner
    local reclaim_policy allow_volume_expansion volume_binding_mode mount_options_yaml
    
    # Extract original labels (excluding system labels, add zone-specific ones)
    orig_labels=$(echo "$params_json" | jq -r '
        .metadata.labels // {}
        | to_entries
        | map(select(.key | test("^(kubernetes\\.io/|k8s\\.io/|pv\\.kubernetes\\.io/|volume\\.kubernetes\\.io/|app\\.kubernetes\\.io/managed-by|storageclass\\.kubernetes\\.io/is-default-class)") | not))
        | map("    " + .key + ": \"" + (.value|tostring) + "\"")
        | join("\n")
    ')
    
    # Extract original annotations (excluding system annotations)
    orig_annotations=$(echo "$params_json" | jq -r '
        .metadata.annotations // {}
        | to_entries
        | map(select(.key | test("^(kubernetes\\.io/|k8s\\.io/|pv\\.kubernetes\\.io/|volume\\.kubernetes\\.io/|kubectl\\.kubernetes\\.io/|deployment\\.kubernetes\\.io/|control-plane\\.|storageclass\\.kubernetes\\.io/is-default-class)") | not))
        | map("    " + .key + ": \"" + (.value|tostring|gsub("\\n"; "\\\\n")|gsub("\\\""; "\\\\\"")) + "\"")
        | join("\n")
    ')
    
    # Extract provisioner (ensure it's CSI-compatible)
    provisioner=$(echo "$params_json" | jq -r '.provisioner // "disk.csi.azure.com"')
    if [[ "$provisioner" == "kubernetes.io/azure-disk" ]]; then
        provisioner="disk.csi.azure.com"
        info "  Provisioner: Converted in-tree to CSI provisioner"
    else
        info "  Provisioner: $provisioner"
    fi
    
    # Extract parameters with zone-specific migration filtering
    params_filtered=$(echo "$params_json" | jq -r '
        .parameters // {}
        | to_entries
        | map(select(
            .key != "cachingMode"
            and (.key | test("^(diskEncryption|encryption)"; "i") | not)
            and (.key | test("^(enableBursting|perfProfile)$") | not)
            and (.key | test("^(LogicalSectorSize)$") | not)
          ))
        | map("  " + .key + ": \"" + (.value|tostring) + "\"")
        | join("\n")
    ')
    
    # Log parameter analysis
    local param_count filtered_count
    param_count=$(echo "$params_json" | jq -r '.parameters // {} | keys | length')
    filtered_count=$(echo "$params_json" | jq -r '
        .parameters // {}
        | to_entries
        | map(select(
            .key != "cachingMode"
            and (.key | test("^(diskEncryption|encryption)"; "i") | not)
            and (.key | test("^(enableBursting|perfProfile)$") | not)
            and (.key | test("^(LogicalSectorSize)$") | not)
          ))
        | length
    ')
    info "  Parameters: $param_count total, $filtered_count preserved (zone-safe filtering applied)"
    
    # Extract storage class properties
    reclaim_policy=$(echo "$params_json" | jq -r '.reclaimPolicy // "Retain"')
    allow_volume_expansion=$(echo "$params_json" | jq -r '.allowVolumeExpansion // true')
    volume_binding_mode=$(echo "$params_json" | jq -r '.volumeBindingMode // "WaitForFirstConsumer"')
    
    # For zone-specific StorageClasses, prefer WaitForFirstConsumer to ensure proper zone placement
    if [[ "$volume_binding_mode" == "Immediate" ]]; then
        volume_binding_mode="WaitForFirstConsumer"
        info "  VolumeBindingMode: Changed from Immediate to WaitForFirstConsumer for zone awareness"
    else
        info "  VolumeBindingMode: $volume_binding_mode (zone-compatible)"
    fi
    
    info "  ReclaimPolicy: $reclaim_policy"
    info "  AllowVolumeExpansion: $allow_volume_expansion"
    
    # Extract mountOptions if present
    local mount_options_count
    mount_options_count=$(echo "$params_json" | jq -r '.mountOptions // [] | length')
    mount_options_yaml=$(echo "$params_json" | jq -r '
        if .mountOptions and (.mountOptions | length > 0) then
          "mountOptions:" +
          (.mountOptions | map("\n- \"" + . + "\"") | join(""))
        else
          ""
        end
    ')
    
    if [[ $mount_options_count -gt 0 ]]; then
        info "  MountOptions: $mount_options_count options preserved"
    fi
    
    # Create zone-specific allowedTopologies (override any existing topology constraints)
    local allowed_topologies_yaml
    allowed_topologies_yaml="allowedTopologies:
- matchLabelExpressions:
  - key: topology.kubernetes.io/zone
    values: [\"$zone\"]"
    
    info "  AllowedTopologies: Zone-specific constraint set to $zone"
    
   
    info "Creating zone-specific StorageClass $zone_sc_name with comprehensive property preservation"
    
    # Build the complete zone-specific StorageClass YAML
    local sc_yaml
    sc_yaml=$(cat <<EOF
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: $zone_sc_name
  labels:
    ${CREATED_BY_LABEL_KEY}: ${MIGRATION_TOOL_ID}
$(if [[ -n "$orig_labels" ]]; then echo "$orig_labels"; fi)
  annotations:
$(if [[ -n "$orig_annotations" ]]; then echo "$orig_annotations"; fi)
    ${ZONE_SC_ANNOTATION_KEY}: ${orig_sc}
provisioner: $provisioner
parameters:
  skuName: $sku
  cachingMode: None
$(if [[ -n "$params_filtered" ]]; then echo "$params_filtered"; fi)
reclaimPolicy: $reclaim_policy
allowVolumeExpansion: $allow_volume_expansion
volumeBindingMode: $volume_binding_mode
$(if [[ -n "$mount_options_yaml" ]]; then echo "$mount_options_yaml"; fi)
$allowed_topologies_yaml
EOF
)
    
    # Apply the zone-specific StorageClass
    if ! echo "$sc_yaml" | kapply_retry; then
        local extra_info="zone=$zone sku=$sku orig=$orig_sc provisioner=$provisioner reclaimPolicy=$reclaim_policy allowVolumeExpansion=$allow_volume_expansion volumeBindingMode=$volume_binding_mode"
        [[ $param_count -gt 0 ]] && extra_info="$extra_info paramCount=$param_count"
        [[ $filtered_count -gt 0 ]] && extra_info="$extra_info filteredParams=$filtered_count"
        [[ $mount_options_count -gt 0 ]] && extra_info="$extra_info mountOptionsCount=$mount_options_count"
        
        audit_add "StorageClass" "$zone_sc_name" "" "create-failed" "N/A" "$extra_info reason=applyFailure"
        return 1
    else
        local extra_info="zone=$zone sku=$sku orig=$orig_sc provisioner=$provisioner reclaimPolicy=$reclaim_policy allowVolumeExpansion=$allow_volume_expansion volumeBindingMode=$volume_binding_mode"
        [[ $param_count -gt 0 ]] && extra_info="$extra_info paramCount=$param_count"
        [[ $filtered_count -gt 0 ]] && extra_info="$extra_info filteredParams=$filtered_count"
        [[ $mount_options_count -gt 0 ]] && extra_info="$extra_info mountOptionsCount=$mount_options_count"
        
        audit_add "StorageClass" "$zone_sc_name" "" "create" "kubectl delete sc $zone_sc_name" "$extra_info"
        ok "Zone-specific StorageClass $zone_sc_name created successfully"
        return 0
    fi
}

# Annotate PVC with zone-specific StorageClass
annotate_pvc_with_zone_storageclass() {
    local pvc_name="$1" pvc_ns="$2" zone_sc="$3" zone="$4"
    
    info "Annotating PVC $pvc_ns/$pvc_name with zone-specific StorageClass: $zone_sc"
    
    if ! kcmd annotate pvc "$pvc_name" -n "$pvc_ns" \
        "${ZONE_SC_ANNOTATION_KEY}=$zone_sc" \
        --overwrite >/dev/null 2>&1; then
        
        warn "Failed to annotate PVC $pvc_ns/$pvc_name"
        audit_add "PersistentVolumeClaim" "$pvc_name" "$pvc_ns" "annotate-failed" \
            "kubectl annotate pvc $pvc_name -n $pvc_ns ${ZONE_SC_ANNOTATION_KEY}-" \
            "zoneStorageClass=$zone_sc zone=$zone reason=kubectlError"
        return 1
    fi
    
    audit_add "PersistentVolumeClaim" "$pvc_name" "$pvc_ns" "annotate" \
        "kubectl annotate pvc $pvc_name -n $pvc_ns ${ZONE_SC_ANNOTATION_KEY}-" \
        "zoneStorageClass=$zone_sc zone=$zone"
    
    ok "PVC $pvc_ns/$pvc_name annotated with zone-specific StorageClass"
    return 0
}

# Process a single PVC for zone-aware migration
process_pvc_for_zone_migration() {
    local pvc_entry="$1"  # Format: namespace|pvcname
    local pvc_ns="${pvc_entry%%|*}"
    local pvc_name="${pvc_entry##*|}"
    
    info "Processing PVC $pvc_ns/$pvc_name for zone-aware migration"
    
    local existing_annotation
    existing_annotation=$(kcmd get pvc "$pvc_name" -n "$pvc_ns" -o jsonpath="{.metadata.annotations.${ZONE_SC_ANNOTATION_KEY//./\\.}}" 2>/dev/null || true)
    if [[ -n "$existing_annotation" ]]; then
        info "PVC $pvc_ns/$pvc_name already annotated with zone-specific StorageClass: $existing_annotation (skipping)"
        return 0
    fi
    
    local zone
    zone=$(determine_zone_for_pvc "$pvc_name" "$pvc_ns")
    local zone_src_rc=$?
    case $zone_src_rc in
        0)
            info "Zone determined from PV nodeAffinity for PVC $pvc_ns/$pvc_name: $zone"
            ;;
        1)
            info "Zone inferred from StorageClass allowedTopologies for PVC $pvc_ns/$pvc_name: $zone"
            return 1
            ;;
        2)
            info "Zone resolved from mapping file for PVC $pvc_ns/$pvc_name: $zone"
            ;;
        *)
            warn "Failed to determine zone for PVC $pvc_ns/$pvc_name (rc=$zone_src_rc)"
            return 2
            ;;
    esac

    local orig_sc
    orig_sc=$(kcmd get pvc "$pvc_name" -n "$pvc_ns" -o jsonpath='{.spec.storageClassName}' 2>/dev/null || true)
    [[ -z "$orig_sc" ]] && { err "PVC $pvc_ns/$pvc_name has no storageClassName"; return 2; }
    
    local zone_sc
    if [[ "${GENERIC_PV2_SC_MODE:-0}" == "1" ]]; then
        # Reuse generic naming, keep zone constraint inside the SC spec
        zone_sc="$(name_pv2_sc "$orig_sc")"
    else
        zone_sc="${orig_sc}-${zone}"
    fi

    if ! create_zone_specific_storageclass "$orig_sc" "$zone" "Premium_LRS" "$zone_sc"; then
        err "Failed to create zone-specific StorageClass $zone_sc (zone=$zone orig=$orig_sc)"
        return 2
    fi
    
    if ! annotate_pvc_with_zone_storageclass "$pvc_name" "$pvc_ns" "$zone_sc" "$zone"; then
        err "Failed to annotate PVC $pvc_ns/$pvc_name with zone SC $zone_sc"
        return 2
    fi
    
    ok "PVC $pvc_ns/$pvc_name zone=$zone (source=$zone_src_rc) annotated with $zone_sc"
    return 0
}

# Process PVCs for zone-aware preparation without full migration
process_pvcs_for_zone_preparation() {
    local start_ts start_epoch
    start_ts="$(date +'%Y-%m-%dT%H:%M:%S')"
    start_epoch="$(date +%s)"
    
    info "Starting zone-aware PVC processing (preparation only)"
    
    # Load zone mapping if file exists
    declare -g -A DISK_ZONE_MAP
    if [[ -f "$ZONE_MAPPING_FILE" ]]; then
        if ! load_zone_mapping "$ZONE_MAPPING_FILE"; then
            err "Failed to load zone mapping file: $ZONE_MAPPING_FILE"
            exit 1
        fi
    else
        warn "Zone mapping file not found: $ZONE_MAPPING_FILE (will rely on StorageClass and PV nodeAffinity only)"
    fi
    
    # Check prerequisites
    if is_direct_exec; then
        migration_rbac_check || exit 1
    fi
    
    # Populate PVCs marked for migration
    populate_pvcs
    detect_generic_pv2_mode

    if is_direct_exec; then
        run_prerequisites_checks
    else
        info "Skipping run_prerequisites_checks (script sourced)"
    fi
    
    local processed=0 skipped=0 failed=0
    for pvc_entry in "${MIG_PVCS[@]}"; do
        local pvc_ns="${pvc_entry%%|*}"
        local pvc_name="${pvc_entry##*|}"

        is_created_by_migrator=$(is_pvc_in_migration "$pvc_name" "$pvc_ns")
        if [[ $is_created_by_migrator == "true" ]]; then
            info "Skipping PVC $pvc_ns/$pvc_name (created by migration tool)"
            skipped=$((skipped + 1))
            continue
        fi

        is_migration=$(is_pvc_created_by_migration_tool "$pvc_name" "$pvc_ns")
        if [[ $is_migration == "true" ]]; then
            info "Skipping PVC $pvc_ns/$pvc_name (already in migration)"
            skipped=$((skipped + 1))
            continue
        fi

        # Check if PVC already has zone-specific StorageClass annotation
        local existing_annotation
        existing_annotation=$(kcmd get pvc "$pvc_name" -n "$pvc_ns" -o jsonpath="{.metadata.annotations.${ZONE_SC_ANNOTATION_KEY//./\\.}}" 2>/dev/null || true)
        if [[ -n "$existing_annotation" ]]; then
            info "Skipping PVC $pvc_ns/$pvc_name (already has zone annotation: $existing_annotation)"
            skipped=$((skipped + 1))
            continue
        fi
        
        run_without_errexit process_pvc_for_zone_migration "$pvc_entry"
        case $LAST_RUN_WITHOUT_ERREXIT_RC in
            1)
                skipped=$((skipped + 1))
                ;;
            0)
                processed=$((processed + 1))
                ;;
            *)
                failed=$((failed + 1))
                ;;
        esac
    done
      
    if is_direct_exec; then
        echo
        ok "Zone-aware PVC processing complete:"
        echo "  Processed: $processed"
        echo "  Skipped: $skipped"
        echo "  Failed: $failed"
        echo "  Total: ${#MIG_PVCS[@]}"
        finalize_audit_summary "$start_ts" "$start_epoch"
    else
      if [[ $failed -gt 0 ]]; then
          err "Zone-aware PVC processing complete with failures:"
          echo "  Processed: $processed"
          echo "  Skipped: $skipped"
          echo "  Failed: $failed"
          echo "  Total: ${#MIG_PVCS[@]}"
          return 1
      else
          ok "Zone-aware PVC processing complete:"
          echo "  Processed: $processed"
          echo "  Skipped: $skipped"
          echo "  Failed: $failed"
          echo "  Total: ${#MIG_PVCS[@]}"
          return 0
      fi
    fi
}

# Generate zone mapping template
generate_zone_mapping_template() {
    local output_file="${1:-disk-zone-mapping-template.txt}"
    
    info "Generating zone mapping template: $output_file"
    
    {
        echo "# Azure Disk Zone Mapping File"
        echo "# Format: <ArmId>=<zone>"
        echo "# Example zones: uksouth-1, uksouth-2, uksouth-3"
        echo "# "
        echo "# Examples:"
        echo "# /subscriptions/12345678-1234-1234-1234-123456789012/resourceGroups/myRG/providers/Microsoft.Compute/disks/myDisk1=uksouth-1"
        echo "# /subscriptions/12345678-1234-1234-1234-123456789012/resourceGroups/myRG/providers/Microsoft.Compute/disks/myDisk2=uksouth-2"
        echo ""
        
        # Generate entries for existing PVCs
        if [[ ${#MIG_PVCS[@]} -gt 0 ]]; then
            echo "# Generated from current PVCs marked for migration:"
            for pvc_entry in "${MIG_PVCS[@]}"; do
                local pvc_ns="${pvc_entry%%|*}"
                local pvc_name="${pvc_entry##*|}"
                local pv_name disk_uri
                
                pv_name=$(kcmd get pvc "$pvc_name" -n "$pvc_ns" -o jsonpath='{.spec.volumeName}' 2>/dev/null || true)
                if [[ -n "$pv_name" ]]; then
                    disk_uri=$(get_disk_uri_from_pv "$pv_name")
                    if [[ -n "$disk_uri" ]]; then
                        echo "# PVC: $pvc_ns/$pvc_name, PV: $pv_name"
                        echo "$disk_uri=<zone-here>  # Replace <zone-here> with actual zone"
                    fi
                fi
            done
        fi
    } > "$output_file"
    
    ok "Zone mapping template created: $output_file"
    echo "Edit this file and set ZONE_MAPPING_FILE=$output_file"
}

# Show help
show_help() {
    cat << 'EOF'
Zone-Aware Azure Disk Migration Helper

This script prepares PVCs for zone-aware migration from Premium_LRS to PremiumV2_LRS
by ensuring disks are created in the correct Azure availability zones.

USAGE:
    premium-to-premiumv2-zonal-aware-helper.sh [OPTIONS] MODE COMMAND

MODE:
    dual               Prepare for migration while keeping existing disks
    inplace            In-place migration mode - migrate disks without keeping existing ones
    attrclass          Attribute class migration mode - migrate disks based on their attributes

COMMANDS:
    process                 Process PVCs and create zone-specific StorageClasses (no migration)
    generate-template       Generate a zone mapping template file
    help                   Show this help

OPTIONS:
    Environment variables (set before running):
    
    ZONE_MAPPING_FILE       Path to disk-zone mapping file
                           Default: disk-zone-mapping.txt
                           Format: /subscriptions/.../disks/mydisk=uksouth-1
    
    MIGRATION_LABEL         Label selector for PVCs to migrate
                           Default: disk.csi.azure.com/pv2migration=true
      
    MAX_PVCS               Maximum PVCs to process in one run
                           Default: 50

ZONE DETECTION LOGIC:
    1. Check StorageClass allowedTopologies - if single zone, use it
    2. If multiple zones or no constraints, check PV nodeAffinity
    3. If no nodeAffinity or multiple zones, consult zone mapping file
    4. Error if zone cannot be determined

EXAMPLES:
    # Generate template
    ./premium-to-premiumv2-zonal-aware-helper.sh generate-template
    
    # Process all labeled PVCs for zone preparation
    ./premium-to-premiumv2-zonal-aware-helper.sh <dual|inplace|attrclass> process
       
    # Edit the template file and run processing
    ZONE_MAPPING_FILE=disk-zone-mapping.txt ./premium-to-premiumv2-zonal-aware-helper.sh <dual|inplace|attrclass> process

OUTPUT:
    - Creates zone-specific StorageClasses (e.g., premium-ssd-uksouth-1)
    - Annotates PVCs with zone-specific StorageClass names
    - Audit log for all actions taken

ANNOTATIONS ADDED:
    disk.csi.azure.com/zone-specific-storageclass: <zone-sc-name>
EOF
}

# Main entry point
main() {
    local mode="${1:-help}"
    local cmd="${2:-help}"

    case "$mode" in
        dual|inplace|attrclass)
            export MODE="$mode"
            ;;
        help|--help|-h)
            show_help
            exit 0
            ;;
        *)
            err "Unknown mode: $mode"
            echo
            show_help
            exit 1
            ;;
    esac
    
    case "$cmd" in
        process)
            process_pvcs_for_zone_preparation
            ;;
        generate-template)
            generate_zone_mapping_template "${2:-}"
            ;;
        help|--help|-h)
            show_help
            ;;
        *)
            err "Unknown command: $cmd"
            echo
            show_help
            exit 1
            ;;
    esac
}

# Run if executed directly
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi