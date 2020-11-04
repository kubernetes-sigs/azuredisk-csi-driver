## Sanity Tests
Testing the Azure Disk CSI driver using the [`sanity`](https://github.com/kubernetes-csi/csi-test/tree/master/pkg/sanity) package test suite.

## Run Integration Tests Locally
### Prerequisite
 - make sure `GOPATH` is set
```
# echo $GOPATH
/root/go
```
 - set following environment variables
```console
export AZURE_TENANT_ID=
export AZURE_SUBSCRIPTION_ID=
export AZURE_CLIENT_ID=
export AZURE_CLIENT_SECRET=
export AZURE_RESOURCE_GROUP=
export AZURE_LOCATION=
```

### Run sanity tests
```
make sanity-test
```
