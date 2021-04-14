/*
Copyright 2021 The Kubernetes Authors.

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

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	compute "github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2020-12-01/compute"
	"io/ioutil"
	"k8s.io/klog/v2"
	"os"
	"os/exec"
	"strings"

	azuredisk "sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk"
)

func init() {
	klog.InitFlags(nil)
}

func main() {

	skusFullfilePath := "skus-full.json"
	defer os.Remove(skusFullfilePath)
	skusFilePath := "deploy/skus.json"

	skuMap := map[string]bool{}
	skuInfo := make([]azuredisk.SkuInfo, 0)

	var resources []compute.ResourceSku
	err := getAllSkus(skusFullfilePath)

	if err != nil {
		klog.Errorf("Could not get skus. Error: %v", err)
	}

	diskSkusFullJSON, err := os.Open(skusFullfilePath)
	if err != nil {
		klog.Errorf("Could not read file. Error: %v, FilePath: %s", err, skusFullfilePath)
		return
	}
	defer diskSkusFullJSON.Close()

	byteValue, _ := ioutil.ReadAll(diskSkusFullJSON)

	err = json.Unmarshal([]byte(byteValue), &resources)
	if err != nil {
		klog.Errorf("Could not parse json file file. Error: %v, FilePath: %s", err, skusFullfilePath)
		return
	}

	for _, sku := range resources {
		resType := strings.ToLower(*sku.ResourceType)
		skuKey := ""
		if resType == "disks" {
			skuKey = fmt.Sprintf("%s-%s-%s", *sku.Name, *sku.Tier, *sku.Size)
		} else if resType == "virtualmachines" {
			skuKey = *sku.Name
		} else {
			continue
		}
		skuKeyLower := strings.ToLower(skuKey)
		if _, ok := skuMap[skuKeyLower]; ok {
			continue
		}
		sku := azuredisk.SkuInfo{ResourceType: &resType, Name: sku.Name, Tier: sku.Tier, Size: sku.Size, Capabilities: sku.Capabilities}
		skuInfo = append(skuInfo, sku)
		skuMap[skuKeyLower] = true
	}

	outputFile, _ := json.MarshalIndent(skuInfo, "", " ")

	err = ioutil.WriteFile(skusFilePath, outputFile, 0644)
	if err != nil {
		klog.Errorf("Could write file. Error: %v, FilePath: %s", err, skusFilePath)
	}
}

func getAllSkus(filePath string) (err error) {
	klog.V(2).Infof("Getting skus and writing to %s", filePath)
	cmd := exec.Command("az", "vm", "list-skus")
	outfile, err := os.Create(filePath)
	var errorBuf bytes.Buffer
	if err != nil {
		klog.Errorf("Could not create file. Error: %v File: %s", err, filePath)
		return err
	}
	defer outfile.Close()

	cmd.Stdout = outfile
	cmd.Stderr = &errorBuf

	err = cmd.Start()
	if err != nil {
		return err
	}
	err = cmd.Wait()
	if err != nil {
		klog.Errorf("Could not get skus. ExitCode: %v Error: %s", err, errorBuf.String())
	}
	return err
}
