# QAD Testing Suite - Requirements & Setup

## Prerequisites

### Kubernetes Cluster
- 2+ nodes in same nodepool (or with same label)
- Kubernetes 1.20+
- Azure Disk CSI driver installed and running
- kubectl access with admin permissions

### Local Machine Requirements

#### Required
- `bash` (version 4+)
- `kubectl` (1.20+)
- `python3` (3.6+)

#### Optional but Recommended
- `jq` - for JSON processing
- `curl` - for manual testing

### Installation

#### macOS
```bash
# Install bash
brew install bash

# Install kubectl
brew install kubectl

# Install Python (usually pre-installed)
# Verify
python3 --version
```

#### Ubuntu/Debian
```bash
apt-get update
apt-get install -y bash kubectl python3
```

#### Windows (WSL2)
```bash
# In WSL2 Ubuntu terminal
sudo apt-get update
sudo apt-get install -y bash kubectl python3
```

## Cluster Requirements

### RBAC Permissions Needed
The test pod needs permissions to:
- Get/List/Delete Pods
- Get/List Nodes
- Patch Nodes (for cordon/uncordon)
- Get/List Deployments
- Get Pod Logs

These are automatically configured in `qad-test-manifest.yaml`

### CSI Driver Configuration
CSI driver must have:
- Logging enabled (typically default)
- Log level >= v=2 (for metrics)
- Access to `/var/lib/kubelet/` directory
- Ability to mount/unmount volumes

### Deployment Requirements
Your test deployment should:
- Use Azure Disk volumes (managed disk CSI)
- Have sufficient replicas for rescheduling
- Support node affinity/pod disruption
- Use labels for selection (e.g., `app=deployment-azuredisk`)

**Example deployment**:
```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: deployment-azuredisk
spec:
  replicas: 1
  selector:
    matchLabels:
      app: deployment-azuredisk
  template:
    metadata:
      labels:
        app: deployment-azuredisk
    spec:
      containers:
      - name: app
        image: ubuntu:latest
        command: ["/bin/sh"]
        args: ["-c", "sleep 3600"]
        volumeMounts:
        - name: azure-disk
          mountPath: /mnt/azure
        resources:
          requests:
            memory: "256Mi"
            cpu: "100m"
      volumes:
      - name: azure-disk
        persistentVolumeClaim:
          claimName: my-pvc
      nodeSelector:
        kubernetes.io/os: linux
```

## Kubernetes Cluster Health Check

Before running tests:

```bash
# Check nodes are ready
kubectl get nodes
# Expected: All nodes in "Ready" state

# Check CSI driver is running
kubectl get pods -n kube-system -l app=csi-azuredisk-node
# Expected: "Running" status, 0 restarts

# Verify RBAC
kubectl auth can-i get pods --as=system:serviceaccount:default:qad-chaos
# Expected: yes

# Check volume provisioning
kubectl get pvc
# Expected: All in "Bound" status

# Test basic operations
kubectl get nodes -o wide
# Expected: Can see node names and IPs
```

## Python Dependencies

The scripts use only Python standard library:
- `json` - JSON parsing
- `re` - Regular expressions
- `sys` - System operations
- `os` - File operations
- `pathlib` - Path handling
- `collections` - defaultdict
- `datetime` - Timestamps

**No pip packages required!**

If you want to use advanced features:
```bash
# Optional: for enhanced reporting
pip3 install pandas matplotlib

# Optional: for beautiful HTML
pip3 install jinja2
```

## Bash/Shell Requirements

Scripts use standard bash features:
- Variables and arrays
- Command substitution `$()`
- For loops
- Kubectl command output parsing
- No special extensions required

Tested on:
- bash 4.0+
- zsh (compatible)
- sh (mostly compatible, some features may not work)

## Kubernetes API Versions

Required API groups (should be on all K8s 1.20+):
- `v1` - Pods, Nodes, Services
- `apps/v1` - Deployments, StatefulSets
- `rbac.authorization.k8s.io/v1` - RBAC

## Resource Requirements

### Pod Resource Requests
```yaml
# Chaos pod
resources:
  requests:
    memory: "64Mi"
    cpu: "100m"
  limits:
    memory: "256Mi"
    cpu: "500m"
```

### Storage Requirements
- Log directory: ~10-50MB per cycle (depending on CSI log verbosity)
- For 20 cycles: ~200-1000MB total
- Keep ~2GB free disk space on pod

### Network Requirements
- Access to Kubernetes API server
- Access to kubectl
- No external internet required

## Permissions Matrix

| Action | Required Role | Scope |
|--------|--------------|-------|
| Get pods | view | Namespace |
| List pods | view | Namespace |
| Delete pods | edit | Namespace |
| Get nodes | view | Cluster |
| List nodes | view | Cluster |
| Patch nodes (cordon) | edit | Cluster |
| Read logs | view | Namespace |
| Access secrets | edit | Namespace |

## Network Isolation

The test works with:
- ✅ Network policies (CSI logs still readable)
- ✅ Private clusters (if kubectl has access)
- ✅ CNI plugins (any Kubernetes network)
- ✅ Service meshes (Istio, Linkerd tested)

## Verification Checklist

Before starting tests:

```bash
# 1. Cluster access
[ ] kubectl cluster-info
[ ] kubectl get nodes

# 2. CSI driver
[ ] kubectl get pods -n kube-system -l app=csi-azuredisk-node
[ ] kubectl logs -n kube-system -l app=csi-azuredisk-node | grep -i "initialized\|started"

# 3. Test deployment
[ ] kubectl get deployment deployment-azuredisk
[ ] kubectl get pvc (has PVCs in Bound state)

# 4. Local tools
[ ] which kubectl
[ ] which python3
[ ] python3 --version

# 5. RBAC test
[ ] kubectl auth can-i create pods
[ ] kubectl auth can-i patch nodes

# 6. Node count
[ ] kubectl get nodes -l kubernetes.io/os=linux (should show 2+)
```

## Troubleshooting Setup

### "kubectl not found"
```bash
# Check if installed
which kubectl

# If not, install
# macOS
brew install kubectl

# Linux
curl -LO "https://dl.k8s.io/release/$(curl -L -s https://dl.k8s.io/release/stable.txt)/bin/linux/amd64/kubectl"
sudo install -o root -g root -m 0755 kubectl /usr/local/bin/kubectl
```

### "Cannot connect to cluster"
```bash
# Verify kubeconfig
echo $KUBECONFIG
ls ~/.kube/config

# Test connection
kubectl cluster-info

# If not set, configure
export KUBECONFIG=~/.kube/config
```

### "Permission denied" errors
```bash
# Check current user
kubectl auth whoami

# Verify admin role
kubectl auth can-i '*' '*'

# If denied, you may need cluster admin
# Contact your cluster administrator
```

### "CSI pods not running"
```bash
# Check pod status
kubectl get pods -n kube-system -l app=csi-azuredisk-node

# Check logs
kubectl logs -n kube-system -l app=csi-azuredisk-node

# Check events
kubectl describe pod <csi-pod-name> -n kube-system | tail -20
```

## Security Considerations

The test pod needs:
- Read access to CSI logs (for metrics)
- Ability to delete pods (controlled deletion only)
- Node cordon/uncordon (reversible operations)
- All operations are namespaced or reversible

**Recommended security practices**:
1. Run in isolated namespace (not production)
2. Use short RBAC token lifetime
3. Remove RBAC after testing: `kubectl delete clusterrolebinding qad-chaos`
4. Audit logs for pod deletions
5. Don't run on production workloads

## Space & Cleanup

Monitor disk usage:
```bash
# Check pod log size
kubectl exec qad-chaos-loop -- du -sh /logs

# In local collection
du -sh /tmp/qad-logs

# Clean up old logs
rm -rf /tmp/qad-logs/csi-logs-cycle-[1-5]-*.log
```

## Summary

You need:
- ✅ 2 Linux nodes in Kubernetes
- ✅ Azure Disk CSI driver running
- ✅ kubectl CLI (connected to cluster)
- ✅ Python 3 (for metrics analysis)
- ✅ Bash 4+
- ✅ ~2GB free disk space

That's it! Ready to test? Start with `./qad-test-helper.sh start`
