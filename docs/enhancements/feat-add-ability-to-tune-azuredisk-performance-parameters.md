# Add ability to tune azuredisk performance parameters

## Table of Contents

<!-- toc -->
- [Summary](#summary)
- [Motivation](#motivation)
- [Proposal](#proposal)
  - [Tuning Mode And Profile](#tuning-mode-and-profile)
  - [Scope Of the Change](#scope-of-the-change)
- [Limitations](#limitations)
- [Future Considerations](#future-considerations)
<!-- /toc -->

## Summary

Persistent volumes in kubernetes are used for wide variety of statefull workloads.
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

Azure Disk CSI driver will read two extra flags from the storageclass, perftuningmode and perfProfile.

- *perftuningmode*: Can be set to `Auto` or `None`. In `Auto` mode, CSI driver will automatically
calculate the optimal configuration for the disk. Users will be able to control some characterstics of the
optimization by selecting different `perfprofile`. The driver defaults to `None`, which means no optimization
is done. Please read the limitations to fully understand the options available today.
- *perfprofile*: Users will be able to select a certain performance profile for their device to match their
workload needs. For ex: we will expose different profile options to let users configure the disks for balanced
IO, high throughput or high IOPS etc. Please read the limitations to fully understand the options available today.

### Tuning Mode And Profile

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: sc-kubestone-perf-optimized-premium-ssd-csi
provisioner: disk.csi.azure.com
parameters:
  skuname: Premium_LRS
  perftuningmode: Auto
  perfprofile: Default
reclaimPolicy: Delete
volumeBindingMode: Immediate
allowVolumeExpansion: true
```

### Scope Of the Change

In this iteration we will only enable the feature for StandardSSD and Premium disks.

## Limitations

- This feature is not supported for HDD or UltraDisk right now.
- No `manual` perftuningmode available today.
- Only `Default` perfprofile is available today, which would provide balanced IO and throughput performance.

## Future Considerations

- We will consider a `manual` perf tuning mode where users will be able to define their own perf profile by means
of a CRD or a ConfigMap.
- We will consider to expose more perf profiles for `Auto` perf tuning mode, tailor made for different IO characterstics.
- We will consider to expand perf optimization for HDD and UltraDisks.
