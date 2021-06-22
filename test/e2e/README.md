## End to End Test

## Run E2E tests Locally
### Prerequisite
 - Make sure a kubernetes cluster(with version >= 1.13) is set up and kubeconfig is under `$HOME/.kube/config`
 - Copy `/etc/kubernetes/azure.json` from agent node to local dev machine where you are going to run e2e tests

### How to run E2E tests
```console
# testing against CSI Driver by default
make e2e-test

# run e2e tests against CSI Driver v2
BUILD_V2=1 make e2e-test

# run e2e tests against CSI Driver V1 on the V2 build
BUILD_V2="true" ADDITIONAL_E2E_HELM_OPTIONS="--set azuredisk.useV2Driver=false" make e2e-test

# Run Windows e2e tests
export TEST_WINDOWS="true"
make e2e-test

# Run specific e2e tests
go test -v -timeout=0 ./test/e2e -ginkgo.noColor -ginkgo.v -ginkgo.focus="deployment"
```

 - testing against in-tree volume driver
```console
export AZURE_STORAGE_DRIVER="kubernetes.io/azure-disk"
make e2e-test
```

 - migration test
```console
export TEST_MIGRATION="true"
export AZURE_STORAGE_DRIVER="kubernetes.io/azure-disk"
make e2e-test
```
