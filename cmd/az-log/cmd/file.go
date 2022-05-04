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

// fileCmd represents the file command
var fileCmd = &cobra.Command{
	Use:   "file",
	Short: "A brief description of your command",
	Long: `A longer description that spans multiple lines and likely contains examples
and usage of using your command. For example:

Cobra is a CLI library for Go that empowers applications.
This application is a tool to generate the needed files
to quickly create a Cobra application.`,
	Run: func(cmd *cobra.Command, args []string) {
		filePath := args[0]
		volume, _ := cmd.Flags().GetStringSlice("volume")
		node, _ := cmd.Flags().GetStringSlice("node")
		requestId, _ := cmd.Flags().GetStringSlice("request-id")
		afterTime, _ := cmd.Flags().GetString("after-time")
		// isWatch, _ := cmd.Flags().GetBool("volume")
		// isPrevious, _ := cmd.Flags().GetBool("volume")

		GetLogsByFile(filePath, volume, node, requestId,afterTime)
		fmt.Printf("file called")
	},
}

func init() {
	getCmd.AddCommand(fileCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// fileCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// fileCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}

func GetLogsByFile(path string, volume []string, node []string, requestId []string, afterTime string) []string{
	// f, err := os.Open(*fptr)
    // if err != nil {
    //     log.Fatal(err)
    // }
    // defer func() {
    //     if err = f.Close(); err != nil {
    //     log.Fatal(err)
    // }
    // }()
    // s := bufio.NewScanner(f)
    // for s.Scan() {
    //     fmt.Println(s.Text())
    // }
    // err = s.Err()
    // if err != nil {
    //     log.Fatal(err)
    // }
	return []string{}
}
