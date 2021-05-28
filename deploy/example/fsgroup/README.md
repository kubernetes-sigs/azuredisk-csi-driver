# fsGroup Support

[fsGroupPolicy](https://kubernetes-csi.github.io/docs/support-fsgroup.html) feature is supported from k8s 1.20, this feature won't be enabled by default.

 - Enable fsGroupPolicy support in [driver helm installation](../../../charts)

add `--set feature.enableFSGroupPolicy=true` in helm installation command.

 - How to enable this feature on a cluster with CSI driver already installed

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
