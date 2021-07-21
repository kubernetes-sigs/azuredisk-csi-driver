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

if [[ "$#" -lt 2 ]]; then
  echo "[Error] wrong number of arguments: the script requires two arguments: <Node Name> <Volume Name>"
  exit 1
fi

mode="attach"
if [[ "$#" -gt 2 ]]; then
  mode=$3
fi

export nodeName=$1
export volumeName=$2
export volumeId=$(kubectl get azvolume ${volumeName} -n azure-disk-csi -o yaml | egrep -i '/subscriptions/[a-z0-9_\-]+/resourceGroups/[a-z0-9_\-]+/providers/Microsoft.Compute/disks/[a-z0-9_\-]+' | awk -F' ' '{ print $2 }')

if [ "$mode" = "attach" ]; then
  tee <<-EOF > new-azvolumeattachment.yaml;
  apiVersion: disk.csi.azure.com/v1alpha1
  kind: AzVolumeAttachment
  metadata:
    name: ${volumeName,,}-${nodeName,,}
    namespace: azure-disk-csi
  spec:
    underlyingVolume: ${volumeName,,}
    volume_id: ${volumeId}
    volume_context: {}
    nodeName: ${nodeName,,}
    role: Primary
  status:
    state: Pending
EOF
  kubectl apply -f new-azvolumeattachment.yaml
  rm -f new-azvolumeattachment.yaml
elif [ "$mode" = "detach" ]; then
  kubectl delete azvolumeattachment "${volumeName,,}-${nodeName,,}" -n azure-disk-csi
else
  echo "[Error] the script current only supports 'attach' and 'detach'"
  exit 1
fi



