// +build linux

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

package azuredisk

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/klog/v2"
)

type DeviceHelper struct{}

func (deviceHelper *DeviceHelper) OptimizeDevicePerformance(nodeInfo *NodeInfo, diskSkus map[string]map[string]DiskSkuInfo, devicePath string, attributes map[string]string) (err error) {

	klog.V(2).Infof("OptimizeDevicePerformance: Tuning settings for %s", devicePath)

	if nodeInfo == nil {
		err = fmt.Errorf("Node info is not provided. Error: invalid parameter")
		klog.Errorf("Error: %s", err)
		return err
	}

	if diskSkus == nil {
		err = fmt.Errorf("Disk SKUs are not provided. Error: invalid parameter")
		klog.Errorf("Error: %s", err)
		return err

	}

	mode, profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr := GetDiskPerfAttributes(attributes)

	if !IsPerfTuningEnabled(mode) {
		klog.Warningf("OptimizeDevicePerformance: Perf tuning not enabled for mode %s devicepath %s", mode, devicePath)
		return nil
	}

	if !AccountSupportsPerfOptimization(accountType) {
		klog.Warningf("OptimizeDevicePerformance: Perf tuning not enabled for accountType %s devicepath %s", accountType, devicePath)
		return nil
	}

	queueDepth, nrRequests, scheduler, maxSectorsKb, readAheadKb, err := getOptimalDeviceSettings(nodeInfo, diskSkus, mode, profile, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr)
	if err != nil {
		klog.Errorf("OptimizeDevicePerformance: Failed to get optimal settings for mode %s profile %s accountType %s device %s Error: %s", mode, profile, accountType, devicePath, err)
		return err
	}

	deviceName, err := getDeviceName(devicePath)
	if err != nil {
		klog.Errorf("OptimizeDevicePerformance: Could not get deviceName for %s. Error: %s", devicePath, err)
		return err
	}

	klog.V(2).Infof("OptimizeDevicePerformance: Tuning settings for devicePath %s, deviceName %s, profile %s queueDepth %s nrRequests %s scheduler %s maxSectorsKb %s readAheadKb %s",
		devicePath,
		deviceName,
		profile,
		queueDepth,
		nrRequests,
		scheduler,
		maxSectorsKb,
		readAheadKb)

	err = echoToFile(maxSectorsKb, filepath.Join("/sys/block", deviceName, "queue/max_sectors_kb"))
	if err != nil {
		klog.Errorf("OptimizeDevicePerformance: Could not set max_sectors_kb for device %s. Error: %v", deviceName, err)
		return err
	}

	err = echoToFile(scheduler, filepath.Join("/sys/block", deviceName, "queue/scheduler"))
	if err != nil {
		klog.Errorf("OptimizeDevicePerformance: Could not set scheduler for device %s. Error: %v", deviceName, err)
		return err
	}

	err = echoToFile("1", filepath.Join("/sys/block", deviceName, "queue/iosched/fifo_batch"))
	if err != nil {
		klog.Errorf("OptimizeDevicePerformance: Could not set scheduler for device %s. Error: %v", deviceName, err)
		return err
	}

	err = echoToFile("1", filepath.Join("/sys/block", deviceName, "queue/iosched/writes_starved"))
	if err != nil {
		klog.Errorf("OptimizeDevicePerformance: Could not set scheduler for device %s. Error: %v", deviceName, err)
		return err
	}

	err = echoToFile(queueDepth, filepath.Join("/sys/block", deviceName, "device/queue_depth"))
	if err != nil {
		klog.Errorf("OptimizeDevicePerformance: Could not set queue_depth for device %s. Error: %v", deviceName, err)
		return err
	}

	err = echoToFile(nrRequests, filepath.Join("/sys/block", deviceName, "queue/nr_requests"))
	if err != nil {
		klog.Errorf("OptimizeDevicePerformance: Could not set nr_requests for device %s. Error: %v", deviceName, err)
		return err
	}

	err = echoToFile(readAheadKb, filepath.Join("/sys/block", deviceName, "queue/read_ahead_kb"))
	if err != nil {
		klog.Errorf("OptimizeDevicePerformance: Could not set max_sectors_kb for device %s. Error: %v", deviceName, err)
		return err
	}

	err = echoToFile("0", filepath.Join("/sys/block", deviceName, "queue/wbt_lat_usec"))
	if err != nil {
		klog.Errorf("OptimizeDevicePerformance: Could not set max_sectors_kb for device %s. Error: %v", deviceName, err)
		return err
	}

	err = echoToFile("0", filepath.Join("/sys/block", deviceName, "queue/rotational"))
	if err != nil {
		klog.Errorf("OptimizeDevicePerformance: Could not set max_sectors_kb for device %s. Error: %v", deviceName, err)
		return err
	}

	return err
}

// getDeviceName gets the device name from the device lunpath
// Device lun path is of the format /dev/disk/azure/scsi1/lun0
func getDeviceName(lunPath string) (deviceName string, err error) {
	devicePath, err := filepath.EvalSymlinks(lunPath)
	if err != nil {
		klog.Errorf("Path %s is not a symlink. Error: %v", lunPath, err)
		return "", err
	}

	return filepath.Base(devicePath), nil
}

// echoToFile echos setting value to the file
func echoToFile(content string, filePath string) (err error) {
	klog.V(2).Infof("Echoing %s to %s", content, filePath)
	cmd := exec.Command("echo", content)
	outfile, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer outfile.Close()

	cmd.Stdout = outfile

	err = cmd.Start()
	if err != nil {
		return err
	}
	err = cmd.Wait()
	if err != nil {
		klog.Errorf("Could not make the set %s to file %s. Error: %v", content, filePath, err)
	}
	return err
}

func getOptimalDeviceSettings(nodeInfo *NodeInfo, diskSkus map[string]map[string]DiskSkuInfo, tuningMode string, perfProfile string, accountType string, diskSizeGibStr string, diskIopsStr string, diskBwMbpsStr string) (queueDepth string, nrRequests string, scheduler string, maxSectorsKb string, readAheadKb string, err error) {
	klog.V(2).Infof("Calculating perf optimizations for mode %s profile %s accountType %s diskSize", tuningMode, perfProfile, accountType, diskSizeGibStr)

	err = nil

	iopsHeadRoom := .25
	maxHwSectorsKb := 512.0

	// TODO: Get matching disk SKU
	// In future get the disk SKU using disk size ex. P10, P30 etc
	diskSku, err := getMatchingDiskSku(diskSkus, accountType, diskSizeGibStr, diskIopsStr, diskBwMbpsStr)

	if err != nil || diskSku == nil {
		err = fmt.Errorf("Could not find sku for account %s size %s. Error: sku not found", accountType, diskSizeGibStr)
		klog.Errorf("Error: %s", err)
		return queueDepth, nrRequests, scheduler, maxSectorsKb, readAheadKb, err
	}

	maxBurstIops := math.Min(float64(diskSku.maxBurstIops), float64(nodeInfo.maxBurstIops))

	// unless otherwise specified, BW Units are in kB (not KB) which is 1000 bytes
	maxBurstBw := math.Min(float64(diskSku.maxBurstBwMbps), float64(nodeInfo.maxBurstBwMbps)) * 1000

	// Adjusted burst IOs possible
	iopsSeqIo := maxBurstIops * (1 - iopsHeadRoom)

	// Request size needed to get max Burst BW, capped at maxHwSectorsKb
	rsMinSeqIo := math.Ceil(math.Min(maxHwSectorsKb, (maxBurstBw / iopsSeqIo)))

	// TODO: dynamically calculate this
	latencyPerIoToGetMaxBwInSec := diskSku.GetSequentialOLatencyInSec()
	latencyForSmallRandOperationInSec := diskSku.GetRandomIOLatencyInSec()

	// queue_depth needed to drive maxBurstBw with IO size rsMinSeqIo and each IO taking latencyPerIoToGetMaxBwInSec
	qdMaxSeqBw := math.Ceil(maxBurstBw * latencyPerIoToGetMaxBwInSec / rsMinSeqIo)

	// queue_depth needed for random IOs to hit IOPS reserved for them
	qdMaxRandomIops := math.Ceil(maxBurstIops * iopsHeadRoom * latencyForSmallRandOperationInSec)

	maxSectorsKb = fmt.Sprintf("%g", rsMinSeqIo)
	scheduler = "mq-deadline"

	qdTotal := fmt.Sprintf("%g", math.Ceil(qdMaxRandomIops+qdMaxSeqBw))
	queueDepth = qdTotal
	nrRequests = qdTotal

	readAheadKb = fmt.Sprintf("%g", math.Ceil(qdMaxSeqBw*rsMinSeqIo))

	klog.V(2).Infof("Returning perf attributes for tuningMode %s perfProfile %s accountType %s queueDepth %s nrRequests %s scheduler %s maxSectorsKb %s readAheadKb %s",
		tuningMode,
		perfProfile,
		accountType,
		queueDepth,
		nrRequests,
		scheduler,
		maxSectorsKb,
		readAheadKb)
	return queueDepth, nrRequests, scheduler, maxSectorsKb, readAheadKb, err
}

// getMatchingDiskSku gets the smallest SKU which matches the size, io and bw requirement
// TODO: Query the disk size (e.g. P10, P30 etc) and use that to find the sku
func getMatchingDiskSku(diskSkus map[string]map[string]DiskSkuInfo, accountType string, diskSizeGibStr string, diskIopsStr string, diskBwMbpsStr string) (matchingSku *DiskSkuInfo, err error) {

	err = nil
	matchingSku = nil
	accountTypeLower := strings.ToLower(accountType)
	skus, ok := diskSkus[accountTypeLower]

	if !ok || skus == nil || len(diskSkus[accountTypeLower]) <= 0 {
		err = fmt.Errorf("Could not find sku for account %s. Error: sku not found", accountType)
		klog.Errorf("Error: %s", err)
		return nil, err
	}

	diskSizeGb, err := strconv.Atoi(diskSizeGibStr)
	if err != nil {
		err = fmt.Errorf("Could not parse disk size %s. Error: incorrect sku size", diskSizeGibStr)
		klog.Errorf("Error: %s", err)
		return nil, err
	}

	// Treating these as non required field, as they come as part of the provisioned size
	// If these are explicitly set then that will be used to get the best possible match
	diskIops, err := strconv.Atoi(diskIopsStr)
	if err != nil {
		diskIops = 0
	}
	diskBwMbps, err := strconv.Atoi(diskBwMbpsStr)
	if err != nil {
		diskBwMbps = 0
	}

	for _, sku := range diskSkus[accountTypeLower] {
		// Use the smallest sku size which can fulfil Size, IOs and BW requirements
		if meetsRequest(&sku, diskSizeGb, diskIops, diskBwMbps) {
			if matchingSku == nil || sku.maxSizeGiB < matchingSku.maxSizeGiB {
				tempSku := sku
				matchingSku = &tempSku
			}
		}
	}

	return matchingSku, nil
}

// meetsRequest checks to see if given SKU meets\has enough size, iops and bw limits
func meetsRequest(sku *DiskSkuInfo, diskSizeGb int, diskIops int, diskBwMbps int) bool {

	if sku == nil {
		return false
	}

	if sku.maxSizeGiB >= diskSizeGb && sku.maxBwMbps >= diskBwMbps && sku.maxIops >= diskIops {
		return true
	}

	return false
}
