#!/usr/bin/env bash
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

set -euo pipefail

#
# Define common functions.
#
printhelp() {
  cat <<EOF
get-logs-up.sh: Gets the Azure Disk CSI Driver controller and node logs.

Options:
  -c, --container                    : The name of the container for which to
                                       get logs. Defaults to "azuredisk".
  -l, --selectors string[,string...] : A comma-delimited list of selectors used
                                       to select the pods from which to get
                                       logs. Defaults to the standard Azure Disk
                                       CSI Driver pod selectors.
  -n, --namespace string             : The namespace in which the driver pods
                                       exists. Defaults to "kube-system".
  -o, --output string                : The output directory.
  -t, --since-time string            : Only returns logs after the specified
                                       date in RFC 8601 format.
  -?, --help                         : Displays this message.
EOF
}

echoerr() {
  printf "%s\n\n" "$*" >&2
}

#
# Process the command line arguments.
#
POSITIONAL=()
SELECTORS=()
while [[ $# -gt 0 ]]; do
  ARG="$1"
  case $ARG in
  -c | --container)
    CONTAINER="$2"
    shift 2 # skip the option arguments
    ;;

  -d | --debug)
    set -x
    shift
    ;;

  -l | --selectors)
    IFS=',' read -ra SELECTORS <<< "$2"
    shift 2 # skip the option arguments
    ;;

  -n | --namespace)
    NAMESPACE="$2"
    shift 2 # skip the option arguments
    ;;

  -o | --output)
    OUTPUT_DIR="$2"
    shift 2 # skip the option arguments
    ;;

  -t | --since-time)
    SINCE_TIME="$2"
    shift 2 # skip the option arguments
    ;;

  -?|--help)
    printhelp
    exit
    ;;

  *)
    POSITIONAL+=("$1")
    shift
    ;;
  esac
done
set -- "${POSITIONAL[@]}" # restore positional parameters

#
# Validate command-line arguments and initialize variables.
#
if [[ ${#POSITIONAL[@]} -ne 0 ]]; then
  echoerr "ERROR: Unknown positional parameters - ${POSITIONAL[*]}"
  printhelp
  exit 1
fi

if [[ ${#SELECTORS[@]} -eq 0 ]]; then
  SELECTORS=("app=csi-azuredisk-controller" "app=csi-azuredisk-node" "app=csi-azuredisk-node-win")
fi

KUBECTL_LOGS_OPTIONS=(--namespace="${NAMESPACE:-kube-system}")
if [[ -n ${SINCE_TIME:-} ]]; then

  # If --since-time is specified, convert it to a UTC time if it isn't already.
  if [[ ! ${SINCE_TIME} =~ "Z$" ]]; then

    # Assume the local time zone if one isn't specified.
    if [[ ! ${SINCE_TIME} =~ "[+-]\d{2}:\d{2}$" ]]; then
      SINCE_TIME+="$(date +%:z)"
    fi
    SINCE_TIME="$(date --iso-8601=seconds --utc --date "${SINCE_TIME}")"
    SINCE_TIME="${SINCE_TIME/+00:00/Z}"
    echo "Converted --since-time to UTC (${SINCE_TIME})"
  fi

  KUBECTL_LOGS_OPTIONS+=(--since-time="${SINCE_TIME}")
fi

if [[ -z ${OUTPUT_DIR:-} ]]; then
  OUTPUT_DIR="${SINCE_TIME:-$(date --iso-8601=seconds --utc)}"
  OUTPUT_DIR="${OUTPUT_DIR/+00:00/Z}"
  OUTPUT_DIR="${OUTPUT_DIR//:/-}"
fi

#
# Get the desired logs.
#
POD_NAMES=()
for SELECTOR in "${SELECTORS[@]}"; do
  mapfile -t TEMP < <(kubectl get pods -n "${NAMESPACE}" -l "${SELECTOR}" -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}')
  POD_NAMES+=("${TEMP[@]}")
done

echo "Writing logs to ${OUTPUT_DIR}..."
mkdir -p "${OUTPUT_DIR}"

for POD_NAME in "${POD_NAMES[@]}"; do
  echo "Getting logs for ${POD_NAME}..."
  kubectl logs "${KUBECTL_LOGS_OPTIONS[@]}" "${POD_NAME}" "${CONTAINER:-azuredisk}" >"${OUTPUT_DIR}/${POD_NAME}.log"
done
