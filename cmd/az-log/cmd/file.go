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
	"bufio"
	"log"
	"os"

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
        volumes, nodes, requestIds, sinceTime, _, _ := GetFlags(cmd)

		GetLogsByFile(filePath, volumes, nodes, requestIds, sinceTime)
	},
}

func init() {
	getCmd.AddCommand(fileCmd)
}

func GetLogsByFile(path string, volumes []string, nodes []string, requestIds []string, sinceTime string) {
    file, err := os.Open(path)
    if err != nil {
        log.Fatal(err)
    }

    defer func() {
        if err = file.Close(); err != nil {
            log.Fatal(err)
        }
    }()

    buf := bufio.NewScanner(file)
    // TODO : convert timestamp to Klog
    LogFilter(buf, volumes, nodes, requestIds, sinceTime)


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
}
