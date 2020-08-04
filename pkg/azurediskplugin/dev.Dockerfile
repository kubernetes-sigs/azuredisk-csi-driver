# Copyright 2020 The Kubernetes Authors.
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

FROM mcr.microsoft.com/aks/fundamental/base-ubuntu:v0.0.5
RUN apt-get update && apt-get install -y util-linux e2fsprogs mount ca-certificates udev xfsprogs libc6
LABEL maintainers="andyzhangx"
LABEL description="Azure Disk CSI Driver"

COPY ./_output/azurediskplugin /azurediskplugin
ENTRYPOINT ["/azurediskplugin"]
