#!/bin/bash

# Copyright 2022 The Kubernetes Authors.
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
  echo "This script requires a node name and volume name as its arguments: ./register-volumeattachment.sh <node-name> <volume-name>"
  exit 1
fi

export nodeName=$1
export volumeName=$2
export volumeAttachmentName=${volumeName}-${nodeName}-attachment

tee <<-EOF > new-volumeattachment.yaml;
apiVersion: storage.k8s.io/v1
kind: VolumeAttachment
metadata:
  name: ${volumeName}-${nodeName}-attachment
spec:
  attacher: disk.csi.azure.com
  nodeName: ${nodeName}
  source:
    persistentVolumeName: ${volumeName}
EOF

kubectl apply -f new-volumeattachment.yaml
rm -f new-volumeattachment.yaml

# update volumeattachment's status to attached
secret=$(kubectl get secrets -n kube-system | grep csi-azuredisk-controller-sa | awk -F" " '{print $1}')
token=$(kubectl get secret $secret -n kube-system -o jsonpath={.data.token} | base64 -d)
curl -k -XPATCH -H "Authorization:Bearer $token" -H "Accept: application/json" -H "Content-Type: application/strategic-merge-patch+json" --data '{"status":{"attached":'"true"'}}' https://127.0.0.1:6443/apis/storage.k8s.io/v1/volumeattachments/$volumeAttachmentName/status