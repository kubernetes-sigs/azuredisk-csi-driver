#!/bin/bash

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

if [[ -z "$(command -v yamllint)" ]]; then
  apt update && apt install yamllint -y
fi

LOG=/tmp/yamllint.log
helmPath=charts/latest/azuredisk-csi-driver/templates

echo "checking yaml files num ..."
deployDirNum=`ls deploy/*.yaml | wc -l`
helmDirNum=`ls $helmPath/*.yaml | grep -v serviceaccount | wc -l`
if [[ "${deployDirNum}" != "${helmDirNum}" ]]; then
  echo "yaml file num($deployDirNum) under deploy/ not equal to num($helmDirNum) under $helmPath"
  exit 1
fi

for path in "deploy/*.yaml" "deploy/example/*.yaml" "deploy/example/metrics/*.yaml" "deploy/example/snapshot/*.yaml" "deploy/example/cloning/*.yaml" "deploy/example/rawblock/*.yaml" "deploy/example/windows/*.yaml" "deploy/example/sharedisk/*.yaml" "deploy/example/modifyvolume/*.yaml" "docs/known-issues/node-shutdown-recovery/*.yaml" "deploy/example/vpa/*.yaml" "deploy/example/volumegroupsnapshot/*.yaml"
do
    echo "checking yamllint under path: $path ..."
    yamllint -f parsable $path | grep -v "line too long" > $LOG
    linecount=`cat $LOG | grep -vE "crd-csi-snapshot|crd-csi-volumegroupsnapshot" | wc -l`
    if [ $linecount -gt 0 ]; then
        echo "yaml files under $path are not linted, failed with: "
        cat $LOG
        exit 1
    fi
done

echo "checking yamllint under path: $helmPath ..."
yamllint -f parsable $helmPath/*.yaml | grep -v "line too long" | grep -v "too many spaces inside braces" | grep -v "missing document start" | grep -v "syntax error" > $LOG
linecount=`cat $LOG | grep -vE "crd-csi-snapshot|crd-csi-volumegroupsnapshot" | wc -l`
if [ $linecount -gt 0 ]; then
	echo "yaml files under $helmPath/ are not linted, failed with: "
	cat $LOG
	exit 1
fi

echo "Congratulations! All Yaml files have been linted."
