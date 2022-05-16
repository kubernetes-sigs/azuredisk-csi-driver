## Pod Failover Test

This test run creates a controller pod and a workload pod. The controller pod is responsible for repeatedly creating and deleting workload pod over the given duration. The workload pod will publish a metric called "default_failover_testing_workload_downtime" with the value of the downtime seen by the workload pod in seconds.

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

### Create metrics service

- Create the metrics service where the workload pod can publish it's metrics using the command mentioned below:

```console
wget https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/main_v2/test/podFailover/metrics-svc.yaml
```

- Edit the value of metrics-service-image and run:

```console
kubectl apply -f metrics-svc.yaml
```

- Create the service monitor to enable Prometheus to scrape the pod failover service metrics.

```console
wget https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/main_v2/test/podFailover/service-monitor.yaml
kubectl apply -f service-monitor.yaml
```

### Run failover test

- Get the deployment file to deploy the controller

```console
wget https://raw.githubusercontent.com/abhisheksinghbaghel/azuredisk-csi-driver/main_v2/test/podFailover/deployment.yaml
```

- Modify the values the following parameters:
    1. controller-pod-image
    2. driver-version
    3. maxshares
    4. duration
    5. workload-image
    6. pod-count
    7. pvc-per-pod
    8. delay-before-failover
    9. all-pods-on-one-node
    10. test-name
    11. auth-enabled

- Run the below command to deploy the controller. This will start the test for the given duration and will also start pushing metrics to the metrics-svc.

```console
kubectl apply -f deployment.yaml
```
