## Azure Disk volume mount recovery in node shutdown scenario

**Issue details**:

When agent node is shutdown(deallocated), pods would be evicted from the `NotReady` node, while disk volume could not be unmounted(pod with in `Terminating` state forever) due to following upstream issues:
   - [Propose to taint node "shutdown" condition](https://github.com/kubernetes/kubernetes/issues/58635)
   - [add node shutdown KEP](https://github.com/kubernetes/enhancements/pull/1116)
   - [Non Graceful node shutdown](https://kubernetes.io/docs/concepts/architecture/nodes/#non-graceful-node-shutdown)

**Work around**:
> get example [here](./statefulset-azuredisk.yaml)

Use following config in your deployment or statefulset, total recover time would be around 8min for pod with volume mount move from `NotReady` node to `Ready` node. 
 - [`tolerations`](https://kubernetes.io/docs/concepts/scheduling-eviction/taint-and-toleration/#taint-based-evictions) config could make sure when agent node is in `NotReady` state, pod would be evicted after `10s` (default: `300s`)
 - `terminationGracePeriodSeconds: 0` config could make sure volume would be leave unmounted when pod is removed from the node(pod would be terminated immediately)

```yaml
    spec:
      tolerations:
      - key: "node.kubernetes.io/not-ready"
        operator: "Exists"
        effect: "NoExecute"
        tolerationSeconds: 10
      - key: "node.kubernetes.io/unreachable"
        operator: "Exists"
        effect: "NoExecute"
        tolerationSeconds: 10
      terminationGracePeriodSeconds: 0
```

The 8 min recover time is composed of:
 - 1 min: agent node changed to `NotReady`
 - 6 min: [`ReconcilerMaxWaitForUnmountDuration`](https://github.com/kubernetes/kubernetes/blob/3f579d8971fcce96d6b01b968a46c720f10940b8/pkg/controller/volume/attachdetach/attach_detach_controller.go#L94) timeout
 - 1 min: disk volume detached from `NotReady` node and attached to `Ready` node

There is a [KEP](https://github.com/kubernetes/enhancements/pull/1116) to address this issue, hopefully `ReconcilerMaxWaitForUnmountDuration` timeout is removed, and target recover time would be around 2min.

**long term solution**
 - [implement Non Graceful node shutdown feature in CCM](https://github.com/kubernetes-sigs/cloud-provider-azure/issues/3269)
