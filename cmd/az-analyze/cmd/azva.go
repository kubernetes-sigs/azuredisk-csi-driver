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
	"reflect"
	"time"

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	azdiskv1beta2 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta2"
	azdisk "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
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
		if hasNamespace := namespace != metav1.NamespaceNone; hasNamespace {
			numFlag--
		}

		// access to config and Clientsets
		config := getConfig()
		clientsetK8s := getKubernetesClientset(config)
		clientsetAzDisk := getAzDiskClientset(config)

		var result []AzvaResource

		if numFlag > 1 {
			fmt.Printf("only one of the flags is allowed.\n" + "Run 'az-analyze --help' for usage.\n")
		} else {
			if numFlag == 0 {
				// if no flag value is provided , list all of the pods/nodes/zone information
				result = GetAllAzVolumeAttachements(clientsetK8s, clientsetAzDisk, namespace)
			} else if pod != "" {
				result = GetAzVolumeAttachementsByPod(clientsetK8s, clientsetAzDisk, pod, namespace)
			} else if node != "" {
				result = GetAzVolumeAttachementsByNode(clientsetAzDisk, node)
			} else if zone != "" {
				result = GetAzVolumeAttachementsByZone(clientsetK8s, clientsetAzDisk, zone)
			}

			if len(result) != 0 {
				displayAzva(result)
			} else {
				fmt.Println("No azVolumeAttachment was found")
			}
		}
	},
}

func init() {
	getCmd.AddCommand(azvaCmd)
	azvaCmd.PersistentFlags().StringP("pod", "p", "", "insert-pod-name (only one of the flags is allowed).")
	azvaCmd.PersistentFlags().StringP("node", "d", "", "insert-node-name (only one of the flags is allowed).")
	azvaCmd.PersistentFlags().StringP("zone", "z", "", "insert-zone-name (only one of the flags is allowed).")
	azvaCmd.PersistentFlags().StringP("namespace", "n", metav1.NamespaceNone, "insert-namespace (optional).")
}

type AzvaResource struct {
	PodName     string
	NodeName    string
	ZoneName    string
	Namespace   string
	Name        string
	Age         time.Duration
	RequestRole azdiskv1beta2.Role
	Role        azdiskv1beta2.Role
	State       azdiskv1beta2.AzVolumeAttachmentAttachmentState
}

// return azVolumeAttachements with all Pods/Nodes/Zones when no flags is provided
func GetAllAzVolumeAttachements(clientsetK8s kubernetes.Interface, clientsetAzDisk azdisk.Interface, namespace string) []AzvaResource {
	result := make([]AzvaResource, 0)

	if namespace == metav1.NamespaceNone {
		namespace = metav1.NamespaceDefault
	}

	// get pvc claim names of pod(s)
	pvcClaimNameSet := make(map[string][]string)

	pods, err := clientsetK8s.CoreV1().Pods(namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, pod := range pods.Items {
		for _, v := range pod.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				pvcClaimNameSet[v.PersistentVolumeClaim.ClaimName] = append(pvcClaimNameSet[v.PersistentVolumeClaim.ClaimName], pod.Name)
			}
		}
	}

	// get azVolumeAttachments with the same claim name in pvcClaimNameSet
	azVolumeAttachments, err := clientsetAzDisk.DiskV1beta2().AzVolumeAttachments(getDriverNamesapce()).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		pvcClaimName := azVolumeAttachment.Spec.VolumeContext[consts.PvcNameKey]
		nodeName := azVolumeAttachment.Spec.NodeName
		node, err := clientsetK8s.CoreV1().Nodes().Get(context.Background(), nodeName, metav1.GetOptions{})
		if err != nil {
			panic(err.Error())
		}

		zoneName := node.Labels[consts.WellKnownTopologyKey]
		// if pvcClaimName is contained in pvcClaimNameSet, add the azVolumeattachment to result
		if pNames, ok := pvcClaimNameSet[pvcClaimName]; ok {
			for _, pName := range pNames {
				result = append(result, AzvaResource{
					PodName:     pName,
					NodeName:    nodeName,
					ZoneName:    zoneName,
					Namespace:   azVolumeAttachment.Namespace,
					Name:        azVolumeAttachment.Name,
					Age:         metav1.Now().Sub(azVolumeAttachment.CreationTimestamp.Time),
					RequestRole: azVolumeAttachment.Spec.RequestedRole,
					Role:        azVolumeAttachment.Status.Detail.Role,
					State:       azVolumeAttachment.Status.State,
				})
			}
		}
	}
	return result
}

// return azVolumeAttachements by pod when pod name is provided
func GetAzVolumeAttachementsByPod(clientsetK8s kubernetes.Interface, clientsetAzDisk azdisk.Interface, podName string, namespace string) []AzvaResource {
	result := make([]AzvaResource, 0)

	if namespace == metav1.NamespaceNone {
		namespace = metav1.NamespaceDefault
	}

	// get pvc claim names of pod(s)
	pvcClaimNameSet := make(map[string][]string)

	pod, err := clientsetK8s.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			fmt.Println(err)
			os.Exit(0)
		} else {
			panic(err.Error())
		}
	}

	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			pvcClaimNameSet[v.PersistentVolumeClaim.ClaimName] = append(pvcClaimNameSet[v.PersistentVolumeClaim.ClaimName], pod.Name)
		}
	}

	// get azVolumeAttachments with the same claim name in pvcClaimNameSet
	azVolumeAttachments, err := clientsetAzDisk.DiskV1beta2().AzVolumeAttachments(getDriverNamesapce()).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		pvcClaimName := azVolumeAttachment.Spec.VolumeContext[consts.PvcNameKey]

		// if pvcClaimName is contained in pvcClaimNameSet, add the azVolumeattachment to result
		if pNames, ok := pvcClaimNameSet[pvcClaimName]; ok {
			for _, pName := range pNames {
				result = append(result, AzvaResource{
					PodName:     pName,
					NodeName:    "",
					ZoneName:    "",
					Namespace:   azVolumeAttachment.Namespace,
					Name:        azVolumeAttachment.Name,
					Age:         metav1.Now().Sub(azVolumeAttachment.CreationTimestamp.Time),
					RequestRole: azVolumeAttachment.Spec.RequestedRole,
					Role:        azVolumeAttachment.Status.Detail.Role,
					State:       azVolumeAttachment.Status.State,
				})
			}
		}
	}
	return result
}

// return azVolumeAttachements by node when node name is provided
func GetAzVolumeAttachementsByNode(clientsetAzDisk azdisk.Interface, nodeName string) []AzvaResource {
	result := make([]AzvaResource, 0)

	azVolumeAttachments, err := clientsetAzDisk.DiskV1beta2().AzVolumeAttachments(getDriverNamesapce()).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		if azVolumeAttachment.Spec.NodeName == nodeName {
			result = append(result, AzvaResource{
				PodName:     "",
				NodeName:    azVolumeAttachment.Spec.NodeName,
				ZoneName:    "",
				Namespace:   azVolumeAttachment.Namespace,
				Name:        azVolumeAttachment.Name,
				Age:         metav1.Now().Sub(azVolumeAttachment.CreationTimestamp.Time),
				RequestRole: azVolumeAttachment.Spec.RequestedRole,
				Role:        azVolumeAttachment.Status.Detail.Role,
				State:       azVolumeAttachment.Status.State,
			})
		}
	}
	return result
}

// return azVolumeAttachements by zone when zone name is provided
func GetAzVolumeAttachementsByZone(clientsetK8s kubernetes.Interface, clientsetAzDisk azdisk.Interface, zoneName string) []AzvaResource {
	result := make([]AzvaResource, 0)

	// get nodes in the zone
	nodeSet := make(map[string]string)

	nodes, err := clientsetK8s.CoreV1().Nodes().List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, node := range nodes.Items {
		if node.Labels[consts.WellKnownTopologyKey] == zoneName {
			nodeSet[node.Name] = node.Labels[consts.WellKnownTopologyKey]
		}
	}

	// get azVolumeAttachments of the nodes in the zone
	azVolumeAttachments, err := clientsetAzDisk.DiskV1beta2().AzVolumeAttachments(getDriverNamesapce()).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}

	for _, azVolumeAttachment := range azVolumeAttachments.Items {
		if zName, ok := nodeSet[azVolumeAttachment.Spec.NodeName]; ok {
			result = append(result, AzvaResource{
				PodName:     "",
				NodeName:    "",
				ZoneName:    zName,
				Namespace:   azVolumeAttachment.Namespace,
				Name:        azVolumeAttachment.Name,
				Age:         metav1.Now().Sub(azVolumeAttachment.CreationTimestamp.Time),
				RequestRole: azVolumeAttachment.Spec.RequestedRole,
				Role:        azVolumeAttachment.Status.Detail.Role,
				State:       azVolumeAttachment.Status.State,
			})
		}
	}
	return result
}

func displayAzva(result []AzvaResource) {
	table := tablewriter.NewWriter(os.Stdout)
	var header []string

	for i := 0; i < len(result); i++ {
		v := reflect.ValueOf(result[i])
		f := v.Type()

		var row []string
		for j := 0; j < v.NumField(); j++ {
			if v.Field(j).Interface() != "" {
				if i == 0 {
					header = append(header, f.Field(j).Name)
				}

				if f.Field(j).Name == "Age" {
					row = append(row, timeFmt(v.Field(j).Interface().(time.Duration)))
				} else {
					row = append(row, fmt.Sprintf("%v", v.Field(j).Interface()))
				}

			}
		}

		table.Append(row)
	}
	table.SetHeader(header)
	table.Render()
}
