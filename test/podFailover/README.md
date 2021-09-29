## Pod Failover Test
This test run creates a controller pod and a workload pod. The controller pod is responsible for repeatedly creating and deleting workload pod over the given duration. The workload pod will publish a metric called "default_failover_testing_workload_downtime" with the value of the downtime seen by the workload pod in seconds.

### Prerequisite
- Make sure a kubernetes cluster(with version >= 1.13) is set up and kubeconfig is under $HOME/.kube/config.
- Make sure that prometheus is setup on the kubernetes cluster where this test is being run to scrape the pod downtime metrics.
- Make sure that azuredisk-csi driver is already deployed on the kubernetes cluster.

### How to run pod failover test
- Navigate to the root directory and run this command. This will create and publish the containers for the controller and the workload. 
```console
make pod-failover-test-containers
```
- Get the deployment file to deploy the controller
```console
wget https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/test/podFailover/deployment.yaml
```
- Modify the values of controller-pod image, workloadpod-image, duration(duration for which the test should run), driver-version(v1 or v2), and maxshares.
- Run the below command to deploy the controller. This will start the test for the given duration.
```console
kubectl apply -f deployment.yaml
```

 
