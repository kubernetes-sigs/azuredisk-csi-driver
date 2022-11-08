## UnpublishVolume


```mermaid
    sequenceDiagram      
    %% setup  
        participant driver as Azure Disk V2 (controllerserver_v2.go) 
        participant crdProvisioner as CRD provisioner (crdprovisioner.go)
        participant azVolAttController as AzVolumeAttachment controller (attach_detach.go/replica.go)
        participant cloudProvisioner as Cloud provisioner (cloudprovisioner.go)
        participant azure as Azure Cloud Provider
            
        loop informer
            azVolAttController->>azVolAttController: watches all events on AzVolumeAtt CRIs
        end

        Note over driver, azure: GRPC call: /csi.v1.Controller/ControllerUnpublishVolume

    %% no replicas case
        Note over driver, azure: Volume without replicas ( maxshares = 1) 

    %% request
        driver->> crdProvisioner: unpublish volume

        crdProvisioner -->> azVolAttController: updates AzVolumeAtt with "volume-detach-request" annotation

        loop 
            crdProvisioner ->> crdProvisioner: waits for AzVolumeAtt CRI deletion or failure
        end

        azVolAttController ->> cloudProvisioner: sees detach annotation, calls unpubish volume

        cloudProvisioner ->> azure: detach volume

    %% response
        azure ->> cloudProvisioner: success

        cloudProvisioner ->> azVolAttController: success

        azVolAttController ->> azVolAttController: deletes AzVolumeAtt CRI, removes finalizer

        azVolAttController ->> crdProvisioner: AzVolumeAtt CRI gets deleted

        crdProvisioner -->> driver: sees azvolumeatt CRI deletion, stops the wait and responds with success


    %% replicas case
        Note over driver, azure: Volume with replicas ( maxshares > 1) 

    %% request
        driver ->> crdProvisioner: unpublish volume

        crdProvisioner -->> azVolAttController: updates AzVolumeAtt role to replica

    %% earlly response
        crdProvisioner ->> driver: responds with success

    %% garbage collection
        loop
            azVolAttController -->> azVolAttController: waits 5min before updating all replica AzVolumeAtt CRIs for the volume with "volume-detach-request" annotation
        end

        azVolAttController ->> cloudProvisioner: sees detach annotation, calls unpubish volume

        cloudProvisioner ->> azure: detach volume

    %% response
        azure ->> cloudProvisioner: success

        cloudProvisioner ->> azVolAttController: success

        azVolAttController ->> azVolAttController: deletes AzVolumeaAtt CRI, removes finalizer