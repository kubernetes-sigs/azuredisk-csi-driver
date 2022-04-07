/*
Copyright © 2022 NAME HERE <EMAIL ADDRESS>

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
	"time"

	v1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	"github.com/spf13/cobra"
)

// azvaCmd represents the azva command
var azvaCmd = &cobra.Command{
	Use:   "azva",
	Short: "A brief description of your command",
	Long: `A longer description that spans multiple lines and likely contains examples
and usage of using your command. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
	Run: func(cmd *cobra.Command, args []string) {

		pod, _ := cmd.Flags().GetString("pod")
		node, _ := cmd.Flags().GetString("node")
		zone, _ := cmd.Flags().GetString("zone")
		
		flags := []bool{pod != "", node != "", zone != ""}

		sum := 0
		for _, f := range flags {
			sum += btoi(f)
		}
		
		var azva AzvaResource

		if sum > 1 {
			fmt.Printf("only one of the flags is allowed.\n" + "Run 'az-analyze --help' for usage.\n")
		} else {
			if sum == 0 {
				// display all?
			} else if pod != "" {
				azva = GetAzVolumeAttachementsByPod(pod)
				// display
				fmt.Println(azva)
			} else if node != "" {
				azva = GetAzVolumeAttachementsByNode(node)
				// display
				fmt.Println(azva)
			} else if zone != "" {
				azva = GetAzVolumeAttachementsByZone(zone)
				// display
				fmt.Println(azva)
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
	resourceType string
	namespace string
	name string
	age time.Duration
	requestRole v1beta1.Role
	role v1beta1.Role
	state v1beta1.AzVolumeAttachmentAttachmentState
}

func GetAzVolumeAttachementsByPod(podName string) AzvaResource {
	// implemetation

	return AzvaResource {
		resourceType: "k8s-agentpool1-47843266-vmss000004",
		namespace: "azure-disk-csi",
		name: "pvc-b2578f0d-e99b-49d9-b1da-66ad771e073b-k8s-agentpool1-47843266-vmss000004-attachment",
		age: time.Hour,
		requestRole: v1beta1.PrimaryRole,
		role: v1beta1.PrimaryRole,
		state: v1beta1.Attached }
}

func GetAzVolumeAttachementsByNode(nodeName string) AzvaResource {
	// implemetation

	return AzvaResource {
		resourceType: "k8s-agentpool1-47843266-vmss000004",
		namespace: "azure-disk-csi",
		name: "pvc-b2578f0d-e99b-49d9-b1da-66ad771e073b-k8s-agentpool1-47843266-vmss000004-attachment",
		age: time.Hour,
		requestRole: v1beta1.PrimaryRole,
		role: v1beta1.PrimaryRole,
		state: v1beta1.Attached }
}

func GetAzVolumeAttachementsByZone(nodeName string) AzvaResource {
	// implemetation

	return AzvaResource {
		resourceType: "k8s-agentpool1-47843266-vmss000004",
		namespace: "azure-disk-csi",
		name: "pvc-b2578f0d-e99b-49d9-b1da-66ad771e073b-k8s-agentpool1-47843266-vmss000004-attachment",
		age: time.Hour,
		requestRole: v1beta1.PrimaryRole,
		role: v1beta1.PrimaryRole,
		state: v1beta1.Attached }
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}