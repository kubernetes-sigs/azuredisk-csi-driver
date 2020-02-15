# Get Prometheus metrics from CSI driver

1. Create `csi-azuredisk-controller` service with targetPort `29604`
```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/metrics/csi-azuredisk-controller-svc.yaml
```

2. Get ClusterIP of service `csi-azuredisk-controller`
```console
$ kubectl get svc csi-azuredisk-controller -n kube-system
NAME                       TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)     AGE
csi-azuredisk-controller   ClusterIP   10.0.184.0   <none>        29604/TCP   47m
```

3. Run following command to get cloudprovider_azure metrics
```console
curl http://{CLUSTER-IP}:29604/metrics | grep cloudprovider_azure
```
