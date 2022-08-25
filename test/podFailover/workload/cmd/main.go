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
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Azure/azure-amqp-common-go/v3/aad"
	eventhub "github.com/Azure/azure-event-hubs-go/v3"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
	podfailure "sigs.k8s.io/azuredisk-csi-driver/test/podFailover/apis/client/clientset/versioned"
	podfailurev1beta1 "sigs.k8s.io/azuredisk-csi-driver/test/podFailover/apis/podfailure/v1beta1"
)

var mountPath = flag.String("mount-path", "/", "The path of the file where timestamps will be logged")
var metricsEndpoint = flag.String("metrics-endpoint", "", "The endpoint where prometheus metrics should be published")
var authEnabled = flag.Bool("auth-enabled", false, "Specify whether request to metrics endpoint requires authentication or not")
var runID = flag.String("run-id", "", "The run id of the test")
var storageProvisioner = flag.String("storage-provisioner", "", "The underlying storage provisioner of test")
var workloadType = flag.String("workload-type", "default-workload-type", "The type of test being run (1pod1pvc, 3pod3pvc, etc)")
var driverVersion = flag.String("driver-version", "", "The version of csi-driver")
var namespace = flag.String("namespace", "pod-failover", "The namespace resources are created in")
var kubeconfig = flag.String("kubeconfig", "", "kube config path for cluster")

type DowntimeMetric struct {
	TimeStamp     string `json:"timeStamp"`
	RunID         string `json:"runID"`
	StorageType   string `json:"storageType"`
	DriverVersion string `json:"driverVersion"`
	ImageVersion  string `json:"imageVersion"`
	FailureType   string `json:"failureType"`
	WorkloadType  string `json:"workloadType"`
	PodDowntime   int64  `json:"downtime"`
}

func main() {
	flag.Parse()
	var config *rest.Config
	var err error
	// get config from flag first, then env variable, then inclusterconfig, then default
	if *kubeconfig != "" {
		config, _ = clientcmd.BuildConfigFromFlags("", *kubeconfig)
	} else {
		kubeConfigPath := os.Getenv("KUBECONFIG")
		if len(kubeConfigPath) == 0 {
			config, err = rest.InClusterConfig()
			if err != nil {
				defaultKubeConfigPath := filepath.Join(os.Getenv("HOME"), ".kube", "config")
				config, _ = clientcmd.BuildConfigFromFlags("", defaultKubeConfigPath)
			}
		} else {
			config, _ = clientcmd.BuildConfigFromFlags("", kubeConfigPath)
		}
	}

	clientset, _ := kubernetes.NewForConfig(config)

	podFailoverClient, err := podfailure.NewForConfig(config)
	if err != nil {
		klog.Errorf("Error occurred while getting pod failure client: %v", err)
	}

	ctx := context.Background()

	podName := os.Getenv("MY_POD_NAME")

	var podfailurecrd *podfailurev1beta1.PodFailure
	podfailurecrd, err = getCrd(ctx, podFailoverClient, podName)
	if err != nil {
		klog.Errorf("Error occurred while getting podfailure crd for pod %s: %v", podName, err)
	}

	if podfailurecrd == nil {
		podfailurecrd, err = createCrd(ctx, podFailoverClient, podName)
		if err != nil {
			klog.Errorf("Error occurred while getting or creating podfailure crd for pod %s: %v", podName, err)
		}
	} else {
		downtime := calculateDowntime(podfailurecrd.Status.HeartBeat)
		reportDowntime(ctx, clientset, downtime, podfailurecrd.Status.FailureType)
	}

	if *storageProvisioner != "" {
		runStatefulWorkload(ctx, clientset)
	} else {
		runStatelessWorkload(ctx, clientset, podFailoverClient, podfailurecrd)
	}
}

func runStatefulWorkload(ctx context.Context, clientset *kubernetes.Clientset) {
	filePath := *mountPath + "/outfile"
	klog.Infof("The file path is %s", filePath)
	_, err := os.Stat(filePath)
	if os.IsNotExist(err) {
		_, err = os.Create(filePath)
		if err != nil {
			log.Fatalf("Error occurred while creating the file %s: %v", filePath, err)
		}
		_, err = os.Stat(filePath)
		if err != nil {
			klog.Errorf("Error occurred while getting the stats for file %s: %v", filePath, err)
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

func runStatelessWorkload(ctx context.Context, clientset *kubernetes.Clientset, podFailoverClient *podfailure.Clientset, failureCrd *podfailurev1beta1.PodFailure) {
	go reportTimestamp(ctx, podFailoverClient, failureCrd)
	preStopMux := http.NewServeMux()
	preStopMux.HandleFunc("/cleanup", func(w http.ResponseWriter, r *http.Request) {
	})
	srv := &http.Server{Addr: ":9091", Handler: preStopMux}

	func() { log.Fatal(srv.ListenAndServe()) }()
}

func reportDowntime(ctx context.Context, clientset *kubernetes.Clientset, downtime int64, failureType string) {
	klog.Infof("The downtime seen by the pod is %d with type %s", downtime, failureType)

	if os.Getenv("AZURE_CLIENT_ID") != "" {
		var imageVersion string
		var err error
		if *storageProvisioner != "" {
			imageVersion, err = getImageVersion(ctx, clientset, *storageProvisioner)
			if err != nil {
				klog.Errorf("Error occurred while getting driver version: %v", err)
			}
		} else {
			imageVersion = ""
		}
		downtimeRecord := DowntimeMetric{time.Now().Format(time.RFC3339), *runID, *storageProvisioner, *driverVersion, imageVersion, failureType, *workloadType, downtime}
		downtimeRecordJSONBytes, err := json.Marshal(downtimeRecord)
		if err != nil {
			log.Fatalf("failed to marshal downtime record to json: %s\n", err)
		}
		downtimeRecordJSON := string(downtimeRecordJSONBytes)

		tokenProvider, err := aad.NewJWTProvider(aad.JWTProviderWithEnvironmentVars())
		if err != nil {
			log.Fatalf("failed to configure AAD JWT provider: %s\n", err)
		}

		hub, err := eventhub.NewHub("xstorecontainerstorage", "podfailover", tokenProvider)
		if err != nil {
			klog.Errorf("Error occurred while connecting to event hub: %v", err)
		}

		err = hub.Send(ctx, eventhub.NewEventFromString(downtimeRecordJSON))
		if err != nil {
			klog.Errorf("Error occurred while sending to event hub: %v", err)
		}
	}

	if *metricsEndpoint != "" {
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
		query.Add("value", strconv.Itoa(int(downtime)))
		query.Add("run-id", *runID)
		metricsEndpointURL.RawQuery = query.Encode()

		resp, err := client.Get(metricsEndpointURL.String())
		if err != nil {
			klog.Infof("Error occurred while making the http call to metrics publisher: %v", err)
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
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

func reportTimestamp(ctx context.Context, clientset *podfailure.Clientset, crd *podfailurev1beta1.PodFailure) {
	for {
		podfailureobj, err := clientset.PodfailureV1beta1().PodFailures(*namespace).Get(ctx, crd.Name, metav1.GetOptions{})
		if err != nil {
			klog.Errorf("failed to get crd for pod %q: %v", crd.Name, err)
		}
		updatedPodFailure := &podfailurev1beta1.PodFailure{}
		podfailureobj.DeepCopyInto(updatedPodFailure)

		updatedPodFailure.Status.HeartBeat = time.Now().Format(time.RFC3339)

		_, err = clientset.PodfailureV1beta1().PodFailures(*namespace).Update(ctx, updatedPodFailure, metav1.UpdateOptions{})
		if err != nil {
			klog.Errorf("Error updating pod failure crd for pod %s: %v", crd.Spec.PodName, err)
		}
		time.Sleep(1 * time.Second)
	}
}

func getCrd(ctx context.Context, podFailoverClient *podfailure.Clientset, podName string) (*podfailurev1beta1.PodFailure, error) {
	podfailureobj, err := podFailoverClient.PodfailureV1beta1().PodFailures(*namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get pod failure crd for pod %s: %v", podName, err)
	}
	return podfailureobj, nil
}

func createCrd(ctx context.Context, podFailoverClient *podfailure.Clientset, podName string) (*podfailurev1beta1.PodFailure, error) {
	newpodfailureobj := &podfailurev1beta1.PodFailure{
		ObjectMeta: metav1.ObjectMeta{
			Name: podName,
		},
		Spec: podfailurev1beta1.PodFailureSpec{
			PodName: podName,
		},
		Status: podfailurev1beta1.PodFailureStatus{},
	}
	podfailureobj, err := podFailoverClient.PodfailureV1beta1().PodFailures(*namespace).Create(ctx, newpodfailureobj, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create pod failure crd for pod %s: %v", podName, err)
	}
	return podfailureobj, nil
}

func getImageVersion(ctx context.Context, client *kubernetes.Clientset, storageProvisioner string) (string, error) {
	res, err := client.StorageV1().CSIDrivers().Get(ctx, storageProvisioner, metav1.GetOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			return "", nil
		}
		return "", fmt.Errorf("failed to get driver version for storage type %s: %v", storageProvisioner, err)
	}
	version, exists := res.Annotations["csiDriver"]
	if exists {
		return version, nil
	}
	return "", nil
}

func calculateDowntime(heartbeat string) int64 {
	var downtime int64
	if *storageProvisioner != "" {
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

		if fi.Size() > 0 {
			downtime = time.Now().Unix() - fileModTime.Unix()
		}
	} else {
		failureTime, err := time.Parse(time.RFC3339, heartbeat)
		if err != nil {
			klog.Errorf("Error occurred while converting heartbeat %s: %v", heartbeat, err)
		}
		downtime = time.Now().Unix() - failureTime.Unix()
	}

	return downtime
}
