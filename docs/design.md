# azuredisk CSI driver design goals
 > azuredisk CSI driver should be implemented as compatitable as possible with built-in [azuredisk](https://kubernetes.io/docs/concepts/storage/volumes/#azuredisk) plugin, it has following goals:

Goal | Status | Notes
--- | --- | --- |
Support service principal and msi authentication | Completed | need verification on msi |
Support both Linux & Windows | In Progress | Windows related work is in progress: [Enable CSI hostpath example on windows](https://github.com/kubernetes-csi/drivers/issues/79) |
Compatible with original storage class parameters and usage| Completed | there is a little difference in static provision, see [example](../deploy/example/pv-azuredisk-csi.yaml) |
Support sovereign cloud| Completed | need verification |

### Other work items
work item | Status | Notes
--- | --- | --- |
Support volume size grow | to-do |  |
Support block size | to-do |  |
Support snapshot | to-do |  |
Enable CI on Windows | to-do |  |
Complete all unit tests | In Progress |  |
Set up E2E test | to-do |  |
Support zone | to-do | need verification since csi has different zone usage compared to original usage |
Implement azure disk csi driver on Windows | to-do |  |

### Implementation details
To prevent possible regression issues, azuredisk CSI driver use [azure cloud provider](https://github.com/kubernetes/kubernetes/tree/v1.13.0/pkg/cloudprovider/providers/azure) library. Thus, all bug fixes in the built-in azure disk plugin would be incorporated into this driver.
