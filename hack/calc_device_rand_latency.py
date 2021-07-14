#!/usr/bin/env python

# Copyright 2015 The Kubernetes Authors.
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

import argparse
import json
import math
import os.path
import shlex
import subprocess
import requests
import sys
import logging

logger = logging.getLogger()

def measure_latency(directory, iops, request_size, rw, name):
    # Through experimentation, 1000 was the fewest number of IOs with a consistent average
    number_ios = 1000
    # Rate limit the fio command to non-burst IOPs to avoid inaccurate latency measurement due to throttling
    fio_command = [
        '/usr/bin/fio', '--direct=1', '--ioengine=libaio', '--iodepth=1',
        '--time_based=0', f'--name={name}', f'--size=10MB',
        '--output-format=json', '--overwrite=1', f'--rw={rw}',
        f'--bs={request_size}k', f'--number_ios={number_ios}',
        f'--rate_iops={iops}', f'--directory={directory}'
    ]
    # For usability, print an approximate expected runtime
    expected_runtime = 2

    logger.info('Running: %s.\nThis will take approximately %d seconds.' % (' '.join(shlex.quote(i) for i in fio_command), expected_runtime))
    return json.loads(subprocess.check_output(fio_command))

def get_latency(directory, iops, request_size, random):
    write = ('rand' if random else '') + 'write'
    read = ('rand' if random else '') + 'read'

    jobname = 'test'
    # The fio filename_format is, by default, $jobname.$jobnum.$filenum (when
    # no other format specifier is given). This script only runs a single job
    # with a single file, so the suffix will always be 0.0
    job_filename = jobname + '.0.0'
    try:
        os.remove(job_filename)
    except FileNotFoundError:
        pass

    parsed = measure_latency(directory, iops, request_size, write, jobname)
    write_clat_mean = parsed['jobs'][0]['write']['clat_ns']['mean']

    parsed = measure_latency(directory, iops, request_size, read, jobname)
    read_clat_mean = parsed['jobs'][0]['read']['clat_ns']['mean']

    winner = max(write_clat_mean, read_clat_mean)

    # convert to seconds
    return winner / 1_000_000_000

parser = argparse.ArgumentParser(description="""
    Calculate the latencies azure disk is seeing for read\writes.
""".strip())

parser.add_argument('--maxIops',
                    type=int,
                    required=True,
                    default=1,
                    help='Max IOs possible')

parser.add_argument('--ioSize',
                    required=True,
                    help='Size of the IOs for calculating latency.')

parser.add_argument('--directory',
                    required=True,
                    help='Directory at which read/writes need to be made.')

args = parser.parse_args()

try:
    handler = logging.StreamHandler()
    logger.addHandler(handler)
    if not sys.stderr.isatty():
        handler.setFormatter(logging.Formatter('%(asctime)s %(message)s'))
    logger.setLevel(logging.DEBUG)


    # completion latency of a single direct IO request of RS_min_seq_IO
    latency_max_BW = get_latency(args.directory, args.maxIops, args.ioSize, False)
    logger.info(f'Measured {latency_max_BW}s latency for a sequential request of size {args.ioSize}kB for disk at LUN {args.directory}.')

    # completion latency of a single direct IO request of the smallest expected size
    latency_base_rand = get_latency(args.directory, args.maxIops, 8, True)
    logger.info(f'Measured {latency_base_rand}s latency for a random request of size 8kB for disk at LUN {args.directory}.')

except Exception as e:
    logger.critical('An exception was encountered while calculating the desired block device settings. As such, no settings can be recommended', exc_info=e)