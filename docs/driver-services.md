### `disk.csi.azure.com` driver services
 
Traditionally, you run the CSI controllers with the Azure Disk driver in the same Kubernetes cluster.
Though, there may be cases where you will only want to run a subset of the available driver services (for example, one scenario is running the controllers outside of the cluster they are serving (while the Azure Disk driver still runs inside the served cluster), but there might be others scenarios).
The CSI driver consists out of these services:

* The **controller** service starts the GRPC server that serves `CreateVolume`, `DeleteVolume`, etc. It is depending on the Azure cloud config + credentials and talks with the Azure API.
* The **identity** service is responsible to provide identity services like capability information of the CSI plugin.
* The **node** service implements the various operations for volumes that are run locally from the node, for example `NodePublishVolume`, `NodeStageVolume`, etc. It does not do operations like `CreateVolume` or `ControllerPublish`. Also, it runs directly on the Azure VM instances. It is depending on the Azure cloud config but does not necessarily need to have the credentials.

The CSI driver has two command line flags, `--run-controller-service` and `--run-node-service` which both default to `true`.
You can disable the individual services by setting the respective flags to `false`.
