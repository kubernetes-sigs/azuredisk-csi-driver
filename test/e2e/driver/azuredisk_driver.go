/*
Copyright 2019 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package driver

import (
	"flag"
	"fmt"
	"os"
	"strings"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v4/apis/volumesnapshot/v1"
	v1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk"
)

const (
	AzureDriverNameVar = "AZURE_STORAGE_DRIVER"
	TopologyKey        = "topology.disk.csi.azure.com/zone"
)

// FeatureGates carries the Azure Disk CSI driver feature gates for the e2e run,
// populated from the --feature-gates flag and shared with the in-process driver.
var FeatureGates = azuredisk.NewDriverFeatureGate()

func init() {
	flag.Var(azuredisk.NewGoFlagFeatureGate(FeatureGates), "feature-gates",
		fmt.Sprintf("A set of key=value pairs that describe Azure Disk CSI driver feature gates. Known features: %s", strings.Join(FeatureGates.KnownFeatures(), ", ")))
}

// QADEnabled reports whether the node-driven attach/detach (QAD) feature gate is
// enabled for this e2e run.
func QADEnabled() bool {
	return FeatureGates.Enabled(azuredisk.NodeDrivenAttachDetach)
}

// Implement DynamicPVTestDriver interface
type azureDiskDriver struct {
	driverName string
}

// normalizeProvisioner extracts any '/' character in the provisioner name to '-'.
// StorageClass name cannot container '/' character.
func normalizeProvisioner(provisioner string) string {
	return strings.ReplaceAll(provisioner, "/", "-")
}

// InitAzureDiskDriver returns azureDiskDriver that implements DynamicPVTestDriver interface
func InitAzureDiskDriver() PVTestDriver {
	driverName := os.Getenv(AzureDriverNameVar)
	if driverName == "" {
		driverName = consts.DefaultDriverName
	}

	klog.Infof("Using azure disk driver: %s", driverName)
	return &azureDiskDriver{
		driverName: driverName,
	}
}

func (d *azureDiskDriver) GetDynamicProvisionStorageClass(parameters map[string]string, mountOptions []string, reclaimPolicy *v1.PersistentVolumeReclaimPolicy, bindingMode *storagev1.VolumeBindingMode, allowedTopologyValues []string, namespace string) *storagev1.StorageClass {
	provisioner := d.driverName
	generateName := fmt.Sprintf("%s-%s-dynamic-sc-", namespace, normalizeProvisioner(provisioner))
	var allowedTopologies []v1.TopologySelectorTerm
	if len(allowedTopologyValues) > 0 {
		allowedTopologies = []v1.TopologySelectorTerm{
			{
				MatchLabelExpressions: []v1.TopologySelectorLabelRequirement{
					{
						Key:    TopologyKey,
						Values: allowedTopologyValues,
					},
				},
			},
		}
	}

	// Apply QAD default parameters if not already set by the test
	if QADEnabled() {
		qadDefaults := map[string]string{
			"skuName":    "Premium_LRS",
			"attachMode": "NodeDriven",
		}
		for k, v := range qadDefaults {
			if _, ok := parameters[k]; !ok {
				parameters[k] = v
			}
		}
	}

	if strings.EqualFold(os.Getenv("AZURE_CLOUD_NAME"), "AZURESTACKCLOUD") {
		if sku, ok := parameters["skuName"]; ok && !strings.EqualFold(sku, "Standard_LRS") && !strings.EqualFold(sku, "Premium_LRS") {
			parameters["skuName"] = "Standard_LRS"
		}
	}

	return getStorageClass(generateName, provisioner, parameters, mountOptions, reclaimPolicy, bindingMode, allowedTopologies)
}

func (d *azureDiskDriver) GetVolumeSnapshotClass(namespace string, parameters map[string]string) *snapshotv1.VolumeSnapshotClass {
	provisioner := d.driverName
	generateName := fmt.Sprintf("%s-%s-dynamic-sc-", namespace, normalizeProvisioner(provisioner))
	return getVolumeSnapshotClass(generateName, provisioner, parameters)
}

func (d *azureDiskDriver) GetPersistentVolume(volumeID, fsType, size string, volumeMode v1.PersistentVolumeMode, accessMode v1.PersistentVolumeAccessMode, reclaimPolicy *v1.PersistentVolumeReclaimPolicy, namespace string, volumeContext map[string]string) *v1.PersistentVolume {
	provisioner := d.driverName
	generateName := fmt.Sprintf("%s-%s-preprovisioned-pv-", namespace, normalizeProvisioner(provisioner))
	// Default to Retain ReclaimPolicy for pre-provisioned volumes
	pvReclaimPolicy := v1.PersistentVolumeReclaimRetain
	if reclaimPolicy != nil {
		pvReclaimPolicy = *reclaimPolicy
	}
	return &v1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: generateName,
			Namespace:    namespace,
			// TODO remove if https://github.com/kubernetes-csi/external-provisioner/issues/202 is fixed
			Annotations: map[string]string{
				"pv.kubernetes.io/provisioned-by": provisioner,
			},
		},
		Spec: v1.PersistentVolumeSpec{
			VolumeMode:  &volumeMode,
			AccessModes: []v1.PersistentVolumeAccessMode{accessMode},
			Capacity: v1.ResourceList{
				v1.ResourceName(v1.ResourceStorage): resource.MustParse(size),
			},
			PersistentVolumeReclaimPolicy: pvReclaimPolicy,
			PersistentVolumeSource: v1.PersistentVolumeSource{
				CSI: &v1.CSIPersistentVolumeSource{
					Driver:           provisioner,
					VolumeHandle:     volumeID,
					FSType:           fsType,
					VolumeAttributes: volumeContext,
				},
			},
		},
	}
}

func GetParameters() map[string]string {
	if QADEnabled() {
		return map[string]string{
			"skuName":    "Premium_LRS",
			"attachMode": "NodeDriven",
		}
	}
	return map[string]string{
		"skuName": "StandardSSD_LRS",
	}
}
