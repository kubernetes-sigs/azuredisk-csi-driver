# Prometheus Metrics for CSI Disk Driver

## Metrics description

The Azure Disk CSI Driver exposes comprehensive Prometheus metrics for monitoring driver operations, performance, and health. The metrics fall into several categories:

### CSI Metrics

| Metric Name | Type | Labels | Description |
|-------------|------|--------|-------------|
| `azuredisk_csi_driver_operations_total` | Counter | `operation`, `success` | Total number of CSI operations (both controller and node) |
| `azuredisk_csi_driver_operation_duration_seconds` | Histogram | `operation`, `success` | Duration of CSI operations in seconds (basic metric without detailed labels) |
| `azuredisk_csi_driver_operation_duration_seconds_labeled` | Histogram | `operation`, `success`, `disk_sku`, `caching_mode`, `zone` | Duration of CSI operations in seconds with detailed labels for analysis |

**Operation Types (Controller):**
- `controller_create_volume` - Create a new disk volume
- `controller_delete_volume` - Delete a disk volume
- `controller_modify_volume` - Modify volume properties
- `controller_publish_volume` - Attach disk to node (VM)
- `controller_unpublish_volume` - Detach disk from node (VM)
- `controller_expand_volume` - Expand volume capacity
- `controller_create_snapshot` - Create a snapshot
- `controller_delete_snapshot` - Delete a snapshot

**Operation Types (Node):**
- `node_stage_volume` - Stage volume to global mount path
- `node_unstage_volume` - Unstage volume from global mount path
- `node_publish_volume` - Mount volume to pod path
- `node_unpublish_volume` - Unmount volume from pod path
- `node_expand_volume` - Expand filesystem on node

**Label Values:**
- `success`: `true` or `false`
- `disk_sku`: Disk SKU type (e.g., `Premium_LRS`, `StandardSSD_LRS`, `Standard_LRS`)
- `caching_mode`: Disk caching mode (e.g., `None`, `ReadOnly`, `ReadWrite`)
- `zone`: Availability zone (e.g., `1`, `2`, `3`, or empty for non-zonal)

### Azure Cloud API Metrics

The driver also exposes Azure Cloud provider API metrics:

| Name | `request` | `source` | Description |
|------|-----------|----------|-------------|
| `cloudprovider_azure_api_request_duration_seconds` | | | Records the Azure Cloud API operation metrics |
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

To scrape metrics directly from the Azure Disk CSI Driver controller, set up port forwarding to access the metrics endpoint. For example:

```console
CONTROLLER_POD=$(kubectl get pod -n kube-system -l app=csi-azuredisk-controller --output jsonpath='{.items[0].metadata.name}')
kubectl port-forward -n kube-system "pods/${CONTROLLER_POD}" 29604:29604 &
PORTFORWARDER=$!
```

This sets up port forwarding to the Azure Disk CSI Driver metrics server on localhost port 29604. After waiting for the port forwarder to initialize and begin serving requests, you can then get the metrics.

The format of the data returned by the Azure Disk CSI Driver metrics server is described in [Prometheus Exposition Formats](https://prometheus.io/docs/instrumenting/exposition_formats/).

### Example: Query CSI Operations

Get total operations count by type and result:

```console
curl http://localhost:29604/metrics | grep "azuredisk_csi_driver_operations_total"
```

Sample output:
```
azuredisk_csi_driver_operations_total{operation="controller_create_volume",success="true"} 15
azuredisk_csi_driver_operations_total{operation="controller_delete_volume",success="true"} 8
azuredisk_csi_driver_operations_total{operation="controller_publish_volume",success="true"} 20
azuredisk_csi_driver_operations_total{operation="controller_publish_volume",success="false"} 2
azuredisk_csi_driver_operations_total{operation="node_stage_volume",success="true"} 18
azuredisk_csi_driver_operations_total{operation="node_publish_volume",success="true"} 25
```


### Example: Query Operation Duration with Labels

Get detailed operation metrics including disk_sku, caching_mode, and zone:

```console
curl http://localhost:29604/metrics | grep "azuredisk_csi_driver_operation_duration_seconds_labeled"
```

Sample output showing histogram buckets:
```
azuredisk_csi_driver_operation_duration_seconds_labeled_bucket{caching_mode="ReadOnly",disk_sku="Premium_LRS",operation="controller_create_volume",success="true",zone="1",le="1"} 5
azuredisk_csi_driver_operation_duration_seconds_labeled_bucket{caching_mode="ReadOnly",disk_sku="Premium_LRS",operation="controller_create_volume",success="true",zone="1",le="5"} 10
azuredisk_csi_driver_operation_duration_seconds_labeled_sum{caching_mode="ReadOnly",disk_sku="Premium_LRS",operation="controller_create_volume",success="true",zone="1"} 12.5
azuredisk_csi_driver_operation_duration_seconds_labeled_count{caching_mode="ReadOnly",disk_sku="Premium_LRS",operation="controller_create_volume",success="true",zone="1"} 10
```

### Example: Monitor Operation Duration

Query basic operation duration metrics:

```console
curl http://localhost:29604/metrics | grep "azuredisk_csi_driver_operation_duration_seconds" | grep -v "labeled"
```

Sample output showing histogram buckets for operations:
```
azuredisk_csi_driver_operation_duration_seconds_bucket{operation="controller_create_volume",success="true",le="1"} 8
azuredisk_csi_driver_operation_duration_seconds_bucket{operation="controller_create_volume",success="true",le="5"} 15
azuredisk_csi_driver_operation_duration_seconds_bucket{operation="controller_create_volume",success="true",le="10"} 15
azuredisk_csi_driver_operation_duration_seconds_sum{operation="controller_create_volume",success="true"} 45.234
azuredisk_csi_driver_operation_duration_seconds_count{operation="controller_create_volume",success="true"} 15
```

### Example: Get Azure API Metrics for Disk Operations

Query Azure API latencies for disk create, delete, and resize operations:

```console
curl http://localhost:29604/metrics | grep -E "cloudprovider_azure_api_request_duration_seconds_(sum|count)" | grep -E "request=\"(disks_create_or_update|disks_delete|disks_update)\""
```

Sample output:
```
cloudprovider_azure_api_request_duration_seconds_sum{request="disks_create_or_update",resource_group="mc_myaks_myaks_eastus",subscription_id="12345678-1234-1234-1234-123456789012"} 45.234
cloudprovider_azure_api_request_duration_seconds_count{request="disks_create_or_update",resource_group="mc_myaks_myaks_eastus",subscription_id="12345678-1234-1234-1234-123456789012"} 15
cloudprovider_azure_api_request_duration_seconds_sum{request="disks_delete",resource_group="mc_myaks_myaks_eastus",subscription_id="12345678-1234-1234-1234-123456789012"} 23.567
cloudprovider_azure_api_request_duration_seconds_count{request="disks_delete",resource_group="mc_myaks_myaks_eastus",subscription_id="12345678-1234-1234-1234-123456789012"} 8
```

### Example: Get `attach_disk` and `detach_disk` Azure API Metrics

Query Azure API latencies for VM attach/detach operations:

```console
curl http://localhost:29604/metrics | grep -E "cloudprovider_azure_api_request_duration_seconds_(sum|count)" | grep -E "source=\"(attach_disk|detach_disk)\""
```

To calculate the average `attach_disk` latency, sum the initiation and completion wait latencies, then divide by the count:

```
cloudprovider_azure_api_request_duration_seconds_sum{request="vmss_wait_for_update_result",resource_group="mc_myaks_myaks_eastus",source="attach_disk",subscription_id="12345678-1234-1234-1234-123456789012"} 176.880914393
cloudprovider_azure_api_request_duration_seconds_count{request="vmss_wait_for_update_result",resource_group="mc_myaks_myaks_eastus",source="attach_disk",subscription_id="12345678-1234-1234-1234-123456789012"} 14
cloudprovider_azure_api_request_duration_seconds_sum{request="vmssvm_update",resource_group="mc_myaks_myaks_eastus",source="detach_disk",subscription_id="12345678-1234-1234-1234-123456789012"} 231.78358075499997
cloudprovider_azure_api_request_duration_seconds_count{request="vmssvm_update",resource_group="mc_myaks_myaks_eastus",source="detach_disk",subscription_id="12345678-1234-1234-1234-123456789012"} 17
cloudprovider_azure_api_request_duration_seconds_sum{request="vmssvm_updateasync",resource_group="mc_myaks_myaks_eastus",source="attach_disk",subscription_id="12345678-1234-1234-1234-123456789012"} 3.6363432579999997
cloudprovider_azure_api_request_duration_seconds_count{request="vmssvm_updateasync",resource_group="mc_myaks_myaks_eastus",source="attach_disk",subscription_id="12345678-1234-1234-1234-123456789012"} 14
```

Stop port forwarding with the following command:

```console
kill -9 $PORTFORWARDER
```

## Grafana Dashboard

You can create a Grafana dashboard to visualize these metrics. Here are some useful PromQL queries:

### Operation Success Rate
```promql
sum(rate(azuredisk_csi_driver_operations_total{success="true"}[5m])) by (operation) / 
sum(rate(azuredisk_csi_driver_operations_total[5m])) by (operation) * 100
```

### Average Operation Duration
```promql
rate(azuredisk_csi_driver_operation_duration_seconds_sum[5m]) / 
rate(azuredisk_csi_driver_operation_duration_seconds_count[5m])
```

### Average Duration by Disk SKU
```promql
rate(azuredisk_csi_driver_operation_duration_seconds_labeled_sum[5m]) / 
rate(azuredisk_csi_driver_operation_duration_seconds_labeled_count[5m]) by (disk_sku)
```

### Average Duration by Zone
```promql
rate(azuredisk_csi_driver_operation_duration_seconds_labeled_sum[5m]) / 
rate(azuredisk_csi_driver_operation_duration_seconds_labeled_count[5m]) by (zone)
```

