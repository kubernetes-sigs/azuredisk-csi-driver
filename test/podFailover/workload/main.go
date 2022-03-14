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
	"crypto/tls"
	"crypto/x509"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"k8s.io/klog/v2"
)

var mountPath = flag.String("mount-path", "", "The path of the file where timestamps will be logged")
var metricsEndpoint = flag.String("metrics-endpoint", "", "The endpoint where prometheus metrics should be published")
var authEnabled = flag.Bool("auth-enabled", false, "Specify whether request to metrics endpoint requires authentication or not")
var testName = flag.String("test-name", "default-test-run", "The name of the test to be used as metrics label to distinguish metrics sent from multiple test runs")

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

		client := &http.Client{}

		if *authEnabled {

			// Read the values from the secret
			var caCert = os.Getenv("CA_CERT")
			var clientCert = os.Getenv("CLIENT_CERT")
			var clientKey = os.Getenv("CLIENT_KEY")

			caCertPool := x509.NewCertPool()
			caCertPool.AppendCertsFromPEM([]byte(caCert))
			cert, err := tls.X509KeyPair([]byte(clientCert), []byte(clientKey))

			if err != nil {
				klog.Errorf("Error occurred while parsing the certificate %v", err)
			}

			client = &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						RootCAs:      caCertPool,
						Certificates: []tls.Certificate{cert},
					},
				},
			}
		}

		metricsEndpointURL, err := url.Parse(*metricsEndpoint)
		if err != nil {
			klog.Infof("Error occurred while parsing the url %v", *metricsEndpoint)
		}

		//Make a request to the metrics service
		query := metricsEndpointURL.Query()
		query.Add("value", strconv.Itoa(int(timeDifference)))
		query.Add("testName", *testName)
		metricsEndpointURL.RawQuery = query.Encode()

		resp, err := client.Get(metricsEndpointURL.String())
		if err != nil {
			klog.Infof("Error occurred while making the http call to metrics publisher: %v", err)
		}
		if resp != nil {
			resp.Body.Close()
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
