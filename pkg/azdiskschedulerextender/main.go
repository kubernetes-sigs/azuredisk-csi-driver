/*
Copyright 2017 The Kubernetes Authors.

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
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"
	schedulerapi "k8s.io/kube-scheduler/extender/v1"
)

func init() {
	klog.InitFlags(nil)
}

var (
	metricsAddress              = flag.String("metrics-address", "0.0.0.0:29606", "export the metrics")
	azDiskSchedulerExtenderPort = flag.String("port", "8888", "port used by az scheduler extender")
)

const (
	metricsRequestStr    = "/metrics"
	apiPrefix            = "/azdiskschedulerextender"
	filterRequestStr     = apiPrefix + "/filter"
	prioritizeRequestStr = apiPrefix + "/prioritize"
	pingRequestStr       = "/ping"
)

func main() {
	flag.Parse()
	ctxroot := context.Background()
	exportMetrics(ctxroot)

	azDiskSchedulerExtenderEndpoint := fmt.Sprintf("%s%s", ":", *azDiskSchedulerExtenderPort)
	klog.V(2).Infof("Starting azdiskschedulerextender on address %s ...", azDiskSchedulerExtenderEndpoint)

	server := &http.Server{Addr: azDiskSchedulerExtenderEndpoint}
	http.HandleFunc(prioritizeRequestStr, handlePrioritizeRequest)
	http.HandleFunc(filterRequestStr, handleFilterRequest)
	http.HandleFunc(pingRequestStr, handlePingRequest)
	http.HandleFunc("/", handleUnknownRequest)

	ctx, cancel := context.WithCancel(ctxroot)
	defer cancel()

	initSchedulerExtender(ctx)

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		klog.Fatalf("Exiting. Error starting extender server: %v", err)
	}

	klog.V(2).Infof("Exiting azdiskschedulerextender ...")
	os.Exit(0)
}

func handleUnknownRequest(response http.ResponseWriter, request *http.Request) {
	klog.Errorf("handleUnknownRequest: Not implemented request received. Request: %s . Status: %v", request.URL.Path, http.StatusNotImplemented)
	http.Error(response, "Not implemented request received. Request: "+request.URL.Path, http.StatusNotImplemented)
}

func handlePingRequest(response http.ResponseWriter, request *http.Request) {
	// TODO: Return an error code based on resource metrics
	// ex. CPU\Memory usage
	started := time.Now()
	duration := time.Since(started)
	if duration.Seconds() > 10 {
		response.WriteHeader(500)
		_, _ = response.Write([]byte(fmt.Sprintf("error: %v", duration.Seconds())))
		klog.V(2).Infof("handlePingRequest: Received ping request. Responding with 500.")
	} else {
		response.WriteHeader(200)
		_, _ = response.Write([]byte("ok"))
		klog.V(2).Infof("handlePingRequest: Received ping request. Responding with 200.")
	}
}

func handleFilterRequest(response http.ResponseWriter, request *http.Request) {
	var args schedulerapi.ExtenderArgs

	decoder := json.NewDecoder(request.Body)
	encoder := json.NewEncoder(response)

	if err := decoder.Decode(&args); err != nil {
		klog.Errorf("handleFilterRequest: Error decoding filter request: %v", err)
		http.Error(response, "Decode error", http.StatusBadRequest)
		return
	}

	allNodes := args.Nodes.Items
	if len(allNodes) == 0 {
		klog.Errorf("handleFilterRequest: No nodes received in filter request")
		http.Error(response, "Bad request", http.StatusBadRequest)
		return
	}

	responseBody, err := filter(request.Context(), args)
	if err != nil {
		klog.Errorf("handleFilterRequest: Failed to filter: %v", err)
		http.Error(response, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}

	if err := encoder.Encode(responseBody); err != nil {
		klog.Errorf("handleFilterRequest: Error encoding filter response: %+v : %v", responseBody, err)
		http.Error(response, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func handlePrioritizeRequest(response http.ResponseWriter, request *http.Request) {
	var args schedulerapi.ExtenderArgs

	decoder := json.NewDecoder(request.Body)
	encoder := json.NewEncoder(response)
	if err := decoder.Decode(&args); err != nil {
		klog.Errorf("handlePrioritizeRequest: Error decoding prioritize request: %v", err)
		http.Error(response, "Decode error", http.StatusBadRequest)
		return
	}

	responseBody, err := prioritize(request.Context(), args)
	if err != nil {
		klog.Errorf("handlePrioritizeRequest: Failed to prioritize: %v", err)
		http.Error(response, "Service Unavailable", http.StatusServiceUnavailable)
		return
	}

	if err := encoder.Encode(responseBody); err != nil {
		klog.Errorf("handlePrioritizeRequest: Failed to encode response: %v", err)
		http.Error(response, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func exportMetrics(ctx context.Context) {
	l, err := net.Listen("tcp", *metricsAddress)
	if err != nil {
		klog.Warningf("failed to get listener for metrics endpoint: %v", err)
		return
	}
	serve(ctx, l, serveMetrics)
}

func serve(ctx context.Context, l net.Listener, serveFunc func(net.Listener) error) {
	path := l.Addr().String()
	klog.V(2).Infof("set up prometheus server on %v", path)
	go func() {
		defer l.Close()
		if err := serveFunc(l); err != nil {
			klog.Fatalf("serve failure(%v), address(%v)", err, path)
		}
	}()
}

func serveMetrics(l net.Listener) error {
	m := http.NewServeMux()
	m.Handle(metricsRequestStr, legacyregistry.Handler()) //nolint, because azure cloud provider uses legacyregistry currently
	return trapClosedConnErr(http.Serve(l, m))
}

func trapClosedConnErr(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "use of closed network connection") {
		return nil
	}
	return err
}
