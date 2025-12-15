# Quick Start Guide - QAD Testing Suite

## One-Minute Setup

```bash
# 1. Navigate to the test directory
cd test-qad/

# 2. Make scripts executable
chmod +x chaos-loop.sh qad-test-helper.sh

# 3. Start the chaos test
./qad-test-helper.sh start

# 4. Watch it run (in another terminal)
./qad-test-helper.sh logs -f
```

## Running Your Test

### Option 1: Using the Helper Script (Recommended)

```bash
# Start the test
./qad-test-helper.sh start

# Wait a few minutes for cycles to complete...

# Check status
./qad-test-helper.sh status

# Collect logs locally
./qad-test-helper.sh collect /tmp/my-qad-test

# Run metrics analysis
./qad-test-helper.sh metrics /tmp/my-qad-test

# Generate full report
./qad-test-helper.sh report /tmp/my-qad-test qad-final-report.txt

# Stop the test
./qad-test-helper.sh stop
```

### Option 2: Using kubectl directly

```bash
# Deploy
kubectl apply -f qad-test-manifest.yaml

# Monitor
kubectl logs -f qad-chaos-loop

# After test, collect logs
kubectl cp default/qad-chaos-loop:/logs /tmp/qad-logs

# Extract metrics
python3 qad_metrics_extractor.py /tmp/qad-logs

# Cleanup
kubectl delete -f qad-test-manifest.yaml
```

## Expected Test Flow

Each cycle:
1. ⏱️ Identifies current pod and node
2. 🔒 Cordons the current node (prevents new pods)
3. 🗑️ Deletes the pod (triggers rescheduling)
4. ⏳ Waits for pod to reschedule on the other node
5. 🔓 Uncordons the original node
6. 📊 Collects CSI driver logs
7. 🔄 Sleeps 30 seconds, repeats

Example cycle output:
```
==========================================
[Thu Dec 12 22:31:54 UTC 2024] CYCLE 1
==========================================
Current pod: deployment-azuredisk-6bc6796f57-vc2xf on node: aks-isci-12043195-vmss000001
Target node: aks-isci-12043195-vmss000000
[Thu Dec 12 22:31:55 UTC 2024] Cordoning aks-isci-12043195-vmss000001 and deleting pod
[Thu Dec 12 22:31:58 UTC 2024] Pod rescheduled to aks-isci-12043195-vmss000000
[Thu Dec 12 22:32:00 UTC 2024] Uncordoning aks-isci-12043195-vmss000001
[Thu Dec 12 22:32:01 UTC 2024] Collecting CSI logs...
  Saved: /logs/csi-logs-cycle-1-aks-isci-12043195-vmss000001.log
  Saved: /logs/csi-logs-cycle-1-aks-isci-12043195-vmss000000.log
[Thu Dec 12 22:32:02 UTC 2024] Cycle 1 completed
```

## What You'll See in Metrics

**Good performance (target):**
```
NODE_STAGE_VOLUME LATENCIES (seconds)
  Count: 10
  Min:   2.1s
  P50:   2.5s
  Avg:   2.6s
  P95:   3.0s
  Max:   3.1s
```

**Monitor these values:**
- **P50 (Median)**: Typical performance
- **P95/P99**: Tail latencies (SLA relevant)
- **Max**: Worst case scenario
- **Count**: Number of operations completed

## Customization

Edit `qad-test-manifest.yaml` to change:

```yaml
# Change deployment being tested
args:
- "my-app-name"                 # ← Change here

# Change namespace
- "my-namespace"                # ← Change here

# Change node selector
- "agentpool=mypoolname"        # ← Change here (or use kubernetes.io/os=linux)

# Change loop interval
- "60"                          # ← Change to 60 seconds between cycles
```

## Common Tasks

### View live CSI logs
```bash
# From the pod
kubectl exec qad-chaos-loop -- tail -f /logs/csi-logs-cycle-*.log

# Or from local after collection
tail -f /tmp/qad-logs/csi-logs-cycle-*.log
```

### Run multiple tests in parallel
```bash
# Test 1: With QAD enabled
./qad-test-helper.sh start  # Default config

# (in another terminal, different directory or with different POD_NAME)
# Modify and run another test
```

### Export results for comparison
```bash
python3 qad_metrics_extractor.py /tmp/qad-logs test1-metrics.json
python3 qad_metrics_extractor.py /tmp/other-qad-logs test2-metrics.json

# Compare JSON files programmatically
python3 << 'EOF'
import json
with open('test1-metrics.json') as f1, open('test2-metrics.json') as f2:
    t1 = json.load(f1)
    t2 = json.load(f2)
    
    print("Test 1 Stage Avg:", t1['stage_volume']['statistics']['avg'])
    print("Test 2 Stage Avg:", t2['stage_volume']['statistics']['avg'])
EOF
```

## Troubleshooting

### Pods not rescheduling
```bash
# Check node status
kubectl get nodes

# Verify deployment exists and has replicas
kubectl get deployment deployment-azuredisk

# Check for scheduling issues
kubectl describe pod deployment-azuredisk-6bc6796f57-vc2xf | grep -A 5 Events
```

### No logs being collected
```bash
# Verify CSI pods are running
kubectl get pods -n kube-system -l app=csi-azuredisk-node

# Check if they're logging
kubectl logs -n kube-system <csi-pod-name> | grep "Observed Request Latency"
```

### Metrics extractor not finding data
```bash
# Verify log format
grep "Observed Request Latency" /tmp/qad-logs/*.log | head -1

# Should output something like:
# I1212 22:31:57.707491 ... "Observed Request Latency" latency_seconds=2.761
```

## Performance Tips

1. **Run at least 5-10 cycles** for meaningful statistics
2. **Monitor node health** during test with `kubectl top nodes`
3. **Check Azure throttling** in diagnostic logs if p99 > 10s
4. **Validate deployment** is actually mounting the volume:
   ```bash
   kubectl exec deployment-azuredisk-6bc6796f57-vc2xf -- mount | grep /mnt
   ```

## Next Steps

1. ✅ Run baseline test with this suite
2. ✅ Compare QAD vs non-QAD performance
3. ✅ Identify outliers and investigate
4. ✅ Document SLA requirements based on results
5. ✅ Set up continuous monitoring using these metrics

Enjoy your QAD performance testing! 🚀
