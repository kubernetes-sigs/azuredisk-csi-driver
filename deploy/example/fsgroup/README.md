# fsGroup Support

[fsGroupPolicy](https://kubernetes-csi.github.io/docs/support-fsgroup.html) feature is Beta from Kubernetes 1.20, and disabled by default, follow below steps to enable this feature.

### Option#1: Enable fsGroupPolicy support in [driver helm installation](../../../charts)

add `--set feature.enableFSGroupPolicy=true` in helm installation command.

### Option#2: Enable fsGroupPolicy support on a cluster with CSI driver already installed

```console
kubectl delete CSIDriver disk.csi.azure.com
cat <<EOF | kubectl create -f -
apiVersion: storage.k8s.io/v1
kind: CSIDriver
metadata:
  name: disk.csi.azure.com
spec:
  attachRequired: true
  podInfoOnMount: false
  fsGroupPolicy: File
EOF
```
