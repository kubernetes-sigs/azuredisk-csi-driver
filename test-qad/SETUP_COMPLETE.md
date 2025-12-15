# QAD Testing Suite - Setup Complete! 🎉

Your complete end-to-end QAD performance testing framework is ready.

## 📦 What You Have

```
test-qad/
├── QUICK_START.md              ← START HERE (5-minute setup)
├── INDEX.md                    ← Overview of all files
├── README.md                   ← Complete documentation
├── REQUIREMENTS.md             ← Prerequisites & checklist
├── qad-test-helper.sh          ← Main CLI tool (run first!)
├── qad-test-manifest.yaml      ← Kubernetes manifests
├── chaos-loop.sh               ← Core testing script
├── qad_metrics_extractor.py    ← Parse logs & extract metrics
├── qad_report_generator.py     ← Generate HTML report
└── SETUP_COMPLETE.md           ← This file
```

## 🚀 Quick Start (Copy & Paste)

```bash
# 1. Navigate to test directory
cd test-qad/

# 2. Make scripts executable
chmod +x *.sh *.py

# 3. Start the chaos test (requires kubectl access)
./qad-test-helper.sh start

# 4. Watch it run
./qad-test-helper.sh logs -f

# 5. After ~10 cycles (5+ minutes), collect logs
./qad-test-helper.sh collect /tmp/qad-results

# 6. Analyze metrics
./qad-test-helper.sh metrics /tmp/qad-results

# 7. Generate beautiful HTML report
python3 qad_report_generator.py /tmp/qad-results/metrics.json

# 8. Stop the test
./qad-test-helper.sh stop
```

## 📋 Component Functions

| File | Purpose | Usage |
|------|---------|-------|
| qad-test-helper.sh | Control everything | `./qad-test-helper.sh <cmd>` |
| qad-test-manifest.yaml | Deploy to k8s | Modify then `kubectl apply` |
| chaos-loop.sh | Core test logic | Auto-run by manifest |
| qad_metrics_extractor.py | Extract metrics from logs | `python3 qad_metrics_extractor.py` |
| qad_report_generator.py | Make HTML report | `python3 qad_report_generator.py` |

## ✅ What Each Script Does

### qad-test-helper.sh
Your main interface - one-command control:
```bash
./qad-test-helper.sh start      # Deploy chaos test
./qad-test-helper.sh stop       # Kill test
./qad-test-helper.sh logs -f    # Watch live logs
./qad-test-helper.sh collect    # Copy results locally
./qad-test-helper.sh metrics    # Analyze logs
./qad-test-helper.sh report     # Generate report
./qad-test-helper.sh cleanup    # Remove all resources
```

### qad-test-manifest.yaml
Kubernetes resources:
- Defines chaos test pod
- Configures RBAC permissions
- Mounts log directory
- Auto-runs chaos-loop.sh

Edit the `args:` section to customize:
```yaml
args:
- "deployment-azuredisk"    # ← Deployment name
- "default"                 # ← Kubernetes namespace
- "kubernetes.io/os=linux"  # ← Node selector
- "/logs"                   # ← Log directory
- "30"                      # ← Loop interval (seconds)
```

### chaos-loop.sh
The test engine that:
1. Finds current pod and node
2. Cordons the node (stops new pods)
3. Deletes the pod (forces rescheduling)
4. Waits for rescheduling to other node
5. Uncordons the original node
6. Collects CSI logs
7. Repeats every N seconds

### qad_metrics_extractor.py
Log analyzer that:
- Parses CSI driver logs
- Extracts latency metrics from log lines
- Calculates min/p50/p95/p99/max
- Exports to JSON for reports
- Works on single files or directories

### qad_report_generator.py
Report generator that:
- Reads metrics JSON
- Creates beautiful HTML report
- Shows statistics and recommendations
- Detects performance issues
- Browser-friendly design

## 🎯 Typical Test Flow

```
10 minutes
├─ Setup (1 min)       → chmod +x && helper.sh start
├─ Running (8 min)     → 8-10 test cycles
└─ Collect (1 min)     → helper.sh collect

20 minutes total
├─ Analysis (5 min)    → metrics & report
└─ Review (15 min)     → read HTML report
```

## 📊 What You'll Get

**After each test cycle:**
- Log file: `csi-logs-cycle-{N}-{node}.log` (CSI driver logs)
- ~2-3 "Observed Request Latency" entries per cycle
- Attach/detach times captured automatically

**Final metrics report:**
```
NODE_STAGE_VOLUME (Attach):
  Count: 10
  Min:   2.1s
  P50:   2.5s
  Avg:   2.6s
  P95:   3.0s
  Max:   3.2s

NODE_UNSTAGE_VOLUME (Detach):
  Count: 10
  Min:   0.8s
  P50:   1.0s
  Avg:   1.1s
  P95:   1.4s
  Max:   1.6s
```

**Beautiful HTML report:**
- Professional layout
- Color-coded status (Good/Warning/Critical)
- Statistical summary
- Performance recommendations
- Easy to share

## 🔧 Customization

### Test Different Deployment
Edit `qad-test-manifest.yaml`:
```yaml
args:
- "my-app-name"  # Change this
```

### Change Node Pool
Edit `qad-test-manifest.yaml`:
```yaml
args:
- "deployment-azuredisk"
- "default"
- "agentpool=my-pool"  # Change this
```

### Increase Test Duration
Edit `qad-test-manifest.yaml`:
```yaml
args:
- "deployment-azuredisk"
- "default"
- "kubernetes.io/os=linux"
- "/logs"
- "60"  # Change from 30 to 60 seconds between cycles
```

## ⚙️ Requirements

### Cluster
- ✅ 2+ Linux nodes
- ✅ Azure Disk CSI driver running
- ✅ kubectl with admin access

### Local Machine
- ✅ bash 4+
- ✅ kubectl CLI
- ✅ python3 (for metrics)
- ✅ 2GB disk space for logs

### Permissions Needed
- ✅ Get/List/Delete pods
- ✅ Get/List/Patch nodes
- ✅ Read pod logs

All configured automatically by manifest!

## 🚨 Before You Start

```bash
# 1. Verify cluster access
kubectl cluster-info

# 2. Check nodes ready
kubectl get nodes

# 3. Verify CSI driver running
kubectl get pods -n kube-system | grep csi-azuredisk

# 4. Check your test deployment exists
kubectl get deployment deployment-azuredisk

# 5. Make scripts executable
chmod +x qad-test-helper.sh chaos-loop.sh *.py

# 6. You're ready!
./qad-test-helper.sh start
```

## 📚 Documentation Files

- **QUICK_START.md** - Get running in 5 minutes
- **INDEX.md** - Overview of all components
- **README.md** - Complete detailed guide
- **REQUIREMENTS.md** - Detailed prerequisites
- **SETUP_COMPLETE.md** - This file

Start with **QUICK_START.md** if you want to begin immediately!

## 🎓 Learning Path

1. **5 min**: Read QUICK_START.md
2. **5 min**: Review qad-test-manifest.yaml to understand what runs
3. **30 min**: Run first test with `./qad-test-helper.sh start`
4. **10 min**: Extract metrics and generate report
5. **15 min**: Review HTML report and understand results

Total: ~1 hour to complete first full test!

## 🐛 Troubleshooting

**Pod won't reschedule?**
```bash
kubectl get nodes          # Both ready?
kubectl get deployment     # Has replicas?
kubectl describe pod <name> # Check events
```

**No metrics found?**
```bash
# Check if CSI logging is enabled
kubectl logs -n kube-system <csi-pod> | grep "Observed Request Latency"

# If nothing, CSI may need higher log level
```

**Permission errors?**
```bash
# Verify RBAC created
kubectl get clusterrolebinding | grep qad

# Check pod can read logs
kubectl exec qad-chaos-loop -- ls /logs/
```

Full troubleshooting in **README.md** and **REQUIREMENTS.md**

## 🎉 You're All Set!

Everything is ready to start testing QAD performance!

### Next Step:
```bash
cd test-qad/
./qad-test-helper.sh start
```

### Questions?
- See **QUICK_START.md** for common issues
- See **README.md** for complete documentation
- See **REQUIREMENTS.md** for prerequisites

---

**Happy QAD Testing! 🚀**

Built with ❤️ for Azure Disk CSI Driver performance testing
