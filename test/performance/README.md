## Performance Test
Performance tests verify the driver's metrics and allow for better insight into the impacts of different configurations on driver performance 

## Run Performance Tests 
### Prerequisite
 - make sure that kustomize is installed
```
GOBIN=$(pwd)/ GO111MODULE=on go get sigs.k8s.io/kustomize/kustomize/v3
```

### Run performance tests
 - Start the performance tests by running the test script
```
./test/performance/start-perfrun.sh
```
 - View the status of the tests
```
kubectl get pods --watch | grep fio
```
 - When completed, examine the contents of pod logs for test results

```
kubectl logs {podname}
```

### Performance test cleanup
```
./test/performance/end-perfrun.sh
```