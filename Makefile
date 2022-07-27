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
IMAGE_NAME ?= azuredisk-csi
SCHEDULER_EXTENDER_IMAGE_NAME ?= azdiskschedulerextender-csi
ifneq ($(BUILD_V2), true)
PLUGIN_NAME = azurediskplugin
IMAGE_VERSION ?= v1.22.0
CHART_VERSION ?= latest
else
PLUGIN_NAME = azurediskpluginv2
IMAGE_VERSION ?= latest-v2
CHART_VERSION ?= latest-v2
GOTAGS += -tags azurediskv2
endif
ifneq ($(USE_HELM_UPGRADE), true)
HELM_COMMAND = install
else
HELM_COMMAND = upgrade
endif
CLOUD ?= AzurePublicCloud
# Use a custom version for E2E tests if we are testing in CI
ifdef CI
ifndef PUBLISH
override IMAGE_VERSION := $(IMAGE_VERSION)-$(GIT_COMMIT)
endif
endif
IMAGE_TAG ?= $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_VERSION)
IMAGE_TAG_LATEST = $(REGISTRY)/$(IMAGE_NAME):latest
AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG ?= $(REGISTRY)/$(SCHEDULER_EXTENDER_IMAGE_NAME):$(IMAGE_VERSION)
AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG_LATEST = $(REGISTRY)/$(SCHEDULER_EXTENDER_IMAGE_NAME):latest
REV = $(shell git describe --long --tags --dirty)
BUILD_DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
ENABLE_TOPOLOGY ?= false
SCHEDULER_EXTENDER_LDFLAGS ?= "-X ${PKG}/pkg/azuredisk.schedulerVersion=${IMAGE_VERSION} -X ${PKG}/pkg/azuredisk.gitCommit=${GIT_COMMIT} -X ${PKG}/pkg/azuredisk.buildDate=${BUILD_DATE} -X ${PKG}/pkg/azuredisk.DriverName=${DRIVER_NAME} -extldflags "-static""
LDFLAGS ?= "-X ${PKG}/pkg/azuredisk.driverVersion=${IMAGE_VERSION} -X ${PKG}/pkg/azuredisk.gitCommit=${GIT_COMMIT} -X ${PKG}/pkg/azuredisk.buildDate=${BUILD_DATE} -extldflags "-static"" ${GOTAGS}
E2E_HELM_OPTIONS ?= --set image.azuredisk.repository=$(REGISTRY)/$(IMAGE_NAME) --set image.azuredisk.tag=$(IMAGE_VERSION) --set image.azuredisk.pullPolicy=Always --set image.schedulerExtender.repository=$(REGISTRY)/$(SCHEDULER_EXTENDER_IMAGE_NAME) --set image.schedulerExtender.tag=$(IMAGE_VERSION) --set image.schedulerExtender.pullPolicy=Always --set driver.userAgentSuffix="e2e-test"
E2E_HELM_OPTIONS += ${EXTRA_HELM_OPTIONS}
ifdef DISABLE_ZONE
E2E_HELM_OPTIONS += --set node.supportZone=false
endif
GINKGO_FLAGS = -ginkgo.v
ifeq ($(ENABLE_TOPOLOGY), true)
GINKGO_FLAGS += -ginkgo.focus="\[multi-az\]"
else
GINKGO_FLAGS += -ginkgo.focus="\[single-az\]"
endif
GOPATH ?= $(shell go env GOPATH)
GOBIN ?= $(GOPATH)/bin
GO111MODULE = on
DOCKER_CLI_EXPERIMENTAL = enabled
export GOPATH GOBIN GO111MODULE DOCKER_CLI_EXPERIMENTAL

# Generate all combination of all OS, ARCH, and OSVERSIONS for iteration
ALL_OS = linux windows
ALL_ARCH.linux = amd64 arm64
ALL_OS_ARCH.linux = $(foreach arch, ${ALL_ARCH.linux}, linux-$(arch))
ALL_ARCH.windows = amd64
ALL_OSVERSIONS.windows := 1809 20H2 ltsc2022
ALL_OS_ARCH.windows = $(foreach arch, $(ALL_ARCH.windows), $(foreach osversion, ${ALL_OSVERSIONS.windows}, windows-${osversion}-${arch}))
ALL_OS_ARCH = $(foreach os, $(ALL_OS), ${ALL_OS_ARCH.${os}})

# If set to true Windows containers will run as HostProcessContainers
WINDOWS_USE_HOST_PROCESS_CONTAINERS ?= false

# The current context of image building
# The architecture of the image
ARCH ?= amd64
# OS Version for the Windows images: 1809, 1903, 1909, 2004, ltsc2022
OSVERSION ?= 1809
# Output type of docker buildx build
OUTPUT_TYPE ?= registry

.PHONY: all
all: azuredisk

.PHONY: verify
verify: unit-test
	hack/verify-all.sh
	go vet ./pkg/...
	go build -o _output/${ARCH}/gen-disk-skus-map ./pkg/tool/

.PHONY: unit-test
unit-test: unit-test-v1 unit-test-v2

.PHONY: unit-test-v1
unit-test-v1:
	go test -v -cover ./pkg/... ./test/utils/credentials

.PHONY: unit-test-v2
unit-test-v2:
	go test -v -cover -tags azurediskv2 ./pkg/azuredisk

.PHONY: sanity-test
sanity-test: azuredisk
	go test -v -timeout=30m ./test/sanity

.PHONY: sanity-test-v2
sanity-test-v2: container-v2
	go test -v -timeout=30m ./test/sanity --test-driver-version=v2 --image-tag ${IMAGE_TAG}

.PHONY: integration-test
integration-test:
	go test -v -timeout=30m ./test/integration

.PHONY: integration-test-v2
integration-test-v2: container-v2
	go test -v -timeout=45m ./test/integration --test-driver-version=v2 --image-tag ${IMAGE_TAG}

.PHONY: e2e-bootstrap
e2e-bootstrap: install-helm
	docker pull $(IMAGE_TAG) || make container-all push-manifest
ifeq ($(BUILD_V2), true)
	docker pull $(AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG) || make azdiskschedulerextender-all push-manifest-azdiskschedulerextender
endif
ifdef TEST_WINDOWS
	helm $(HELM_COMMAND) azuredisk-csi-driver charts/${CHART_VERSION}/azuredisk-csi-driver --namespace kube-system --wait --timeout=15m -v=5 --debug \
		${E2E_HELM_OPTIONS} \
		--set windows.enabled=true \
		--set windows.useHostProcessContainers=${WINDOWS_USE_HOST_PROCESS_CONTAINERS} \
		--set linux.enabled=false \
		--set controller.replicas=1 \
		--set controller.logLevel=6 \
		--set schedulerExtender.replicas=1 \
		--set node.logLevel=6 \
		--set cloud=$(CLOUD)
else
	helm $(HELM_COMMAND) azuredisk-csi-driver charts/${CHART_VERSION}/azuredisk-csi-driver --namespace kube-system --wait --timeout=15m -v=5 --debug \
		${E2E_HELM_OPTIONS} \
		--set snapshot.enabled=true \
		--set cloud=$(CLOUD)
endif

.PHONY: e2e-upgrade-v2
e2e-upgrade-v2:
	USE_HELM_UPGRADE=true BUILD_V2=true $(MAKE) e2e-bootstrap

.PHONY: install-helm
install-helm:
	curl https://raw.githubusercontent.com/helm/helm/master/scripts/get-helm-3 | bash

.PHONY: e2e-teardown
e2e-teardown:
	helm delete azuredisk-csi-driver --namespace kube-system
	kubectl wait --namespace=kube-system --for=delete pod --selector app=csi-azuredisk-controller --timeout 5m || true
	kubectl wait --namespace=kube-system --for=delete pod --selector app=csi-azuredisk-node --timeout 5m || true
ifeq ($(BUILD_V2), true)
	kubectl wait --namespace=kube-system --for=delete pod --selector app=csi-azuredisk-scheduler-extender --timeout 5m || true
endif

.PHONY: azuredisk
azuredisk:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -a -ldflags ${LDFLAGS} -mod vendor -o _output/${ARCH}/${PLUGIN_NAME} ./pkg/azurediskplugin

.PHONY: azuredisk-v2
azuredisk-v2:
	BUILD_V2=true $(MAKE) azuredisk

.PHONY: azuredisk-windows
azuredisk-windows:
	CGO_ENABLED=0 GOOS=windows go build -a -ldflags ${LDFLAGS} -mod vendor -o _output/${ARCH}/${PLUGIN_NAME}.exe ./pkg/azurediskplugin

.PHONY: azuredisk-windows-v2
azuredisk-windows-v2:
	BUILD_V2=true $(MAKE) azuredisk-windows

.PHONY: azuredisk-darwin
azuredisk-darwin:
	CGO_ENABLED=0 GOOS=darwin go build -a -ldflags ${LDFLAGS} -mod vendor -o _output/${ARCH}/${PLUGIN_NAME}.exe ./pkg/azurediskplugin

.PHONY: azdiskschedulerextender
azdiskschedulerextender:
	CGO_ENABLED=0 GOOS=linux GOARCH=$(ARCH) go build -a -ldflags ${SCHEDULER_EXTENDER_LDFLAGS} -tags azurediskv2 -mod vendor -o _output/${ARCH}/azdiskschedulerextender ./pkg/azdiskschedulerextender

.PHONY: container
container: azuredisk
	docker build --no-cache -t $(IMAGE_TAG) --build-arg PLUGIN_NAME=${PLUGIN_NAME} --output=type=docker -f ./pkg/azurediskplugin/Dockerfile .

.PHONY: container-v2
container-v2: azuredisk-v2
	docker build --no-cache -t $(IMAGE_TAG) --build-arg PLUGIN_NAME=${PLUGIN_NAME} --output=type=docker -f ./pkg/azurediskplugin/Dockerfile .

.PHONY: container-linux
container-linux:
	docker buildx build . \
		--pull \
		--output=type=$(OUTPUT_TYPE) \
		--tag $(IMAGE_TAG)-linux-$(ARCH) \
		--file ./pkg/azurediskplugin/Dockerfile \
		--platform="linux/$(ARCH)" \
		--build-arg ARCH=${ARCH} \
		--build-arg PLUGIN_NAME=${PLUGIN_NAME}

.PHONY: container-windows
container-windows:
	docker buildx build . \
		--pull \
		--output=type=$(OUTPUT_TYPE) \
		--platform="windows/$(ARCH)" \
		--tag $(IMAGE_TAG)-windows-$(OSVERSION)-$(ARCH) \
		--file ./pkg/azurediskplugin/Windows.Dockerfile \
		--build-arg ARCH=${ARCH} \
		--build-arg PLUGIN_NAME=${PLUGIN_NAME} \
		--build-arg OSVERSION=$(OSVERSION)

.PHONY: azdiskschedulerextender-container
azdiskschedulerextender-container: azdiskschedulerextender
	docker build --no-cache -t $(AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG) -f ./pkg/azdiskschedulerextender/dev.Dockerfile .

.PHONY: azdiskschedulerextender-container-linux
azdiskschedulerextender-container-linux:
	docker buildx build . \
		--pull \
		--output=type=$(OUTPUT_TYPE) \
		--platform="linux/$(ARCH)" \
		--tag $(AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG)-linux-$(ARCH) \
		--file ./pkg/azdiskschedulerextender/Dockerfile \
		--build-arg ARCH=${ARCH}

.PHONY: container-setup
container-setup:
	docker buildx rm container-builder || true
	docker buildx create --use --name=container-builder
ifeq ($(CLOUD), AzureStackCloud)
	docker run --privileged --name buildx_buildkit_container-builder0 -d --mount type=bind,src=/etc/ssl/certs,dst=/etc/ssl/certs moby/buildkit:latest || true
endif
	# enable qemu for arm64 build
	# https://github.com/docker/buildx/issues/464#issuecomment-741507760
	docker run --privileged --rm tonistiigi/binfmt --uninstall qemu-aarch64
	docker run --rm --privileged tonistiigi/binfmt --install all

.PHONY: azdiskschedulerextender-all
azdiskschedulerextender-all: container-setup
	for arch in $(ALL_ARCH.linux); do \
		ARCH=$${arch} $(MAKE) azdiskschedulerextender; \
		ARCH=$${arch} $(MAKE) azdiskschedulerextender-container-linux; \
	done

.PHONY: container-all
container-all: azuredisk-windows container-setup
	for arch in $(ALL_ARCH.linux); do \
		ARCH=$${arch} $(MAKE) azuredisk; \
		ARCH=$${arch} $(MAKE) container-linux; \
	done
	for osversion in $(ALL_OSVERSIONS.windows); do \
		OSVERSION=$${osversion} $(MAKE) container-windows; \
	done

.PHONY: push-manifest
push-manifest:
	docker manifest create --amend $(IMAGE_TAG) $(foreach osarch, $(ALL_OS_ARCH), $(IMAGE_TAG)-${osarch})
	# add "os.version" field to windows images (based on https://github.com/kubernetes/kubernetes/blob/master/build/pause/Makefile)
	set -x; \
	for arch in $(ALL_ARCH.windows); do \
		for osversion in $(ALL_OSVERSIONS.windows); do \
			BASEIMAGE=mcr.microsoft.com/windows/nanoserver:$${osversion}; \
			full_version=`docker manifest inspect $${BASEIMAGE} | jq -r '.manifests[0].platform["os.version"]'`; \
			docker manifest annotate --os windows --arch $${arch} --os-version $${full_version} $(IMAGE_TAG) $(IMAGE_TAG)-windows-$${osversion}-$${arch}; \
		done; \
	done
	docker manifest push --purge $(IMAGE_TAG)
	docker manifest inspect $(IMAGE_TAG)
ifdef PUBLISH
	docker manifest create --amend $(IMAGE_TAG_LATEST) $(foreach osarch, $(ALL_OS_ARCH), $(IMAGE_TAG)-${osarch})
	set -x; \
	for arch in $(ALL_ARCH.windows); do \
		for osversion in $(ALL_OSVERSIONS.windows); do \
			BASEIMAGE=mcr.microsoft.com/windows/nanoserver:$${osversion}; \
			full_version=`docker manifest inspect $${BASEIMAGE} | jq -r '.manifests[0].platform["os.version"]'`; \
			docker manifest annotate --os windows --arch $${arch} --os-version $${full_version} $(IMAGE_TAG_LATEST) $(IMAGE_TAG)-windows-$${osversion}-$${arch}; \
		done; \
	done
	docker manifest inspect $(IMAGE_TAG_LATEST)
endif

.PHONY: push-latest
push-latest:
ifdef CI
	docker manifest push --purge $(IMAGE_TAG_LATEST)
else
	docker push $(IMAGE_TAG_LATEST)
endif

.PHONY: push-manifest-azdiskschedulerextender
push-manifest-azdiskschedulerextender:
	docker manifest create --amend $(AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG) $(foreach osarch, $(ALL_OS_ARCH.linux), $(AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG)-${osarch})
	docker manifest push --purge $(AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG)
	docker manifest inspect $(AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG)
ifdef PUBLISH
	docker manifest create $(AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG_LATEST) $(foreach osarch, $(ALL_OS_ARCH.linux), $(AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG)-${osarch})
	docker manifest inspect $(AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG_LATEST)
endif

.PHONY: push-latest-azdiskschedulerextender
push-latest-azdiskschedulerextender:
ifdef CI
	docker manifest push --purge $(AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG_LATEST)
else
	docker push $(AZ_DISK_SCHEDULER_EXTENDER_IMAGE_TAG)
endif

.PHONY: clean
clean:
	go clean -r -x
	-rm -rf _output

.PHONY: create-metrics-svc
create-metrics-svc:
	kubectl create -f deploy/example/metrics/csi-azuredisk-controller-svc.yaml
ifeq ($(BUILD_V2), true)
	kubectl create -f deploy/example/metrics/csi-azuredisk-scheduler-extender-svc.yaml
endif

.PHONY: delete-metrics-svc
delete-metrics-svc:
ifeq ($(BUILD_V2), true)
	kubectl delete -f deploy/example/metrics/csi-azuredisk-scheduler-extender-svc.yaml --ignore-not-found
endif
	kubectl delete -f deploy/example/metrics/csi-azuredisk-controller-svc.yaml --ignore-not-found

.PHONY: e2e-test
e2e-test:
	if [ ! -z "$(EXTERNAL_E2E_TEST)" ]; then \
		bash ./test/external-e2e/run.sh;\
	else \
		bash ./hack/parse-prow-creds.sh;\
		go test -v -timeout=0 ${GOTAGS} ./test/e2e ${GINKGO_FLAGS};\
	fi

.PHONY: e2e-test-v2
e2e-test-v2:
	BUILD_V2=true make e2e-test

.PHONY: scale-test
scale-test:
	go test -v -timeout=0 ${GOTAGS} ./test/scale -ginkgo.focus="Scale test scheduling and starting multiple pods with a persistent volume";

.PHONY: scale-test-v2
scale-test-v2:
	BUILD_V2=true make scale-test

POD_FAILOVER_IMAGE_VERSION = latest
ifdef CI
override POD_FAILOVER_IMAGE_VERSION = $(GIT_COMMIT)
endif
.PHONY: pod-failover-test-containers
pod-failover-test-containers:
	CGO_ENABLED=0 go build -a -mod vendor -o _output/${ARCH}/workloadPod ./test/podFailover/workload
	CGO_ENABLED=0 go build -a -mod vendor -o _output/${ARCH}/controllerPod ./test/podFailover/controller
	CGO_ENABLED=0 go build  -o _output/${ARCH}/metricsPod ./test/podFailover/metrics
	docker build -t $(REGISTRY)/workloadpod:$(POD_FAILOVER_IMAGE_VERSION) -f ./test/podFailover/workload/Dockerfile .
	docker build -t $(REGISTRY)/controllerpod:$(POD_FAILOVER_IMAGE_VERSION) -f ./test/podFailover/controller/Dockerfile .
	docker build -t $(REGISTRY)/metricspod:$(POD_FAILOVER_IMAGE_VERSION) -f ./test/podFailover/metrics/Dockerfile .
	docker push $(REGISTRY)/workloadpod:$(POD_FAILOVER_IMAGE_VERSION)
	docker push $(REGISTRY)/controllerpod:$(POD_FAILOVER_IMAGE_VERSION)
	docker push $(REGISTRY)/metricspod:$(POD_FAILOVER_IMAGE_VERSION)

.PHONY: upgrade-test
upgrade-test:
	go test -v -timeout=0 ${GOTAGS} ./test/upgrade

.PHONY: install-az-log
install-az-log:
	go install ./cmd/az-log

.PHONY: install-az-analyze
install-az-analyze:
	go install ./cmd/az-analyze
