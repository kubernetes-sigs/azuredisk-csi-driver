# QAD Performance Testing Suite

This suite continuously reschedules your Azure Disk CSI test pod across nodes while collecting QAD attach/detach latency metrics from CSI driver logs.

## Components

1. **chaos-loop.sh** - Bash script that:
   - Deletes the deployment pod
   - Cordons the current node
   - Forces rescheduling to the other node
   - Collects CSI driver logs after each cycle
   - Repeats indefinitely

2. **qad_metrics_extractor.py** - Python script that:
   - Parses CSI logs for QAD latency metrics
   - Extracts `Observed Request Latency` entries
   - Calculates statistics (min/p50/p95/p99/max)
   - Exports results to JSON
   - Supports batch processing multiple log files

3. **qad-test-manifest.yaml** - Kubernetes manifests:
   - ServiceAccount with required RBAC
   - ConfigMap with chaos scripts
   - Pod running the chaos loop
   - Pod for log viewing

## Quick Start

### Prerequisites

- 2 nodes in a nodepool
- Deployment pod running with Azure Disk CSI volumes
- kubectl access to the cluster
- Python 3.6+ (for metrics extraction)

### Setup

1. **Deploy the chaos test** (modify as needed):

```bash
# Update deployment name if different
kubectl apply -f qad-test-manifest.yaml

# Verify the chaos pod is running
kubectl get pod qad-chaos-loop
```

2. **Monitor the test in real-time**:

```bash
# Option 1: Watch chaos loop logs
kubectl logs -f qad-chaos-loop

# Option 2: View latest collected logs
kubectl exec -it qad-logs-viewer -- tail -f /logs/csi-logs-cycle-*.log

# Option 3: Connect to pod and view logs directory
kubectl exec -it qad-chaos-loop -- bash
ls -la /logs/
```

3. **Collect logs after test completes**:

```bash
# Copy logs from the pod
kubectl cp default/qad-chaos-loop:/logs /tmp/qad-logs

# Or extract directly
kubectl exec qad-chaos-loop -- cat /logs/csi-logs-cycle-1-aks-isci-12043195-vmss000001.log
```

## Running Metrics Extraction

### Basic Usage

```bash
# Single log file
python3 qad_metrics_extractor.py csi-logs-cycle-1-node1.log

# All logs in a directory
python3 qad_metrics_extractor.py /tmp/qad-logs/

# Export to JSON
python3 qad_metrics_extractor.py /tmp/qad-logs/ metrics.json
```

### Output Example

```
================================================================================
QAD METRICS REPORT
================================================================================

NODE_STAGE_VOLUME LATENCIES (seconds)
----------------------------------------
  Count: 5
  Min:   2.104s
  P50:   2.456s
  Avg:   2.614s
  P95:   3.022s
  P99:   3.022s
  Max:   3.022s

NODE_UNSTAGE_VOLUME LATENCIES (seconds)
----------------------------------------
  Count: 5
  Min:   0.842s
  P50:   1.023s
  Avg:   1.156s
  P95:   1.456s
  P99:   1.456s
  Max:   1.456s

================================================================================
```

## Configuration

Edit the chaos pod arguments in `qad-test-manifest.yaml`:

```yaml
args:
- "deployment-azuredisk"        # Change deployment name
- "default"                     # Change namespace
- "kubernetes.io/os=linux"      # Node selector label
- "/logs"                       # Log directory (keep as-is)
- "30"                          # Loop interval (seconds between cycles)
```

## Stopping the Test

```bash
# Delete the chaos pod
kubectl delete pod qad-chaos-loop

# Clean up all resources
kubectl delete -f qad-test-manifest.yaml
```

## Advanced: Manual Testing

If you want to run the chaos script manually:

```bash
# Copy script to a pod
kubectl cp chaos-loop.sh default/qad-chaos-loop:/tmp/

# Execute it
kubectl exec qad-chaos-loop -- bash /tmp/chaos-loop.sh \
  deployment-azuredisk default kubernetes.io/os=linux /tmp/logs 30
```

## Log Format

The metrics are extracted from logs like:

```
I1212 22:31:57.707491       1 azure_metrics.go:105] "Observed Request Latency" \
latency_seconds=2.761356459 request="azuredisk_csi_driver_node_stage_volume" \
resource_group="mc_ctl-aks-1053-rg_ctl-aks-1053_eastus2" subscription_id="" \
source="disk.csi.azure.com" volumeid="..." result_code="succeeded"
```

Metrics extractor looks for:
- `latency_seconds=<value>` - The latency in seconds
- `request="<type>"` - The request type (stage/unstage)
- `result_code="<code>"` - Success/failure status

## Troubleshooting

### Pod not rescheduling
- Check if both nodes are Ready: `kubectl get nodes`
- Verify deployment has resource requests to trigger scheduling
- Check node cordoning: `kubectl describe node <node-name>`

### No metrics found
- Ensure CSI logs contain "Observed Request Latency" messages
- Check log level: CSI should be at v=2 or higher
- Verify pod actually performed volume operations (not cached)

### Permission denied errors
- Verify ServiceAccount RBAC is applied: `kubectl get clusterrolebinding qad-chaos`
- Check pod events: `kubectl describe pod qad-chaos-loop`

## Performance Interpretation

**Stage (Attach) Latency**: Time to attach disk to VM
- Typically 2-5 seconds
- Includes: VM API call + disk attachment + mount

**Unstage (Detach) Latency**: Time to detach disk from VM
- Typically 1-3 seconds
- Includes: Umount + disk detachment + cleanup

**Outliers (>5s)**: May indicate:
- Azure API throttling
- Network latency
- Resource contention
- Underlying infrastructure issues

## Next Steps

1. Run several cycles to get baseline metrics
2. Compare with non-QAD attach times if needed
3. Analyze p95/p99 latencies for SLA requirements
4. Export results to JSON for further analysis
