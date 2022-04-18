/*
Copyright 2022 The Kubernetes Authors.

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
	Long:  `Azure Volume Attachment is a Kubernetes Custom Resource.`,
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
				// TODO: the same as  kubectl get AzVolumeAttachment
				fmt.Println("no flags")
			} else if pod != "" {
				azva = GetAzVolumeAttachementsByPod(pod, namespace)
				displayAzva(azva, "POD")
			} else if node != "" {
				azva = GetAzVolumeAttachementsByNode(node)
				displayAzva(azva, "NODE")
			} else if zone != "" {
				azva = GetAzVolumeAttachementsByZone(zone)
				displayAzva(azva, "ZONE")
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
	azvaCmd.PersistentFlags().StringP("namespace", "n", "", "insert-namespace (optional).")

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
	Namespace    string
	Name         string
	Age          time.Duration
	RequestRole  v1beta1.Role
	Role         v1beta1.Role
	State        v1beta1.AzVolumeAttachmentAttachmentState
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

	// get pvc claim names of pod
	pvcClaimNameSet := make(map[string]bool)

	pods, err := clientsetK8s.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, pod := range pods.Items {
		if pod.Name == podName {
			for _, v := range pod.Spec.Volumes {
				if v.PersistentVolumeClaim != nil {
					pvcClaimNameSet[v.PersistentVolumeClaim.ClaimName] = true
				}
			}
		}
	}

	// get azVolumes with the same claim name in pvcClaimNameSet
	azVolumeAttachments, err := clientsetAzDisk.DiskV1beta1().AzVolumeAttachments(consts.DefaultAzureDiskCrdNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		pvcClaimName := azVolumeAttachment.Spec.VolumeContext[consts.PvcNameKey]

		// if pvcClaimName is contained in pvcClaimNameSet, add the azVolumeattachment to result
		if pvcClaimNameSet[pvcClaimName] {
			result = append(result, AzvaResource{
				ResourceType: podName,
				Namespace:    azVolumeAttachment.Namespace,
				Name:         azVolumeAttachment.Name,
				Age:          time.Duration(metav1.Now().Sub(azVolumeAttachment.CreationTimestamp.Time).Hours()), //TODO: change format of age
				RequestRole:  azVolumeAttachment.Spec.RequestedRole,
				Role:         azVolumeAttachment.Status.Detail.Role,
				State:        azVolumeAttachment.Status.State})
		}
	}

	return result
}

func GetAzVolumeAttachementsByNode(nodeName string) []AzvaResource {
	result := make([]AzvaResource, 0)

	// access to config and Clientset
	config := getConfig()
	clientsetAzDisk := getAzDiskClientset(config)

	azVolumeAttachments, err := clientsetAzDisk.DiskV1beta1().AzVolumeAttachments(consts.DefaultAzureDiskCrdNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		if azVolumeAttachment.Spec.NodeName == nodeName {
			result = append(result, AzvaResource{
				ResourceType: azVolumeAttachment.Spec.NodeName,
				Namespace:    azVolumeAttachment.Namespace,
				Name:         azVolumeAttachment.Name,
				Age:          metav1.Now().Sub(azVolumeAttachment.CreationTimestamp.Time),
				RequestRole:  azVolumeAttachment.Spec.RequestedRole,
				Role:         azVolumeAttachment.Status.Detail.Role,
				State:        azVolumeAttachment.Status.State})
		}
	}

	return result
}

func GetAzVolumeAttachementsByZone(zoneName string) []AzvaResource {
	result := make([]AzvaResource, 0)

	// access to config and Clientsets
	config := getConfig()
	clientsetK8s := getKubernetesClientset(config)
	clientsetAzDisk := getAzDiskClientset(config)

	// get nodes in the zone
	nodeSet := make(map[string]bool)

	nodes, err := clientsetK8s.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, node := range nodes.Items {
		if node.Labels[consts.WellKnownTopologyKey] == zoneName {
			nodeSet[node.Name] = true
		}
	}

	// get azVolumeAttachments of the nodes in the zone
	azVolumeAttachments, err := clientsetAzDisk.DiskV1beta1().AzVolumeAttachments(consts.DefaultAzureDiskCrdNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		if nodeSet[azVolumeAttachment.Spec.NodeName] {
			result = append(result, AzvaResource{
				ResourceType: zoneName,
				Namespace:    azVolumeAttachment.Namespace,
				Name:         azVolumeAttachment.Name,
				Age:          metav1.Now().Sub(azVolumeAttachment.CreationTimestamp.Time),
				RequestRole:  azVolumeAttachment.Spec.RequestedRole,
				Role:         azVolumeAttachment.Status.Detail.Role,
				State:        azVolumeAttachment.Status.State})
		}
	}

	return result
}

func displayAzva(result []AzvaResource, typeName string) {
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{strings.ToUpper(typeName) + "NAME", "NAMESPACE", "NAME", "AGE", "REQUESTEDROLE", "ROLE", "STATE"})

	for _, azva := range result {
		table.Append([]string{azva.ResourceType, azva.Namespace, azva.Name, azva.Age.String()[:2] + "h", string(azva.RequestRole), string(azva.Role), string(azva.State)})
	}

	table.Render()
}
