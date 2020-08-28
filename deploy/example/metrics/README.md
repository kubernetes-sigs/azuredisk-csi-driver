# Get Prometheus metrics from CSI driver

1. Create `csi-azuredisk-controller` service with targetPort `29604`
```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/metrics/csi-azuredisk-controller-svc.yaml
```

2. Get `EXTERNAL-IP` of service `csi-azuredisk-controller`
```console
$ kubectl get svc csi-azuredisk-controller -n kube-system
NAME                       TYPE        CLUSTER-IP   EXTERNAL-IP   PORT(S)     AGE
csi-azuredisk-controller   ClusterIP   10.0.184.0   20.39.21.132  29604/TCP   47m
```

3. Run following command to get cloudprovider_azure disk operation metrics
```console
ip=`kubectl get svc csi-azuredisk-controller -n kube-system | grep disk | awk '{print $4}'`
curl http://$ip:29604/metrics | grep cloudprovider_azure | grep disk | grep -e sum -e count
```

 - following output shows `attach_disk` costs 12s and `detach_disk` costs 18s
```
cloudprovider_azure_api_request_duration_seconds_sum{request="disks_create_or_update",resource_group="xxx",source="",subscription_id="xxx"} 24.803934967
cloudprovider_azure_api_request_duration_seconds_count{request="disks_create_or_update",resource_group="xxx",source="",subscription_id="xxx"} 10
cloudprovider_azure_api_request_duration_seconds_sum{request="disks_get",resource_group="xxx",source="",subscription_id="xxx"} 1.4042459639999998
cloudprovider_azure_api_request_duration_seconds_count{request="disks_get",resource_group="xxx",source="",subscription_id="xxx"} 40
cloudprovider_azure_api_request_duration_seconds_sum{request="vmssvm_update",resource_group="xxx",source="attach_disk",subscription_id="xxx"} 124.44062281500001
cloudprovider_azure_api_request_duration_seconds_count{request="vmssvm_update",resource_group="xxx",source="attach_disk",subscription_id="xxx"} 10
cloudprovider_azure_api_request_duration_seconds_sum{request="vmssvm_update",resource_group="xxx",source="detach_disk",subscription_id="xxx"} 183.693748363
cloudprovider_azure_api_request_duration_seconds_count{request="vmssvm_update",resource_group="xxx",source="detach_disk",subscription_id="xxx"} 10
```
