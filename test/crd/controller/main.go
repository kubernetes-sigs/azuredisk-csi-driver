package main

import (
	"os"
	"os/signal"
	"syscall"
	"strings"

	log "github.com/sirupsen/logrus"
	meta_v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"

	azVolumeClientSet "github.com/abhisheksinghbaghel/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
	azVolumeInformerV1Beta1 "github.com/abhisheksinghbaghel/azuredisk-csi-driver/pkg/apis/client/informers/externalversions/azuredisk/v1beta1"
)

// retrieve the Kubernetes cluster client from outside of the cluster
func getKubernetesClient() (kubernetes.Interface, azVolumeClientSet.Interface) {

	// construct the path to resolve to `~/.kube/config`
	kubeConfigPath := os.Getenv("KUBERNETES_KUBE_CONFIG")
	if strings.EqualFold(kubeConfigPath, "") {
		kubeConfigPath = os.Getenv("HOME") + "/.kube/config"
	}

	// create the config from the path
	config, err := clientcmd.BuildConfigFromFlags("", kubeConfigPath)
	if err != nil {
		log.Fatalf("getClusterConfig: %v", err)
	}

	// generate the client based off of the config
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("getClusterConfig: %v", err)
	}

	azVolumeClient, err := azVolumeClientSet.NewForConfig(config)
	if err != nil {
		log.Fatalf("getClusterConfig: %v", err)
	}

	log.Info("Successfully constructed k8s client")
	return client, azVolumeClient
}

// main code path
func main() {
	// get the Kubernetes client for connectivity
	client, volumeClient := getKubernetesClient()

	volumeInformer := azVolumeInformerV1Beta1.NewAzVolumeInformer(
		volumeClient,
		meta_v1.NamespaceAll,
		0,
		cache.Indexers{},
	)

	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())


	volumeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(obj)
			log.Infof("Add myresource: %s", key)
			if err == nil {
				queue.Add(key)
			}
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			key, err := cache.MetaNamespaceKeyFunc(newObj)
			log.Infof("Update myresource: %s", key)
			if err == nil {
				queue.Add(key)
			}
		},
		DeleteFunc: func(obj interface{}) {
			key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
			log.Infof("Delete myresource: %s", key)
			if err == nil {
				queue.Add(key)
			}
		},
	})

	controller := Controller{
		logger:    log.NewEntry(log.New()),
		clientset: client,
		informer:  volumeInformer,
		queue:     queue,
		handler:   &TestHandler{},
	}

	// use a channel to synchronize the finalization for a graceful shutdown
	stopCh := make(chan struct{})
	defer close(stopCh)

	// run the controller loop to process items
	go controller.Run(stopCh)

	// use a channel to handle OS signals to terminate and gracefully shut
	// down processing
	sigTerm := make(chan os.Signal, 1)
	signal.Notify(sigTerm, syscall.SIGTERM)
	signal.Notify(sigTerm, syscall.SIGINT)
	<-sigTerm
}