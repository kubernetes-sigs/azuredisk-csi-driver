# Copyright 2019 The Kubernetes Authors.
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

FROM alpine:3.18.4
RUN apk upgrade --available --no-cache && \
    apk add --no-cache util-linux e2fsprogs e2fsprogs-extra ca-certificates udev xfsprogs xfsprogs-extra btrfs-progs btrfs-progs-extra

LABEL maintainers="andyzhangx"
LABEL description="Azure Disk CSI Driver"

ARG ARCH=amd64
ARG PLUGIN_NAME=azurediskplugin
ARG binary=./_output/${ARCH}/${PLUGIN_NAME}
COPY ${binary} /azurediskplugin
ENTRYPOINT ["/azurediskplugin"]
