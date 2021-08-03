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

export nodeName=$1

tee <<-EOF > new-azdrivernode.yaml;
apiVersion: disk.csi.azure.com/v1alpha1
kind: AzDriverNode
metadata:
  creationTimestamp: "2021-06-23T03:16:51Z"
  generation: 1
  labels:
    azdrivernodes.disk.csi.azure.com/partition: default
  name: ${nodeName}
  namespace: azure-disk-csi
spec:
  nodeName: ${nodeName}
EOF

kubectl apply -f new-azdrivernode.yaml
rm -f new-azdrivernode.yaml
