# QAD Testing Suite - Complete Overview

## What You're Getting

A complete end-to-end testing framework for measuring Azure Disk CSI Quick Attach-Detach (QAD) performance with automatic pod rescheduling, log collection, and metrics analysis.

## File Structure

```
test-qad/
├── README.md                      # Comprehensive documentation
├── QUICK_START.md                 # Get started in 5 minutes
├── chaos-loop.sh                  # Main test script
├── qad-test-helper.sh             # Helper CLI tool
├── qad-test-manifest.yaml         # Kubernetes manifests
├── qad_metrics_extractor.py       # Log parser & metrics calculator
├── qad_report_generator.py        # HTML report generator
└── INDEX.md                       # This file
```

## File Descriptions

### chaos-loop.sh
**Purpose**: Continuously runs test cycles that:
- Delete the deployment pod
- Cordon a node to force rescheduling
- Collect CSI driver logs
- Repeat every N seconds

**Usage**: 
```bash
./chaos-loop.sh <deployment> <namespace> <node-label> <log-dir> <interval>
```

**Output**: Log files in format: `csi-logs-cycle-{N}-{node-name}.log`

### qad-test-helper.sh
**Purpose**: User-friendly CLI wrapper for all operations

**Usage**:
```bash
./qad-test-helper.sh <command>

Commands:
  start       - Deploy chaos test
  stop        - Kill chaos test
  status      - Show current status
  logs        - Show chaos pod logs
  csi-logs    - Show collected CSI logs
  collect     - Copy logs to local machine
  metrics     - Run metrics analyzer
  report      - Generate full report
  cleanup     - Remove all resources
```

### qad-test-manifest.yaml
**Purpose**: Kubernetes manifests defining:
- ServiceAccount with RBAC permissions
- ConfigMap with chaos scripts
- Pod running the chaos loop
- Pod for log viewing (optional)

**Key Configs**:
- Line 1: Deployment name (change if testing different app)
- Line 2: Kubernetes namespace
- Line 3: Node selector label
- Line 5: Interval between cycles (seconds)

### qad_metrics_extractor.py
**Purpose**: Parses logs and extracts QAD latency metrics

**Input**: Log files containing "Observed Request Latency" entries

**Output**: 
- Console report with statistics (min/p50/p95/p99/max)
- JSON export for programmatic access

**Usage**:
```bash
python3 qad_metrics_extractor.py <log-file-or-dir> [output.json]
```

**What It Finds**:
```
I1212 22:31:57.707491 ... "Observed Request Latency" 
latency_seconds=2.761356459 request="azuredisk_csi_driver_node_stage_volume"
```

Extracts:
- `latency_seconds`: Time in seconds
- `request`: Operation type (stage/unstage)
- `result_code`: Success/failure

### qad_report_generator.py
**Purpose**: Converts metrics JSON into beautiful HTML report

**Usage**:
```bash
python3 qad_report_generator.py metrics.json [output.html]
```

**Output**: Professional HTML report with:
- Performance summary
- Statistics tables
- Status badges (Good/Warning/Critical)
- Recommendations based on metrics

## Typical Workflow

### 1. Deploy Test (5 minutes)
```bash
./qad-test-helper.sh start
```

### 2. Let It Run (10-30 minutes)
```bash
./qad-test-helper.sh logs -f
```
Target: 10+ cycles for statistical significance

### 3. Collect Results
```bash
./qad-test-helper.sh collect /tmp/my-results
```

### 4. Analyze
```bash
./qad-test-helper.sh metrics /tmp/my-results
python3 qad_report_generator.py /tmp/my-results/metrics.json
```

### 5. Report
```bash
./qad-test-helper.sh report /tmp/my-results final-report.txt
# Then open qad-report.html in browser
```

### 6. Cleanup
```bash
./qad-test-helper.sh stop
```

## Expected Results

### Good Performance (Target SLA)
```
Stage Volume (Attach):
  Count: 20+
  P50:   ~2.5s
  P95:   <3.5s
  P99:   <4.0s

Unstage Volume (Detach):
  Count: 20+
  P50:   ~1.0s
  P95:   <2.0s
  P99:   <2.5s
```

### Metrics You Should Monitor
- **Count**: Number of operations (higher = more reliable stats)
- **Min/Max**: Baseline and worst case
- **P50**: Median (typical performance)
- **P95/P99**: Tail latencies (SLA critical)
- **Avg**: Average across all ops

## Key Features

✅ **Automated**: Full end-to-end testing with zero manual intervention
✅ **Stateful**: Tracks progress across multiple cycles
✅ **Real Logs**: Collects actual CSI driver logs, not synthetic
✅ **Metrics**: Automatic extraction and statistical analysis
✅ **Reports**: Professional HTML + text reports
✅ **Safe**: Uses cordon/uncordon, respects pod grace periods
✅ **Observable**: Live logging, status checks, log access
✅ **Flexible**: Configure deployment, namespace, node selector
✅ **Reproducible**: Exactly same test scenario each cycle

## Customization Guide

### Change the Deployment Being Tested
Edit `qad-test-manifest.yaml`, find the `args:` section:
```yaml
args:
- "my-app-name"              # ← Change here
- "my-namespace"
```

### Change Loop Interval
```yaml
args:
- "deployment-azuredisk"
- "default"
- "kubernetes.io/os=linux"
- "/logs"
- "60"                       # ← Change to 60 seconds between cycles
```

### Change Node Selection
```yaml
args:
- "deployment-azuredisk"
- "default"
- "agentpool=my-pool"        # ← Change to different node selector
```

### Use Different CSI Driver
The script assumes `app=csi-azuredisk-node`. If using different driver:
Edit `qad-test-manifest.yaml` line ~70:
```bash
CSI_PODS=$(kubectl get pods -n kube-system -l app=my-csi-driver \
```

## Troubleshooting

### Pod Never Reschedules
```bash
kubectl get nodes                    # Check both nodes Ready
kubectl get pod <pod> -o yaml | grep -A5 nodeName
kubectl describe pod <pod>           # Check Events section
```

### No Metrics Found
```bash
kubectl logs <csi-pod> -n kube-system | grep "Observed Request Latency"
# If nothing, CSI logging may be at wrong level
```

### High Memory Usage
- Reduce log collection interval
- Delete cycles older than needed
- Limit log tail size in manifest

## Performance Interpretation

**2-3 seconds for attach** is normal:
- Azure API call: ~500-800ms
- Disk attachment: ~500-1000ms
- Device discovery: ~300-500ms
- Filesystem mount: ~200-400ms

**1-2 seconds for detach** is normal:
- Filesystem unmount: ~200-400ms
- Azure API call: ~500-800ms
- Device cleanup: ~200-400ms

**Outliers (>5 seconds)** suggest:
- Azure rate limiting/throttling
- Network latency
- Resource contention on node
- Disk I/O issues

## Integration with CI/CD

To integrate into your testing pipeline:

```bash
#!/bin/bash
set -e

# Deploy
kubectl apply -f qad-test-manifest.yaml

# Wait for cycles
sleep 600  # 10 minutes for ~10 cycles

# Collect
kubectl cp default/qad-chaos-loop:/logs /tmp/results

# Analyze
python3 qad_metrics_extractor.py /tmp/results results.json
python3 qad_report_generator.py results.json

# Validate (example)
python3 << 'EOF'
import json
with open('results.json') as f:
    data = json.load(f)
    p95 = data['stage_volume']['statistics']['p95']
    if p95 > 5:
        print("FAIL: P95 attach latency too high")
        exit(1)
    print("PASS: Performance acceptable")
EOF

# Cleanup
kubectl delete -f qad-test-manifest.yaml
```

## Next Steps

1. **Run baseline test** - Establish baseline metrics
2. **Compare with/without QAD** - Measure improvement
3. **Test at scale** - Try with larger workloads
4. **Monitor continuously** - Add to regular test suite
5. **Set SLAs** - Define acceptable latency ranges

## Support & Feedback

- Check QUICK_START.md for common issues
- Review README.md for detailed documentation
- Test scripts are in bash - easy to modify/debug
- Metrics extractor is in Python - portable across platforms

---

**Happy Testing! 🚀**
