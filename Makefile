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

REGISTRY_NAME=andyzhangx
IMAGE_NAME=azuredisk-csi
IMAGE_VERSION=v0.2.0-alpha
IMAGE_TAG=$(REGISTRY_NAME)/$(IMAGE_NAME):$(IMAGE_VERSION)
IMAGE_TAG_LATEST=$(REGISTRY_NAME)/$(IMAGE_NAME):latest
REV=$(shell git describe --long --tags --dirty)

.PHONY: all azuredisk azuredisk-container clean

all: azuredisk

test:
	go test github.com/kubernetes-sigs/azuredisk-csi-driver/pkg/... -cover
	go vet github.com/kubernetes-sigs/azuredisk-csi-driver/pkg/...
integration-test:
	test/integration/run-tests-all-clouds.sh
test-sanity:
	go test -v ./test/sanity/...
azuredisk:
	if [ ! -d ./vendor ]; then dep ensure -vendor-only; fi
	CGO_ENABLED=0 GOOS=linux go build -a -ldflags '-X github.com/kubernetes-sigs/azuredisk-csi-driver/pkg/azuredisk.vendorVersion=$(IMAGE_VERSION) -extldflags "-static"' -o _output/azurediskplugin ./pkg/azurediskplugin
azuredisk-windows:
	if [ ! -d ./vendor ]; then dep ensure -vendor-only; fi
	CGO_ENABLED=0 GOOS=windows go build -a -ldflags '-X github.com/kubernetes-sigs/azuredisk-csi-driver/pkg/azuredisk.vendorVersion=$(IMAGE_VERSION) -extldflags "-static"' -o _output/azurediskplugin.exe ./pkg/azurediskplugin
azuredisk-container: azuredisk
	docker build --no-cache -t $(IMAGE_TAG) -f ./pkg/azurediskplugin/Dockerfile .
push: azuredisk-container
	docker push $(IMAGE_TAG)
push-latest: azuredisk-container
	docker push $(IMAGE_TAG)
	docker tag $(IMAGE_TAG) $(IMAGE_TAG_LATEST)
	docker push $(IMAGE_TAG_LATEST)
clean:
	go clean -r -x
	-rm -rf _output
