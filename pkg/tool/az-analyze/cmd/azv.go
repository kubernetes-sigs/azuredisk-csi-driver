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

	v1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	"github.com/spf13/cobra"
)

// azvCmd represents the azv command
var azvCmd = &cobra.Command{
	Use:   "azv",
	Short: "Azure Volume",
	Long: `Azure Volume is a Kubernetes Custom Resource

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
	Run: func(cmd *cobra.Command, args []string) {
		pod, _ := cmd.Flags().GetString("pod")

		var azv AzvResource
		if pod == "" {
			azv = GetAzVolumesByPod(pod)
			// display
			fmt.Println(azv)
		} else {
			// list all pods
		}
	},
}

func init() {
	getCmd.AddCommand(azvCmd)
	azvCmd.PersistentFlags().StringP("pod", "p", "", "insert-pod-name")

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// azvCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// azvCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

type AzvResource struct {
	resourceType string
	namespace string 
	name string
	state v1beta1.AzVolumeState
	phase v1beta1.AzVolumePhase
}

func GetAzVolumesByPod(podName string) AzvResource {
	// implemetation

	return AzvResource {
		resourceType: "example-pod-123", 
		namespace: "azure-disk-csi",
		name: "pvc-b2578f0d-e99b-49d9-b1da-66ad771e073b",
		state: v1beta1.VolumeCreated,
		phase: v1beta1.VolumeBound }
}