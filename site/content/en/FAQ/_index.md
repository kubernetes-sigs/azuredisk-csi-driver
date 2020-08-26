---
title: FAQ
linkTitle: FAQ
type: docs
menu:
  main:
    weight: 40
---

## What is CSI Driver?

The Container Storage Interface (CSI) is a standard for exposing arbitrary block and file storage systems to containerized workloads on Container Orchestration Systems (COs) like Kubernetes.   
Using CSI third-party storage providers can write and deploy plugins exposing new storage systems in Kubernetes without ever having to touch the core Kubernetes code.


## Goals? 

* Define Kubernetes API for interacting with an arbitrary, third-party CSI volume drivers.
* Define mechanism by which Kubernetes master and node components will securely communicate with an arbitrary, third-party CSI volume drivers.
* Define mechanism by which Kubernetes master and node components will discover and register an arbitrary, third-party CSI volume driver deployed on Kubernetes.
* Recommend packaging requirements for Kubernetes compatible, third-party CSI Volume drivers.
* Recommend deployment process for Kubernetes compatible, third-party CSI Volume drivers on a Kubernetes cluster.

## Non-Goals? 

* Replace [Flex Volume plugin](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-storage/flexvolume.md)
* The Flex volume plugin exists as an exec based mechanism to create “out-of-tree” volume plugins.
* Because Flex drivers exist and depend on the Flex interface, it will continue to be supported with a stable API.
* The CSI Volume plugin will co-exist with Flex volume plugin.