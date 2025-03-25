## Install CSI driver development version on a Kubernetes cluster
If you have already installed Helm, you can also use it to install this driver. Please check [Installation with Helm](../charts/README.md).

### Install by kubectl
 - Option#1. remote install
```console
curl -skSL https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/v1.28.12/deploy/install-driver.sh | bash -s v1.28.12 snapshot --
```

 - Option#2. local install
```console
git clone https://github.com/kubernetes-sigs/azuredisk-csi-driver.git
cd azuredisk-csi-driver
git checkout v1.28.12
./deploy/install-driver.sh v1.28.12 local
```

### Check pods status:

```console
kubectl -n kube-system get pod -o wide --watch -l app=csi-azuredisk-controller
kubectl -n kube-system get pod -o wide --watch -l app=csi-azuredisk-node
```

example output:

```console
NAME                                            READY   STATUS    RESTARTS   AGE     IP             NODE
csi-azuredisk-controller-56bfddd689-dh5tk       6/6     Running   0          35s     10.240.0.19    k8s-agentpool-22533604-0
csi-azuredisk-controller-56bfddd689-7s8yg       6/6     Running   0          35s     10.240.0.29    k8s-agentpool-22533604-1
csi-snapshot-controller-84db6dbbb-stzwr         6/6     Running   0          41s     10.240.0.17    k8s-agentpool-22533604-0
csi-azuredisk-node-cvgbs                        3/3     Running   0          7m4s    10.240.0.35    k8s-agentpool-22533604-1
csi-azuredisk-node-dr4s4                        3/3     Running   0          7m4s    10.240.0.4     k8s-agentpool-22533604-0
```

### clean up CSI driver
 - Option#1. remote uninstall
```console
curl -skSL https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/v1.28.12/deploy/uninstall-driver.sh | bash -s v1.28.12 --
```

 - Option#2. local uninstall
```console
git clone https://github.com/kubernetes-sigs/azuredisk-csi-driver.git
cd azuredisk-csi-driver
git checkout v1.28.12
./deploy/install-driver.sh v1.28.12 local
```
