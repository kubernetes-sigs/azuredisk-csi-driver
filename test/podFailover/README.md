## Pod Failover Test

This test run creates a controller pod and a workload pod. The controller pod is responsible for repeatedly deleting pods with the label app=pod-failover. The workload pod will use a custom CRD or file to calculate downtime and can publish a metric called "default_failover_testing_workload_downtime" with the value of the downtime seen by the workload pod in seconds to prometheus or publish downtime to Azure Event Hub.

## Prerequisite

- Make sure a kubernetes cluster(with version >= 1.18) is set up and kubeconfig is under $HOME/.kube/config.
- Make sure that prometheus is setup on the kubernetes cluster where this test is being run to scrape the pod downtime metrics.
- Make sure that azuredisk-csi driver is already deployed on the kubernetes cluster.

## How to run pod failover test

- Navigate to the root directory and run this command. This will create and publish the containers for the controller and the workload. 

```console
export REGISTRY=<your-personal-docker-repo>
make pod-failover-test-containers
```

### Run failover test

- Modify test/podFailover/controller/chart/values.yaml

- Values/Parameters - controllerPodImage(required), metricPodImage(optional, used for prometheus metrics)


- Deployment via helm chart (controller/metrics)
``` console
helm install pod-failover-controller test/podFailover/controller/chart/
```

- Create or use a scenario from test/podFailover/workload/scenarios

- The following values/parameters are required for workloads.
    1. namespace
    2. podCount
    3. pvcCount (can be 0 if running stateless)
    4. failureType
    5. workloadPodImage
- The following are optional parameters.
    1. storageClass(see 1pod1pvc for example. includes all fields related to storageClass creation)
    2. metricsEndpoint (used for sending metrics to prometheus)
    3. azureClientId, azureClientSecret, azureTenantId (used for authenticating to event hub to send metrics to kusto, use the azdiskdrivertest-sp service principal)
    4. driverVersion (reports driver version to kusto)
    5. runID

- Deployment via helm chart (workload)

```console
helm install pod-failover-workload test/podFailover/workload/chart/ --values test/podFailover/workload/scenarios/1pod1pvc.yaml --set azureClientId=$AZURE_CLIENT_ID --set azureClientSecret=$AZURE_CLIENT_SECRET --set azureTenantId=$AZURE_TENANT_ID
```


