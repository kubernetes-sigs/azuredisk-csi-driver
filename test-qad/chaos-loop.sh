#!/bin/bash

# QAD Testing Chaos Loop
# Continuously deletes deployment pod and cordons nodes to force rescheduling
# Collects QAD attach/detach metrics from CSI node logs

set -e

DEPLOYMENT_NAME="${1:-deployment-azuredisk}"
DEPLOYMENT_NS="${2:-default}"
NODEPOOL_LABEL="${3:-agentpool=nodepool1}"
LOG_DIR="${4:-.}"
LOOP_INTERVAL="${5:-10}"  # seconds between delete cycles

echo "[$(date)] Starting QAD Testing Chaos Loop"
echo "Deployment: $DEPLOYMENT_NAME in namespace $DEPLOYMENT_NS"
echo "Node pool label: $NODEPOOL_LABEL"
echo "Log directory: $LOG_DIR"
echo "Loop interval: $LOOP_INTERVAL seconds"

mkdir -p "$LOG_DIR"

CYCLE=1
while true; do
    echo ""
    echo "=========================================="
    echo "[$(date)] CYCLE $CYCLE"
    echo "=========================================="
    
    # Get current pod
    POD=$(kubectl get pods -n "$DEPLOYMENT_NS" -l "app.kubernetes.io/name=$DEPLOYMENT_NAME" \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    
    if [ -z "$POD" ]; then
        echo "No pod found for deployment $DEPLOYMENT_NAME"
        CYCLE=$((CYCLE + 1))
        sleep "$LOOP_INTERVAL"
        continue
    fi
    
    CURRENT_NODE=$(kubectl get pod "$POD" -n "$DEPLOYMENT_NS" \
        -o jsonpath='{.spec.nodeName}' 2>/dev/null || echo "")
    
    echo "Current pod: $POD on node: $CURRENT_NODE"
    
    # Get all nodes in the nodepool
    NODES=($(kubectl get nodes -l "$NODEPOOL_LABEL" \
        -o jsonpath='{.items[*].metadata.name}'))
    
    if [ ${#NODES[@]} -lt 2 ]; then
        echo "Warning: Less than 2 nodes in nodepool. Found: ${#NODES[@]}"
    fi
    
    # Find the other node
    TARGET_NODE=""
    for node in "${NODES[@]}"; do
        if [ "$node" != "$CURRENT_NODE" ]; then
            TARGET_NODE="$node"
            break
        fi
    done
    
    if [ -z "$TARGET_NODE" ]; then
        echo "No target node found for rescheduling"
        CYCLE=$((CYCLE + 1))
        sleep "$LOOP_INTERVAL"
        continue
    fi
    
    echo "Target node for rescheduling: $TARGET_NODE"
    
    # Cordon current node
    echo "[$(date)] Cordoning node $CURRENT_NODE"
    kubectl cordon "$CURRENT_NODE" || true
    
    # Delete the pod
    echo "[$(date)] Deleting pod $POD"
    kubectl delete pod "$POD" -n "$DEPLOYMENT_NS" --grace-period=30 || true
    
    # Wait for pod to be recreated on target node
    echo "[$(date)] Waiting for pod to reschedule..."
    TIMEOUT=60
    ELAPSED=0
    while [ $ELAPSED -lt $TIMEOUT ]; do
        NEW_POD=$(kubectl get pods -n "$DEPLOYMENT_NS" -l "app.kubernetes.io/name=$DEPLOYMENT_NAME" \
            -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
        
        if [ -n "$NEW_POD" ] && [ "$NEW_POD" != "$POD" ]; then
            NEW_NODE=$(kubectl get pod "$NEW_POD" -n "$DEPLOYMENT_NS" \
                -o jsonpath='{.spec.nodeName}' 2>/dev/null || echo "")
            
            if [ "$NEW_NODE" != "$CURRENT_NODE" ]; then
                echo "Pod rescheduled to node: $NEW_NODE"
                break
            fi
        fi
        
        sleep 2
        ELAPSED=$((ELAPSED + 2))
    done
    
    # Uncordon the original node
    echo "[$(date)] Uncordoning node $CURRENT_NODE"
    kubectl uncordon "$CURRENT_NODE" || true
    
    # Collect logs from CSI nodes
    echo "[$(date)] Collecting CSI logs..."
    collect_csi_logs "$DEPLOYMENT_NS" "$LOG_DIR" "$CYCLE"
    
    echo "[$(date)] Cycle $CYCLE completed"
    CYCLE=$((CYCLE + 1))
    
    echo "Sleeping for $LOOP_INTERVAL seconds before next cycle..."
    sleep "$LOOP_INTERVAL"
done

collect_csi_logs() {
    local ns=$1
    local log_dir=$2
    local cycle=$3
    
    # Get CSI node pods
    CSI_PODS=$(kubectl get pods -n kube-system \
        -l app=csi-azuredisk-node \
        -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || echo "")
    
    for csi_pod in $CSI_PODS; do
        NODE=$(kubectl get pod "$csi_pod" -n kube-system \
            -o jsonpath='{.spec.nodeName}' 2>/dev/null || echo "unknown")
        
        LOG_FILE="$log_dir/csi-logs-cycle-$cycle-$NODE.log"
        
        echo "Collecting logs from $csi_pod on node $NODE -> $LOG_FILE"
        kubectl logs "$csi_pod" -n kube-system --tail=100 > "$LOG_FILE" 2>&1 || true
    done
}
