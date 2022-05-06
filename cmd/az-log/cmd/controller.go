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
	"github.com/spf13/cobra"
)

// controllerCmd represents the controller command
var controllerCmd = &cobra.Command{
	Use:   "controller",
	Short: "A brief description of your command",
	Long: `A longer description that spans multiple lines and likely contains examples
and usage of using your command. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
	Run: func(cmd *cobra.Command, args []string) {
		volumes, nodes, requestIds, afterTime, isWatch, isPrevious := GetFlags(cmd)

		GetLogsByAzDriverPod("csi-azuredisk2-controller-6cb54cc5b5-6h6bt", "azuredisk", volumes, nodes, requestIds, "2019-08-22T12:00:00Z", true, false) // TODO: find constant
	},
}

func init() {
	getCmd.AddCommand(controllerCmd)
}

func getControllerPodName() string {
	return ""
}

