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

PKG = sigs.k8s.io/azuredisk-csi-driver
GIT_COMMIT ?= $(shell git rev-parse HEAD)
REGISTRY ?= andyzhangx
REGISTRY_NAME ?= $(shell echo $(REGISTRY) | sed "s/.azurecr.io//g")
DRIVER_NAME = disk.csi.azure.com
IMAGE_NAME ?= azuredisk-csi
IMAGE_VERSION ?= v0.10.0
# Use a custom version for E2E tests if we are testing in CI
ifdef CI
ifndef PUBLISH
override IMAGE_VERSION := e2e-$(GIT_COMMIT)
endif
endif
IMAGE_TAG ?= $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_VERSION)
IMAGE_TAG_LATEST = $(REGISTRY)/$(IMAGE_NAME):latest
REV = $(shell git describe --long --tags --dirty)
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
TOPOLOGY_KEY = topology.$(DRIVER_NAME)/zone
ENABLE_TOPOLOGY ?= false
LDFLAGS ?= "-X ${PKG}/pkg/azuredisk.driverVersion=${IMAGE_VERSION} -X ${PKG}/pkg/azuredisk.gitCommit=${GIT_COMMIT} -X ${PKG}/pkg/azuredisk.buildDate=${BUILD_DATE} -X ${PKG}/pkg/azuredisk.DriverName=${DRIVER_NAME} -X ${PKG}/pkg/azuredisk.topologyKey=${TOPOLOGY_KEY} -extldflags "-static""
E2E_HELM_OPTIONS ?= --set image.azuredisk.repository=$(REGISTRY)/$(IMAGE_NAME) --set image.azuredisk.tag=$(IMAGE_VERSION)
GINKGO_FLAGS = -ginkgo.noColor -ginkgo.v
ifeq ($(ENABLE_TOPOLOGY), true)
GINKGO_FLAGS += -ginkgo.focus="\[multi-az\]"
else
GINKGO_FLAGS += -ginkgo.focus="\[single-az\]"
endif
GOPATH ?= $(shell go env GOPATH)
GOBIN ?= $(GOPATH)/bin
GO111MODULE = off
DOCKER_CLI_EXPERIMENTAL = enabled
export
export GOPATH GOBIN GO111MODULE DOCKER_CLI_EXPERIMENTAL

.PHONY: all
all: azuredisk

.PHONY: verify
verify: unit-test
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
	docker pull $(IMAGE_TAG) || make azuredisk-container push
ifdef TEST_WINDOWS
	helm install azuredisk-csi-driver charts/latest/azuredisk-csi-driver --namespace kube-system --wait --timeout=15m -v=5 --debug \
		${E2E_HELM_OPTIONS} \
		--set windows.enabled=true \
		--set linux.enabled=false \
		--set controller.replicas=1
else
	helm install azuredisk-csi-driver charts/latest/azuredisk-csi-driver --namespace kube-system --wait --timeout=15m -v=5 --debug \
		${E2E_HELM_OPTIONS} \
		--set snapshot.enabled=true
endif

.PHONY: install-helm
install-helm:
	curl https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 | bash

.PHONY: e2e-teardown
e2e-teardown:
	helm delete azuredisk-csi-driver --namespace kube-system

.PHONY: azuredisk
azuredisk:
	CGO_ENABLED=0 GOOS=linux go build -a -ldflags ${LDFLAGS} -o _output/azurediskplugin ./pkg/azurediskplugin

.PHONY: azuredisk-windows
azuredisk-windows:
	CGO_ENABLED=0 GOOS=windows go build -a -ldflags ${LDFLAGS} -o _output/azurediskplugin.exe ./pkg/azurediskplugin

.PHONY: container
container: azuredisk
	docker build --no-cache -t $(IMAGE_TAG) -f ./pkg/azurediskplugin/dev.Dockerfile .

.PHONY: azuredisk-container
azuredisk-container: azuredisk azuredisk-windows
	docker buildx rm container-builder || true
	docker buildx create --use --name=container-builder
ifdef CI
	bash -x build.sh build_and_push
	bash -x build.sh manifest
ifdef PUBLISH
	docker manifest create $(IMAGE_TAG_LATEST) $(IMAGE_TAG)-linux-amd64 $(IMAGE_TAG)-windows-1809-amd64
	docker manifest inspect $(IMAGE_TAG_LATEST)
endif
else
ifdef TEST_WINDOWS
	docker buildx build --no-cache -t $(IMAGE_TAG)-windows-1809-amd64 -f ./pkg/azurediskplugin/Windows.Dockerfile --platform="windows/amd64" --output "type=image" .
else
	docker buildx build --no-cache -t $(IMAGE_TAG) -f ./pkg/azurediskplugin/Dockerfile --platform="linux/amd64" --output "type=docker,push=false" .
endif
endif

.PHONY: push
push:
ifdef CI
	docker manifest push --purge $(IMAGE_TAG)
else
	docker push $(IMAGE_TAG)
endif

.PHONY: push-latest
push-latest:
ifdef CI
	docker manifest push --purge $(IMAGE_TAG_LATEST)
else
	docker push $(IMAGE_TAG_LATEST)
endif

.PHONY: build-push
build-push: azuredisk-container
	docker tag $(IMAGE_TAG) $(IMAGE_TAG_LATEST)
	docker push $(IMAGE_TAG_LATEST)

.PHONY: clean
clean:
	go clean -r -x
	-rm -rf _output

.PHONY: create-metrics-svc
create-metrics-svc:
	kubectl create -f deploy/example/metrics/csi-azuredisk-controller-svc.yaml
