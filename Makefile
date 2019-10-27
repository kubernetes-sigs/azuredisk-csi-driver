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
GIT_COMMIT ?= $(shell git rev-parse HEAD)
REGISTRY ?= andyzhangx
DRIVER_NAME = disk.csi.azure.com
IMAGE_NAME = azuredisk-csi
IMAGE_VERSION ?= v0.5.0
# Use a custom version for E2E tests if we are in Prow
ifdef AZURE_CREDENTIALS
override IMAGE_VERSION := e2e-$(GIT_COMMIT)
endif
IMAGE_TAG = $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_VERSION)
IMAGE_TAG_LATEST = $(REGISTRY)/$(IMAGE_NAME):latest
REV = $(shell git describe --long --tags --dirty)
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
TOPOLOGY_KEY = topology.$(DRIVER_NAME)/zone
LDFLAGS ?= "-X ${PKG}/pkg/azuredisk.driverVersion=${IMAGE_VERSION} -X ${PKG}/pkg/azuredisk.gitCommit=${GIT_COMMIT} -X ${PKG}/pkg/azuredisk.buildDate=${BUILD_DATE} -X ${PKG}/pkg/azuredisk.DriverName=${DRIVER_NAME} -X ${PKG}/pkg/azuredisk.topologyKey=${TOPOLOGY_KEY} -extldflags "-static""
GINKGO_FLAGS = "-ginkgo.noColor"
GOPATH ?= $(shell go env GOPATH)
GOBIN ?= $(GOPATH)/bin
GO111MODULE = off
export GOPATH GOBIN GO111MODULE

.PHONY: all
all: azuredisk

.PHONY: verify
verify:
	hack/verify-all.sh
	go vet ./pkg/...

.PHONY: unit-test
unit-test:
	go test -v -cover ./pkg/... ./test/utils/credentials

.PHONY: sanity-test
sanity-test: azuredisk
	go test -v -timeout=30m ./test/sanity

.PHONY: integration-test
integration-test: azuredisk
	go test -v -timeout=30m ./test/integration

.PHONY: e2e-test
e2e-test:
	go test -v -timeout=0 ./test/e2e ${GINKGO_FLAGS}

.PHONY: e2e-bootstrap
e2e-bootstrap: install-helm
	# Only build and push the image if it does not exist in the registry
	docker pull $(IMAGE_TAG) || make azuredisk-container push
	helm install charts/latest/azuredisk-csi-driver -n azuredisk-csi-driver --namespace kube-system --wait \
		--set image.pullPolicy=IfNotPresent \
		--set image.repository=$(REGISTRY)/$(IMAGE_NAME) \
		--set image.tag=$(IMAGE_VERSION)

.PHONY: install-helm
install-helm:
	# Use v2.11.0 helm to match tiller's version in clusters made by aks-engine
	curl https://raw.githubusercontent.com/helm/helm/master/scripts/get | DESIRED_VERSION=v2.11.0 bash
	# Make sure tiller is ready
	kubectl wait pod -l name=tiller --namespace kube-system --for condition=ready --timeout 5m
	helm version

.PHONY: e2e-teardown
e2e-teardown:
	helm delete --purge azuredisk-csi-driver

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

.PHONY: build-push
build-push: azuredisk-container
	docker tag $(IMAGE_TAG) $(IMAGE_TAG_LATEST)
	docker push $(IMAGE_TAG_LATEST)

.PHONY: clean
clean:
	go clean -r -x
	-rm -rf _output
