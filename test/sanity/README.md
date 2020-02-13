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
export TENANT_ID=
export SUBSCRIPTION_ID=
export AAD_CLIENT_ID=
export AAD_CLIENT_SECRET=
export RESOURCE_GROUP=
export LOCATION=
```

### Run sanity tests
```
make sanity-test
```
