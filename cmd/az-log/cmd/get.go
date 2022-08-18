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

	"github.com/spf13/cobra"
)

// getCmd represents the get command
var getCmd = &cobra.Command{
	Use:   "get",
	Short: "Fetching logs of pods in kube-system namespace",
	Long:  `Fetching logs of pods in kube-system namespace`,
	Run: func(cmd *cobra.Command, args []string) {
		if len(args) == 0 {
			fmt.Printf("You must specify which source to retrieve and process logs from to \"%s\".\n", cmd.Name())
			fmt.Printf("Run 'az-log --help' for usage.\n")
		} else {
			fmt.Printf("error: the tool doesn't support a source type \"%s\".\n", args[0])
		}
	},
}

func init() {
	rootCmd.AddCommand(getCmd)
	getCmd.PersistentFlags().StringSlice("volume", []string{}, "insert-volume-name")
	getCmd.PersistentFlags().StringSlice("node", []string{}, "insert-node-name")
	getCmd.PersistentFlags().StringSlice("request-id", []string{}, "insert-request-id")
	getCmd.PersistentFlags().String("since-time", "", "insert-timestamp")
	getCmd.PersistentFlags().Bool("follow", false, "watch-logs")
	getCmd.PersistentFlags().Bool("previous", false, "fetch-and-prepend-logs-from-the-previously-terminated-container")
	getCmd.PersistentFlags().String("since", "", "insert-time-duration")
}
