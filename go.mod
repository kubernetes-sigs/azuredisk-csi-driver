module sigs.k8s.io/azuredisk-csi-driver

go 1.13

require (
	github.com/Azure/azure-sdk-for-go v43.0.0+incompatible
	github.com/Azure/go-autorest/autorest v0.11.1
	github.com/Azure/go-autorest/autorest/adal v0.9.5
	github.com/Azure/go-autorest/autorest/date v0.3.0
	github.com/Azure/go-autorest/autorest/to v0.2.0
	github.com/container-storage-interface/spec v1.3.0
	github.com/golang/mock v1.4.3
	github.com/golang/protobuf v1.4.1
	github.com/kubernetes-csi/csi-lib-utils v0.7.1
	github.com/kubernetes-csi/csi-proxy/client v0.0.0-20200330215040-9eff16441b2a
	github.com/kubernetes-csi/external-snapshotter/v2 v2.0.0-20200617021606-4800ca72d403
	github.com/onsi/ginkgo v1.11.0
	github.com/onsi/gomega v1.7.0
	github.com/pborman/uuid v1.2.0
	github.com/pelletier/go-toml v1.2.0
	github.com/stretchr/testify v1.6.1
	golang.org/x/net v0.0.0-20200707034311-ab3426394381
	google.golang.org/grpc v1.28.0
	k8s.io/api v0.18.1
	k8s.io/apimachinery v0.18.1
	k8s.io/client-go v0.18.1
	k8s.io/cloud-provider v0.0.0
	k8s.io/component-base v0.18.1
	k8s.io/klog v1.0.0
	k8s.io/klog/v2 v2.4.0
	k8s.io/kubernetes v1.18.1
	k8s.io/legacy-cloud-providers v0.0.0
	k8s.io/utils v0.0.0-20201110183641-67b214c5f920
	sigs.k8s.io/yaml v1.2.0
)

replace (
	github.com/container-storage-interface/spec => github.com/container-storage-interface/spec v1.3.0
	github.com/prometheus/client_golang => github.com/prometheus/client_golang v1.0.0
	go.etcd.io/etcd => go.etcd.io/etcd v0.0.0-20200410171415-59f5fb25a533
	google.golang.org/grpc => google.golang.org/grpc v1.27.0
	k8s.io/api => k8s.io/api v0.18.1
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.18.1
	k8s.io/apimachinery => k8s.io/apimachinery v0.18.2-beta.0
	k8s.io/apiserver => k8s.io/apiserver v0.18.1
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.18.1
	k8s.io/client-go => k8s.io/client-go v0.18.1
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.0.0-20200711020125-52e5381d8dd1
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.18.1
	k8s.io/code-generator => k8s.io/code-generator v0.18.3-beta.0
	k8s.io/component-base => k8s.io/component-base v0.18.1
	k8s.io/cri-api => k8s.io/cri-api v0.18.4-rc.0
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.0.0-20200530124324-08bf6a63b59d
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.18.1
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.18.1
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.18.1
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.18.1
	k8s.io/kubectl => k8s.io/kubectl v0.18.1
	k8s.io/kubelet => k8s.io/kubelet v0.18.1
	k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.0.0-20201124071319-e8b0165dd714
	k8s.io/metrics => k8s.io/metrics v0.18.1
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.18.1
	k8s.io/sample-cli-plugin => k8s.io/sample-cli-plugin v0.18.1
	k8s.io/sample-controller => k8s.io/sample-controller v0.18.1
)
