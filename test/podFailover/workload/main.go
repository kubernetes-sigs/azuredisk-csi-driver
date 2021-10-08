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

const metricsServiceURL = "metrics-publisher-service.default"
const metricsServiceEndpoint = "/podDowntime"

var mountPath = flag.String("mount-path", "", "The path of the file where timestamps will be logged")

func main() {

	flag.Parse()
	filePath := *mountPath + "/outfile"
	klog.Infof("The file path is %s", filePath)
	fi, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		os.Create(filePath)
		fi, err = os.Stat(filePath)
	}
	fileModTime := fi.ModTime()

	var timeDifference int64
	if fi.Size() > 0 {
		timeDifference = time.Now().Unix() - fileModTime.Unix()
		klog.Infof("The downtime seen by the pod is %d", timeDifference)
		//Make a request to the metrics service
		req, err := http.NewRequest("GET", "http://20.150.156.25:9091/podDowntime", nil)
		if err != nil {
			klog.Errorf("Error occured while creating the get http request: %v", err)
		}
		query := req.URL.Query()
		query.Add("value", strconv.Itoa(int(timeDifference)))
		req.URL.RawQuery = query.Encode()
		client := &http.Client{}

		_, err = client.Do(req)
		if err != nil {
			klog.Infof("Error occured while making the http call to metrics publisher: %v", err)
		}
	}

	file, err := os.OpenFile(filePath, os.O_RDWR, os.ModeAppend)
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
