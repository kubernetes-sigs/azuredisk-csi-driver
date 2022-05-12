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
	"fmt"
	"log"
	"os"
	"regexp"
	"time"

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
    if sinceTime != "" {
        if isMatch, _ := regexp.MatchString(RFC3339Format, sinceTime); isMatch {
            // sinceTime is RFC3339 format, input validation, convert it to UTC time and then Klog format
            t, err := time.Parse(time.RFC3339, sinceTime)
            if err != nil {
                fmt.Printf("error: %v\n", err)
                return
            }

            t = t.UTC()
            sinceTime = t.Format("0102 15:04:05")

        } else if isMatch, _ := regexp.MatchString(KlogTimeFormat, sinceTime); isMatch {
            // Input validation
            _, err := time.Parse("0102 15:04:05", sinceTime)
            if err != nil {
                fmt.Printf("error: %v\n", err)
                return
            }
        } else {
            fmt.Printf("\"%v\" is not a valid timestamp format\n", sinceTime)
			return
        }
    }

    // Open file
    file, err := os.Open(path)
    if err != nil {
        log.Fatal(err)
    }

    defer func() {
        if err = file.Close(); err != nil {
            log.Fatal(err)
        }
    }()

    // Read file
    buf := bufio.NewScanner(file)

    LogFilter(buf, volumes, nodes, requestIds, sinceTime)
}
