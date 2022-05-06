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
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// podCmd represents the pod command
var podCmd = &cobra.Command{
	Use:   "pod",
	Short: "A brief description of your command",
	Long: `A longer description that spans multiple lines and likely contains examples
and usage of using your command. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
	Run: func(cmd *cobra.Command, args []string) {
		names := strings.Split(args[0], "/")

		if len(names) < 2 {
			fmt.Println("Pod and Container should be provided.")
		} else {
			podName := names[0]
			container := names[1]
			volumes, nodes, requestIds, afterTime, isWatch, isPrevious := GetFlags(cmd)

			GetLogsByAzDriverPod(podName, container, volumes, nodes, requestIds, afterTime, isWatch, isPrevious)
		}
	},
}

func init() {
	getCmd.AddCommand(podCmd)
}
