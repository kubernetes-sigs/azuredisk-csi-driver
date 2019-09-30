# Copyright 2017 The Kubernetes Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

PKG = github.com/kubernetes-sigs/azuredisk-csi-driver
REGISTRY ?= andyzhangx
DRIVER_NAME = disk.csi.azure.com
IMAGE_NAME = azuredisk-csi
IMAGE_VERSION ?= v0.5.0
IMAGE_TAG = $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_VERSION)
IMAGE_TAG_LATEST = $(REGISTRY)/$(IMAGE_NAME):latest
REV = $(shell git describe --long --tags --dirty)
GIT_COMMIT ?= $(shell git rev-parse HEAD)
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
TOPOLOGY_KEY = topology.$(DRIVER_NAME)/zone
LDFLAGS ?= "-X ${PKG}/pkg/azuredisk.driverVersion=${IMAGE_VERSION} -X ${PKG}/pkg/azuredisk.gitCommit=${GIT_COMMIT} -X ${PKG}/pkg/azuredisk.buildDate=${BUILD_DATE} -X ${PKG}/pkg/azuredisk.DriverName=${DRIVER_NAME} -X ${PKG}/pkg/azuredisk.topologyKey=${TOPOLOGY_KEY} -extldflags "-static""
GOPATH ?= $(shell go env GOPATH)
GOBIN ?= $(GOPATH)/bin
export GOPATH GOBIN

.PHONY: all
all: azuredisk

.PHONY: verify
verify:
	hack/verify-all.sh
	go vet github.com/kubernetes-sigs/azuredisk-csi-driver/pkg/...

.PHONY: unit-test
unit-test:
	go test -v -cover ./pkg/... ./test/utils/credentials

.PHONY: sanity-test
sanity-test: azuredisk
	go test -v -timeout=20m ./test/sanity

.PHONY: integration-test
integration-test: azuredisk
	go test -v -timeout=20m ./test/integration

.PHONY: e2e-test
e2e-test: azuredisk
	test/e2e/run-test.sh

.PHONY: azuredisk
azuredisk:
	if [ ! -d ./vendor ]; then dep ensure -vendor-only; fi
	CGO_ENABLED=0 GOOS=linux go build -a -ldflags ${LDFLAGS} -o _output/azurediskplugin ./pkg/azurediskplugin

.PHONY: azuredisk-windows
azuredisk-windows:
	if [ ! -d ./vendor ]; then dep ensure -vendor-only; fi
	CGO_ENABLED=0 GOOS=windows go build -a -ldflags ${LDFLAGS} -o _output/azurediskplugin.exe ./pkg/azurediskplugin

.PHONY: azuredisk-container
azuredisk-container: azuredisk
	docker build --no-cache -t $(IMAGE_TAG) -f ./pkg/azurediskplugin/Dockerfile .

.PHONY: azuredisk-container-windows
azuredisk-container-windows: azuredisk-windows
	docker build --no-cache --platform windows/amd64 -t $(IMAGE_TAG) -f ./pkg/azurediskplugin/Windows.Dockerfile .

.PHONY: push
push:
	docker push $(IMAGE_TAG)

.PHONY: push-latest
push-latest:
	docker tag $(IMAGE_TAG) $(IMAGE_TAG_LATEST)
	docker push $(IMAGE_TAG_LATEST)

.PHONY: clean
clean:
	go clean -r -x
	-rm -rf _output
