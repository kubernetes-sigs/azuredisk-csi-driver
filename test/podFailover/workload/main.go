package main

import (
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"k8s.io/klog"
)

const filePath = "/mnt/azuredisk/outfile"

func main() {

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
	}

	file, err := os.OpenFile(filePath, os.O_RDWR, os.ModeAppend)
	// Write timestamp to the file every second
	go logTimestamp(file)
	defer file.Close()

	//Register prometheus metrics
	containerStoppedTimeMetric := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "default",
		Name:      "failover_testing_workload_downtime",
		Help:      "Downtime seen by the workload pod after failing",
	})

	r := prometheus.NewRegistry()
	r.MustRegister(containerStoppedTimeMetric)

	if timeDifference > 0 {
		containerStoppedTimeMetric.Set(float64(timeDifference))
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(r, promhttp.HandlerOpts{}))
	srv := &http.Server{Addr: ":9090", Handler: mux}

	preStopMux := http.NewServeMux()
	preStopMux.HandleFunc("/cleanup", func(w http.ResponseWriter, r *http.Request) {
		file.Close()
	})
	srv2 := &http.Server{Addr: ":9091", Handler: preStopMux}

	go func() { log.Fatal(srv.ListenAndServe()) }()
	func() { log.Fatal(srv2.ListenAndServe()) }()

}

func logTimestamp(file *os.File) {
	for {
		_, err := file.WriteString(strconv.Itoa(int(time.Now().Unix())))
		if err != nil {
			klog.Errorf("File write on file: %s failed with err: %v", filePath, err)
		}
		time.Sleep(1 * time.Second)
	}
}
