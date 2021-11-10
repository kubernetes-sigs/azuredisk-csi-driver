# Get Prometheus metrics from CSI driver

## Metrics description

The metrics emitted by the Azure Disk CSI Driver fall broadly into two categories: CSI and Azure Cloud operation latency metrics. The CSI metrics record the latency of the CSI calls made to the driver, e,g, `ControllerPublishVolume`. The Azure Cloud metrics record the latency of Azure Cloud operations perform as part driver operation, e.g. `attach_disk`. The individual operation metrics are recorded in two different [histogram](https://prometheus.io/docs/concepts/metric_types/#histogram) metrics using the labels `request` and/or `source` to differentiate among the operations. The table below describes the values of the individual operation metrics.

| Name | `request` | `source` | Description |
|------|-----------|----------|-------------|
| `cloudprovider_azure_op_duration_seconds` | | | Records the CSI operation metrics |
| | `azuredisk_csi_driver_controller_create_volume` | `disk.csi.azure.com` | `ControllerCreateVolume` latency |
| | `azuredisk_csi_driver_controller_delete_volume` | `disk.csi.azure.com` | `ControllerDeleteVolume` latency |
| | `azuredisk_csi_driver_controller_expand_volume` | `disk.csi.azure.com` | `ControllerExpandVolume` latency |
| | `azuredisk_csi_driver_controller_create_snapshot` | `disk.csi.azure.com` | `ControllerCreateSnapshot` latency |
| | `azuredisk_csi_driver_controller_delete_snapshot` | `disk.csi.azure.com` | `ControllerDeleteSnapshot` latency |
| | `azuredisk_csi_driver_controller_publish_volume` | `disk.csi.azure.com` | `ControllerPublishVolume` latency |
| | `azuredisk_csi_driver_controller_unpublish_volume` | `disk.csi.azure.com` | `ControllerUnpublishVolume` latency |
| `cloudprovider_azure_api_request_duration_seconds` | | | Records the Azure Cloud operation metrics |
| | `disks_create_or_update` | | `create_disk` latency |
| | `disks_delete` | | `delete_disk` latency |
| | `disks_update` | | `resize_disk` latency |
| | `snapshot_create_or_update` | | `create_snapshot` latency |
| | `snapshot_delete` | | `delete_snapshot` latency |
| | `vmssvm_updateasync` (VM Scale Set) or `vm_updateasync` (VM Availability Set) | `attach_disk` | Initiation latency of an asynchronous `attach_disk` operation |
| | `vmss_wait_for_update_result` (VM Scale Set) or `vm_wait_for_update_result` (VM Availability Set)  | `attach_disk` | Completion wait latency of an asynchronous `attach_disk` operation |
| | `vmssvm_update` (VM Scale Set) or `vm_update` (VM Availability Set)  | `detach_disk` | `detach_disk` latency |

## Prometheus integration

To enable Prometheus to discover the Azure Disk CSI Driver and scrape its metrics, run the following commands. This assumes you have already installed [Prometheus Operator](https://github.com/prometheus-operator/prometheus-operator) or [kube-prometheus-stack](https://github.com/prometheus-community/helm-charts/tree/main/charts/kube-prometheus-stack) to your cluster.

```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/metrics/csi-azuredisk-controller-svc.yaml
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/metrics/csi-azuredisk-controller-monitor.yaml
```

The first creates a `Service` object that exposes the default Azure Disk CSI Driver's controller metric port through a `ClusterIP`. The second creates a `ServiceMonitor` object that allows Prometheus to discover the controller's metrics server and begin to scrape metrics from it.

## Direct scraping

To scrape metrics directly from the Azure Disk CSI Driver controller, first get the leader node for one of the CSI sidecars depending on the metrics you wish to observe:

| Sidecar | Lease Lock Name | CSI Metrics | Azure Cloud Metrics |
|---------|-----------------|-------------|---------------------|
| `external-provisioner` | `disk-csi-azure-com` | `ControllerCreateVolume` & `ControllerDeleteVolume` | `create_disk` & `delete_disk` |
| `external-attacher` | `external-attacher-leader-disk-csi-azure-com` | `ControllerPublishVolume` & `ControllerUnpublishVolume` | `attach_disk` & `detach_disk` |
| `external-resizer` | `external-resizer-disk-csi-azure-com` | `ControllerExpandVolume` | `resize_disk` |
| `external-snapshotter` | `external-snapshotter-leader-disk-csi-azure-com` | `ControllerCreateSnapshot` & `ControllerDeleteSnapshot` | `create_snapshot` & `delete_snapshot` |

The leader sidecar communicates with the Azure Disk CSI Driver on the same node to manage Azure Managed Disks. Once you determine which set of metrics you want to scrape, use the leader election lease name to find the current leader and set up a local port forwarder to the Azure Disk CSI Driver's metrics port. For example, run the following commands to set up port forwarding to the Azure Disk CSI Driver metrics server in the pod with the `external-attacher` leader:

```console
LEADER_LEASE=external-attacher-leader-disk-csi-azure-com
LEADER_NODE=$(kubectl get lease -n kube-system "${LEADER_LEASE}" --output jsonpath='{.spec.holderIdentity}')
LEADER_POD=$(kubectl get pod -n kube-system -l app=csi-azuredisk-controller --output jsonpath="{.items[?(@.spec.nodeName==\"${LEADER_NODE}\")].metadata.name}")
kubectl port-forward -n kube-system "pods/${LEADER_POD}" 29604:29604 &
PORTFORWARDER=$!
```

This example gets the name of the node holding the CSI `external-attacher` leader election lease lock, finds the name of the Azure Disk CSI Driver pod containing the leader and sets up port forwarding to the metrics server on localhost port 29604. After waiting for the port forwarder to initialize and begin serving requests, you can then get the metrics.

The format of the data returned by the Azure Disk CSI Driver metrics server is described in [Prometheus Exposition Formats](https://prometheus.io/docs/instrumenting/exposition_formats/).

### Example: Get `ControllerPublishVolume` and `ControllerUnpublishVolume` metrics

Once you have set up port forwarding, you can use the following command to get the `ControllerPublishVolume` and `ControlUnpublishVolume` metrics.

```console
curl http://localhost:29604/metrics | grep -E "cloudprovider_azure_op_duration_seconds_(sum|count)" | grep -E "request=\"azuredisk_csi_driver_controller_(un)?publish_volume\""
```

We can calculate the average latency of each operation by dividing its `*_sum` by `*_count` metric. The `*_sum` value is in seconds. The following output shows an average `ControllerPublishVolume` latency of 9.5s and `ControllerUnpublishVolume` of 13.7s.

```
cloudprovider_azure_op_duration_seconds_sum{request="azuredisk_csi_driver_controller_publish_volume",resource_group="edreed-k8s-failover-rg",source="disk.csi.azure.com",subscription_id="d64ddb0c-7399-4529-a2b6-037b33265372"} 181.10639633399998
cloudprovider_azure_op_duration_seconds_count{request="azuredisk_csi_driver_controller_publish_volume",resource_group="edreed-k8s-failover-rg",source="disk.csi.azure.com",subscription_id="d64ddb0c-7399-4529-a2b6-037b33265372"} 19
cloudprovider_azure_op_duration_seconds_sum{request="azuredisk_csi_driver_controller_unpublish_volume",resource_group="edreed-k8s-failover-rg",source="disk.csi.azure.com",subscription_id="d64ddb0c-7399-4529-a2b6-037b33265372"} 232.39884008299998
cloudprovider_azure_op_duration_seconds_count{request="azuredisk_csi_driver_controller_unpublish_volume",resource_group="edreed-k8s-failover-rg",source="disk.csi.azure.com",subscription_id="d64ddb0c-7399-4529-a2b6-037b33265372"} 17
```

### Example: Get `attach_disk` and `detach_disk` metrics

```console
curl http://localhost:29604/metrics | grep -E "cloudprovider_azure_api_request_duration_seconds_(sum|count)" | grep -E "source=\"(attach_disk|detach_disk)\""
```

To calculate the average `attach_disk` latency, we must sum the initiation and completion wait latencies. These are 3.6363432579999997 and 176.880914393, respectively, in the output below. The sum is 180.5172576509999997. We then divide by the count from either `attach_disk` metric since the two latencies represent one total operation. We see 14 `attach_disk` operations, so the average latency is 12.9s. The average `detach_disk` latency is 13.6s.

```console
cloudprovider_azure_api_request_duration_seconds_sum{request="vmss_wait_for_update_result",resource_group="edreed-k8s-failover-rg",source="attach_disk",subscription_id="d64ddb0c-7399-4529-a2b6-037b33265372"} 176.880914393
cloudprovider_azure_api_request_duration_seconds_count{request="vmss_wait_for_update_result",resource_group="edreed-k8s-failover-rg",source="attach_disk",subscription_id="d64ddb0c-7399-4529-a2b6-037b33265372"} 14
cloudprovider_azure_api_request_duration_seconds_sum{request="vmssvm_update",resource_group="edreed-k8s-failover-rg",source="detach_disk",subscription_id="d64ddb0c-7399-4529-a2b6-037b33265372"} 231.78358075499997
cloudprovider_azure_api_request_duration_seconds_count{request="vmssvm_update",resource_group="edreed-k8s-failover-rg",source="detach_disk",subscription_id="d64ddb0c-7399-4529-a2b6-037b33265372"} 17
cloudprovider_azure_api_request_duration_seconds_sum{request="vmssvm_updateasync",resource_group="edreed-k8s-failover-rg",source="attach_disk",subscription_id="d64ddb0c-7399-4529-a2b6-037b33265372"} 3.6363432579999997
cloudprovider_azure_api_request_duration_seconds_count{request="vmssvm_updateasync",resource_group="edreed-k8s-failover-rg",source="attach_disk",subscription_id="d64ddb0c-7399-4529-a2b6-037b33265372"} 14
```

Stop port forwarding with the following command:

```console
kill -9 $PORTFORWARDER
```
