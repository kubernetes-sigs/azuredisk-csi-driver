# Azure Disk CSI Driver Debugging

## Control Plane Debugging

Creating, deleting, resizing, attaching and detaching disks and creating and deleting snapshots are control plane operations handled by the Azure Disk CSI Driver controller. The controller is typically deployed with multiple replica pods in an active-passive mode. The active, or leader, pod performs the control plane operations and is indicated via a [Lease](https://kubernetes.io/docs/concepts/architecture/leases/) object. Sidecar containers deployed in the CSI controller pod interact with the Kubernetes API server to orchestrate the control plane operations. Since each sidecar handles a subset of the operations, the leader may be different for each grouping of operations. The table below describes the sidecars and the operations they perform.

| Sidecar         | Operations                  | Lease Prefix                  | Hosting Identity  |
|-----------------|-----------------------------|-------------------------------|-------------------|
| csi-provisioner | Create and Delete Disks     | *none*                        | unique identifier |
| csi-attacher    | Attach and Detach Disks     | `external-attacher-leader-`   | node name         |
| csi-resizer     | Resize Disks                | `external-resizer-`           | node name         |
| csi-snapshotter | Create and Delete Snapshots | `external-snapshotter=leader` | node name         |

To debug issues with a control plane operation, you will first need to find the leader controller pod. The following example finds the leader controller pod for disk attach and detach operations.

```bash
# Set the driver name. This is usually "disk.csi.azure.com" but could be different in an alternate deployment.
export DRIVERNAME=disk.csi.azure.com
# Choose the appropriate prefix from the table above.
export LEASEPREFIX=external-attacher-leader-
# Find the leader hosting identity
export HOSTINGIDENTITY=$(kubectl get lease -n kube-system "${LEASEPREFIX}${DRIVERNAME//./-}" -o json | jq -r '.spec.holderIdentity')
# Find the leader pod name
export LEADERPOD=$(kubectl get pods -n kube-system -l app=csi-azuredisk-controller -o json | jq -r '.items[] | select(.spec.nodeName == "'${HOSTINGIDENTITY}'").metadata.name')
```

Once the leader controller pod is found, you can then get its description and logs.

```bash
kubectl describe pod "${LEADERPOD}" -n kube-system > csi-azuredisk-controller-description.txt
kubectl logs "${LEADERPOD}" -c azuredisk -n kube-system > csi-azuredisk-controller.log
```

### V2 Control Plane Debugging

Azure Disk CSI Driver V2 uses operators and custom resources to orchestrate control plane operations. (See the [Azure Disk CSI Driver V2 Design](design-v2.md) document for details.) It also adds a scheduler extender to influence pod scheduling when mount replicas are used.

#### Operator Debugging

Although the controller pod is still the entry point for CSI requests, operators do much of the work for control plane operations. The following example shows how to find the leader operator pod.

> **NOTE:** The operators are built in to and deployed as part of the controller deployment. The leader operator and controller pods may be the same.

```bash
# Note that the hosting identity is a concatenation of the node name and a unique identifier.
# The following command removes the unique identifier suffix.
export HOSTINGIDENTITY=$(kubectl get lease -n kube-system "csi-azuredisk-controller" -o json | jq -r '.spec.holderIdentity | split("_")[0]')
# Find the leader pod name
export LEADERPOD=$(kubectl get pods -n kube-system -l app=csi-azuredisk-controller -o json | jq -r '.items[] | select(.spec.nodeName == "'${HOSTINGIDENTITY}'").metadata.name')
```

Once the leader controller pod is found, you can then get its description and logs.

```bash
kubectl describe pod "${LEADERPOD}" -n kube-system > csi-azuredisk-controller-description.txt
kubectl logs "${LEADERPOD}" -c azuredisk -n kube-system > csi-azuredisk-controller.log
```

The [az-log](../cmd/az-log) tool can also be used to obtain the leader controller pod logs.

Since the V2 controller uses custom resources to orchestrate control plane operations, inspecting the them can be helful as well. The [az-analyze](../cmd/az-analyze/) tool can be used to get the custom resource instances.

#### Scheduler Extender Debugging

> TBD

## Node Debugging

Once the controller has attached the disk to a node, the node driver mounts the file system and creates the symbolic links to make it accessible to its pod. Use the following command to list all of the node driver pods.

```bash
$ kubectl get pods -n kube-system -l app=csi-azuredisk-node -o wide

NAME                       READY   STATUS    RESTARTS   AGE     IP             NODE                        NOMINATED NODE   READINESS GATES
csi-azuredisk-node-9w44c   3/3     Running   0          5h13m   10.240.255.5   k8s-master-41146550-0       <none>           <none>
csi-azuredisk-node-dvg4t   3/3     Running   0          5h13m   10.240.0.4     k8s-agentpool1-41146550-1   <none>           <none>
csi-azuredisk-node-gtr6h   3/3     Running   0          5h13m   10.240.0.6     k8s-agentpool1-41146550-0   <none>           <none>
csi-azuredisk-node-qc4gk   3/3     Running   0          5h13m   10.240.0.5     k8s-agentpool1-41146550-2   <none>           <none>
```

To debug mount-related issues, first find the node to which the workload has been deployed. Then, use the following command to get the name of the node driver.

```bash
# Choose the desired node to debug. We choose one from the table above as an example.
export DRIVERNODE=k8s-agentpool1-41146550-1
export DRIVERPOD=$(kubectl get pods -n kube-system -l app=csi-azuredisk-node -o json | jq -r '.items[] | select(.spec.nodeName == "'${DRIVERNODE}'").metadata.name')
```

Once the node driver pod is found, you can then get its description and logs.

```bash
kubectl describe pod "${DRIVERPOD}" -n kube-system > csi-azuredisk-node-description.txt
kubectl logs "${DRIVERPOD}" -c azuredisk -n kube-system > csi-azuredisk-node.log
```

### Linux Node Debugging

Use the following command to inspect the disk mounts inside the driver node pod.

```bash
kubectl exec -it csi-azuredisk-node-dvg4t -n kube-system -c azuredisk -- mount | grep "/dev/sd"
```

The following command checks DNS resolution inside the driver node pod.

```bash
kubectl exec -it csi-azuredisk-node-dvg4t -n kube-system -c azuredisk -- apt update && apt install curl -y && curl https://microsoft.com -k -v 2>&1
```

> **NOTE:** Azure Disk Driver V2 uses Alpine Linux as its base container image. Use the following command with the V2 driver to check DNS resolution.

```bash
kubectl exec -it csi-azuredisk-node-dvg4t -n kube-system -c azuredisk -- apk add --no-cache curl && curl https://microsoft.com -k -v 2>&1
```

Use the following command to get the Azure cloud configuration file on a Linux node.

```bash
kubectl exec -it "${DRIVERPOD}" -n kube-system -c azuredisk -- cat /etc/kubernetes/azure.json
```

### Windows Node Debugging

Use the following command to get the Azure cloud configuration file on a Windows node.

```bash
kubectl exec -it "${DRIVERPOD}" -n kube-system -c azuredisk -- cmd type c:\k\azure.json
```

Use the following command to get the CSI Proxy log on a Windows node.

```bash
kubectl exec -it "${DRIVERPOD}" -n kube-system -c azuredisk -- cmd type :\k\csi-proxy.err.log
```

## Advanced Debugging

Azure Disk Driver V2 supports an optional pprof server for advanced debugging using the [pprof](https://github.com/google/pprof) visualization tool.

To enable pprof server for the controller/operator, node driver or scheduler extender, override the corresponding of the Helm chart value described in the table below when deploying the driver.

| Value | Description | Default |
|-|-|-|
| controller.profiler.port | The port number for the controller/operator pod to listen on for pprof requests. | |
| node.profiler.port | The port number for the node driver pod to listen on for pprof requests. | |
| schedulerExtender.profiler.port | The port number for the scheduler extender pod to listen on for pprof requests. | |

When not using Helm, the profiler address can be specified through the `--profiler-port` command-line parameter on the azuredisk or scheduler extender container.

Once set up, you can use port forwarding to access the server.

```bash
# Set the profiler port number.
export PROFILERPORT=6060
# Start port forwarding to the target pod. See above to learn how to find the desired pod.
kubectl port-forward "${POD}" -n kube-system ${PROFILERPORT}:${PROFILERPORT} &
# Get the current memory profile using the pprof visualization tool
go tool pprof http://localhost:${PROFILERPORT}/debug/pprof/heap
```

## Additional References

- [Errors when mounting Azure disk volumes](https://docs.microsoft.com/en-us/troubleshoot/azure/azure-kubernetes/fail-to-mount-azure-disk-volume)
