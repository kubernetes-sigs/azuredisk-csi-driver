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
	"flag"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"k8s.io/klog"
)

var mountPath = flag.String("mount-path", "", "The path of the file where timestamps will be logged")
var metricsEndpoint = flag.String("metrics-endpoint", "", "The endpoint where prometheus metrics should be published")

func main() {

	flag.Parse()
	filePath := *mountPath + "/outfile"
	klog.Infof("The file path is %s", filePath)
	fi, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		_, err = os.Create(filePath)
		if err != nil {
			log.Fatalf("Error occurred while creating the file %s: %v", filePath, err)
		}
		fi, err = os.Stat(filePath)
		if err != nil {
			klog.Errorf("Error occurred while getting the stats for file %s: %v", filePath, err)
		}
	}
	fileModTime := fi.ModTime()

	var timeDifference int64
	if fi.Size() > 0 {
		timeDifference = time.Now().Unix() - fileModTime.Unix()
		klog.Infof("The downtime seen by the pod is %d", timeDifference)
		//Make a request to the metrics service
		req, err := http.NewRequest("GET", *metricsEndpoint, nil)
		if err != nil {
			klog.Errorf("Error occurred while creating the get http request: %v", err)
		}
		query := req.URL.Query()
		query.Add("value", strconv.Itoa(int(timeDifference)))
		req.URL.RawQuery = query.Encode()
		client := &http.Client{}

		_, err = client.Do(req)
		if err != nil {
			klog.Infof("Error occurred while making the http call to metrics publisher: %v", err)
		}
	}

	file, err := os.OpenFile(filePath, os.O_RDWR, os.ModeAppend)
	if err != nil {
		log.Fatalf("Couldn't open the file %s", filePath)
	}
	// Write timestamp to the file every second
	go logTimestamp(file)
	defer file.Close()

	preStopMux := http.NewServeMux()
	preStopMux.HandleFunc("/cleanup", func(w http.ResponseWriter, r *http.Request) {
		file.Close()
	})
	srv := &http.Server{Addr: ":9091", Handler: preStopMux}

	func() { log.Fatal(srv.ListenAndServe()) }()

}

func logTimestamp(file *os.File) {
	for {
		_, err := file.WriteString(strconv.Itoa(int(time.Now().Unix())))
		if err != nil {
			klog.Errorf("File write on file: %s failed with err: %v", file.Name(), err)
		}
		time.Sleep(1 * time.Second)
	}
}
