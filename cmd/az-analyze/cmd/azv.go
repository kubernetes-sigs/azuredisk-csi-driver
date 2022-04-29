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

	"github.com/olekukonko/tablewriter"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	v1beta1 "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/azuredisk/v1beta1"
	"sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
)

// azvCmd represents the azv command
var azvCmd = &cobra.Command{
	Use:   "azv",
	Short: "Azure Volume",
	Long:  `Azure Volume is a Kubernetes Custom Resource.`,
	Run: func(cmd *cobra.Command, args []string) {
		pod, _ := cmd.Flags().GetString("pod")
		namespace, _ := cmd.Flags().GetString("namespace")

		// access to Config and Clientsets
		config := getConfig()
		clientsetK8s := getKubernetesClientset(config)
		clientsetAzDisk := getAzDiskClientset(config)

		result := GetAzVolumesByPod(clientsetK8s, clientsetAzDisk, pod, namespace)

		// display
		if len(result) != 0 {
			displayAzv(result)
		} else {
			// not found, display an error
			fmt.Println("No azVolume was found")
		}
	},
}

func init() {
	getCmd.AddCommand(azvCmd)
	azvCmd.PersistentFlags().StringP("pod", "p", "", "insert-pod-name")
	azvCmd.PersistentFlags().StringP("namespace", "n", "default", "insert-namespace")
}

type AzvResource struct {
	ResourceType string
	Namespace    string
	Name         string
	State        v1beta1.AzVolumeState
}

// return azVolumes by pod. If pod name isn't provided, return by all pods
func GetAzVolumesByPod(clientsetK8s kubernetes.Interface, clientsetAzDisk versioned.Interface, podName string, namespace string) []AzvResource {
	result := make([]AzvResource, 0)

	// get pvc claim name set of pod
	pvcClaimNameSet := make(map[string][]string)
	if podName != "" {
		singlePod, err := clientsetK8s.CoreV1().Pods(namespace).Get(context.Background(), podName, metav1.GetOptions{})
		if err != nil {
			if errors.IsNotFound(err) {
				fmt.Println(err)
				os.Exit(0)
			} else {
				panic(err.Error())
			}
		}

		for _, v := range singlePod.Spec.Volumes {
			if v.PersistentVolumeClaim != nil {
				pvcClaimNameSet[v.PersistentVolumeClaim.ClaimName] = append(pvcClaimNameSet[v.PersistentVolumeClaim.ClaimName], singlePod.Name)
			}
		}
	} else { // if pod name isn't provided, print all pods
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
	}

	// get azVolumes with the same claim name in pvcSet
	azVolumes, err := clientsetAzDisk.DiskV1beta1().AzVolumes(getDriverNamesapce()).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		panic(err.Error())
	}
	for _, azVolume := range azVolumes.Items {
		pvcClaimName := azVolume.Spec.Parameters[consts.PvcNameKey]
		// if pvcClaimName is contained in pvcClaimNameSet, add the azVolume to result
		if pNames, ok := pvcClaimNameSet[pvcClaimName]; ok {
			for _, pName := range pNames {
				result = append(result, AzvResource{
					ResourceType: pName,
					Namespace:    azVolume.Namespace,
					Name:         azVolume.Spec.VolumeName,
					State:        azVolume.Status.State})
			}
		}
	}
	fmt.Println(result) // debug
	return result
}

func displayAzv(result []AzvResource) {
	table := tablewriter.NewWriter(os.Stdout)
	table.SetHeader([]string{"PODNAME", "NAMESPACE", "NAME", "STATE"})

	for _, azv := range result {
		table.Append([]string{azv.ResourceType, azv.Namespace, azv.Name, string(azv.State)})
	}

	table.Render()
}
