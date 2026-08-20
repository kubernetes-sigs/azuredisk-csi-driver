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

FROM registry.k8s.io/build-image/debian-base:bookworm-v1.0.8 AS e2fsprogs-builder

ARG E2FSPROGS_VERSION=1.47.4
ARG E2FSPROGS_SHA256=fd5bf388cbdbe006a3d3b318d983b2948382440acc85a87f1e7d108653e8db0b

RUN clean-install build-essential ca-certificates curl xz-utils
WORKDIR /tmp/e2fsprogs
RUN curl -fsSLo e2fsprogs.tar.xz \
        "https://mirrors.edge.kernel.org/pub/linux/kernel/people/tytso/e2fsprogs/v${E2FSPROGS_VERSION}/e2fsprogs-${E2FSPROGS_VERSION}.tar.xz" && \
    echo "${E2FSPROGS_SHA256}  e2fsprogs.tar.xz" | sha256sum --check - && \
    tar --extract --file=e2fsprogs.tar.xz --xz --strip-components=1 && \
    ./configure --prefix=/usr --with-root-prefix=/usr --disable-nls && \
    make -j"$(nproc)" && \
    make DESTDIR=/opt/e2fsprogs install install-libs

FROM registry.k8s.io/build-image/debian-base:bookworm-v1.0.8

COPY --from=e2fsprogs-builder /opt/e2fsprogs/ /

RUN ldconfig && apt update && apt upgrade -y && apt-mark unhold libcap2 && clean-install util-linux mount ca-certificates udev xfsprogs btrfs-progs

LABEL maintainers="andyzhangx"
LABEL description="Azure Disk CSI Driver"

ARG ARCH=amd64
ARG PLUGIN_NAME=azurediskplugin
ARG binary=./_output/${ARCH}/${PLUGIN_NAME}
COPY ${binary} /azurediskplugin

ENTRYPOINT ["/azurediskplugin"]
