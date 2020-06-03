## End to End Test

## Run E2E tests Locally
### Prerequisite
 - Make sure a kubernetes cluster(with version >= 1.13) is set up and kubeconfig is under `$HOME/.kube/config`
 - Copy out `/etc/kubernetes/azure.json` under one agent node to local machine
 > For AKS cluster, need to modify `resourceGroup` to the node resource group name inside `/etc/kubernetes/azure.json`

### How to run E2E tests
```console
# Using CSI Driver
make e2e-test

# Using in-tree volume plugin
export AZURE_STORAGE_DRIVER="kubernetes.io/azure-disk"
make e2e-test

# Run in a Windows cluster
export TEST_WINDOWS="true"
make e2e-test

# Run specific e2e tests
go test -v -timeout=0 ./test/e2e -ginkgo.noColor -ginkgo.v -ginkgo.focus="deployment"
```
