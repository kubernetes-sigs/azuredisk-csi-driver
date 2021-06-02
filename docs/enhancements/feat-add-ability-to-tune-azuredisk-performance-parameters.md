# Add ability to tune azuredisk performance parameters

## Table of Contents

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
  - [Perf Profile](#perf-profile)
  - [Scope Of the Change](#scope-of-the-change)
- [Limitations](#limitations)
<!-- /toc -->

## Summary

Persistent volumes in kubernetes are used for wide variety of stateful workloads.
These workloads have different runtime/IO characterstics, certain device level config settings
on the data disk can make a huge difference in performance of the application.

Azure storage publishes [guidelines](https://docs.microsoft.com/en-us/azure/virtual-machines/premium-storage-performance)
for the applications to configure the disks' guest OS settings to drive maximum IOPS and Bandwidth.

Azure Disk CSI driver users currently do not have an easy way to tune disk device configuration to
get better performance out of them for their workloads.

This proposal proposes to provide users with ability to enable automatic perf optimization of data
disks by tweaking guest OS level device/disk/driver parameters.

## Motivation

As the adoption of the Azure Disk CSI driver increases, we will encounter different type
of workloads which have different disk IO chracterstics. Some may require to drive the data
disks to maximum IOPS, while others may need to do larger size writes and drive maximum throughput.

Azure Disk CSI driver should evolve to let users tune the data disks configurations, to get optimal
performance for their workloads.

With this feature, we try to provide a configurable and handsfree way for the users to enable
perf optimizations of data disk by enabling a feature in storageclass and be able to select from
multiple optimization profiles based on their requirement.

We intend to provide a way for the users to opt-in for the new behavior and expect not to break
any existing applications/configurations.

## Proposal

Azure Disk CSI driver will read one extra parameter from the storageclass, `perfProfile`.

- *perfprofile*: Users will be able to select a certain performance profile for their device to match their
workload needs. In the beginning we will expose `Basic` profile which sets the disks for balanaced IO and throughput
workloads. If users want to keep this feature disabled they can skip adding `perfProfile` parameter in the storage class
or set `perfProfile` to `None`. If `perfProfile` is set to an unknown value, it will result in CreateVolume failure.
Please read the limitations to fully understand the options available today.

### Perf Profile

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: sc-kubestone-perf-optimized-premium-ssd-csi
provisioner: disk.csi.azure.com
parameters:
  skuName: Premium_LRS
  perfProfile: Basic # available values: None(by default), Basic. These are case insensitive.
reclaimPolicy: Delete
volumeBindingMode: Immediate
allowVolumeExpansion: true
```

### Scope Of the Change

In this iteration we will only enable the feature for StandardSSD and Premium disks.

## Limitations

- This feature is not supported for HDD or UltraDisk right now.
- This feature only optimizes data disks (PVs). Local/temp disks on the VM are not optimized by this feature.
- The current implementation only optimizes the disks which use the storVsc linux disk driver.
- Only `Basic` `perfProfile` is available today, which would provide balanced IO and throughput performance.
