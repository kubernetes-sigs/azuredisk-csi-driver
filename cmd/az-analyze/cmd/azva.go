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
	"bytes"
	"context"
	"fmt"
	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"os"
	v1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	"strings"
	"time"
)

// azvaCmd represents the azva command
var azvaCmd = &cobra.Command{
	Use:   "azva",
	Short: "Azure Volume Attachment",
	Long:  `Azure Volume Attachment is a Kubernetes Custom Resource.`,
	Run: func(cmd *cobra.Command, args []string) {
		pod, _ := cmd.Flags().GetString("pod")
		node, _ := cmd.Flags().GetString("node")
		zone, _ := cmd.Flags().GetString("zone")
		namespace, _ := cmd.Flags().GetString("namespace")

		numFlag := cmd.Flags().NFlag()
		if hasNamespace := namespace != ""; hasNamespace {
			numFlag--
		}

		var result []AzvaResource
		// access to config and Clientsets
		config := getConfig()
		clientsetK8s := getKubernetesClientset(config)
		clientsetAzDisk := getAzDiskClientset(config)

		if numFlag > 1 {
			fmt.Printf("only one of the flags is allowed.\n" + "Run 'az-analyze --help' for usage.\n")
		} else {
			if numFlag == 0 {
				// if no flag value is provided , list all of the pods/nodes/zone information
				result = GetAzVolumeAttachementsByPod(clientsetK8s, clientsetAzDisk, pod, namespace)
				if len(result) != 0 {
					displayAzva(result, "POD")
				} else {
					fmt.Println("No azVolumeAttachment was found by pod")
				}

				result = GetAzVolumeAttachementsByNode(clientsetK8s, clientsetAzDisk, node)
				if len(result) != 0 {
					displayAzva(result, "NODE")
				} else {
					fmt.Println("No azVolumeAttachment was found by node")
				}

				result = GetAzVolumeAttachementsByZone(clientsetK8s, clientsetAzDisk, zone)
				if len(result) != 0 {
					displayAzva(result, "ZONE")
				} else {
					fmt.Println("No azVolumeAttachment was found by zone")
				}
			} else if pod != "" {
				result = GetAzVolumeAttachementsByPod(clientsetK8s, clientsetAzDisk, pod, namespace)
				if len(result) != 0 {
					displayAzva(result, "POD")
				} else {
					fmt.Println("No azVolumeAttachment was found")
				}
			} else if node != "" {
				result = GetAzVolumeAttachementsByNode(clientsetK8s, clientsetAzDisk, node)
				if len(result) != 0 {
					displayAzva(result, "NODE")
				} else {
					fmt.Println("No azVolumeAttachment was found")
				}
			} else if zone != "" {
				result = GetAzVolumeAttachementsByZone(clientsetK8s, clientsetAzDisk, zone)
				if len(result) != 0 {
					displayAzva(result, "ZONE")
				} else {
					fmt.Println("No azVolumeAttachment was found")
				}
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

func GetAzVolumeAttachementsByPod(clientsetK8s *kubernetes.Clientset, clientsetAzDisk *azDiskClientSet.Clientset, podName string, namespace string) []AzvaResource {
	result := make([]AzvaResource, 0)

	if namespace == "" {
		namespace = "default"
	}

	// get pvc claim names of pod
	pvcClaimNameSet := make(map[string]string)

	if podName != "" {
		singlePod, err := clientsetK8s.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})

		if err != nil {
			panic(err.Error())
		}

		for _, v := range singlePod.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				pvcClaimNameSet[v.PersistentVolumeClaim.ClaimName] = singlePod.Name
			}
		}
	} else {
		pods, err := clientsetK8s.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
		if err != nil {
			panic(err.Error())
		}

		for _, pod := range pods.Items {
			for _, v := range pod.Spec.Volumes {
				if v.PersistentVolumeClaim != nil {
					pvcClaimNameSet[v.PersistentVolumeClaim.ClaimName] = pod.Name
				}
			}
		}
	}

	// get azVolumes with the same claim name in pvcClaimNameSet
	azVolumeAttachments, err := clientsetAzDisk.DiskV1beta1().AzVolumeAttachments(driverNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		pvcClaimName := azVolumeAttachment.Spec.VolumeContext[consts.PvcNameKey]

		// if pvcClaimName is contained in pvcClaimNameSet, add the azVolumeattachment to result
		if pName, ok := pvcClaimNameSet[pvcClaimName]; ok {
			result = append(result, AzvaResource{
				ResourceType: pName,
				Namespace:    azVolumeAttachment.Namespace,
				Name:         azVolumeAttachment.Name,
				Age:          metav1.Now().Sub(azVolumeAttachment.CreationTimestamp.Time), //TODO: change format of age
				RequestRole:  azVolumeAttachment.Spec.RequestedRole,
				Role:         azVolumeAttachment.Status.Detail.Role,
				State:        azVolumeAttachment.Status.State})
		}
	}

	return result
}

func GetAzVolumeAttachementsByNode(clientsetK8s *kubernetes.Clientset, clientsetAzDisk *azDiskClientSet.Clientset, nodeName string) []AzvaResource {
	result := make([]AzvaResource, 0)

	// get list of nodes
	nodeNames := make(map[string]bool)
	if nodeName == "" {
		nodes, err := clientsetK8s.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
		if err != nil {
			panic(err.Error())
		}

		for _, n := range nodes.Items {
			nodeNames[n.Name] = true
		}
	} else {
		nodeNames[nodeName] = true
	}

	azVolumeAttachments, err := clientsetAzDisk.DiskV1beta1().AzVolumeAttachments(driverNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		if nodeNames[azVolumeAttachment.Spec.NodeName] {
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

func GetAzVolumeAttachementsByZone(clientsetK8s *kubernetes.Clientset, clientsetAzDisk *azDiskClientSet.Clientset, zoneName string) []AzvaResource {
	result := make([]AzvaResource, 0)

	// get nodes in the zone
	nodeSet := make(map[string]string)

	nodes, err := clientsetK8s.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, node := range nodes.Items {
		if zoneName == "" || node.Labels[consts.WellKnownTopologyKey] == zoneName {
			nodeSet[node.Name] = node.Labels[consts.WellKnownTopologyKey]
		}
	}

	// get azVolumeAttachments of the nodes in the zone
	azVolumeAttachments, err := clientsetAzDisk.DiskV1beta1().AzVolumeAttachments(driverNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		if zName, ok := nodeSet[azVolumeAttachment.Spec.NodeName]; ok {
			result = append(result, AzvaResource{
				ResourceType: zName,
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
		table.Append([]string{azva.ResourceType, azva.Namespace, azva.Name, timeFmt(azva.Age), string(azva.RequestRole), string(azva.Role), string(azva.State)})
	}

	table.Render()
}

func timeFmt(t time.Duration) string {
	day := t / (24 * time.Hour)
	t = t % (24 * time.Hour)
	hour := t / time.Hour
	t = t % time.Hour
	minute := t / time.Minute
	t = t % time.Minute
	second := int(t)

	var buffer bytes.Buffer
	if day > 0 {
		buffer.WriteString(fmt.Sprintf("%dd", day))
	}
	if hour > 0 {
		buffer.WriteString(fmt.Sprintf("%dh", hour))
		return buffer.String()
	}
	if minute > 0 {
		buffer.WriteString(fmt.Sprintf("%dm", minute))
	}
	if second > 0 {
		buffer.WriteString(fmt.Sprintf("%ds", second))
	}
	return buffer.String()
}
