module sigs.k8s.io/azuredisk-csi-driver

go 1.19

require (
	github.com/Azure/azure-sdk-for-go v68.0.0+incompatible
	github.com/Azure/go-autorest/autorest v0.11.28
	github.com/Azure/go-autorest/autorest/adal v0.9.23
	github.com/Azure/go-autorest/autorest/date v0.3.0
	github.com/Azure/go-autorest/autorest/to v0.4.0
	github.com/container-storage-interface/spec v1.7.0
	github.com/go-logr/logr v1.2.4
	github.com/golang/mock v1.6.0
	github.com/golang/protobuf v1.5.2
	github.com/kubernetes-csi/csi-lib-utils v0.10.0
	github.com/kubernetes-csi/csi-proxy/client v1.0.1
	github.com/kubernetes-csi/external-snapshotter/client/v4 v4.1.0
	github.com/onsi/ginkgo/v2 v2.8.0
	github.com/onsi/gomega v1.26.0
	github.com/pborman/uuid v1.2.0
	github.com/pelletier/go-toml v1.9.4
	github.com/prometheus/client_golang v1.14.0
	github.com/sirupsen/logrus v1.8.1
	github.com/stretchr/testify v1.8.4
	golang.org/x/net v0.8.0
	google.golang.org/grpc v1.49.0
	google.golang.org/protobuf v1.28.1
	k8s.io/api v0.26.6
	k8s.io/apimachinery v0.26.6
	k8s.io/apiserver v0.26.6
	k8s.io/client-go v0.26.6
	k8s.io/cloud-provider v0.26.6
	k8s.io/code-generator v0.26.6
	k8s.io/component-base v0.26.6
	k8s.io/component-helpers v0.26.6
	k8s.io/klog/v2 v2.90.0
	k8s.io/kube-scheduler v0.26.6
	k8s.io/kubernetes v1.26.6
	k8s.io/mount-utils v0.0.0
	k8s.io/utils v0.0.0-20221128185143-99ec85e7a448
	sigs.k8s.io/cloud-provider-azure v0.7.4
	sigs.k8s.io/controller-runtime v0.14.1
	sigs.k8s.io/controller-tools v0.9.0
	sigs.k8s.io/yaml v1.3.0
)

require (
	github.com/Azure/azure-amqp-common-go/v3 v3.2.3
	github.com/Azure/azure-event-hubs-go/v3 v3.3.18
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.4.0
	github.com/Azure/azure-sdk-for-go/sdk/azidentity v1.2.2
	github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/compute/armcompute/v5 v5.0.0
	github.com/Azure/go-autorest/autorest/mocks v0.4.2
	github.com/edreed/go-batch v0.0.0-20230626221829-93a203a9c72d
	github.com/jongio/azidext/go/azidext v0.4.0
	github.com/mitchellh/go-homedir v1.1.0
	github.com/olekukonko/tablewriter v0.0.4
	github.com/pkg/errors v0.9.1
	github.com/spf13/cobra v1.6.1
	github.com/spf13/viper v1.10.0
	golang.org/x/crypto v0.6.0
	k8s.io/apiextensions-apiserver v0.26.6
	k8s.io/csi-translation-lib v0.26.6
	k8s.io/pod-security-admission v0.26.6
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.2.0 // indirect
	github.com/Azure/go-amqp v0.17.5 // indirect
	github.com/Azure/go-autorest v14.2.0+incompatible // indirect
	github.com/Azure/go-autorest/autorest/azure/auth v0.5.11 // indirect
	github.com/Azure/go-autorest/autorest/validation v0.3.1 // indirect
	github.com/Azure/go-autorest/logger v0.2.1 // indirect
	github.com/Azure/go-autorest/tracing v0.6.0 // indirect
	github.com/AzureAD/microsoft-authentication-library-for-go v0.9.0 // indirect
	github.com/Microsoft/go-winio v0.4.17 // indirect
	github.com/aws/aws-sdk-go v1.44.116 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/cenkalti/backoff/v4 v4.2.0 // indirect
	github.com/cespare/xxhash/v2 v2.1.2 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/devigned/tab v0.1.1 // indirect
	github.com/docker/distribution v2.8.1+incompatible // indirect
	github.com/emicklei/go-restful/v3 v3.9.0 // indirect
	github.com/evanphx/json-patch v5.6.0+incompatible // indirect
	github.com/evanphx/json-patch/v5 v5.6.0 // indirect
	github.com/fatih/color v1.13.0 // indirect
	github.com/felixge/httpsnoop v1.0.3 // indirect
	github.com/fsnotify/fsnotify v1.6.0 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/go-openapi/jsonpointer v0.19.5 // indirect
	github.com/go-openapi/jsonreference v0.20.0 // indirect
	github.com/go-openapi/swag v0.19.14 // indirect
	github.com/gobuffalo/flect v0.2.5 // indirect
	github.com/gogo/protobuf v1.3.2 // indirect
	github.com/golang-jwt/jwt/v4 v4.5.0 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/google/gnostic v0.5.7-v3refs // indirect
	github.com/google/go-cmp v0.5.9 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/google/uuid v1.3.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.7.0 // indirect
	github.com/hashicorp/hcl v1.0.0 // indirect
	github.com/imdario/mergo v0.3.12 // indirect
	github.com/inconshreveable/mousetrap v1.0.1 // indirect
	github.com/jmespath/go-jmespath v0.4.0 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/jpillora/backoff v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/magiconair/properties v1.8.5 // indirect
	github.com/mailru/easyjson v0.7.6 // indirect
	github.com/mattn/go-colorable v0.1.12 // indirect
	github.com/mattn/go-isatty v0.0.14 // indirect
	github.com/mattn/go-runewidth v0.0.7 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.2 // indirect
	github.com/mitchellh/mapstructure v1.5.0 // indirect
	github.com/moby/spdystream v0.2.0 // indirect
	github.com/moby/sys/mountinfo v0.6.2 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/selinux v1.10.0 // indirect
	github.com/pkg/browser v0.0.0-20210911075715-681adbf594b8 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/prometheus/client_model v0.3.0 // indirect
	github.com/prometheus/common v0.37.0 // indirect
	github.com/prometheus/procfs v0.8.0 // indirect
	github.com/spf13/afero v1.6.0 // indirect
	github.com/spf13/cast v1.4.1 // indirect
	github.com/spf13/jwalterweatherman v1.1.0 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/subosito/gotenv v1.2.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.35.0 // indirect
	go.opentelemetry.io/otel v1.10.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/internal/retry v1.10.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.10.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.10.0 // indirect
	go.opentelemetry.io/otel/metric v0.31.0 // indirect
	go.opentelemetry.io/otel/sdk v1.10.0 // indirect
	go.opentelemetry.io/otel/trace v1.10.0 // indirect
	go.opentelemetry.io/proto/otlp v0.19.0 // indirect
	golang.org/x/mod v0.8.0 // indirect
	golang.org/x/oauth2 v0.0.0-20220223155221-ee480838109b // indirect
	golang.org/x/sync v0.3.0 // indirect
	golang.org/x/sys v0.6.0 // indirect
	golang.org/x/term v0.6.0 // indirect
	golang.org/x/text v0.8.0 // indirect
	golang.org/x/time v0.3.0 // indirect
	golang.org/x/tools v0.6.0 // indirect
	gomodules.xyz/jsonpatch/v2 v2.2.0 // indirect
	google.golang.org/appengine v1.6.7 // indirect
	google.golang.org/genproto v0.0.0-20220502173005-c8bf987b8c21 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/ini.v1 v1.66.2 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/gengo v0.0.0-20220902162205-c0856e24416d // indirect
	k8s.io/kube-openapi v0.0.0-20221012153701-172d655c2280 // indirect
	k8s.io/kubectl v0.0.0 // indirect
	sigs.k8s.io/apiserver-network-proxy/konnectivity-client v0.0.37 // indirect
	sigs.k8s.io/json v0.0.0-20220713155537-f223a00ba0e2 // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.2.3 // indirect
)

replace (
	github.com/niemeyer/pretty => github.com/niemeyer/pretty v0.0.0-20200227124842-a10e7caefd8e
	github.com/onsi/ginkgo/v2 => github.com/onsi/ginkgo/v2 v2.4.0
	go.etcd.io/etcd => go.etcd.io/etcd v0.0.0-20200410171415-59f5fb25a533
	k8s.io/api => k8s.io/api v0.26.6
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.26.6
	k8s.io/apimachinery => k8s.io/apimachinery v0.26.6
	k8s.io/apiserver => k8s.io/apiserver v0.26.6
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.26.6
	k8s.io/client-go => k8s.io/client-go v0.26.6
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.26.6
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.26.6
	k8s.io/code-generator => k8s.io/code-generator v0.26.6
	k8s.io/component-base => k8s.io/component-base v0.26.6
	k8s.io/component-helpers => k8s.io/component-helpers v0.26.6
	k8s.io/controller-manager => k8s.io/controller-manager v0.26.6
	k8s.io/cri-api => k8s.io/cri-api v0.26.6
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.26.6
	k8s.io/dynamic-resource-allocation => k8s.io/dynamic-resource-allocation v0.26.6
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.26.6
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.26.6
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.26.6
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.26.6
	k8s.io/kubectl => k8s.io/kubectl v0.26.6
	k8s.io/kubelet => k8s.io/kubelet v0.26.6
	k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.26.6
	k8s.io/metrics => k8s.io/metrics v0.26.6
	k8s.io/mount-utils => k8s.io/mount-utils v0.0.0-20221216112627-49433b159e95
	k8s.io/pod-security-admission => k8s.io/pod-security-admission v0.26.6
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.26.6
	k8s.io/sample-cli-plugin => k8s.io/sample-cli-plugin v0.26.6
	k8s.io/sample-controller => k8s.io/sample-controller v0.26.6

	sigs.k8s.io/cloud-provider-azure => github.com/edreed/cloud-provider-azure v0.7.3-0.20230227234418-c94372fa2571
)
