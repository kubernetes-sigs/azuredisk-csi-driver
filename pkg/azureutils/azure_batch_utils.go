package azureutils

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	armcompute "github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5"
	"github.com/edreed/go-batch"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/klogr"
	"k8s.io/utils/pointer"
)

type DiskOperationParams struct {
	Name						*string
	CachingType                 *armcompute.CachingTypes
	CreateOption            	*armcompute.DiskCreateOptionTypes
	DiskEncryptionSetID			*string
	WriteAcceleratorEnabled 	*bool
	DiskURI						*string
	Lun							*int32
	VMName						*string
	Async						*bool
}

const keyAttributesSeparator = "|"

// KeyFromAttributes concatenates the parameters to form a unique key for the referenced resource. Since the
// values are case-insensitive, they are converted to lowercase before concatenation.
func KeyFromAttributes(subscriptionID, resourceGroup, resourceName string) string {
	return strings.Join(
	[]string{strings.ToLower(subscriptionID),
		strings.ToLower(resourceGroup),
		strings.ToLower(resourceName)},
		keyAttributesSeparator,)
}

func NewDiskOperationBatchProcessor(az *Cloud) {
	attachBatch := func(ctx context.Context, key string, values []interface{}) ([]interface{}, error) {
		klog.Infof("here 1")
		disksToAttach := make([]DiskOperationParams, len(values))
		for i, value := range values {
			disksToAttach[i] = value.(DiskOperationParams)
		}

		klog.Infof("here 2")

		lunChans, err := az.attachDiskBatchToNode(ctx, disksToAttach)
		if err != nil {
			return nil, err
		}

		klog.Infof("here 3")

		results := make([]interface{}, len(lunChans))
		for i, lun := range lunChans {
			results[i] = lun
		}

		klog.Infof("here 4")

		return results, nil
	}

	detachBatch := func(ctx context.Context, key string, values []interface{}) ([]interface{}, error) {
		disksToDetach := make([]DiskOperationParams, len(values))
		for i, value := range values {
			disksToDetach[i] = value.(DiskOperationParams)
		}

		err := az.detachDiskBatchFromNode(ctx, disksToDetach)
		if err != nil {
			return nil, err
		}

		return make([]interface{}, len(disksToDetach)), nil
	}

	logger := klogr.NewWithOptions(klogr.WithFormat(klogr.FormatKlog)).WithName("azuredisk-csi-driver").WithValues("type", "batch")

	processorOptions := []batch.ProcessorOption{
		batch.WithVerboseLogLevel(3),
	}

	attachDiskProcessorOptions := append(processorOptions,
		batch.WithLogger(logger.WithValues("operation", "attach_disk")),)

	detachDiskProcessorOptions := append(processorOptions,
		batch.WithLogger(logger.WithValues("operation", "detach_disk")),)
	
	az.DiskOperationBatchProcessor = &DiskOperationBatchProcessor{}
	klog.Infof("processor: %+v", az.DiskOperationBatchProcessor)
	az.DiskOperationBatchProcessor.AttachDiskProcessor = batch.NewProcessor(attachBatch, attachDiskProcessorOptions...)
	az.DiskOperationBatchProcessor.DetachDiskProcessor = batch.NewProcessor(detachBatch, detachDiskProcessorOptions...)
}

func (az *Cloud) attachDiskBatchToNode(ctx context.Context, toBeAttachedDisks []DiskOperationParams) ([]chan (AttachDiskResult), error) {
	lunChans := make([]chan (AttachDiskResult), len(toBeAttachedDisks))
	async := false

	entry, resultErr := az.GetVMSSVM(ctx, *toBeAttachedDisks[0].VMName)
	if resultErr != nil {
		return nil, fmt.Errorf("failed to get VM: %v", resultErr)
	}

	vmss, err := GetLastSegment(*entry.VMSSName, "/")
	if err != nil {
		return nil, err
	}
	
	var disks []*armcompute.DataDisk
	if entry != nil && entry.VM != nil && entry.VM.Properties != nil && entry.VM.Properties.StorageProfile != nil && entry.VM.Properties.StorageProfile.DataDisks != nil {
		disks = entry.VM.Properties.StorageProfile.DataDisks
	} else {
		return nil, fmt.Errorf("failed to get vm's disks")
	}

	attached := false
	usedLuns := make([]bool, len(disks) + 1)
	count := 0
	for _, tbaDisk := range toBeAttachedDisks {
		for _, disk := range disks {
			count++
			if disk.ManagedDisk != nil && strings.EqualFold(*disk.ManagedDisk.ID, strings.ToLower(*tbaDisk.DiskURI)) && disk.Lun != nil {
				if *disk.Lun == *tbaDisk.Lun {
					attached = true
					break
				} else {
					return nil, fmt.Errorf("disk(%s) already attached to node(%s) on LUN(%d), but target LUN is %d", *tbaDisk.DiskURI, *entry.Name, *disk.Lun, *tbaDisk.Lun)
				}
			}

			usedLuns[*disk.Lun] = true
		}

		if attached {
			klog.V(2).Infof("azureDisk - disk(%s) already attached to node(%s) on LUN(%d)", *tbaDisk.DiskURI, entry.Name, *tbaDisk.Lun)
		} else {
			storageProfile := entry.VM.Properties.StorageProfile
			managedDisk := &armcompute.ManagedDiskParameters{ID: tbaDisk.DiskURI}
			if *tbaDisk.DiskEncryptionSetID == "" {
				if storageProfile.OSDisk != nil &&
					storageProfile.OSDisk.ManagedDisk != nil &&
					storageProfile.OSDisk.ManagedDisk.DiskEncryptionSet != nil &&
					storageProfile.OSDisk.ManagedDisk.DiskEncryptionSet.ID != nil {
					// set diskEncryptionSet as value of os disk by default
					tbaDisk.DiskEncryptionSetID = storageProfile.OSDisk.ManagedDisk.DiskEncryptionSet.ID
				}
			}
			if *tbaDisk.DiskEncryptionSetID != "" {
				managedDisk.DiskEncryptionSet = &armcompute.DiskEncryptionSetParameters{ID: tbaDisk.DiskEncryptionSetID}
			}
			newDisk := &armcompute.DataDisk{
				Name:                    tbaDisk.Name,
				Caching:                 tbaDisk.CachingType,
				CreateOption:            tbaDisk.CreateOption,
				ManagedDisk:             managedDisk,
				WriteAcceleratorEnabled: tbaDisk.WriteAcceleratorEnabled,
			}
			if *tbaDisk.Lun != -1 {
				newDisk.Lun = tbaDisk.Lun
			} else {
				for index, used := range usedLuns {
					if !used {
						tbaDisk.Lun = to.Ptr(int32(index))
						newDisk.Lun = to.Ptr(int32(index))
						klog.Infof("lun is -1 or duplicate, new lun is: %+v", index)
						break
					}
				}
			}
			disks = append(disks, newDisk)
		}
		klog.Infof("async: %+v", tbaDisk.Async)
		async = async || *tbaDisk.Async
	}

	newVM := armcompute.VirtualMachineScaleSetVM{
		Properties: &armcompute.VirtualMachineScaleSetVMProperties{
			StorageProfile: &armcompute.StorageProfile{
				DataDisks: disks,
			},
		},
	}

	poller, err := az.VMSSVMClient.BeginUpdate(ctx, az.ResourceGroup, vmss, *entry.InstanceID, newVM, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to finish request: %v", err)
	}

	var resp armcompute.VirtualMachineScaleSetVMsClientUpdateResponse
	attach := func() {
		resultCtx := ctx

		if async {
			// The context, ctx, passed to attachDiskBatchToNode is owned by batch.Processor which will
			// cancel it when we return. Since we're asynchronously waiting for the attach disk result,
			// we must create an independent context passed to WaitForUpdateResult with the deadline
			// provided in ctx. This avoids an earlier return due to ctx being canceled while still
			// respecting the deadline for the overall attach operation.
			resultCtx = context.Background()
			if deadline, ok := ctx.Deadline(); ok {
				var cancel func()
				resultCtx, cancel = context.WithDeadline(resultCtx, deadline)
				defer cancel()
			}
		}

		resp, err = poller.PollUntilDone(ctx, nil)
		if err != nil {
			err = fmt.Errorf("failed to pull result: %v", err)
		} else {
			klog.Infof("attach operation successful: volumes attached to node %q.", *entry.Name)
		}

		for i, disk := range toBeAttachedDisks {
			lunChans[i] <- AttachDiskResult{Lun: *disk.Lun, Err: err}
		}
	}

	if async {
		go attach()
	} else {
		attach()
	}

	klog.Infof("updating cache")

	// update the cache
	az.VMSSVMCache.SetVMSSAndVM(*entry.VMSSName, *entry.Name, *entry.InstanceID, *entry.ResourceGroup, &resp.VirtualMachineScaleSetVM)

	return lunChans, nil
}

func (az *Cloud) detachDiskBatchFromNode(ctx context.Context, toBeDetachedDisks []DiskOperationParams) error {
	errChans := make([]chan (DetachDiskResult), len(toBeDetachedDisks))

	entry, resultErr := az.GetVMSSVM(ctx, *toBeDetachedDisks[0].VMName)
	if resultErr != nil {
		return fmt.Errorf("failed to get storage profile: %v", resultErr)
	}

	var disks []*armcompute.DataDisk
	if entry != nil && entry.VM != nil && entry.VM.Properties != nil && entry.VM.Properties.StorageProfile != nil && entry.VM.Properties.StorageProfile.DataDisks != nil {
		disks = entry.VM.Properties.StorageProfile.DataDisks
	} else {
		return fmt.Errorf("failed to get vm's disks")
	}

	var newDisks []*armcompute.DataDisk
	var found bool

	for _, tbdDisk := range toBeDetachedDisks {
		for i, disk := range disks {
			if disk.Lun != nil && (disk.Name != nil && *tbdDisk.Name != "" && strings.EqualFold(*disk.Name, *tbdDisk.Name)) ||
				(disk.Vhd != nil && disk.Vhd.URI != nil && *tbdDisk.DiskURI != "" && strings.EqualFold(*disk.Vhd.URI, *tbdDisk.DiskURI)) ||
				(disk.ManagedDisk != nil && *tbdDisk.DiskURI != "" && strings.EqualFold(*disk.ManagedDisk.ID, *tbdDisk.DiskURI)) {
				// found the disk
				klog.V(2).Infof("azureDisk - detach disk: name %s uri %s", *tbdDisk.Name, *tbdDisk.DiskURI)
				disks[i].ToBeDetached = pointer.Bool(true)
				found = true
			}
		}

		if !found {
			klog.Warningf("to be detached disk(%s) on node(%s) not found", *tbdDisk.Name, *entry.Name)
		} else {
			for _, disk := range disks {
				// if disk.ToBeDetached is true
				if !pointer.BoolDeref(disk.ToBeDetached, false) {
					newDisks = append(newDisks, disk)
				}
			}
		}
	}

	newVM := armcompute.VirtualMachineScaleSetVM{
		Properties: &armcompute.VirtualMachineScaleSetVMProperties{
			StorageProfile: &armcompute.StorageProfile{
				DataDisks: newDisks,
			},
		},
	}

	poller, err := az.VMSSVMClient.BeginUpdate(ctx, az.ResourceGroup, *entry.VMSSName, *entry.InstanceID, newVM, nil)
	if err != nil {
		return fmt.Errorf("failed to finish request: %v", err)
	}

	go func() {
		_, err = poller.PollUntilDone(ctx, nil)
		if err != nil {
			klog.Warningf("could not detach volumes from node %q: %v", *entry.InstanceID, err)
			err = fmt.Errorf("failed to pull result: %v", err)
		}

		for i := range toBeDetachedDisks {
			errChans[i] <- DetachDiskResult{Err: err}
		}
	}()


	resp, err := az.VMSSVMClient.Get(ctx, az.ResourceGroup, *entry.VMSSName, *entry.InstanceID, nil)
	if err != nil {
		return fmt.Errorf("failed to get vm: %v", err)
	}

	// update the cache
	az.VMSSVMCache.SetVMSSAndVM(*entry.VMSSName, *entry.Name, *entry.InstanceID, *entry.ResourceGroup, &resp.VirtualMachineScaleSetVM)
	return nil
}