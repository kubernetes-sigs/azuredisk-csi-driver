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

if [[ "$#" -lt 1 ]]; then
  echo "This script requires a volume name as its arguments: ./register-pv.sh <volume-name>"
  exit 1
fi

export volumeName=$1

export diskURI=$(kubectl get azvolumes -n azure-disk-csi $volumeName -o jsonpath={.status.detail.volume_id})

tee <<-EOF > new-pv.yaml;
apiVersion: v1
kind: PersistentVolume
metadata:
  name: ${volumeName}
spec:
  csi:
    driver: disk.csi.azure.com
    volumeHandle: ${diskURI}
  accessModes:
  - ReadWriteOnce
  capacity:
    storage: 256Gi
  storageClassName: test-sc
EOF

kubectl apply -f new-pv.yaml
# rm -f new-pv.yaml

tee <<-EOF > new-pvc.yaml;
apiVersion: v1
kind: PersistentVolumeClaim
apiVersion: v1
metadata:
  name: ${volumeName}
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 10Gi
  volumeName: ${volumeName}
  storageClassName: test-sc
EOF

kubectl apply -f new-pvc.yaml
rm -f new-pvc.yaml

# secret=$(kubectl get secrets -n kube-system | grep csi-azuredisk-controller-sa | awk -F" " '{print $1}')
# token=$(kubectl get secret $secret -n kube-system -o jsonpath={.data.token} | base64 -d)
# curl -k -XPATCH -H "Authorization:Bearer $token" -H "Accept: application/json" -H "Content-Type: application/strategic-merge-patch+json" --data '{"status":{"phase":"Bound"}}' https://127.0.0.1:6443/api/v1/persistentvolumes/$volumeName/status