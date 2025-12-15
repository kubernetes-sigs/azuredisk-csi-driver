#!/bin/bash

# Simple QAD test runner - run directly from your machine
# Usage: ./run-test.sh

set -e

DEPLOYMENT_NAME="deployment-azuredisk"
DEPLOYMENT_NS="default"
POD_LABEL="app=nginx"
NODEPOOL_LABEL="kubernetes.io/os=linux"
LOG_DIR="/tmp/qad-logs"
LOOP_INTERVAL=30

echo "[$(date)] Starting QAD Testing Chaos Loop"
echo "Deployment: $DEPLOYMENT_NAME in namespace $DEPLOYMENT_NS"
echo "Pod Label: $POD_LABEL"
echo "Log Directory: $LOG_DIR"
echo ""

mkdir -p "$LOG_DIR"
CYCLE=1

cleanup() {
    echo ""
    echo "[$(date)] Test interrupted. Uncordoning all nodes..."
    for node in $(kubectl get nodes -o jsonpath='{.items[*].metadata.name}'); do
        kubectl uncordon "$node" 2>/dev/null || true
    done
    echo "Done. Logs saved in $LOG_DIR"
}

trap cleanup SIGINT SIGTERM

while true; do
    echo "=========================================="
    echo "[$(date)] CYCLE $CYCLE"
    echo "=========================================="
    
    # Get current pod
    POD=$(kubectl get pods -n "$DEPLOYMENT_NS" -l "$POD_LABEL" \
        -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
    
    if [ -z "$POD" ]; then
        echo "[-] No pod found with label: $POD_LABEL"
        echo "[*] Available pods in $DEPLOYMENT_NS:"
        kubectl get pods -n "$DEPLOYMENT_NS" --no-headers 2>/dev/null | head -5 || echo "    None"
        echo "[*] Available labels:"
        kubectl get pods -n "$DEPLOYMENT_NS" --show-labels 2>/dev/null | head -3 || echo "    None"
        
        CYCLE=$((CYCLE + 1))
        sleep "$LOOP_INTERVAL"
        continue
    fi
    
    CURRENT_NODE=$(kubectl get pod "$POD" -n "$DEPLOYMENT_NS" \
        -o jsonpath='{.spec.nodeName}' 2>/dev/null || echo "")
    
    echo "[+] Current pod: $POD on node: $CURRENT_NODE"
    
    # Get all worker nodes (excluding system nodes)
    NODES=($(kubectl get nodes -l "$NODEPOOL_LABEL" \
        -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || echo ""))
    
    if [ ${#NODES[@]} -lt 2 ]; then
        echo "[-] Need at least 2 nodes, found: ${#NODES[@]}"
        CYCLE=$((CYCLE + 1))
        sleep "$LOOP_INTERVAL"
        continue
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
        echo "[-] No target node found (all nodes occupied by pod)"
        CYCLE=$((CYCLE + 1))
        sleep "$LOOP_INTERVAL"
        continue
    fi
    
    echo "[+] Target node for rescheduling: $TARGET_NODE"
    
    # Cordon current node
    echo "[*] Cordoning node $CURRENT_NODE"
    kubectl cordon "$CURRENT_NODE" 2>/dev/null || true
    sleep 1
    
    # Delete the pod
    echo "[*] Deleting pod $POD"
    kubectl delete pod "$POD" -n "$DEPLOYMENT_NS" --grace-period=30 2>/dev/null || true
    
    # Wait for pod to be recreated on target node
    echo "[*] Waiting for pod to reschedule..."
    TIMEOUT=180
    ELAPSED=0
    RESCHEDULED=false
    
    while [ $ELAPSED -lt $TIMEOUT ]; do
        NEW_POD=$(kubectl get pods -n "$DEPLOYMENT_NS" -l "$POD_LABEL" \
            -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || echo "")
        
        if [ -n "$NEW_POD" ] && [ "$NEW_POD" != "$POD" ]; then
            NEW_NODE=$(kubectl get pod "$NEW_POD" -n "$DEPLOYMENT_NS" \
                -o jsonpath='{.spec.nodeName}' 2>/dev/null || echo "")
            
            if [ "$NEW_NODE" != "$CURRENT_NODE" ]; then
                echo "[+] Pod rescheduled to node: $NEW_NODE"
                sleep 5  # Give CSI time to complete operations
                RESCHEDULED=true
                break
            fi
        fi
        
        sleep 2
        ELAPSED=$((ELAPSED + 2))
    done
    
    if [ "$RESCHEDULED" = false ]; then
        echo "[-] Pod failed to reschedule within $TIMEOUT seconds"
    fi
    
    # Uncordon the original node
    echo "[*] Uncordoning node $CURRENT_NODE"
    kubectl uncordon "$CURRENT_NODE" 2>/dev/null || true
    sleep 1
    
    # Collect logs from CSI nodes
    echo "[*] Collecting CSI logs..."
    CSI_PODS=$(kubectl get pods -n kube-system -l app=csi-azuredisk-node \
        -o jsonpath='{.items[*].metadata.name}' 2>/dev/null || echo "")
    
    for csi_pod in $CSI_PODS; do
        NODE=$(kubectl get pod "$csi_pod" -n kube-system \
            -o jsonpath='{.spec.nodeName}' 2>/dev/null || echo "unknown")
        
        LOG_FILE="$LOG_DIR/csi-logs-cycle-$CYCLE-$NODE.log"
        
        echo "  [*] Collecting from $csi_pod ($NODE)"
        kubectl logs "$csi_pod" -n kube-system --tail=300 > "$LOG_FILE" 2>&1 || true
        
        # Show metrics from this log
        if grep -q "Observed Request Latency" "$LOG_FILE"; then
            echo "  [+] Found metrics in $LOG_FILE:"
            grep "Observed Request Latency" "$LOG_FILE" | tail -2 | sed 's/^/      /'
        fi
    done
    
    echo "[+] Cycle $CYCLE completed"
    echo ""
    
    CYCLE=$((CYCLE + 1))
    
    echo "[*] Waiting $LOOP_INTERVAL seconds before next cycle..."
    sleep "$LOOP_INTERVAL"
done
