# Azure Disk CSI Driver V2

The Azure Disk CSI Driver V2 enhances the Azure Disk CSI Driver to improve scalability and reduce pod failover latency. It uses shared disks to provision attachment replicas on multiple cluster nodes and integrates with the pod scheduler to ensure a node with a attachment replica is chosen on pod failover. It is beneficial for both single zone use case as well as multi zone use case that uses [Zone Redundant Disks](https://docs.microsoft.com/en-us/azure/virtual-machines/disks-redundancy#zone-redundant-storage-for-managed-disks). This demo is based on [this guide](https://github.com/mohmdnofal/aks-best-practices/blob/master/stateful_workloads/zrs/README.md)

# Azure Disk CSI Driver V2 with ZRS Demo Introduction 
In this demo we will create a 3 node cluster distributed across 3 availability zones, deploy a single mysql pod, ingest some data there, and then drain the node hosing the pod. This means that we took one node offline, triggering the scheduler to migrate your pod to another node. 

# Demo

1. Create the cluster 
```shell 

#Set the parameters
LOCATION=northeurope # Location 
AKS_NAME=az-zrs
RG=$AKS_NAME-$LOCATION
AKS_CLUSTER_NAME=$AKS_NAME-cluster # name of the cluster
K8S_VERSION=$(az aks get-versions  -l $LOCATION --query 'orchestrators[-1].orchestratorVersion' -o tsv)


##Create RG
az group create --name $RG --location $LOCATION


## create the cluster 
az aks create \
-g $RG \
-n $AKS_CLUSTER_NAME \
-l $LOCATION \
--kubernetes-version $K8S_VERSION \
--zones 1 2 3 \
--generate-ssh-keys 


## get the credentials 

az aks get-credentials -n $AKS_CLUSTER_NAME -g $RG

## verify access to the cluster

kubectl get nodes  

NAME                                STATUS   ROLES   AGE   VERSION
aks-nodepool1-20996793-vmss000000   Ready    agent   77s   v1.21.2
aks-nodepool1-20996793-vmss000001   Ready    agent   72s   v1.21.2
aks-nodepool1-20996793-vmss000002   Ready    agent   79s   v1.21.2
```

2. Install the Azure Disk CSI Driver v2

As of K8s 1.21, you can see the default storage class now pointing to the Azure Disk CSI Driver V1. To demonstrate the Azure Disk CSI Driver V2, we will install it side-by-side with the V1 driver. Tips for troubleshooting helm installation [here](https://github.com/kubernetes-sigs/azuredisk-csi-driver/blob/668a54a797fd90f015ce2b89ff2fbac2d0a4600b/charts/README.md).

```shell
helm install azuredisk-csi-driver-v2 charts/v2.0.0-alpha.1/azuredisk-csi-driver --namespace kube-system --wait --timeout=15m -v=5 \
        --set snapshot.enabled=true \
        --set snapshot.createCRDs=false \
        --set snapshot.name="csi-azuredisk2-snapshot-controller" \
        --set snapshot.snapshotController.name="csi-azuredisk2-snapshot-controller" \
        --set driver.name="disk2.csi.azure.com" \
        --set controller.name="csi-azuredisk2-controller" \
        --set rbac.name=azuredisk2 \
        --set serviceAccount.controller=csi-azuredisk2-controller-sa \
        --set serviceAccount.node=csi-azuredisk2-node-sa \
        --set linux.dsName=csi-azuredisk2-node \
        --set windows.dsName=csi-azuredisk2-node-win \
        --set serviceAccount.snapshotController="csi2-snapshot-controller-sa" \
        --set image.azuredisk.pullPolicy=Always \
        --set image.schedulerExtender.pullPolicy=Always \
        --set storageClasses.enableZRS=true \
        --set controller.metricsPort=29614 \
        --set controller.livenessProbe.healthPort=29612 \
        --set node.metricsPort=29615 \
        --set node.livenessProbe.healthPort=29613 \
        --set schedulerExtender.metricsPort=29616 \
        --set schedulerExtender.servicePort=8899
```


Verify that the new storage classes were created
```shell

kubectl get storageclasses.storage.k8s.io 

NAME                    PROVISIONER                RECLAIMPOLICY   VOLUMEBINDINGMODE      ALLOWVOLUMEEXPANSION   AGE
azurefile               kubernetes.io/azure-file   Delete          Immediate              true                   2m30s
azurefile-csi           file.csi.azure.com         Delete          Immediate              true                   2m30s
azurefile-csi-premium   file.csi.azure.com         Delete          Immediate              true                   2m30s
azurefile-premium       kubernetes.io/azure-file   Delete          Immediate              true                   2m30s
csi-v2                  disk2.csi.azure.com        Delete          Immediate              true                   1m30s
default (default)       disk.csi.azure.com         Delete          WaitForFirstConsumer   true                   2m30s
managed                 kubernetes.io/azure-disk   Delete          WaitForFirstConsumer   true                   2m30s
managed-csi-premium     disk.csi.azure.com         Delete          WaitForFirstConsumer   true                   2m30s
managed-premium         kubernetes.io/azure-disk   Delete          WaitForFirstConsumer   true                   2m30s
```

To benefit from the faster pod failover through replica mount feature of the Azure Disk CSI V2 driver, we will create a new storage class that sets the parameter for `maxShares`. 

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: azuredisk-standard-ssd-zrs-replicas
parameters:
  cachingmode: None
  skuName: StandardSSD_ZRS
  maxShares: "2"
provisioner: disk.csi.azure.com
reclaimPolicy: Delete
volumeBindingMode: Immediate
allowVolumeExpansion: true
```

4. Create mysql statefulset using volumes provisioned by the V2 driver 
- This deployment is based on [this guide](https://kubernetes.io/docs/tasks/run-application/run-replicated-stateful-application/)
- The statefulset was modified to use the csi-v2 class and scheduler extender
- The config map and service deployments can be taken straight from the guide
<details>
  <summary> Statefulset YAML Details </summary>

  ```yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: mysql
spec:
  selector:
    matchLabels:
      app: mysql
  serviceName: mysql
  replicas: 1
  template:
    metadata:
      labels:
        app: mysql
    spec:
      schedulerName: csi-azuredisk-scheduler-extender
      initContainers:
      - name: init-mysql
        image: mysql:5.7
        command:
        - bash
        - "-c"
        - |
          set -ex
          # Generate mysql server-id from pod ordinal index.
          [[ `hostname` =~ -([0-9]+)$ ]] || exit 1
          ordinal=${BASH_REMATCH[1]}
          echo [mysqld] > /mnt/conf.d/server-id.cnf
          # Add an offset to avoid reserved server-id=0 value.
          echo server-id=$((100 + $ordinal)) >> /mnt/conf.d/server-id.cnf
          # Copy appropriate conf.d files from config-map to emptyDir.
          if [[ $ordinal -eq 0 ]]; then
            cp /mnt/config-map/primary.cnf /mnt/conf.d/
          else
            cp /mnt/config-map/replica.cnf /mnt/conf.d/
          fi          
        volumeMounts:
        - name: conf
          mountPath: /mnt/conf.d
        - name: config-map
          mountPath: /mnt/config-map
      - name: clone-mysql
        image: gcr.io/google-samples/xtrabackup:1.0
        command:
        - bash
        - "-c"
        - |
          set -ex
          # Skip the clone if data already exists.
          [[ -d /var/lib/mysql/mysql ]] && exit 0
          # Skip the clone on primary (ordinal index 0).
          [[ `hostname` =~ -([0-9]+)$ ]] || exit 1
          ordinal=${BASH_REMATCH[1]}
          [[ $ordinal -eq 0 ]] && exit 0
          # Clone data from previous peer.
          ncat --recv-only mysql-$(($ordinal-1)).mysql 3307 | xbstream -x -C /var/lib/mysql
          # Prepare the backup.
          xtrabackup --prepare --target-dir=/var/lib/mysql          
        volumeMounts:
        - name: data
          mountPath: /var/lib/mysql
          subPath: mysql
        - name: conf
          mountPath: /etc/mysql/conf.d
      containers:
      - name: mysql
        image: mysql:5.7
        env:
        - name: MYSQL_ALLOW_EMPTY_PASSWORD
          value: "1"
        ports:
        - name: mysql
          containerPort: 3306
        volumeMounts:
        - name: data
          mountPath: /var/lib/mysql
          subPath: mysql
        - name: conf
          mountPath: /etc/mysql/conf.d
        resources:
          requests:
            cpu: 500m
            memory: 1Gi
        livenessProbe:
          exec:
            command: ["mysqladmin", "ping"]
          initialDelaySeconds: 30
          periodSeconds: 10
          timeoutSeconds: 5
        readinessProbe:
          exec:
            # Check we can execute queries over TCP (skip-networking is off).
            command: ["mysql", "-h", "127.0.0.1", "-e", "SELECT 1"]
          initialDelaySeconds: 5
          periodSeconds: 2
          timeoutSeconds: 1
      - name: xtrabackup
        image: gcr.io/google-samples/xtrabackup:1.0
        ports:
        - name: xtrabackup
          containerPort: 3307
        command:
        - bash
        - "-c"
        - |
          set -ex
          cd /var/lib/mysql

          # Determine binlog position of cloned data, if any.
          if [[ -f xtrabackup_slave_info && "x$(<xtrabackup_slave_info)" != "x" ]]; then
            # XtraBackup already generated a partial "CHANGE MASTER TO" query
            # because we're cloning from an existing replica. (Need to remove the tailing semicolon!)
            cat xtrabackup_slave_info | sed -E 's/;$//g' > change_master_to.sql.in
            # Ignore xtrabackup_binlog_info in this case (it's useless).
            rm -f xtrabackup_slave_info xtrabackup_binlog_info
          elif [[ -f xtrabackup_binlog_info ]]; then
            # We're cloning directly from primary. Parse binlog position.
            [[ `cat xtrabackup_binlog_info` =~ ^(.*?)[[:space:]]+(.*?)$ ]] || exit 1
            rm -f xtrabackup_binlog_info xtrabackup_slave_info
            echo "CHANGE MASTER TO MASTER_LOG_FILE='${BASH_REMATCH[1]}',\
                  MASTER_LOG_POS=${BASH_REMATCH[2]}" > change_master_to.sql.in
          fi

          # Check if we need to complete a clone by starting replication.
          if [[ -f change_master_to.sql.in ]]; then
            echo "Waiting for mysqld to be ready (accepting connections)"
            until mysql -h 127.0.0.1 -e "SELECT 1"; do sleep 1; done

            echo "Initializing replication from clone position"
            mysql -h 127.0.0.1 \
                  -e "$(<change_master_to.sql.in), \
                          MASTER_HOST='mysql-0.mysql', \
                          MASTER_USER='root', \
                          MASTER_PASSWORD='', \
                          MASTER_CONNECT_RETRY=10; \
                        START SLAVE;" || exit 1
            # In case of container restart, attempt this at-most-once.
            mv change_master_to.sql.in change_master_to.sql.orig
          fi

          # Start a server to send backups when requested by peers.
          exec ncat --listen --keep-open --send-only --max-conns=1 3307 -c \
            "xtrabackup --backup --slave-info --stream=xbstream --host=127.0.0.1 --user=root"          
        volumeMounts:
        - name: data
          mountPath: /var/lib/mysql
          subPath: mysql
        - name: conf
          mountPath: /etc/mysql/conf.d
        resources:
          requests:
            cpu: 100m
            memory: 100Mi
      volumes:
      - name: conf
        emptyDir: {}
      - name: config-map
        configMap:
          name: mysql
  volumeClaimTemplates:
  - metadata:
      name: data
    spec:
      accessModes: ["ReadWriteOnce"]
      storageClassName: csi-v2
      resources:
        requests:
          storage: 256Gi
  ```
</details>

```shell 
## create the configmap 
kubectl apply -f mysql-configmap.yaml

## create the headless service 
kubectl apply -f mysql-services.yaml

## create the statefulset 
kubectl apply -f mysql-statefulset.yaml

## check that 2 services were created (headless one for the statefulset and mysql-read for the reads) 
kubectl get svc -l app=mysql  

NAME         TYPE        CLUSTER-IP     EXTERNAL-IP   PORT(S)    AGE
mysql        ClusterIP   None           <none>        3306/TCP   5h43m
mysql-read   ClusterIP   10.0.205.191   <none>        3306/TCP   5h43m

## check the deployment (wait a bit until its running)
kubectl get pods -l app=mysql --watch

NAME      READY   STATUS    RESTARTS   AGE
mysql-0   2/2     Running   0          6m34s
```

- now that the DB is running lets inject some data so we later can simulate failures
```shell 
## create a database called v2test and a table called "messages", then inject a record in the database 

kubectl run mysql-client --image=mysql:5.7 -i --rm --restart=Never --\
  mysql -h mysql-0.mysql <<EOF
CREATE DATABASE v2test;
CREATE TABLE v2test.messages (message VARCHAR(250));
INSERT INTO v2test.messages VALUES ('Hello from V2');
EOF

If you don\'t see a command prompt, try pressing enter.
pod "mysql-client" deleted
## it may take a moment for the final delete to appear
## validate the data exist 
kubectl run mysql-client --image=mysql:5.7 -i -t --rm --restart=Never --\
  mysql -h mysql-read -e "SELECT * FROM v2test.messages"

+----------------+
| message        |
+----------------+
| Hello from V2  |
+----------------+
pod "mysql-client" deleted
```

5. Simulate failure by cordoning and draining a node 

```shell 
## when we created our cluster we activated the availability zones feature, as we created 3 nodes, we should see that they are equally split across AZs 
kubectl describe nodes | grep -i topology.kubernetes.io/zone

                    topology.kubernetes.io/zone=northeurope-1
                    topology.kubernetes.io/zone=northeurope-2
                    topology.kubernetes.io/zone=northeurope-3

## lets check in which node our pods is running 
kubectl get pods -l app=mysql -o wide 
NAME      READY   STATUS    RESTARTS   AGE   IP           NODE                                NOMINATED NODE   READINESS GATES
mysql-0   2/2     Running   0          17m   10.244.2.4   aks-nodepool1-20996793-vmss000001   <none>           <none>

## We can see that the pod is running in "aks-nodepool1-20996793-vmss000001", through cordoning and draining the node it will trigger the pod to failover to a new node. So, lets try this out 

kubectl cordon aks-nodepool1-20996793-vmss000001

node/aks-nodepool1-20996793-vmss000001 cordoned

kubectl drain aks-nodepool1-20996793-vmss000001

```

6. At this moment our statefulset should try to restart in a different node in a new zone. With the v1 driver, the pod should be up in just over a minute. 

```shell 
kubectl get pods -l app=mysql --watch -o wide
....
NAME      READY   STATUS    RESTARTS   AGE   IP           NODE                                NOMINATED NODE   READINESS GATES
mysql-0   2/2     Running   0          10m   10.244.0.7   aks-nodepool1-20996793-vmss000002   <none>           <none>

## now that the pods started, lets validate the ZRS magic, we should see the data we injected originally in the pod
kubectl run mysql-client --image=mysql:5.7 -i -t --rm --restart=Never --\
  mysql -h mysql-read -e "SELECT * FROM v2test.messages"


+----------------+
| message        |
+----------------+
| Hello from V2  |
+----------------+
pod "mysql-client" deleted

## This showcases the speed at which the v2 can facilitate pod failover
```