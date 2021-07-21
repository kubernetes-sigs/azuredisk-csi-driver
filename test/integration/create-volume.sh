#!/bin/bash

# Copyright 2021 The Kubernetes Authors.
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

if [[ "$#" -lt 1 ]]; then
  echo "This script requires a node name as its argument."
  exit 1
fi

export volumeName=$1

tee <<-EOF > new-azvolume.yaml;
apiVersion: disk.csi.azure.com/v1alpha1
kind: AzVolume
metadata:
    name: ${volumeName}
    namespace: azure-disk-csi
spec:
    capacityRange:
        required_bytes: 274877906944
    maxMountReplicaCount: 2
    underlyingVolume: ${volumeName}
    parameters:
        kind: managed
        maxShares: "2"
        skuname: Premium_LRS
    volumeCapability:
    - access_details:
        access_type: 0
        fs_type: ""
      access_mode: 1
status:
    state: Pending
EOF

cat new-azvolume.yaml

kubectl apply -f new-azvolume.yaml
rm -f new-azvolume.yaml
