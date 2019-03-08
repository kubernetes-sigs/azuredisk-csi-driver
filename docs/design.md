# azuredisk CSI driver design goals
 > azuredisk CSI driver should be implemented as compatitable as possible with built-in [azuredisk](https://kubernetes.io/docs/concepts/storage/volumes/#azuredisk) plugin, it has following goals:

Goal | Status | Notes
--- | --- | --- |
Support Kubernetes release 1.12 or later | In Progress | release prior to 1.12 won't be supported |
Support service principal and msi authentication | Completed | need verification on msi |
Support both Linux & Windows | In Progress | Windows related work is in progress: [Enable CSI hostpath example on windows](https://github.com/kubernetes-csi/drivers/issues/79) |
Compatible with original storage class parameters and usage| Completed | there is a little difference in static provision, see [example](../deploy/example/pv-azuredisk-csi.yaml) |
Support sovereign cloud| done | verification pass on Azure China |
Support unmanaged(blob based) azure disk| Completed | need verification |

### Work items
Item | Status | Notes
--- | --- | --- |
Support volume size grow | Pending | Not ready on upstream: [Feature request: CSI plugin support expand volume size](https://github.com/kubernetes/kubernetes/issues/62096) |
Support raw block size | to-do | upstream feature is beta in 1.14: [Add resizing support to CSI volumes](https://github.com/kubernetes/enhancements/issues/556)|
Support snapshot | to-do | upstream feature is beta in 1.14: [Snapshot / Restore Volume Support for Kubernetes (CRD + External Controller) ](https://github.com/kubernetes/enhancements/issues/177) |
Enable CI on Windows | done |  |
Complete all unit tests | done | https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/7 |
Set up sanity test | In Progress | https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/9 |
Set up E2E test | In Progress | https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/6 |
Support zone | to-do | need verification since csi has different zone usage compared to original usage |
Implement azure disk csi driver on Windows | to-do | https://github.com/kubernetes-sigs/azuredisk-csi-driver/issues/45 |

### Implementation details
To prevent possible regression issues, azuredisk CSI driver use [azure cloud provider](https://github.com/kubernetes/kubernetes/tree/v1.13.0/pkg/cloudprovider/providers/azure) library. Thus, all bug fixes in the built-in azure disk plugin would be incorporated into this driver.
