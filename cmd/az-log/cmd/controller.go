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
		volumes, _ := cmd.Flags().GetStringSlice("volume")
		nodes, _ := cmd.Flags().GetStringSlice("node")
		requestIds, _ := cmd.Flags().GetStringSlice("request-id")
		afterTime, _ := cmd.Flags().GetString("after-time")
		isWatch, _ := cmd.Flags().GetBool("volume")
		isPrevious, _ := cmd.Flags().GetBool("volume")
		fmt.Println("controller called")
	},
}

func init() {
	getCmd.AddCommand(controllerCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// controllerCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// controllerCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

func getControllerPodName() string {
	return ""
}

func GetLogsByConttroler(podName string, volumes []string, nodes []string, ) {

}
