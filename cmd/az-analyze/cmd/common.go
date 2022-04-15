package cmd

import (
	"flag"
	"path/filepath"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	azDiskClientSet "sigs.k8s.io/azuredisk-csi-driver/pkg/apis/client/clientset/versioned"
)

// access to config
func getConfig() *rest.Config {
	var kubeconfig *string
	if home := homedir.HomeDir(); home != "" {
		kubeconfig = flag.String("kubeconfig", filepath.Join(home, ".kube", "config"), "(optional) absolute path to the kubeconfig file")
	} else {
		kubeconfig = flag.String("kubeconfig", "", "absolute path to the kubeconfig file")
	}
	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		panic(err.Error())
	}

	return config
}

func getKubernetesClientset(config *rest.Config) *kubernetes.Clientset {
	clientsetK8s, err := kubernetes.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	return clientsetK8s
}
func getAzDiskClientset(config *rest.Config) *azDiskClientSet.Clientset {
	clientsetAzDisk, err := azDiskClientSet.NewForConfig(config)
	if err != nil {
		panic(err.Error())
	}
	return clientsetAzDisk
}
