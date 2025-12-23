# Freeze/Unfreeze Feature - AKS Test Plan

## Overview
This document provides a comprehensive test plan for validating the filesystem freeze/unfreeze implementation for consistent snapshots in Azure Disk CSI Driver on AKS clusters.

## Test Environment Setup

### Prerequisites
- AKS cluster (version 1.27+)
- Azure Disk CSI Driver with freeze/unfreeze feature enabled
- VolumeSnapshot CRDs installed
- External-snapshotter controller running
- Test workloads with filesystem volumes (ext4/xfs)

### Configuration Options to Test
- `--snapshot-consistency-mode`: `best-effort` (default) or `strict`
- `--fsfreeze-wait-timeout-mins`: Default `2` minutes, configurable
- `--enable-snapshot-consistency`: Must be `true`

### Test Cluster Configurations

#### Configuration 1: Best-Effort Mode (Default)
```yaml
controller:
  args:
    - --snapshot-consistency-mode=best-effort
    - --fsfreeze-wait-timeout-mins=2
    - --enable-snapshot-consistency=true
```

#### Configuration 2: Strict Mode
```yaml
controller:
  args:
    - --snapshot-consistency-mode=strict
    - --fsfreeze-wait-timeout-mins=5
    - --enable-snapshot-consistency=true
```

#### Configuration 3: Strict Mode with Zero Timeout (Indefinite Wait)
```yaml
controller:
  args:
    - --snapshot-consistency-mode=strict
    - --fsfreeze-wait-timeout-mins=0
    - --enable-snapshot-consistency=true
```

---

## Test YAML Templates

### Common Resources

#### StorageClass - Standard_LRS
```yaml
# sc-standard-lrs.yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: managed-csi-standard
provisioner: disk.csi.azure.com
parameters:
  skuName: Standard_LRS
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

#### StorageClass - Premium_LRS
```yaml
# sc-premium-lrs.yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: managed-csi-premium
provisioner: disk.csi.azure.com
parameters:
  skuName: Premium_LRS
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

#### StorageClass - PremiumV2_LRS
```yaml
# sc-premiumv2.yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: managed-csi-premiumv2
provisioner: disk.csi.azure.com
parameters:
  skuName: PremiumV2_LRS
  DiskIOPSReadWrite: "3000"
  DiskMBpsReadWrite: "125"
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

#### StorageClass - UltraSSD_LRS
```yaml
# sc-ultrassd.yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: managed-csi-ultrassd
provisioner: disk.csi.azure.com
parameters:
  skuName: UltraSSD_LRS
  DiskIOPSReadWrite: "10000"
  DiskMBpsReadWrite: "500"
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

#### StorageClass - Block Mode
```yaml
# sc-block-mode.yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: managed-csi-block
provisioner: disk.csi.azure.com
parameters:
  skuName: Premium_LRS
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
```

#### VolumeSnapshotClass
```yaml
# volumesnapshotclass.yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshotClass
metadata:
  name: csi-azuredisk-vsc
driver: disk.csi.azure.com
deletionPolicy: Delete
```

### Test Pods

#### Basic Writer Pod (Filesystem)
```yaml
# pod-writer.yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-writer-pod
spec:
  containers:
  - name: writer
    image: ubuntu:22.04
    command: ["/bin/bash", "-c", "apt update && apt install -y util-linux && sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /mnt/data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-pvc-standard
```

#### MySQL Database Pod
```yaml
# pod-mysql.yaml
apiVersion: v1
kind: Pod
metadata:
  name: mysql-pod
spec:
  containers:
  - name: mysql
    image: mysql:8.0
    env:
    - name: MYSQL_ROOT_PASSWORD
      value: "test123"
    ports:
    - containerPort: 3306
    volumeMounts:
    - name: mysql-data
      mountPath: /var/lib/mysql
  volumes:
  - name: mysql-data
    persistentVolumeClaim:
      claimName: mysql-pvc
```

#### Block Mode Pod
```yaml
# pod-block.yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-block-pod
spec:
  containers:
  - name: block-user
    image: ubuntu:22.04
    command: ["/bin/bash", "-c", "sleep 3600"]
    volumeDevices:
    - name: block-device
      devicePath: /dev/xvda
  volumes:
  - name: block-device
    persistentVolumeClaim:
      claimName: test-pvc-block
```

#### I/O Workload Pod (fio)
```yaml
# pod-fio-workload.yaml
apiVersion: v1
kind: Pod
metadata:
  name: fio-workload-pod
spec:
  containers:
  - name: fio
    image: nixery.dev/shell/fio
    command: ["/bin/sh", "-c"]
    args:
      - |
        fio --name=randwrite \
            --directory=/mnt/data \
            --size=5G \
            --bs=4k \
            --rw=randwrite \
            --ioengine=libaio \
            --iodepth=16 \
            --runtime=86400 \
            --time_based \
            --numjobs=1
    volumeMounts:
    - name: data
      mountPath: /mnt/data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-pvc-standard
```

### Test PVCs

#### Standard 10Gi PVC (Filesystem)
```yaml
# pvc-10gi-standard.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc-standard
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: managed-csi-standard
  resources:
    requests:
      storage: 10Gi
```

#### Premium 100Gi PVC
```yaml
# pvc-100gi-premium.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc-premium-100gi
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: managed-csi-premium
  resources:
    requests:
      storage: 100Gi
```

#### Block Mode PVC
```yaml
# pvc-block.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc-block
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: managed-csi-block
  volumeMode: Block
  resources:
    requests:
      storage: 10Gi
```

#### MySQL PVC
```yaml
# pvc-mysql.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: mysql-pvc
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: managed-csi-premium
  resources:
    requests:
      storage: 20Gi
```

### Helper Scripts

#### Multi-Terminal Monitoring Script
```bash
# monitor-freeze.sh
#!/bin/bash
# Usage: ./monitor-freeze.sh <va-name> <snapshot-name>

VA_NAME=$1
SNAPSHOT_NAME=$2

if [ -z "$VA_NAME" ] || [ -z "$SNAPSHOT_NAME" ]; then
  echo "Usage: $0 <volumeattachment-name> <snapshot-name>"
  exit 1
fi

echo "=== Starting monitoring for VA: $VA_NAME, Snapshot: $SNAPSHOT_NAME ==="

# Terminal multiplexer approach (tmux)
tmux new-session -d -s freeze-monitor

# Window 1: VolumeAttachment annotations
tmux send-keys -t freeze-monitor "watch -n 0.5 'kubectl get volumeattachment $VA_NAME -o jsonpath=\"{.metadata.annotations}\" | jq .'" C-m

# Window 2: Controller logs
tmux split-window -h -t freeze-monitor
tmux send-keys -t freeze-monitor "kubectl logs -f -n kube-system azuredisk-controller-0 --timestamps | grep -E 'freeze|$SNAPSHOT_NAME'" C-m

# Window 3: Snapshot status
tmux split-window -v -t freeze-monitor
tmux send-keys -t freeze-monitor "kubectl get volumesnapshot $SNAPSHOT_NAME -w" C-m

# Window 4: Events
tmux split-window -v -t freeze-monitor:0.0
tmux send-keys -t freeze-monitor "watch -n 2 'kubectl get events --sort-by=.lastTimestamp | tail -10'" C-m

tmux attach -t freeze-monitor
```

#### Data Integrity Test Script
```bash
# verify-integrity.sh
#!/bin/bash
# Create test file, calculate checksum, snapshot, restore, verify

POD_NAME=$1
PVC_NAME=$2
MOUNT_PATH=${3:-/mnt/data}

if [ -z "$POD_NAME" ] || [ -z "$PVC_NAME" ]; then
  echo "Usage: $0 <pod-name> <pvc-name> [mount-path]"
  exit 1
fi

echo "=== Creating test file with known pattern ==="
kubectl exec $POD_NAME -- dd if=/dev/urandom of=$MOUNT_PATH/testfile bs=1M count=100
kubectl exec $POD_NAME -- sync

echo "=== Calculating checksum ==="
CHECKSUM=$(kubectl exec $POD_NAME -- md5sum $MOUNT_PATH/testfile | awk '{print $1}')
echo "Original checksum: $CHECKSUM"
echo $CHECKSUM > /tmp/original-checksum.txt

echo "=== Create snapshot and restore to verify later ==="
echo "Run: kubectl apply -f volumesnapshot.yaml"
echo "Then restore and run: ./verify-restored.sh <restored-pod> $CHECKSUM"
```

#### Controller Configuration Helper
```bash
# configure-freeze-mode.sh
#!/bin/bash
# Switch between best-effort and strict modes

MODE=$1
TIMEOUT=${2:-2}

if [ "$MODE" != "best-effort" ] && [ "$MODE" != "strict" ]; then
  echo "Usage: $0 <best-effort|strict> [timeout-mins]"
  exit 1
fi

echo "=== Configuring freeze mode: $MODE, timeout: $TIMEOUT mins ==="

kubectl set env deployment/azuredisk-controller -n kube-system \
  SNAPSHOT_CONSISTENCY_MODE=$MODE \
  FSFREEZE_WAIT_TIMEOUT_MINS=$TIMEOUT

echo "Waiting for controller to restart..."
kubectl rollout status deployment/azuredisk-controller -n kube-system --timeout=120s

echo "=== Configuration complete ==="
kubectl get deployment azuredisk-controller -n kube-system -o yaml | grep -A5 env
```

---

## Test Categories

### Category 1: Basic Functionality Tests

#### Test 1.1: Basic Snapshot with Freeze - Single Volume
**Objective**: Verify basic freeze/unfreeze workflow works correctly

**Setup**:
- Deploy pod with single ext4 PVC (10Gi, Standard_LRS)
- Write data to volume and sync
- Configuration: best-effort mode

**Test Resources**:

```yaml
# pvc-10gi-standard.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc-standard
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: managed-csi-standard
  resources:
    requests:
      storage: 10Gi
```

```yaml
# pod-writer.yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-writer-pod
spec:
  containers:
  - name: writer
    image: ubuntu:22.04
    command: ["/bin/bash", "-c", "sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /mnt/data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-pvc-standard
```

```yaml
# volumesnapshot-1.yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: test-snapshot-1
spec:
  volumeSnapshotClassName: csi-azuredisk-vsc
  source:
    persistentVolumeClaimName: test-pvc-standard
```

**Detailed Steps**:

1. **Create StorageClass, VolumeSnapshotClass and PVC** (wait for binding)
   ```bash
   kubectl apply -f sc-standard-lrs.yaml
   kubectl apply -f volumesnapshotclass.yaml
   kubectl apply -f pvc-10gi-standard.yaml
   
   # Verify PVC is created (will be Pending until pod is created)
   kubectl get pvc test-pvc-standard
   # Expected: STATUS=Pending (WaitForFirstConsumer)
   ```

2. **Deploy pod and wait for PVC to bind**
   ```bash
   kubectl apply -f pod-writer.yaml
   
   # Wait for pod to be Running (takes 30-60 seconds)
   kubectl wait --for=condition=Ready pod/test-writer-pod --timeout=120s
   
   # Verify PVC is now Bound
   kubectl get pvc test-pvc-standard
   # Expected: STATUS=Bound, CAPACITY=10Gi
   ```

3. **Write test data with known checksum**
   ```bash
   # Create 1GB file with random data
   kubectl exec test-writer-pod -- dd if=/dev/urandom of=/mnt/data/testfile bs=1M count=1024
   
   # Force filesystem sync (critical for data consistency)
   kubectl exec test-writer-pod -- sync
   
   # Calculate and save checksum for later verification
   ORIGINAL_CHECKSUM=$(kubectl exec test-writer-pod -- md5sum /mnt/data/testfile | awk '{print $1}')
   echo "Original checksum: $ORIGINAL_CHECKSUM"
   ```

4. **Get VolumeAttachment name before snapshot** (for monitoring)
   ```bash
   # Find the VolumeAttachment for our PVC
   VA_NAME=$(kubectl get volumeattachment -o json | jq -r '.items[] | select(.spec.source.persistentVolumeName == "'$(kubectl get pvc test-pvc-standard -o jsonpath='{.spec.volumeName}')'") | .metadata.name')
   echo "VolumeAttachment: $VA_NAME"
   
   # Verify no freeze annotations exist yet
   kubectl get volumeattachment $VA_NAME -o jsonpath='{.metadata.annotations}'
   # Expected: No freeze-related annotations
   ```

5. **Create VolumeSnapshot and immediately start monitoring** (timing is critical)
   ```bash
   # Open a second terminal for real-time monitoring
   # Terminal 2:
   watch -n 0.5 "kubectl get volumeattachment $VA_NAME -o yaml | grep -A5 'annotations:'"
   
   # Terminal 1: Create snapshot
   kubectl apply -f volumesnapshot-1.yaml
   
   # Immediately watch snapshot status (don't wait)
   kubectl get volumesnapshot test-snapshot-1 -w
   ```

6. **Monitor freeze annotation lifecycle** (observe in Terminal 2)
   - **t+0s**: No annotations
   - **t+1-2s**: `disk.csi.azure.com/freeze-required: "2025-11-25T10:00:00Z"` appears
   - **t+2-5s**: `disk.csi.azure.com/freeze-state: "frozen"` appears
   - **t+10-30s**: Snapshot shows `readyToUse: true`
   - **t+30-35s**: Both freeze annotations removed from VolumeAttachment

7. **Verify snapshot completion**
   ```bash
   # Wait for readyToUse (timeout 5 minutes)
   kubectl wait --for=jsonpath='{.status.readyToUse}'=true volumesnapshot/test-snapshot-1 --timeout=300s
   
   # Verify VolumeSnapshot has freeze annotations preserved
   kubectl get volumesnapshot test-snapshot-1 -o jsonpath='{.metadata.annotations}' | jq .
   # Expected: Contains disk.csi.azure.com/freeze-required and disk.csi.azure.com/freeze-state
   ```

8. **Restore snapshot to new PVC and verify data**
   ```yaml
   # pvc-restored.yaml
   apiVersion: v1
   kind: PersistentVolumeClaim
   metadata:
     name: test-pvc-restored
   spec:
     accessModes:
       - ReadWriteOnce
     storageClassName: managed-csi-standard
     dataSource:
       name: test-snapshot-1
       kind: VolumeSnapshot
       apiGroup: snapshot.storage.k8s.io
     resources:
       requests:
         storage: 10Gi
   ```
   
   ```bash
   kubectl apply -f pvc-restored.yaml
   
   # Create pod to mount restored PVC
   kubectl apply -f - <<EOF
   apiVersion: v1
   kind: Pod
   metadata:
     name: test-reader-pod
   spec:
     containers:
     - name: reader
       image: ubuntu:22.04
       command: ["/bin/bash", "-c", "sleep 3600"]
       volumeMounts:
       - name: data
         mountPath: /mnt/data
     volumes:
     - name: data
       persistentVolumeClaim:
         claimName: test-pvc-restored
   EOF
   
   # Wait for pod ready
   kubectl wait --for=condition=Ready pod/test-reader-pod --timeout=120s
   
   # Verify data integrity (checksum must match)
   RESTORED_CHECKSUM=$(kubectl exec test-reader-pod -- md5sum /mnt/data/testfile | awk '{print $1}')
   echo "Restored checksum: $RESTORED_CHECKSUM"
   
   if [ "$ORIGINAL_CHECKSUM" == "$RESTORED_CHECKSUM" ]; then
     echo "✅ PASS: Data integrity verified"
   else
     echo "❌ FAIL: Checksum mismatch!"
   fi
   ```

**Expected Results**:
- **t+0s**: VolumeSnapshot created, controller initiates CreateSnapshot
- **t+1-2s**: VolumeAttachment gets `disk.csi.azure.com/freeze-required` annotation with RFC3339 timestamp
- **t+2-5s**: Node watcher detects annotation, executes fsfreeze, sets `disk.csi.azure.com/freeze-state: frozen`
- **t+5-30s**: Controller sees frozen state, proceeds with Azure snapshot creation
- **t+10-30s**: Snapshot completes, VolumeSnapshot shows `readyToUse: true`
- **t+30-35s**: Controller calls ReleaseFreeze, both annotations removed from VolumeAttachment
- VolumeSnapshot retains freeze annotations in metadata (never removed)
- Restored data checksum matches original (100% data integrity)
- No errors in controller or node logs

**Validation Commands**:
```bash
# Check controller logs for complete freeze workflow
kubectl logs -n kube-system azuredisk-controller-0 | grep -E "CheckOrRequestFreeze|ReleaseFreeze|snapshot.*test-snapshot-1"

# Check node logs for fsfreeze execution
NODE_NAME=$(kubectl get pod test-writer-pod -o jsonpath='{.spec.nodeName}')
NODE_POD=$(kubectl get pod -n kube-system -l app=csi-azuredisk-node --field-selector spec.nodeName=$NODE_NAME -o jsonpath='{.items[0].metadata.name}')
kubectl logs -n kube-system $NODE_POD -c azuredisk | grep -E "Freeze|Unfreeze|test-pvc-standard"

# Verify events (should show no warnings for successful freeze)
kubectl get events --field-selector involvedObject.name=test-snapshot-1 --sort-by='.lastTimestamp'

# Verify VolumeAttachment is clean (no freeze annotations remaining)
kubectl get volumeattachment $VA_NAME -o jsonpath='{.metadata.annotations}' | jq .

# Verify VolumeSnapshot has freeze annotations
kubectl get volumesnapshot test-snapshot-1 -o yaml | grep -A5 annotations
```

---

#### Test 1.2: Snapshot of Block Volume (No Freeze)
**Objective**: Verify block volumes skip freeze operation

**Setup**:
- Deploy pod with block mode PVC
- Configuration: best-effort mode

**Steps**:
1. Create block mode PVC
   ```yaml
   volumeMode: Block
   ```
2. Create pod using block device
3. Create VolumeSnapshot
4. Monitor annotations

**Expected Results**:
- No freeze-required annotation set
- Snapshot proceeds immediately without freeze
- Controller logs show "volume is block mode, skipping freeze"
- Snapshot completes successfully

---

#### Test 1.3: Snapshot of Unattached Volume
**Objective**: Verify snapshots work when volume is not attached to any node

**Setup**:
- Create PVC without pod
- Configuration: best-effort mode

**Steps**:
1. Create PVC
2. Wait for PVC to be bound
3. Create VolumeSnapshot immediately (no pod)
4. Monitor snapshot creation

**Expected Results**:
- No VolumeAttachment exists
- Freeze is skipped (no attachment found)
- Snapshot proceeds normally
- Controller logs show "no attached VolumeAttachment found, skipping freeze"

---

#### Test 1.4: PremiumV2_LRS SKU Optimization
**Objective**: Verify SKU-based freeze skip with LRU caching

**Setup**:
- Deploy pod with PremiumV2_LRS PVC
- Configuration: best-effort mode
- **Critical**: PremiumV2 snapshots typically take 5-10 minutes; freeze is skipped for this SKU

**Test Resources**:

```yaml
# pvc-premiumv2.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: test-pvc-premiumv2
spec:
  accessModes:
    - ReadWriteOnce
  storageClassName: managed-csi-premiumv2
  resources:
    requests:
      storage: 10Gi
```

```yaml
# pod-premiumv2.yaml
apiVersion: v1
kind: Pod
metadata:
  name: test-premiumv2-pod
spec:
  containers:
  - name: app
    image: ubuntu:22.04
    command: ["/bin/bash", "-c", "sleep 3600"]
    volumeMounts:
    - name: data
      mountPath: /mnt/data
  volumes:
  - name: data
    persistentVolumeClaim:
      claimName: test-pvc-premiumv2
```

```yaml
# volumesnapshot-premiumv2-1.yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: snapshot-premiumv2-1
spec:
  volumeSnapshotClassName: csi-azuredisk-vsc
  source:
    persistentVolumeClaimName: test-pvc-premiumv2
```

```yaml
# volumesnapshot-premiumv2-2.yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: snapshot-premiumv2-2
spec:
  volumeSnapshotClassName: csi-azuredisk-vsc
  source:
    persistentVolumeClaimName: test-pvc-premiumv2
```

**Detailed Steps**:

1. **Create PremiumV2 PVC and pod**
   ```bash
   kubectl apply -f sc-premiumv2.yaml
   kubectl apply -f pvc-premiumv2.yaml
   kubectl apply -f pod-premiumv2.yaml
   
   # Wait for pod ready
   kubectl wait --for=condition=Ready pod/test-premiumv2-pod --timeout=120s
   
   # Get PV name for verification
   PV_NAME=$(kubectl get pvc test-pvc-premiumv2 -o jsonpath='{.spec.volumeName}')
   echo "PV Name: $PV_NAME"
   
   # Verify PV has PremiumV2_LRS SKU
   kubectl get pv $PV_NAME -o jsonpath='{.spec.csi.volumeAttributes}' | jq .
   # Expected: skuName=PremiumV2_LRS or storageAccountType=PremiumV2_LRS
   ```

2. **Enable controller debug logging** (to see cache operations)
   ```bash
   # Set log level to 4 or 6 to see cache debug messages
   kubectl exec -n kube-system azuredisk-controller-0 -- /bin/sh -c "kill -USR1 1" || true
   
   # Or restart with increased verbosity
   kubectl set env deployment/azuredisk-controller -n kube-system KLOG_V=6
   ```

3. **Create first snapshot and monitor controller logs in real-time**
   ```bash
   # Terminal 1: Stream controller logs (capture cache operations)
   kubectl logs -f -n kube-system azuredisk-controller-0 | grep -E "skip.*freeze|cache|PremiumV2|snapshot-premiumv2-1"
   
   # Terminal 2: Create first snapshot
   kubectl apply -f volumesnapshot-premiumv2-1.yaml
   
   # Immediately check snapshot status
   kubectl get volumesnapshot snapshot-premiumv2-1 -w
   ```

4. **Verify first snapshot behavior** (observe in Terminal 1 logs)
   - Look for: `"CheckOrRequestFreeze: volume vol-xxx is block mode, skipping freeze"` OR
   - Look for: `"volume uses a SKU that should skip freeze, adding to cache"`
   - Snapshot should proceed immediately without setting freeze-required annotation

5. **Verify no freeze annotation was set**
   ```bash
   VA_NAME=$(kubectl get volumeattachment -o json | jq -r '.items[] | select(.spec.source.persistentVolumeName == "'$PV_NAME'") | .metadata.name')
   
   # Check for freeze-required annotation (should NOT exist)
   kubectl get volumeattachment $VA_NAME -o jsonpath='{.metadata.annotations.disk\.csi\.azure\.com/freeze-required}'
   # Expected: Empty (no output)
   ```

6. **Create second snapshot IMMEDIATELY** (do not wait for first to complete)
   ```bash
   # Terminal 2: Apply second snapshot right away (test cache hit)
   kubectl apply -f volumesnapshot-premiumv2-2.yaml
   
   # Both snapshots should now be processing
   kubectl get volumesnapshot | grep premiumv2
   # Expected: Both snapshots shown, both may be in progress
   ```

7. **Verify cache hit in controller logs** (Terminal 1)
   - Look for: `"found in skip freeze cache, skipping freeze"` for snapshot-premiumv2-2
   - **Critical**: Second snapshot should NOT trigger PV lookup (cache hit)
   - Should see log entry faster than first snapshot (no PV/VA lookup overhead)

8. **Wait for both snapshots to complete**
   ```bash
   # PremiumV2 snapshots take longer (5-10 minutes typically)
   kubectl wait --for=jsonpath='{.status.readyToUse}'=true volumesnapshot/snapshot-premiumv2-1 --timeout=600s
   kubectl wait --for=jsonpath='{.status.readyToUse}'=true volumesnapshot/snapshot-premiumv2-2 --timeout=600s
   ```

9. **Verify LRU cache persistence** (create third snapshot after delay)
   ```bash
   # Wait 2 minutes (cache should persist)
   sleep 120
   
   kubectl apply -f - <<EOF
   apiVersion: snapshot.storage.k8s.io/v1
   kind: VolumeSnapshot
   metadata:
     name: snapshot-premiumv2-3
   spec:
     volumeSnapshotClassName: csi-azuredisk-vsc
     source:
       persistentVolumeClaimName: test-pvc-premiumv2
   EOF
   
   # Should still hit cache (no PV lookup)
   kubectl logs -n kube-system azuredisk-controller-0 | grep "snapshot-premiumv2-3" | grep cache
   ```

**Expected Results**:

**First Snapshot (snapshot-premiumv2-1)**:
- Controller receives CreateSnapshot request
- Calls CheckOrRequestFreeze with volumeHandle
- Checks if volume is block mode: NO (it's filesystem mode)
- Checks if VolumeAttachment exists: YES
- Gets PersistentVolume to check mode and SKU
- Identifies `skuName: PremiumV2_LRS` in PV spec
- Calls `shouldSkipFreezeBySKU()` → returns TRUE
- Adds volumeHandle to LRU cache (cache size 100)
- Logs: `"volume vol-xxx uses a SKU that should skip freeze, adding to cache"`
- Returns empty freezeState, freezeComplete=true (skip freeze path)
- No freeze-required annotation set
- Snapshot proceeds immediately to Azure disk snapshot creation
- Completes in 5-10 minutes (PremiumV2 snapshot time)

**Second Snapshot (snapshot-premiumv2-2)** - Created immediately after first:
- Controller receives CreateSnapshot request
- Calls CheckOrRequestFreeze with SAME volumeHandle
- Checks LRU cache FIRST: **CACHE HIT**
- Logs: `"found in skip freeze cache, skipping freeze"`
- Returns empty freezeState, freezeComplete=true immediately
- **Critical**: No PV lookup, no VolumeAttachment check (cache optimized path)
- No freeze-required annotation set
- Snapshot proceeds immediately
- Completes in 5-10 minutes

**Cache Behavior**:
- Cache entry persists across multiple snapshots
- Cache size limit: 100 volumes (LRU eviction)
- Cache survives controller restarts (rebuilt on demand)
- Subsequent snapshots on same volume always hit cache

**Performance Comparison**:
- First snapshot: ~10-20ms overhead (PV lookup + cache add)
- Second snapshot: ~1-2ms overhead (cache hit only)
- 10x faster decision making on cache hit

**Validation Commands**:
```bash
# Verify neither snapshot set freeze annotation
kubectl get volumeattachment $VA_NAME -o yaml | grep freeze
# Expected: No matches

# Count cache hit log entries
kubectl logs -n kube-system azuredisk-controller-0 | grep "found in skip freeze cache" | wc -l
# Expected: At least 2 (for snapshot-2 and snapshot-3)

# Verify both snapshots completed successfully
kubectl get volumesnapshot | grep premiumv2 | grep true
# Expected: All 3 snapshots show readyToUse=true

# Check for any freeze-related events (should be none)
kubectl get events --field-selector reason=FreezeSuccess
kubectl get events --field-selector reason=FreezeFailed
# Expected: No events (freeze was skipped)
```

---

### Category 2: Timeout Handling Tests

#### Test 2.1: Timeout in Best-Effort Mode
**Objective**: Verify snapshot proceeds after timeout in best-effort mode

**Setup**:
- Deploy pod with PVC
- Node watcher artificially delayed or stopped
- Configuration: best-effort, timeout=2 mins
- **Critical**: Must prevent node watcher from setting freeze-state to trigger timeout

**Test Resources**:

```yaml
# volumesnapshot-timeout-test.yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: timeout-test-snapshot
spec:
  volumeSnapshotClassName: csi-azuredisk-vsc
  source:
    persistentVolumeClaimName: test-pvc-standard
```

**Detailed Steps**:

1. **Verify controller timeout configuration**
   ```bash
   # Check controller has timeout=2 mins for best-effort
   kubectl get pod -n kube-system azuredisk-controller-0 -o yaml | grep -E "fsfreeze-wait-timeout|snapshot-consistency"
   # Expected: --snapshot-consistency-mode=best-effort, --fsfreeze-wait-timeout-mins=2
   ```

2. **Identify and stop node watcher** (before creating snapshot)
   ```bash
   # Get node where PVC is attached
   POD_NODE=$(kubectl get pod test-writer-pod -o jsonpath='{.spec.nodeName}')
   echo "Pod on node: $POD_NODE"
   
   # Get node daemonset pod
   NODE_POD=$(kubectl get pod -n kube-system -l app=csi-azuredisk-node --field-selector spec.nodeName=$POD_NODE -o jsonpath='{.items[0].metadata.name}')
   echo "Node pod: $NODE_POD"
   
   # Delete node pod to stop watcher (simulates node watcher failure)
   kubectl delete pod -n kube-system $NODE_POD --grace-period=0 --force
   
   # Verify pod is terminating/gone
   kubectl get pod -n kube-system $NODE_POD
   # Expected: NotFound or Terminating
   
   # IMPORTANT: Prevent pod from restarting by scaling down daemonset temporarily
   kubectl patch daemonset -n kube-system csi-azuredisk-node -p '{"spec":{"template":{"spec":{"nodeSelector":{"non-existing-label":"true"}}}}}'
   
   # Verify no node pod is running on our node
   kubectl get pod -n kube-system -l app=csi-azuredisk-node --field-selector spec.nodeName=$POD_NODE
   # Expected: No resources found
   ```

3. **Setup monitoring**
   ```bash
   # Get VolumeAttachment
   PV_NAME=$(kubectl get pvc test-pvc-standard -o jsonpath='{.spec.volumeName}')
   VA_NAME=$(kubectl get volumeattachment -o json | jq -r '.items[] | select(.spec.source.persistentVolumeName == "'$PV_NAME'") | .metadata.name')
   
   # Terminal 1: Watch annotations (should never get freeze-state)
   watch -n 1 "kubectl get volumeattachment $VA_NAME -o jsonpath='{.metadata.annotations}' | jq ."
   
   # Terminal 2: Stream controller logs with timestamps
   kubectl logs -f -n kube-system azuredisk-controller-0 --timestamps | grep -E "timeout|CheckOrRequestFreeze.*timeout-test"
   
   # Terminal 3: Watch snapshot status
   kubectl get volumesnapshot timeout-test-snapshot -w
   ```

4. **Create snapshot and record start time**
   ```bash
   # Record exact start time
   START_TIME=$(date +%s)
   echo "Snapshot creation started at: $(date -d @$START_TIME)"
   
   # Create snapshot
   kubectl apply -f volumesnapshot-timeout-test.yaml
   
   # Immediately verify freeze-required annotation appears
   sleep 2
   kubectl get volumeattachment $VA_NAME -o jsonpath='{.metadata.annotations.disk\.csi\.azure\.com/freeze-required}'
   # Expected: Timestamp in RFC3339 format
   ```

5. **Monitor timeout progression** (observe retries over 2 minutes)
   ```bash
   # External-snapshotter retries CreateSnapshot every ~5-10 seconds
   # Controller CheckOrRequestFreeze called repeatedly:
   # - t+0s: Sets freeze-required, returns Unavailable
   # - t+5s: Retry, freeze-state still empty, returns Unavailable
   # - t+10s: Retry, freeze-state still empty, returns Unavailable
   # - t+15s: Retry, freeze-state still empty, returns Unavailable
   # ... continues for 2 minutes ...
   # - t+120s: Retry, detects timeout (time.Since(freezeTime) > 2 minutes)
   
   # Count retries in controller logs
   watch -n 5 "kubectl logs -n kube-system azuredisk-controller-0 | grep 'CheckOrRequestFreeze.*timeout-test' | wc -l"
   # Expected: Increases every ~5-10 seconds
   ```

6. **Wait for exact timeout** (2 minutes = 120 seconds)
   ```bash
   # Calculate elapsed time
   while true; do
     CURRENT_TIME=$(date +%s)
     ELAPSED=$((CURRENT_TIME - START_TIME))
     echo -ne "Elapsed: ${ELAPSED}s / 120s\r"
     
     if [ $ELAPSED -ge 120 ]; then
       echo "Timeout threshold reached!"
       break
     fi
     sleep 5
   done
   ```

7. **Verify timeout behavior** (immediately after 2 minutes)
   ```bash
   # Check controller logs for timeout message
   kubectl logs -n kube-system azuredisk-controller-0 | grep -A5 "timeout.*timeout-test"
   # Expected: "freeze timed out for snapshot timeout-test-snapshot"
   # Expected: Returns freeze-state="skipped", freezeComplete=true
   
   # Verify snapshot proceeds (should complete within 30-60 seconds after timeout)
   kubectl wait --for=jsonpath='{.status.readyToUse}'=true volumesnapshot/timeout-test-snapshot --timeout=120s
   
   # Record completion time
   END_TIME=$(date +%s)
   TOTAL_TIME=$((END_TIME - START_TIME))
   echo "Snapshot completed in: ${TOTAL_TIME}s (expected: 120-180s)"
   ```

8. **Verify final state**
   ```bash
   # VolumeAttachment should have no freeze annotations (cleaned up)
   kubectl get volumeattachment $VA_NAME -o jsonpath='{.metadata.annotations}' | jq .
   # Expected: No freeze-related annotations
   
   # VolumeSnapshot should show freeze-state: skipped
   kubectl get volumesnapshot timeout-test-snapshot -o jsonpath='{.metadata.annotations.disk\.csi\.azure\.com/freeze-state}'
   # Expected: "skipped"
   
   # Check for warning event about inconsistent snapshot
   kubectl get events --field-selector involvedObject.name=timeout-test-snapshot,reason=InconsistentSnapshot
   # Expected: Warning event present
   ```

9. **Restore node watcher**
   ```bash
   # Remove node selector patch to allow pod to restart
   kubectl patch daemonset -n kube-system csi-azuredisk-node --type=json -p='[{"op": "remove", "path": "/spec/template/spec/nodeSelector/non-existing-label"}]'
   
   # Wait for node pod to restart
   kubectl wait --for=condition=Ready pod -l app=csi-azuredisk-node --field-selector spec.nodeName=$POD_NODE -n kube-system --timeout=60s
   ```

**Expected Results**:

**Phase 1: Freeze requested (t=0 to t=5s)**
- Snapshot created at t=0s
- Controller CheckOrRequestFreeze called
- freeze-required annotation set with timestamp T0
- No freeze-state annotation (node watcher stopped)
- Controller returns Unavailable (retry needed)
- Snapshot status: readyToUse=false or undefined

**Phase 2: Retry loop (t=5s to t=120s)**
- External-snapshotter retries CreateSnapshot every 5-10 seconds
- Controller CheckOrRequestFreeze called repeatedly (~12-24 times)
- Each retry:
  - Checks freeze-required timestamp
  - Calculates elapsed time: time.Since(T0)
  - freeze-state still empty (node watcher down)
  - Elapsed < 120s → returns Unavailable
- Annotation state unchanged (freeze-required present, no freeze-state)
- Controller logs show: "waiting for freeze state for snapshot timeout-test-snapshot"

**Phase 3: Timeout detected (t=120s)**
- External-snapshotter retries CreateSnapshot
- Controller CheckOrRequestFreeze called
- Calculates elapsed time: time.Since(T0) = 120+ seconds
- Timeout logic:
  ```go
  if fo.hasTimedOut(freezeTime) {
    // best-effort mode: proceed with skip
    return freeze.FreezeStateSkipped, true, nil
  }
  ```
- Controller logs: "freeze timed out for snapshot timeout-test-snapshot"
- Returns: freezeState="skipped", freezeComplete=true, err=nil
- Controller proceeds with Azure snapshot creation

**Phase 4: Snapshot proceeds (t=120s to t=150s)**
- Controller creates Azure disk snapshot (no freeze)
- Azure snapshot completes in 20-30 seconds
- Controller calls ReleaseFreeze (removes tracking)
- freeze-required annotation removed from VolumeAttachment
- Snapshot status: readyToUse=true

**Phase 5: Warning event generated**
- Controller detects freezeState="skipped"
- Generates Kubernetes event:
  - Type: Warning
  - Reason: InconsistentSnapshot or similar
  - Message: "Snapshot created but filesystem freeze skipped. Data consistency is not guaranteed."

**Best-Effort Mode Behavior**:
- ✅ Timeout does NOT fail the snapshot
- ✅ Snapshot proceeds without freeze (potentially inconsistent)
- ✅ Warning event alerts user to potential inconsistency
- ✅ VolumeSnapshot annotations preserve freeze-state="skipped"
- ✅ System remains operational (no error state)

**Timing Validation**:
```bash
# Verify timeout was exactly 2 minutes (120 seconds ±5s tolerance)
FREEZE_TIME=$(kubectl get volumesnapshot timeout-test-snapshot -o jsonpath='{.metadata.annotations.disk\.csi\.azure\.com/freeze-required}')
READY_TIME=$(kubectl get volumesnapshot timeout-test-snapshot -o jsonpath='{.status.creationTime}')

# Calculate difference (should be ~120-125 seconds)
echo "Freeze requested: $FREEZE_TIME"
echo "Snapshot ready: $READY_TIME"

# Verify retry count (should be 12-24 retries in 2 minutes)
RETRY_COUNT=$(kubectl logs -n kube-system azuredisk-controller-0 | grep "CheckOrRequestFreeze.*timeout-test" | wc -l)
echo "Total CheckOrRequestFreeze calls: $RETRY_COUNT"
# Expected: 12-24 (one every 5-10 seconds)
```

**Validation Commands**:
```bash
# Verify timeout log message
kubectl logs -n kube-system azuredisk-controller-0 | grep -i "timeout.*timeout-test"
# Expected: "freeze timed out for snapshot timeout-test-snapshot"

# Verify freeze-state is skipped
kubectl get volumesnapshot timeout-test-snapshot -o jsonpath='{.metadata.annotations.disk\.csi\.azure\.com/freeze-state}'
# Expected: "skipped"

# Verify warning event
kubectl get events --field-selector involvedObject.name=timeout-test-snapshot --sort-by='.lastTimestamp' | tail -5
# Expected: Warning event about inconsistent snapshot

# Verify snapshot is ready despite timeout
kubectl get volumesnapshot timeout-test-snapshot -o jsonpath='{.status.readyToUse}'
# Expected: true

# Verify VolumeAttachment is clean
kubectl get volumeattachment $VA_NAME -o jsonpath='{.metadata.annotations.disk\.csi\.azure\.com/freeze-required}'
# Expected: Empty (annotation cleaned up)
```

---

#### Test 2.2: Timeout in Strict Mode - Snapshot Fails
**Objective**: Verify snapshot fails after timeout in strict mode

**Setup**:
- Deploy pod with PVC
- Node watcher stopped
- Configuration: strict mode, timeout=5 mins

**Steps**:
1. Create pod and PVC
2. Stop node watcher
3. Create VolumeSnapshot
4. Wait for 5+ minutes
5. Verify snapshot failure

**Expected Results**:
- freeze-required annotation set at t=0
- Controller retries for 5 minutes
- After timeout, controller returns `FailedPrecondition` error
- Snapshot status shows error: "snapshot consistency check failed: freeze operation timed out in strict mode"
- freeze-required annotation is removed
- Snapshot remains in non-ready state
- Error event generated

**Validation**:
```bash
kubectl get volumesnapshot -o jsonpath='{.status.error}'
kubectl get events --field-selector reason=SnapshotCreateError
```

---

#### Test 2.3: Timeout in Strict Mode with Zero Timeout (Indefinite Wait)
**Objective**: Verify indefinite wait behavior in strict mode

**Setup**:
- Configuration: strict mode, timeout=0 (indefinite)

**Steps**:
1. Create pod and PVC
2. Stop node watcher initially
3. Create VolumeSnapshot
4. Wait 10 minutes (beyond normal timeout)
5. Restart node watcher
6. Verify snapshot completes

**Expected Results**:
- Controller keeps retrying indefinitely (no timeout)
- Controller logs show retries continuing beyond 10 minutes
- Once node watcher starts and freeze completes, snapshot succeeds
- No timeout error occurs

---

### Category 3: Concurrent Snapshot Tests

#### Test 3.1: Multiple Snapshots of Same Volume
**Objective**: Verify freeze annotation is maintained across concurrent snapshots

**Setup**:
- Single pod with PVC
- Configuration: best-effort mode
- **Critical**: Second snapshot must be created WHILE first is still in progress (before freeze completes)

**Test Resources**:

```yaml
# volumesnapshot-concurrent-1.yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: concurrent-snap-1
spec:
  volumeSnapshotClassName: csi-azuredisk-vsc
  source:
    persistentVolumeClaimName: test-pvc-standard
```

```yaml
# volumesnapshot-concurrent-2.yaml
apiVersion: snapshot.storage.k8s.io/v1
kind: VolumeSnapshot
metadata:
  name: concurrent-snap-2
spec:
  volumeSnapshotClassName: csi-azuredisk-vsc
  source:
    persistentVolumeClaimName: test-pvc-standard
```

**Detailed Steps**:

1. **Prepare monitoring (before creating snapshots)**
   ```bash
   # Get PV and VA names
   PV_NAME=$(kubectl get pvc test-pvc-standard -o jsonpath='{.spec.volumeName}')
   VA_NAME=$(kubectl get volumeattachment -o json | jq -r '.items[] | select(.spec.source.persistentVolumeName == "'$PV_NAME'") | .metadata.name')
   
   echo "PV: $PV_NAME"
   echo "VA: $VA_NAME"
   
   # Terminal 1: Watch VolumeAttachment annotations in real-time
   watch -n 0.5 "kubectl get volumeattachment $VA_NAME -o jsonpath='{.metadata.annotations}' | jq ."
   
   # Terminal 2: Stream controller logs
   kubectl logs -f -n kube-system azuredisk-controller-0 | grep -E "concurrent-snap|tracking|ReleaseFreeze"
   ```

2. **Create first snapshot**
   ```bash
   # Terminal 3: Create snapshot-1
   kubectl apply -f volumesnapshot-concurrent-1.yaml
   
   # Record creation time
   echo "Snapshot 1 created at: $(date +%H:%M:%S)"
   ```

3. **Create second snapshot IMMEDIATELY** (timing critical - within 1-2 seconds)
   ```bash
   # DO NOT WAIT - apply immediately (within 1-2 seconds of first)
   kubectl apply -f volumesnapshot-concurrent-2.yaml
   
   echo "Snapshot 2 created at: $(date +%H:%M:%S)"
   
   # Verify both snapshots exist
   kubectl get volumesnapshot | grep concurrent
   # Expected: Both shown, both in progress (readyToUse=false or empty)
   ```

4. **Monitor freeze annotation sequence** (observe in Terminal 1)
   - **t+0-1s**: No annotations
   - **t+1-2s**: `freeze-required: "2025-11-25T10:05:00Z"` appears (snapshot-1 sets it)
   - **t+2-3s**: snapshot-2 CheckOrRequestFreeze sees EXISTING freeze-required annotation
   - **t+3-5s**: `freeze-state: frozen` appears (node watcher sets it ONCE)
   - **t+5-30s**: snapshot-1 Azure disk snapshot completes
   - **t+30-32s**: snapshot-1 calls ReleaseFreeze → controller checks for other snapshots
   - **t+32s**: freeze-required annotation REMAINS (snapshot-2 still tracked)
   - **t+35-60s**: snapshot-2 Azure disk snapshot completes
   - **t+60-62s**: snapshot-2 calls ReleaseFreeze → no other snapshots found
   - **t+62s**: freeze-required annotation REMOVED

5. **Verify controller tracking logic** (observe in Terminal 2)
   ```bash
   # Should see these log patterns:
   # "Track this snapshot" - for both snapshots
   # "ReleaseFreeze: other snapshots exist for volume" - after snapshot-1
   # "ReleaseFreeze: released freeze for snapshot concurrent-snap-2" - after snapshot-2
   ```

6. **Check snapshot timing and state**
   ```bash
   # Get creation and ready timestamps
   kubectl get volumesnapshot concurrent-snap-1 -o jsonpath='{.metadata.creationTimestamp}'
   kubectl get volumesnapshot concurrent-snap-2 -o jsonpath='{.metadata.creationTimestamp}'
   
   # Both should have very similar timestamps (within 1-2 seconds)
   
   # Wait for both to complete
   kubectl wait --for=jsonpath='{.status.readyToUse}'=true volumesnapshot/concurrent-snap-1 --timeout=300s
   kubectl wait --for=jsonpath='{.status.readyToUse}'=true volumesnapshot/concurrent-snap-2 --timeout=300s
   ```

7. **Verify final state**
   ```bash
   # VolumeAttachment should have NO freeze annotations (both removed)
   kubectl get volumeattachment $VA_NAME -o jsonpath='{.metadata.annotations}' | jq .
   # Expected: No freeze-required or freeze-state
   
   # Both VolumeSnapshots should preserve freeze annotations
   kubectl get volumesnapshot concurrent-snap-1 -o jsonpath='{.metadata.annotations}' | jq .
   kubectl get volumesnapshot concurrent-snap-2 -o jsonpath='{.metadata.annotations}' | jq .
   # Expected: Both have freeze-required and freeze-state annotations
   ```

8. **Verify internal tracking was correct**
   ```bash
   # Check controller logs for tracking operations
   kubectl logs -n kube-system azuredisk-controller-0 | grep -A2 "addTracking.*concurrent"
   # Expected: 2 entries (one for each snapshot)
   
   kubectl logs -n kube-system azuredisk-controller-0 | grep "removeTracking.*concurrent"
   # Expected: 2 entries (one for each snapshot cleanup)
   
   kubectl logs -n kube-system azuredisk-controller-0 | grep "other snapshots exist.*$(echo $PV_NAME | cut -d'-' -f3-)"
   # Expected: 1 entry (snapshot-1 detected snapshot-2 ongoing)
   ```

**Expected Results**:

**Phase 1: Both snapshots created (t+0 to t+2s)**
- concurrent-snap-1 created at t=0s
- concurrent-snap-2 created at t=1s (must be within 2 seconds)
- Controller processes CreateSnapshot for snap-1
- Controller processes CreateSnapshot for snap-2

**Phase 2: First snapshot sets freeze-required (t+1 to t+3s)**
- snap-1: CheckOrRequestFreeze called
  - No freeze-required annotation exists
  - Sets `freeze-required: <timestamp-1>`
  - Adds snap-1 to internal tracking map: `{name: "concurrent-snap-1", volumeHandle: "vol-xxx", timestamp: t1}`
  - Returns freezeComplete=false
  - Controller returns Unavailable (retry needed)

**Phase 3: Second snapshot sees existing freeze-required (t+2 to t+4s)**
- snap-2: CheckOrRequestFreeze called
  - Sees EXISTING `freeze-required: <timestamp-1>` annotation
  - Adds snap-2 to internal tracking map: `{name: "concurrent-snap-2", volumeHandle: "vol-xxx", timestamp: t2}`
  - Uses SAME timestamp from annotation (no update needed)
  - Returns freezeComplete=false
  - Controller returns Unavailable (retry needed)

**Phase 4: Node watcher freezes filesystem (t+3 to t+5s)**
- Node watcher detects freeze-required annotation
- Executes fsfreeze on mount path
- Sets `freeze-state: frozen` annotation ONCE (affects both snapshots)
- Both snapshots waiting for this state

**Phase 5: Controller retries detect frozen state (t+5 to t+10s)**
- External-snapshotter retries both CreateSnapshot calls
- snap-1 CheckOrRequestFreeze: sees freeze-state=frozen → returns freezeComplete=true
- snap-2 CheckOrRequestFreeze: sees freeze-state=frozen → returns freezeComplete=true
- Both proceed with Azure snapshot creation

**Phase 6: Snapshot-1 completes first (t+10 to t+30s)**
- snap-1 Azure snapshot finishes
- snap-1 calls ReleaseFreeze
- Controller checks: `hasOtherSnapshotsForVolume("vol-xxx", "concurrent-snap-1")`
- Finds snap-2 in tracking map (still ongoing)
- Logs: "other snapshots exist for volume vol-xxx, not removing freeze-required annotation yet"
- Removes snap-1 from tracking map
- freeze-required annotation REMAINS on VolumeAttachment

**Phase 7: Snapshot-2 completes (t+30 to t+60s)**
- snap-2 Azure snapshot finishes
- snap-2 calls ReleaseFreeze
- Controller checks: `hasOtherSnapshotsForVolume("vol-xxx", "concurrent-snap-2")`
- No other snapshots in tracking map
- Removes freeze-required annotation from VolumeAttachment
- Removes snap-2 from tracking map
- Logs: "released freeze for snapshot concurrent-snap-2"

**Critical Behaviors Validated**:
- ✅ Per-volume locking prevents race conditions during annotation updates
- ✅ Tracking map correctly maintains multiple snapshots per volume
- ✅ freeze-required annotation shared across concurrent snapshots (set once, removed after last)
- ✅ freeze-state set once by node, used by all concurrent snapshots
- ✅ No duplicate freeze operations on node
- ✅ Both snapshots complete successfully with correct freeze annotations

**Validation Commands**:
```bash
# Verify freeze-required was set exactly once
kubectl logs -n kube-system azuredisk-controller-0 | grep "setFreezeRequiredAnnotation.*$(echo $PV_NAME | cut -d'-' -f3-)" | wc -l
# Expected: 1

# Verify freeze-state was set exactly once by node watcher
NODE_POD=$(kubectl get pod -n kube-system -l app=csi-azuredisk-node --field-selector spec.nodeName=$(kubectl get pod test-writer-pod -o jsonpath='{.spec.nodeName}') -o jsonpath='{.items[0].metadata.name}')
kubectl logs -n kube-system $NODE_POD -c azuredisk | grep "updateFreezeState.*frozen.*$VA_NAME" | wc -l
# Expected: 1

# Verify tracking was correctly managed
kubectl logs -n kube-system azuredisk-controller-0 | grep "addTracking" | grep concurrent | wc -l
# Expected: 2 (one per snapshot)

kubectl logs -n kube-system azuredisk-controller-0 | grep "removeTracking" | grep concurrent | wc -l
# Expected: 2 (one per snapshot cleanup)

# Verify "other snapshots exist" was logged once (by snap-1)
kubectl logs -n kube-system azuredisk-controller-0 | grep "other snapshots exist" | grep concurrent
# Expected: 1 line mentioning concurrent-snap-1

# Verify both snapshots have freeze annotations
kubectl get volumesnapshot concurrent-snap-1 -o yaml | grep -A2 "disk.csi.azure.com/freeze"
kubectl get volumesnapshot concurrent-snap-2 -o yaml | grep -A2 "disk.csi.azure.com/freeze"
# Expected: Both have freeze-required and freeze-state annotations

# Verify VolumeAttachment is clean
kubectl get volumeattachment $VA_NAME -o jsonpath='{.metadata.annotations.disk\.csi\.azure\.com/freeze-required}'
# Expected: Empty (annotation removed after snap-2 completed)
```

---

#### Test 3.2: Concurrent Snapshots of Different Volumes
**Objective**: Verify independent freeze operations on different volumes

**Setup**:
- Two pods with different PVCs
- Configuration: best-effort mode

**Steps**:
1. Create pod-1 with pvc-1, pod-2 with pvc-2
2. Create snapshot-1 and snapshot-2 simultaneously
   ```bash
   kubectl apply -f snapshot-1.yaml -f snapshot-2.yaml
   ```
3. Monitor both VolumeAttachments
4. Verify independent freeze operations

**Expected Results**:
- Each VolumeAttachment gets its own freeze-required annotation
- Node watcher processes both independently
- Both filesystems frozen independently
- Both snapshots complete successfully
- No interference between operations
- Per-volume locking ensures no race conditions

---

#### Test 3.3: Race Condition - Snapshot During Volume Detach
**Objective**: Test race between snapshot creation and volume detachment

**Setup**:
- Pod with PVC
- Configuration: best-effort mode

**Steps**:
1. Create pod and PVC
2. Create VolumeSnapshot
3. Immediately delete pod (triggers volume detach)
   ```bash
   kubectl apply -f snapshot.yaml && kubectl delete pod pod-name --force --grace-period=0
   ```
4. Monitor both operations
5. Verify graceful handling

**Expected Results**:
- If freeze-required set before detach: node watcher handles freeze before detach completes
- If VolumeAttachment deleted during freeze: controller detects 404, proceeds with snapshot
- Snapshot either completes with freeze-state:skipped or freeze-state:frozen
- No hanging annotations or stuck states
- Controller logs show "VolumeAttachment not found, skipping freeze"

---

### Category 4: Failure and Error Handling Tests

#### Test 4.1: Filesystem Already Frozen by User
**Objective**: Verify detection of user-initiated freeze

**Setup**:
- Pod with PVC
- Manually freeze filesystem before snapshot
- Configuration: best-effort mode

**Steps**:
1. Create pod and PVC
2. Exec into pod and manually freeze
   ```bash
   kubectl exec -it pod-name -- bash
   # Inside pod
   findmnt /mnt/data  # Get mount device
   fsfreeze -f /mnt/data
   ```
3. Create VolumeSnapshot (while filesystem is frozen)
4. Monitor behavior

**Expected Results**:
- Node watcher detects EBUSY (filesystem already frozen)
- freeze-state set to `user-frozen`
- Snapshot proceeds (assuming user knows what they're doing)
- Warning event generated: "UserFrozenDetected"
- VolumeSnapshot annotations show `freeze-state: user-frozen`
- Snapshot completes successfully
- Node watcher attempts unfreeze after snapshot

**Validation**:
```bash
kubectl get events --field-selector reason=UserFrozenDetected
kubectl describe volumesnapshot | grep freeze-state
```

---

#### Test 4.2: Freeze Operation Fails on Node
**Objective**: Test handling of fsfreeze command failure

**Setup**:
- Pod with PVC on filesystem that doesn't support freeze (e.g., tmpfs)
- Configuration: strict mode

**Steps**:
1. Create pod with special filesystem or simulate freeze failure
2. Create VolumeSnapshot
3. Verify error handling

**Expected Results**:
- Node watcher executes fsfreeze, gets error
- freeze-state set to `failed`
- In strict mode: snapshot fails with FailedPrecondition
- In best-effort mode: snapshot proceeds with warning
- Appropriate event generated
- Annotations cleaned up properly

---

#### Test 4.3: Unfreeze Failure
**Objective**: Test critical unfreeze failure handling

**Setup**:
- Pod with PVC
- Simulate unfreeze failure (e.g., mount point disappeared)
- Configuration: best-effort mode

**Steps**:
1. Create pod and complete snapshot with freeze
2. Before unfreeze, simulate mount point issue
   ```bash
   # On node, unmount the volume (simulates detach race)
   umount /var/lib/kubelet/pods/.../volumes/...
   ```
3. Verify unfreeze error handling

**Expected Results**:
- Node watcher attempts unfreeze
- Unfreeze fails with error
- Critical warning event generated: "UnfreezeFailed"
- freeze-state annotation NOT removed (manual intervention needed)
- Alert/monitoring should trigger on this event
- Volume may remain frozen (requires manual recovery)

**Recovery**:
```bash
# Manual recovery on node
ssh to-node
findmnt | grep frozen-device
fsfreeze -u /path/to/mount
```

---

#### Test 4.4: Controller Restart During Snapshot
**Objective**: Test resiliency when controller restarts mid-operation

**Setup**:
- Pod with PVC
- Configuration: best-effort mode

**Steps**:
1. Create VolumeSnapshot
2. Wait for freeze-required annotation to appear
3. Restart controller pod
   ```bash
   kubectl delete pod -n kube-system azuredisk-controller-0
   ```
4. Wait for controller to restart
5. Verify snapshot continues

**Expected Results**:
- External-snapshotter detects controller restart
- Retries CreateSnapshot call
- Controller CheckOrRequestFreeze finds existing tracking (from annotations)
- If freeze already completed: snapshot proceeds immediately
- If freeze pending: controller continues waiting
- Snapshot completes successfully
- No duplicate freeze operations

**Validation**:
```bash
# Check for duplicate freeze-required annotations
kubectl get volumeattachment -o yaml | grep freeze-required

# Verify snapshot completes
kubectl get volumesnapshot -w
```

---

#### Test 4.5: Node Watcher Restart During Freeze
**Objective**: Test node watcher restart resilience

**Setup**:
- Pod with PVC
- Configuration: best-effort mode

**Steps**:
1. Create VolumeSnapshot (freeze-required set)
2. Immediately restart node daemonset pod
   ```bash
   kubectl delete pod -n kube-system azuredisk-node-xxxxx
   ```
3. Wait for node pod to restart
4. Verify freeze operation resumes

**Expected Results**:
- Node watcher restarts
- Bootstrap reconciliation detects pending freeze-required annotation
- Re-evaluates all VolumeAttachments on node
- Executes freeze if not already frozen (checked via state file)
- Updates freeze-state annotation
- Snapshot completes successfully
- State file on node persists across restarts

---

### Category 5: Node Failure and Network Partition Tests

#### Test 5.1: Node Network Partition
**Objective**: Test behavior when node loses network connectivity

**Setup**:
- Multi-node AKS cluster
- Pod with PVC on node-1
- Configuration: best-effort mode, 2 min timeout

**Steps**:
1. Create pod and VolumeSnapshot
2. Simulate network partition
   ```bash
   # Using Azure NSG or on-node iptables
   iptables -A OUTPUT -d <k8s-api-ip> -j DROP
   iptables -A INPUT -s <k8s-api-ip> -j DROP
   ```
3. Wait for timeout period
4. Restore network
5. Verify recovery

**Expected Results**:
- Node watcher cannot update freeze-state annotation (API unreachable)
- Controller times out after 2 minutes
- Snapshot proceeds with freeze-state:skipped (best-effort mode)
- When network restored, node watcher reconciles and unfreezes
- No frozen filesystem left behind

---

#### Test 5.2: Node Crash During Freeze
**Objective**: Test recovery from node failure while filesystem frozen

**Setup**:
- Pod with PVC
- Configuration: best-effort mode

**Steps**:
1. Create VolumeSnapshot
2. Wait for filesystem to freeze
3. Simulate node crash
   ```bash
   # Via Azure Portal: restart VM
   # Or via SSH: echo b > /proc/sysrq-trigger
   ```
4. Wait for node to restart
5. Verify filesystem state recovery

**Expected Results**:
- Node crashes with filesystem frozen
- On node reboot, frozen filesystem may cause mount issues
- Kernel automatically unfreezes on reboot
- Node watcher starts, reconciles state
- State file cleaned up
- Volume can be mounted normally
- Snapshot may be in failed/skipped state (depending on timing)

---

#### Test 5.3: Complete Node Failure - Volume Migrates
**Objective**: Test volume migration after node failure

**Setup**:
- Multi-node cluster
- Pod with PVC on node-1
- Configuration: best-effort mode

**Steps**:
1. Create pod and VolumeSnapshot (freeze in progress)
2. Simulate permanent node failure
   ```bash
   # Stop node VM or drain node
   kubectl drain node-1 --force --delete-emptydir-data
   ```
3. Pod rescheduled to node-2
4. Verify snapshot handling

**Expected Results**:
- Original snapshot may fail (node-1 unavailable)
- VolumeAttachment for node-1 deleted
- New VolumeAttachment created for node-2
- Controller detects old VolumeAttachment 404, proceeds with snapshot
- New snapshot can be created successfully on node-2
- No frozen filesystem remnants

---

### Category 6: Stress and Performance Tests

#### Test 6.1: High Volume Snapshot Concurrency
**Objective**: Test system under high concurrent snapshot load

**Setup**:
- 50 pods with individual PVCs
- Configuration: best-effort mode

**Steps**:
1. Deploy 50 pods with PVCs across cluster
2. Create 50 VolumeSnapshots simultaneously
   ```bash
   for i in {1..50}; do
     kubectl apply -f snapshot-${i}.yaml &
   done
   ```
3. Monitor resource usage and success rate
4. Verify all complete successfully

**Expected Results**:
- All snapshots complete successfully
- Controller CPU/memory remain within limits
- Node watcher handles concurrent freeze operations
- Per-volume locking prevents race conditions
- LRU cache handles volume tracking efficiently
- Average snapshot completion time < 5 minutes

**Performance Metrics**:
```bash
# Controller metrics
kubectl top pod -n kube-system azuredisk-controller-0

# Snapshot success rate
kubectl get volumesnapshot | grep -c "true"

# Check for any errors
kubectl logs -n kube-system azuredisk-controller-0 | grep -i error
```

---

#### Test 6.2: Rapid Snapshot Creation/Deletion
**Objective**: Test stability under rapid snapshot churn

**Setup**:
- Single pod with PVC
- Configuration: best-effort mode

**Steps**:
1. Create snapshot
2. Immediately delete it
3. Create new snapshot
4. Repeat 100 times rapidly
   ```bash
   for i in {1..100}; do
     kubectl apply -f snapshot.yaml
     sleep 5
     kubectl delete volumesnapshot test-snapshot
     sleep 2
   done
   ```
5. Monitor for resource leaks

**Expected Results**:
- All snapshots either complete or are deleted cleanly
- No stuck freeze annotations
- No leaked tracking state in controller
- No stuck frozen filesystems on nodes
- Memory usage remains stable (no leaks)
- State files cleaned up properly

---

#### Test 6.3: Long-Running Workload with Periodic Snapshots
**Objective**: Test stability over extended period with active I/O

**Setup**:
- Pod running continuous I/O workload (fio or sysbench)
- Create snapshots every 5 minutes for 24 hours
- Configuration: best-effort mode

**Steps**:
1. Deploy I/O workload
   ```bash
   # fio continuous random write
   fio --name=randwrite --size=5G --bs=4k --rw=randwrite \
       --ioengine=libaio --iodepth=16 --runtime=86400 \
       --time_based --directory=/mnt/data
   ```
2. Create snapshots in loop
   ```bash
   for i in {1..288}; do  # 24 hours * 12 per hour
     kubectl apply -f snapshot-${i}.yaml
     sleep 300  # 5 minutes
   done
   ```
3. Monitor application impact and success rate

**Expected Results**:
- All 288 snapshots complete successfully
- Application I/O pauses briefly during freeze (< 5 seconds)
- No application crashes or errors
- No performance degradation over time
- No resource leaks
- All snapshots verified for data consistency

---

### Category 7: Edge Cases and Corner Cases

#### Test 7.1: Empty Filesystem Snapshot
**Objective**: Verify freeze/unfreeze on filesystem with no data

**Steps**:
1. Create PVC and pod
2. Don't write any data
3. Create VolumeSnapshot immediately
4. Verify freeze still executes

**Expected Results**:
- Freeze executes successfully even on empty filesystem
- Snapshot completes normally
- No errors about empty volume

---

#### Test 7.2: Filesystem at 99% Capacity
**Objective**: Test freeze on near-full filesystem

**Steps**:
1. Create 10Gi PVC
2. Fill to 99% capacity
   ```bash
   kubectl exec pod -- dd if=/dev/zero of=/mnt/data/bigfile bs=1M count=9900
   ```
3. Create VolumeSnapshot
4. Verify freeze succeeds

**Expected Results**:
- Freeze succeeds despite low free space
- State file can be created (in /tmp on node, not on volume)
- Snapshot completes successfully

---

#### Test 7.3: Very Large Volume (32TB)
**Objective**: Test freeze on very large Azure disk

**Steps**:
1. Create 32TB Ultra Disk PVC
2. Create pod and write data
3. Create VolumeSnapshot
4. Verify freeze time

**Expected Results**:
- Freeze completes within timeout (size doesn't affect freeze time significantly)
- Snapshot creation may take longer (Azure limitation)
- Freeze/unfreeze operations remain fast (< 5 seconds)

---

#### Test 7.4: Snapshot with Active Database (MySQL)
**Objective**: Test real-world database snapshot scenario

**Setup**:
- MySQL pod with PVC for data directory
- Active TPC-C workload running

**Steps**:
1. Deploy MySQL with persistent volume
2. Run active transaction workload
3. Create VolumeSnapshot during transactions
4. Restore snapshot to new PVC
5. Verify database consistency

**Expected Results**:
- MySQL transactions pause during freeze
- No MySQL errors (InnoDB handles freeze gracefully)
- Restored database starts successfully
- No database corruption
- Recovered database has consistent state (crash-consistent)

---

#### Test 7.5: Multiple Mounts of Same Volume (ReadWriteMany simulation)
**Objective**: Verify freeze behavior with volume mounted multiple times

**Setup**:
- Volume with multiple bind mounts in same pod

**Steps**:
1. Create PVC
2. Mount same volume at multiple paths in pod
   ```yaml
   volumeMounts:
     - name: vol
       mountPath: /mnt/data1
     - name: vol
       mountPath: /mnt/data2
   ```
3. Create VolumeSnapshot
4. Verify freeze affects all mount points

**Expected Results**:
- Node watcher identifies one mount point
- Freeze applied to device (affects all mounts)
- Snapshot completes successfully

---

### Category 8: Upgrade and Compatibility Tests

#### Test 8.1: Upgrade from Version Without Freeze
**Objective**: Test upgrade path from older CSI driver version

**Steps**:
1. Deploy AKS with older azuredisk-csi-driver (no freeze feature)
2. Create PVCs and snapshots (no freeze)
3. Upgrade to new version with freeze feature
4. Create new snapshots
5. Verify backward compatibility

**Expected Results**:
- Upgrade completes successfully
- Old snapshots remain functional
- New snapshots use freeze feature
- No disruption to existing volumes
- Feature flag controls freeze behavior

---

#### Test 8.2: Downgrade Scenario
**Objective**: Test downgrade with active freeze annotations

**Steps**:
1. Create snapshots with freeze (annotations present)
2. Downgrade CSI driver to version without freeze
3. Verify graceful degradation

**Expected Results**:
- Downgrade succeeds
- Existing freeze annotations ignored by old controller
- New snapshots work without freeze
- No stuck states or errors

---

#### Test 8.3: Mixed Version Cluster (Controller vs Node)
**Objective**: Test version mismatch scenarios

**Setup**:
- Controller with freeze feature
- Some nodes with old daemonset (no watcher)

**Steps**:
1. Upgrade controller only
2. Create snapshots on both old and new nodes
3. Verify behavior differences

**Expected Results**:
- Snapshots on new nodes: freeze executes
- Snapshots on old nodes: timeout after 2 mins, proceeds with skipped state
- No errors or crashes
- System remains functional

---

### Category 9: Observability and Debugging Tests

#### Test 9.1: Event Generation Validation
**Objective**: Verify all expected events are generated

**Steps**:
1. Create snapshot with successful freeze
2. Create snapshot with timeout
3. Create snapshot with freeze failure (strict mode)
4. Verify all events

**Expected Events**:
```bash
kubectl get events --field-selector involvedObject.kind=VolumeSnapshot

# Expected event reasons:
# - FreezeSuccess (from node watcher)
# - FreezeFailed (if strict mode + failure)
# - FreezeSkipped (if timeout in best-effort)
# - FreezeTimeout (if timeout)
# - UserFrozenDetected (if user pre-froze)
# - UnfreezeSuccess (from node watcher)
# - UnfreezeFailed (if unfreeze fails - critical)
# - InconsistentSnapshot (from controller if skipped)
```

---

#### Test 9.2: Log Verbosity Levels
**Objective**: Verify logging at different verbosity levels

**Steps**:
1. Set controller log level to 2, 4, 6
   ```bash
   kubectl set env deployment/azuredisk-controller -n kube-system LOG_LEVEL=6
   ```
2. Create snapshots
3. Verify appropriate log detail at each level

**Expected Log Levels**:
- Level 2: Major operations (freeze requested, completed)
- Level 4: Detailed flow (retries, state checks)
- Level 6: Debug info (cache hits, lock operations)

---

#### Test 9.3: Metrics and Monitoring
**Objective**: Verify freeze metrics are exported

**Steps**:
1. Configure Prometheus to scrape CSI driver
2. Create snapshots
3. Verify metrics

**Expected Metrics**:
```
# Controller metrics
csi_snapshot_operations_total{operation="freeze_requested"}
csi_snapshot_operations_total{operation="freeze_completed"}
csi_snapshot_operations_total{operation="freeze_skipped"}
csi_snapshot_operations_total{operation="freeze_failed"}
csi_snapshot_duration_seconds{operation="freeze_wait"}

# Node metrics  
azuredisk_freeze_operations_total{state="frozen"}
azuredisk_freeze_operations_total{state="failed"}
azuredisk_freeze_duration_seconds
```

---

#### Test 9.4: State File Inspection
**Objective**: Verify state file management on nodes

**Steps**:
1. Create snapshot with freeze
2. SSH to node during freeze
3. Inspect state file
   ```bash
   cat /tmp/freeze-state-<volume-uuid>.json
   ```
4. Verify cleanup after unfreeze

**Expected State File Format**:
```json
{
  "command": "freeze",
  "state": "frozen",
  "timestamp": "2025-11-25T10:30:00Z"
}
```

**Expected Behavior**:
- State file created during freeze
- Updated after freeze completes
- Deleted after successful unfreeze
- Persists across node watcher restarts

---

### Category 10: Security and Permissions Tests

#### Test 10.1: RBAC Validation
**Objective**: Verify RBAC permissions are correct

**Steps**:
1. Review controller ServiceAccount permissions
2. Verify VolumeAttachment read/write access
3. Verify VolumeSnapshot read/write access
4. Test with restricted permissions

**Required Permissions**:
```yaml
# Controller RBAC
- apiGroups: ["storage.k8s.io"]
  resources: ["volumeattachments"]
  verbs: ["get", "list", "watch", "update", "patch"]
- apiGroups: ["snapshot.storage.k8s.io"]
  resources: ["volumesnapshots"]
  verbs: ["get", "list", "watch", "update", "patch"]
```

---

#### Test 10.2: Node Privilege Requirements
**Objective**: Verify node daemonset has necessary privileges for fsfreeze

**Steps**:
1. Verify node pods run as privileged
2. Test fsfreeze access without privileges
3. Verify hostPath mounts for /tmp and volume paths

**Expected**:
- Daemonset runs as privileged (required for fsfreeze ioctl)
- Has access to /dev for freeze operations
- State directory writable

---

## Test Execution Guide

### Test Priority Levels

**P0 (Critical - Must Pass)**:
- 1.1, 1.2, 1.3, 2.1, 2.2, 3.1, 4.1, 4.4, 4.5

**P1 (High Priority)**:
- 1.4, 2.3, 3.2, 3.3, 4.2, 4.3, 5.1, 5.2, 6.1

**P2 (Medium Priority)**:
- 5.3, 6.2, 6.3, 7.1-7.5, 8.1-8.3, 9.1-9.4

**P3 (Low Priority - Nice to Have)**:
- 10.1, 10.2

### Automation Strategy

**Automated Tests**:
- All Category 1 (Basic Functionality)
- Category 3 (Concurrent Snapshots)
- Category 6 (Stress Tests)
- Category 7 (Edge Cases)

**Manual Tests**:
- Category 5 (Node Failures - requires VM manipulation)
- Category 8 (Upgrades - requires version management)
- Category 10 (Security - requires RBAC changes)

**Semi-Automated**:
- Category 2 (Timeout - requires controlled delays)
- Category 4 (Failures - requires fault injection)
- Category 9 (Observability - requires validation tools)

### Test Reporting Template

For each test, document:
```yaml
Test ID: <category>.<number>
Status: [PASS|FAIL|BLOCKED|SKIP]
Configuration:
  - consistency-mode: [best-effort|strict]
  - timeout: <minutes>
  - cluster-version: <aks-version>
  - driver-version: <git-commit>
Execution Time: <duration>
Results:
  - Expected: <description>
  - Actual: <description>
  - Logs: <log-snippets>
  - Screenshots: <if-applicable>
Issues Found:
  - <issue-description>
  - <github-issue-link>
```

### Success Criteria

- **All P0 tests**: 100% pass rate
- **All P1 tests**: 95% pass rate
- **All P2 tests**: 90% pass rate
- **No critical bugs**: No data corruption, deadlocks, or resource leaks
- **Performance**: Snapshot completion < 5 minutes for 10Gi volumes
- **Resource usage**: Controller memory < 500Mi, CPU < 1 core under load

---

## Test Scenarios Summary

| Category | Test Count | Automation | Priority |
|----------|------------|------------|----------|
| Basic Functionality | 4 | Full | P0-P1 |
| Timeout Handling | 3 | Partial | P0 |
| Concurrent Snapshots | 3 | Full | P0-P1 |
| Failure Handling | 5 | Partial | P0-P1 |
| Node Failures | 3 | Manual | P1-P2 |
| Stress/Performance | 3 | Full | P1-P2 |
| Edge Cases | 5 | Full | P2 |
| Upgrade/Compatibility | 3 | Manual | P2 |
| Observability | 4 | Semi | P2 |
| Security | 2 | Manual | P3 |
| **Total** | **35** | **Mixed** | **P0-P3** |

---

## Appendix: Useful Commands

### Monitoring Commands
```bash
# Watch VolumeAttachment annotations
watch -n 1 "kubectl get volumeattachment -o yaml | grep -A10 annotations"

# Stream controller logs with freeze filter
kubectl logs -f -n kube-system azuredisk-controller-0 | grep -i freeze

# Stream node logs
kubectl logs -f -n kube-system azuredisk-node-xxxxx | grep -i freeze

# Watch snapshot status
kubectl get volumesnapshot -w

# Check events continuously
watch "kubectl get events --sort-by=.lastTimestamp | tail -20"

# Check frozen filesystems on node
ssh to-node "cat /proc/mounts | grep frozen"
```

### Debugging Commands
```bash
# Get all freeze-related annotations
kubectl get volumeattachment -o json | jq '.items[].metadata.annotations | select(. != null) | with_entries(select(.key | contains("freeze")))'

# Check state files on node
kubectl exec -n kube-system azuredisk-node-xxxxx -- ls -la /tmp/freeze-state-*.json

# Force unfreeze (emergency)
kubectl exec -n kube-system azuredisk-node-xxxxx -- fsfreeze -u /var/lib/kubelet/pods/...

# Delete stuck freeze annotation
kubectl annotate volumeattachment <name> disk.csi.azure.com/freeze-required-
```

### Cleanup Commands
```bash
# Clean up test resources
kubectl delete volumesnapshot --all
kubectl delete pvc --all
kubectl delete pod --all

# Reset controller
kubectl rollout restart deployment/azuredisk-controller -n kube-system

# Reset node daemonset
kubectl rollout restart daemonset/azuredisk-node -n kube-system
```

---

## Notes and Considerations

1. **Azure Limits**: Be aware of Azure snapshot rate limits (300 snapshots per disk per hour)
2. **Timeout Values**: Adjust timeout values based on cluster load and network latency
3. **Log Retention**: Ensure adequate log retention for debugging failed tests
4. **Resource Quotas**: Tests may hit quota limits in subscription - monitor and adjust
5. **Cost**: Large-scale stress tests will incur Azure costs - budget accordingly
6. **Network**: Tests involving network partition should be done in isolated test clusters
7. **Backup**: Before destructive tests (node failures), ensure test data can be recreated
