/*
Copyright YEAR The Kubernetes Authors.

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
package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	v1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

// azvaCmd represents the azva command
var azvaCmd = &cobra.Command{
	Use:   "azva",
	Short: "Azure Volume Attachment",
	Long: `Azure Volume Attachment is a Kubernetes Custom Resource.`,
	Run: func(cmd *cobra.Command, args []string) {
		// typesFlag := []string{"pod", "node", "zone", "namespace"}
		// valuesFlag := []string{pod, node, zone, namespace}
		
		// for _, value := range valuesFlag {

		// }
		
		pod, _ := cmd.Flags().GetString("pod")
		node, _ := cmd.Flags().GetString("node")
		zone, _ := cmd.Flags().GetString("zone")
		namespace, _ := cmd.Flags().GetString("namespace")

		numFlag := cmd.Flags().NFlag()
		if hasNamespace := namespace != ""; hasNamespace {
			numFlag--
		}
		
		var azva []AzvaResource

		if numFlag > 1 {
			fmt.Printf("only one of the flags is allowed.\n" + "Run 'az-analyze --help' for usage.\n")
		} else {
			if numFlag == 0 {
				fmt.Println("no flags")
				// the same as  kubectl get AzVolumeAttachment
			} else if pod != "" {
				azva = GetAzVolumeAttachementsByPod(pod, namespace)
				// display
				displayAzva(azva, "POD")
			} else if node != "" {
				//azva = GetAzVolumeAttachementsByNode(node)
				// display
				displayAzva(azva, node)
			} else if zone != "" {
				//azva = GetAzVolumeAttachementsByZone(zone)
				// display
				displayAzva(azva,zone)
			} else {
				fmt.Printf("invalid flag name\n" + "Run 'az-analyze --help' for usage.\n")
			}
		}	
	},
}

func init() {
	getCmd.AddCommand(azvaCmd)
	azvaCmd.PersistentFlags().StringP("pod", "p", "", "insert-pod-name (only one of the flags is allowed).")
	azvaCmd.PersistentFlags().StringP("node", "d", "", "insert-node-name (only one of the flags is allowed).")
	azvaCmd.PersistentFlags().StringP("zone", "z", "", "insert-zone-name (only one of the flags is allowed).")
	azvaCmd.PersistentFlags().StringP("namesapce", "n", "", "insert-namespace (optional).")
	
	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// azvaCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// azvaCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

type AzvaResource struct {
	ResourceType string
	Namespace string
	Name string
	Age time.Duration
	RequestRole v1beta1.Role
	Role v1beta1.Role
	State v1beta1.AzVolumeAttachmentAttachmentState
}



func GetAzVolumeAttachementsByPod(podName string, namespace string) []AzvaResource {
	result := make([]AzvaResource, 0)

	if namespace == "" {
		namespace = "default"
	}

	// access to config and Clientsets
	config := getConfig()
	clientsetK8s := getKubernetesClientset(config)
	clientsetAzDisk := getAzDiskClientset(config)

	// assume under "default" namesapce
	pvcSet := make(map[string]string)
	pods, err := clientsetK8s.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, pod := range pods.Items {
		// if pod flag isn't provided, print all pods
		if podName == "" || pod.Name == podName {
			for _, v := range pod.Spec.Volumes {
				if v.PersistentVolumeClaim != nil {
					pvcSet[v.PersistentVolumeClaim.ClaimName] = pod.Name
				}
			}
		}
	}

	// get azVolumes with the same claim name in pvcSet
	

	azVolumeAttachments, err := clientsetAzDisk.DiskV1beta1().AzVolumeAttachments(consts.DefaultAzureDiskCrdNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		// pvName := azVolume.Status.PersistentVolume;
		fmt.Println(azVolumeAttachment.Name)
		fmt.Println(azVolumeAttachment.Namespace)
		//fmt.Println(azVolumeAttachment.Labels["disk.csi.azure.com/node-name"]) // "disk.csi.azure.com/requested-role", "disk.csi.azure.com/volume-name"
		fmt.Println(azVolumeAttachment.Spec.NodeName)
		fmt.Println(azVolumeAttachment.Spec.RequestedRole)
		fmt.Println(azVolumeAttachment.Spec.VolumeContext["csi.storage.k8s.io/pvc/name"])
		fmt.Println(azVolumeAttachment.Spec.VolumeName)
		fmt.Println(azVolumeAttachment.Status.Detail.Role)
		fmt.Println(azVolumeAttachment.Status.State)
		fmt.Println(azVolumeAttachment.ObjectMeta.CreationTimestamp)
		fmt.Println(azVolumeAttachment.ObjectMeta.ManagedFields[0].Time)
		pvcClaimName := azVolumeAttachment.Spec.VolumeContext["csi.storage.k8s.io/pvc/name"]

		// if pvcName is contained in pvcSet, add the azVolume to result
		if pName, ok := pvcSet[pvcClaimName]; ok {
			result = append(result, AzvaResource {
				ResourceType: pName,
				Namespace:    azVolumeAttachment.Namespace,
				Name:         azVolumeAttachment.Name,
				//Age: metav1.Now().Sub(time.Time(azVolumeAttachment.ObjectMeta.CreationTimestamp)).Hours(),
				RequestRole: azVolumeAttachment.Spec.RequestedRole,
				Role: azVolumeAttachment.Status.Detail.Role,
				State: azVolumeAttachment.Status.State })
		}


	}

	// result = append(result, AzvaResource {
	// 	ResourceType: "k8s-agentpool1-47843266-vmss000004",
	// 	Namespace: "azure-disk-csi",
	// 	Name: "pvc-b2578f0d-e99b-49d9-b1da-66ad771e073b-k8s-agentpool1-47843266-vmss000004-attachment",
	// 	Age: time.Hour,
	// 	RequestRole: v1beta1.PrimaryRole,
	// 	Role: v1beta1.PrimaryRole,
	// 	State: v1beta1.Attached })

	return result
}

// func GetAzVolumeAttachementsByNode(nodeName string) AzvaResource {
// 	// implementation

// 	return AzvaResource {
// 		ResourceType: "k8s-agentpool1-47843266-vmss000004",
// 		Namespace: "azure-disk-csi",
// 		Name: "pvc-b2578f0d-e99b-49d9-b1da-66ad771e073b-k8s-agentpool1-47843266-vmss000004-attachment",
// 		Age: time.Hour,
// 		RequestRole: v1beta1.PrimaryRole,
// 		Role: v1beta1.PrimaryRole,
// 		State: v1beta1.Attached }
// }

// func GetAzVolumeAttachementsByZone(nodeName string) AzvaResource {
// 	// implementation

// 	return AzvaResource {
// 		ResourceType: "k8s-agentpool1-47843266-vmss000004",
// 		Namespace: "azure-disk-csi",
// 		Name: "pvc-b2578f0d-e99b-49d9-b1da-66ad771e073b-k8s-agentpool1-47843266-vmss000004-attachment",
// 		Age: time.Hour,
// 		RequestRole: v1beta1.PrimaryRole,
// 		Role: v1beta1.PrimaryRole,
// 		State: v1beta1.Attached }
// }

func displayAzva(result []AzvaResource, typeName string) {
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{strings.ToUpper(typeName) + "NAME", "NAMESPACE", "NAME", "AGE", "REQUESTEDROLE", "ROLE", "STATE"})
	//table.Append([]string{azva.ResourceType, azva.Namespace, azva.Name, azva.Age.String()[:2], string(azva.RequestRole),string(azva.Role), string(azva.State)})
	// for _, azva := range result {
	// 	table.Append([]string{azva.ResourceType, azva.Namespace, azva.Name, string(azva.Age), string(azva.RequestRole),string(azva.Role), string(azva.State)})
	// }
	for _, azva := range result {
		table.Append([]string{azva.ResourceType, azva.Namespace, azva.Name, azva.Age.String()[:2], string(azva.RequestRole),string(azva.Role), string(azva.State)})
	}
	
	table.Render()
}