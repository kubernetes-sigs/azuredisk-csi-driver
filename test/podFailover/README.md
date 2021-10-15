## Pod Failover Test
This test run creates a controller pod and a workload pod. The controller pod is responsible for repeatedly creating and deleting workload pod over the given duration. The workload pod will publish a metric called "default_failover_testing_workload_downtime" with the value of the downtime seen by the workload pod in seconds.

## Prerequisite
- Make sure a kubernetes cluster(with version >= 1.13) is set up and kubeconfig is under $HOME/.kube/config.
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
wget https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/test/podFailover/metrics-svc.yaml
```

- Edit the value of metrics-service-image and run:
```console
kubectl apply -f metrics-svc.yaml
```

- Get the external endpoint of the service from the cluster
```console
kubectl get service pod-failover-metrics-service
```

### Run failover test

- Get the deployment file to deploy the controller
```console
wget https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/test/podFailover/deployment.yaml
```
- Modify the values the following parameters:
    1. controller-pod-image
    2. driver-version
    3. maxshares
    5. duration
    6. workload-image
    7. pod-count
    8. pvc-per-pod
    9. metrics-endpoint(Use the value retrieved from the above steps)
    10. delay-before-failover

- Run the below command to deploy the controller. This will start the test for the given duration and will also start pushing metrics to the metrics-svc.
```console
kubectl apply -f deployment.yaml
```

 
