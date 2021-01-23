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
	"flag"
	"fmt"
	"encoding/json"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
	v1 "k8s.io/api/core/v1"
	"k8s.io/component-base/metrics/legacyregistry"
	"k8s.io/klog/v2"
	schedulerapi "k8s.io/kube-scheduler/extender/v1"
	"time"
)

func init() {
	klog.InitFlags(nil)
}

var (
	metricsAddress = flag.String("metrics-address", "0.0.0.0:29604", "export the metrics")
	azDiskSchedulerExtenderPort = flag.String("port", "8080", "port used by az scheduler extender")	
)

const (
	metricsRequestStr = "/metrics"
	apiPrefix = "/azdiskschedulerextender"
	filterRequestStr     = apiPrefix + "/filter"
	prioritizeRequestStr = apiPrefix + "/prioritize"
	pingRequestStr = "/ping"
)

func main() {
	flag.Parse();
	exportMetrics()

	azDiskSchedulerExtenderEndpoint := fmt.Sprintf("%s%s", ":", *azDiskSchedulerExtenderPort)
	klog.V(2).Infof("Starting azdiskschedulerextender on address %s ...", azDiskSchedulerExtenderEndpoint)

	server := &http.Server{Addr: azDiskSchedulerExtenderEndpoint}
	http.HandleFunc(prioritizeRequestStr, handlePrioritizeRequest)
	http.HandleFunc(filterRequestStr, handleFilterRequest)
	http.HandleFunc(pingRequestStr, handlePingRequest)
	http.HandleFunc("/", handleUnknownRequest)

	if err := server.ListenAndServe(); err != http.ErrServerClosed {
		klog.Fatalf("Exiting. Error starting extender server: %v", err)
	}
	klog.V(2).Infof("Exiting azdiskschedulerextender ...")
	os.Exit(0)
}

func handleUnknownRequest(response http.ResponseWriter, request *http.Request) {
	klog.Errorf("handleUnknownRequest: Not implemented request received. Request: %s . Status: %v", request.URL.Path, http.StatusNotImplemented)
	http.Error(response, "Not implemented request received. Request: " + request.URL.Path, http.StatusNotImplemented)
}

func handlePingRequest(response http.ResponseWriter, request *http.Request) {
	// TODO: Return an error code based on resource metrics
	// ex. CPU\Memory usage
	started := time.Now()
	duration := time.Now().Sub(started)
    if duration.Seconds() > 10 {
        response.WriteHeader(500)
		response.Write([]byte(fmt.Sprintf("error: %v", duration.Seconds())))
		klog.V(2).Infof("handlePingRequest: Received ping request. Responding with 500.")
    } else {
        response.WriteHeader(200)
        response.Write([]byte("ok"))
		klog.V(2).Infof("handlePingRequest: Received ping request. Responding with 200.")
    }
}

func handleFilterRequest(response http.ResponseWriter, request *http.Request) {
	decoder := json.NewDecoder(request.Body)
	defer func() {
		if err := request.Body.Close(); err != nil {
			klog.Errorf("handleFilterRequest: Error closing decoder: %v", err)
		}
	}()
	encoder := json.NewEncoder(response)

	var args schedulerapi.ExtenderArgs
	if err := decoder.Decode(&args); err != nil {
		klog.Errorf("handleFilterRequest: Error decoding filter request: %v", err)
		http.Error(response, "Decode error", http.StatusBadRequest)
		return
	}

	filteredNodes := args.Nodes.Items

	// TODO: Filter the nodes based on node heartbeat and AzDiverNode CRI status
	for _, node := range filteredNodes {
		klog.V(2).Infof("handleFilterRequest: %v %+v", node.Name, node.Status.Addresses)
	}
	responseNodes := &schedulerapi.ExtenderFilterResult{
		Nodes: &v1.NodeList{
			Items: filteredNodes,
		},
	}
	if err := encoder.Encode(responseNodes); err != nil {
		klog.Errorf("handleFilterRequest: Error encoding filter response: %+v : %v", responseNodes, err)
	}
}

func handlePrioritizeRequest(response http.ResponseWriter, request *http.Request) {
	decoder := json.NewDecoder(request.Body)
	
	defer func() {
		if err := request.Body.Close(); err != nil {
			klog.Warningf("handlePrioritizeRequest: Error closing decoder: %v", err)
		}
	}()

	encoder := json.NewEncoder(response)

	var args schedulerapi.ExtenderArgs
	if err := decoder.Decode(&args); err != nil {
		klog.Errorf("handlePrioritizeRequest: Error decoding filter request: %v", err)
		http.Error(response, "Decode error", http.StatusBadRequest)
		return
	}
	
	respList := schedulerapi.HostPriorityList{}
	rand.Seed(time.Now().UnixNano())
	for _, node := range args.Nodes.Items {
		score := getNodeScore(&node)
		hostPriority := schedulerapi.HostPriority{Host: node.Name, Score: score}
		respList = append(respList, hostPriority)
	}

	klog.V(2).Infof("handlePrioritizeRequest: Nodes for pod %+v in response:", args.Pod)
	for _, node := range respList {
		klog.V(2).Infof("handlePrioritizeRequest: %+v", node)
	}

	if err := encoder.Encode(respList); err != nil {
		klog.Errorf("handlePrioritizeRequest: Failed to encode response: %v", err)
	}
}

// TODO: Populate the node score based on number of disks already attached.
func getNodeScore(node *v1.Node) int64 {
	score := rand.Int63()
	return score
}

func exportMetrics() {
	l, err := net.Listen("tcp", *metricsAddress)
	if err != nil {
		klog.Warningf("failed to get listener for metrics endpoint: %v", err)
		return
	}
	serve(context.Background(), l, serveMetrics)
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
