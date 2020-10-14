# Set up Azure Disk attach/detach stress test

## Set up an AKS cluster
> follow guide [Create an AKS cluster](https://docs.microsoft.com/en-us/azure/aks/)
 - set up an AKS cluster with CLI commands
```console
RESOURCE_GROUP_NAME=
CLUSTER_NAME=
LOCATION=eastus2euap

az group create -n $RESOURCE_GROUP_NAME -l $LOCATION
az aks create -g $RESOURCE_GROUP_NAME -n $CLUSTER_NAME --node-count 2 --generate-ssh-keys --kubernetes-version 1.19.0 --node-vm-size Standard_DS2_v2
# after around 4min, try get aks nodes
az aks get-credentials -g $RESOURCE_GROUP_NAME -n $CLUSTER_NAME --overwrite-existing
kubectl get nodes
```

## Install CSI driver and metrics service
 - [install CSI driver](./install-azuredisk-csi-driver.md)
```console
curl -skSL https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/install-driver.sh | bash -s master --
# scale csi-azuredisk-controller down to 1 replica: make it easier to get prometheus metrics
kubectl scale deployment csi-azuredisk-controller --replicas=1 -n kube-system
# check csi driver pods deployment
kubectl get po -n kube-system -o wide | grep csi
```

 - install prometheus metrics service
```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/metrics/csi-azuredisk-controller-svc.yaml
```

## Setup a statefulset with disk PV mount
```
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/storageclass-azuredisk-csi.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/statefulset.yaml
```

## Check prometheus metrics
```console
ip=`kubectl get svc csi-azuredisk-controller -n kube-system | grep disk | awk '{print $4}'`
curl http://$ip:29604/metrics | grep cloudprovider_azure | grep ch_disk | grep -e sum -e count
```

## Set cron job to attach/detach disk periodially
```console
cp /root/.kube/config /root/.kube/config-$CLUSTER_NAME

export KUBECONFIG=/root/.kube/config-$CLUSTER_NAME
/root/go/bin/kubectl scale sts statefulset-azuredisk --replicas=10
sleep 200
/root/go/bin/kubectl scale sts statefulset-azuredisk --replicas=0
```
