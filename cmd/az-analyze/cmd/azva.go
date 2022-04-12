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
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	v1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
)

// azvaCmd represents the azva command
var azvaCmd = &cobra.Command{
	Use:   "azva",
	Short: "Azure Volume Attachment",
	Long: `Azure Volume Attachment is a Kubernetes Custom Resource.`,
	Run: func(cmd *cobra.Command, args []string) {
		numFlag := cmd.Flags().NFlag()
		
		// typesFlag := []string{"pod", "node", "zone"}
		// var valuesFlag []string

		// for _, t := range typesFlag {
		// 	value, _ := cmd.Flags().GetString(t)
		// 	valuesFlag = append(valuesFlag, value)
		// }
		pod, _ := cmd.Flags().GetString("pod")
		node, _ := cmd.Flags().GetString("node")
		zone, _ := cmd.Flags().GetString("zone")
		
		var azva AzvaResource

		if numFlag > 1 {
			fmt.Printf("only one of the flags is allowed.\n" + "Run 'az-analyze --help' for usage.\n")
		} else {
			if numFlag == 0 {
				fmt.Println("no flags")
				// the same as  kubectl get AzVolumeAttachment
			} else if pod != "" {
				azva = GetAzVolumeAttachementsByPod(pod)
				// display
				displayAzva(azva, pod)
			} else if node != "" {
				azva = GetAzVolumeAttachementsByNode(node)
				// display
				displayAzva(azva, node)
			} else if zone != "" {
				azva = GetAzVolumeAttachementsByZone(zone)
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
	azvaCmd.PersistentFlags().StringP("node", "n", "", "insert-node-name (only one of the flags is allowed).")
	azvaCmd.PersistentFlags().StringP("zone", "z", "", "insert-zone-name (only one of the flags is allowed).")

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

func GetAzVolumeAttachementsByPod(podName string) AzvaResource {
	// implementation

	return AzvaResource {
		ResourceType: "k8s-agentpool1-47843266-vmss000004",
		Namespace: "azure-disk-csi",
		Name: "pvc-b2578f0d-e99b-49d9-b1da-66ad771e073b-k8s-agentpool1-47843266-vmss000004-attachment",
		Age: time.Hour,
		RequestRole: v1beta1.PrimaryRole,
		Role: v1beta1.PrimaryRole,
		State: v1beta1.Attached }
}

func GetAzVolumeAttachementsByNode(nodeName string) AzvaResource {
	// implementation

	return AzvaResource {
		ResourceType: "k8s-agentpool1-47843266-vmss000004",
		Namespace: "azure-disk-csi",
		Name: "pvc-b2578f0d-e99b-49d9-b1da-66ad771e073b-k8s-agentpool1-47843266-vmss000004-attachment",
		Age: time.Hour,
		RequestRole: v1beta1.PrimaryRole,
		Role: v1beta1.PrimaryRole,
		State: v1beta1.Attached }
}

func GetAzVolumeAttachementsByZone(nodeName string) AzvaResource {
	// implementation

	return AzvaResource {
		ResourceType: "k8s-agentpool1-47843266-vmss000004",
		Namespace: "azure-disk-csi",
		Name: "pvc-b2578f0d-e99b-49d9-b1da-66ad771e073b-k8s-agentpool1-47843266-vmss000004-attachment",
		Age: time.Hour,
		RequestRole: v1beta1.PrimaryRole,
		Role: v1beta1.PrimaryRole,
		State: v1beta1.Attached }
}

func displayAzva(azva AzvaResource, typeName string) {
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{strings.ToUpper(typeName) + "NAME", "NAMESPACE", "NAME", "AGE", "REQUESTEDROLE", "ROLE", "STATE"})
	table.Append([]string{azva.ResourceType, azva.Namespace, azva.Name, azva.Age.String()[:2], string(azva.RequestRole),string(azva.Role), string(azva.State)})
	// for _, azva := range result {
	// 	table.Append([]string{azva.ResourceType, azva.Namespace, azva.Name, string(azva.Age), string(azva.RequestRole),string(azva.Role), string(azva.State)})
	// }
	
	table.Render()
}