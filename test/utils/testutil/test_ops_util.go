/*
Copyright 2019 The Kubernetes Authors.

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

package testutil

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	restclientset "k8s.io/client-go/rest"
	"k8s.io/kubernetes/test/e2e/framework"

	consts "sigs.k8s.io/azuredisk-csi-driver/pkg/azureconstants"
	testconsts "sigs.k8s.io/azuredisk-csi-driver/test/const"
	testtypes "sigs.k8s.io/azuredisk-csi-driver/test/types"
)

// Normalize volumes by adding allowed topology values and WaitForFirstConsumer binding mode if we are testing in a multi-az cluster
func NormalizeVolumes(volumes []testtypes.VolumeDetails, allowedTopologies []string, isMultiZone bool) []testtypes.VolumeDetails {
	for i := range volumes {
		volumes[i] = NormalizeVolume(volumes[i], allowedTopologies, isMultiZone)
	}

	return volumes
}

func NormalizeVolume(volume testtypes.VolumeDetails, allowedTopologies []string, isMultiZone bool) testtypes.VolumeDetails {
	driverName := os.Getenv(testconsts.AzureDriverNameVar)
	switch driverName {
	case "kubernetes.io/azure-disk":
		volumeBindingMode := storagev1.VolumeBindingWaitForFirstConsumer
		volume.VolumeBindingMode = &volumeBindingMode
	case "", consts.DefaultDriverName:
		if !isMultiZone {
			return volume
		}
		volume.AllowedTopologyValues = allowedTopologies
		volumeBindingMode := storagev1.VolumeBindingWaitForFirstConsumer
		volume.VolumeBindingMode = &volumeBindingMode
	}

	return volume
}

func RestClient(group string, version string) (restclientset.Interface, error) {
	config, err := framework.LoadConfig()
	if err != nil {
		ginkgo.Fail(fmt.Sprintf("could not load config: %v", err))
	}
	gv := schema.GroupVersion{Group: group, Version: version}
	config.GroupVersion = &gv
	config.APIPath = "/apis"
	config.NegotiatedSerializer = serializer.WithoutConversionCodecFactory{CodecFactory: serializer.NewCodecFactory(runtime.NewScheme())}
	return restclientset.RESTClientFor(config)
}

func ExecTestCmd(cmds []testtypes.TestCmd) {
	err := os.Chdir("../..")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	defer func() {
		err := os.Chdir("test/e2e")
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}()

	projectRoot, err := os.Getwd()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(strings.HasSuffix(projectRoot, "azuredisk-csi-driver")).To(gomega.Equal(true))

	for _, cmd := range cmds {
		log.Println(cmd.StartLog)
		cmdSh := exec.Command(cmd.Command, cmd.Args...)
		cmdSh.Dir = projectRoot
		cmdSh.Stdout = os.Stdout
		cmdSh.Stderr = os.Stderr
		err = cmdSh.Run()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		log.Println(cmd.EndLog)
	}
}

func ConvertToPowershellorCmdCommandIfNecessary(command string) string {
	if !testconsts.IsWindowsCluster {
		return command
	}

	switch command {
	case "echo 'hello world' > /mnt/test-1/data && grep 'hello world' /mnt/test-1/data":
		return "echo 'hello world' | Out-File -FilePath C:\\mnt\\test-1\\data.txt; Get-Content C:\\mnt\\test-1\\data.txt | findstr 'hello world'"
	case "touch /mnt/test-1/data":
		return "echo $null >> C:\\mnt\\test-1\\data"
	case "while true; do echo $(date -u) >> /mnt/test-1/data; sleep 3600; done":
		return "while (1) { Add-Content -Encoding Ascii C:\\mnt\\test-1\\data.txt $(Get-Date -Format u); sleep 3600 }"
	case "echo 'hello world' >> /mnt/test-1/data && while true; do sleep 3600; done":
		return "echo 'hello world' | Out-File -Append -FilePath C:\\mnt\\test-1\\data.txt; Get-Content C:\\mnt\\test-1\\data.txt | findstr 'hello world'; Start-Sleep 3600"
	case "echo 'hello world' > /mnt/test-1/data && echo 'hello world' > /mnt/test-2/data && echo 'hello world' > /mnt/test-3/data && grep 'hello world' /mnt/test-1/data && grep 'hello world' /mnt/test-2/data && grep 'hello world' /mnt/test-3/data":
		return "echo 'hello world' | Out-File -FilePath C:\\mnt\\test-1\\data.txt; Get-Content C:\\mnt\\test-1\\data.txt | findstr 'hello world'; echo 'hello world' | Out-File -FilePath C:\\mnt\\test-2\\data.txt; Get-Content C:\\mnt\\test-2\\data.txt | findstr 'hello world'; echo 'hello world' | Out-File -FilePath C:\\mnt\\test-3\\data.txt; Get-Content C:\\mnt\\test-3\\data.txt | findstr 'hello world'"
	case "while true; do sleep 5; done":
		return "while ($true) { Start-Sleep 5 }"
	}

	return command
}
