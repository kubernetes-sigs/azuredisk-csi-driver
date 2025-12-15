#!/bin/bash

# QAD Test Helper - Automates common test operations

set -e

COMMAND="${1:-help}"
POD_NAME="qad-chaos-loop"
NAMESPACE="default"
LOG_DIR="/tmp/qad-logs"

usage() {
    cat << EOF
QAD Testing Helper

Usage: ./qad-test-helper.sh <command> [options]

Commands:
  start              Deploy chaos test pods
  stop               Delete chaos test pods
  status             Show pod status and current cycle
  logs               Show chaos pod logs
  csi-logs           Show collected CSI logs
  collect            Copy logs from pod to local machine
  metrics            Run metrics extractor on local logs
  report             Generate a full test report
  cleanup            Remove all test resources
  help               Show this help message

Examples:
  ./qad-test-helper.sh start
  ./qad-test-helper.sh logs -f
  ./qad-test-helper.sh collect /tmp/my-logs
  ./qad-test-helper.sh metrics /tmp/my-logs
EOF
}

start() {
    echo "[*] Deploying QAD test manifests..."
    if [ -f "qad-test-manifest.yaml" ]; then
        kubectl apply -f qad-test-manifest.yaml
        echo "[+] Chaos test started"
        echo "    Monitor with: kubectl logs -f $POD_NAME"
    else
        echo "[-] qad-test-manifest.yaml not found in current directory"
        exit 1
    fi
}

stop() {
    echo "[*] Stopping chaos test..."
    kubectl delete pod $POD_NAME -n $NAMESPACE 2>/dev/null || echo "    Pod not running"
    kubectl delete pod qad-logs-viewer -n $NAMESPACE 2>/dev/null || true
    echo "[+] Chaos test stopped"
}

status() {
    echo "[*] QAD Test Status"
    echo "-------------------"
    
    if kubectl get pod $POD_NAME -n $NAMESPACE &>/dev/null; then
        echo "Chaos Pod: Running"
        kubectl get pod $POD_NAME -n $NAMESPACE
        
        echo ""
        echo "Recent Activity:"
        kubectl logs $POD_NAME -n $NAMESPACE --tail=20 | grep "CYCLE\|cordoning\|rescheduled"
    else
        echo "Chaos Pod: Not running"
    fi
}

show_logs() {
    local follow=""
    if [ "$2" = "-f" ]; then
        follow="-f"
    fi
    
    if kubectl get pod $POD_NAME -n $NAMESPACE &>/dev/null; then
        kubectl logs $follow $POD_NAME -n $NAMESPACE
    else
        echo "[-] Chaos pod not running"
        exit 1
    fi
}

show_csi_logs() {
    echo "[*] Latest CSI logs from pod..."
    if kubectl get pod $POD_NAME -n $NAMESPACE &>/dev/null; then
        kubectl exec $POD_NAME -n $NAMESPACE -- \
            sh -c 'ls -t /logs/*.log 2>/dev/null | head -5 | xargs -I {} bash -c "echo \"\\n=== {} ===\"; tail -30 {}"'
    else
        echo "[-] Chaos pod not running"
        exit 1
    fi
}

collect_logs() {
    local dest="${2:-.}"
    mkdir -p "$dest"
    
    if kubectl get pod $POD_NAME -n $NAMESPACE &>/dev/null; then
        echo "[*] Collecting logs from pod..."
        kubectl cp "$NAMESPACE/$POD_NAME:/logs" "$dest/qad-logs" 2>/dev/null || \
            echo "[-] Failed to copy logs. Pod may have exited."
        
        if [ -d "$dest/qad-logs" ]; then
            echo "[+] Logs collected to: $dest/qad-logs"
            ls -lh "$dest/qad-logs/" | tail -5
        fi
    else
        echo "[-] Chaos pod not running. Cannot collect logs."
        exit 1
    fi
}

run_metrics() {
    local source="${2:-.}"
    
    if [ ! -f "qad_metrics_extractor.py" ]; then
        echo "[-] qad_metrics_extractor.py not found in current directory"
        exit 1
    fi
    
    if [ ! -d "$source" ] && [ ! -f "$source" ]; then
        echo "[-] Source not found: $source"
        exit 1
    fi
    
    echo "[*] Running metrics extractor..."
    python3 qad_metrics_extractor.py "$source" "${3:-.}"
}

generate_report() {
    local log_source="${2:-.}"
    local output_file="${3:-qad-report.txt}"
    
    echo "[*] Generating QAD test report..."
    echo "[*] Log source: $log_source"
    
    {
        echo "QAD Test Report - $(date)"
        echo "=============================================="
        echo ""
        echo "Test Configuration:"
        echo "  Log Source: $log_source"
        echo ""
        
        echo "Node Information:"
        kubectl get nodes -o wide || echo "  Unable to fetch nodes"
        echo ""
        
        echo "Deployment Status:"
        kubectl get deployment deployment-azuredisk 2>/dev/null || echo "  Deployment not found"
        echo ""
        
        echo "Pod Status:"
        kubectl get pods -l app=deployment-azuredisk 2>/dev/null || echo "  No pods found"
        echo ""
        
        echo "=============================================="
        echo "QAD Latency Metrics"
        echo "=============================================="
        echo ""
        
        if [ -d "$log_source" ]; then
            python3 qad_metrics_extractor.py "$log_source"
        elif [ -f "$log_source" ]; then
            python3 qad_metrics_extractor.py "$log_source"
        else
            echo "No log source found at: $log_source"
        fi
    } | tee "$output_file"
    
    echo ""
    echo "[+] Report saved to: $output_file"
}

cleanup() {
    echo "[*] Cleaning up all QAD test resources..."
    
    # Delete pods
    kubectl delete pod $POD_NAME -n $NAMESPACE 2>/dev/null || true
    kubectl delete pod qad-logs-viewer -n $NAMESPACE 2>/dev/null || true
    
    # Delete RBAC
    kubectl delete clusterrolebinding qad-chaos 2>/dev/null || true
    kubectl delete clusterrole qad-chaos 2>/dev/null || true
    kubectl delete serviceaccount qad-chaos -n $NAMESPACE 2>/dev/null || true
    
    # Delete ConfigMap
    kubectl delete configmap qad-chaos-scripts -n $NAMESPACE 2>/dev/null || true
    
    echo "[+] Cleanup complete"
}

case "$COMMAND" in
    start)
        start
        ;;
    stop)
        stop
        ;;
    status)
        status
        ;;
    logs)
        show_logs "$@"
        ;;
    csi-logs)
        show_csi_logs
        ;;
    collect)
        collect_logs "$@"
        ;;
    metrics)
        run_metrics "$@"
        ;;
    report)
        generate_report "$@"
        ;;
    cleanup)
        cleanup
        ;;
    help|--help|-h)
        usage
        ;;
    *)
        echo "Unknown command: $COMMAND"
        echo ""
        usage
        exit 1
        ;;
esac
