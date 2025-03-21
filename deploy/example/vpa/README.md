# Vertical pod autoscaler to Azure disk CSI controller pods
## Prerequisites: install vertical pod autoscaler
You should install Vertical Pod Autoscaler first, please refer to the [vertical-pod-autoscaler](https://github.com/kubernetes/autoscaler/blob/master/vertical-pod-autoscaler/README.md). You can use script in `azuredisk-csi-driver/deploy/example/vpa/install-vpa.sh` to install it.

In AKS, you should add label `admissions.enforcer/disabled: true` to admission controller webhooks to impact kube-system AKS namespaces, refer to [Can admission controller webhooks impact kube-system and internal AKS namespaces?](https://learn.microsoft.com/en-us/azure/aks/faq#can-admission-controller-webhooks-impact-kube-system-and-internal-aks-namespaces-), you can use --webhook-labels flag in vpa-admission-controller to set it, refer to [Running the admission-controller](https://github.com/kubernetes/autoscaler/blob/master/vertical-pod-autoscaler/docs/components.md#:~:text=You%20can%20specify%20a%20comma%20separated%20list%20to%20set%20webhook%20labels%20with%20%2D%2Dwebhook%2Dlabels%2C%20example%20format%3A%20key1%3Avalue1%2Ckey2%3Avalue2.)

If you don't use `azuredisk-csi-driver/deploy/example/vpa/install-vpa.sh` to install vpa, you should add webhook-labels flags in vpa-admission-controller
> edit vpa-admission-controller deployment
```
k edit deploy vpa-admission-controller -n kube-system
```
add `- --webhook-labels=admissions.enforcer/disabled:true` in containers args
```
  template:
    metadata:
      creationTimestamp: null
      labels:
        app: vpa-admission-controller
    spec:
      containers:
      - args:
        - --v=4
        - --stderrthreshold=info
        - --reload-cert
        - --webhook-labels=admissions.enforcer/disabled:true # add webhook-labels flags
```

> check vpa-admission-controller pod running 
```
k get po -n kube-system | grep vpa-admission-controller
vpa-admission-controller-7fcb5c6b86-s69p8              1/1     Running   0               13m
```

## Create a VPA corresponding to CSI controller deployment
> create a VPA for CSI controller
```console
kubectl apply -f https://raw.githubusercontent.com/kubernetes-sigs/azuredisk-csi-driver/master/deploy/example/vpa/vertical-pod-autoscaler.yaml
```
> check the VPA config and current recommended resource requests
```
kubectl get vpa -n kube-system
NAME                       MODE   CPU   MEM        PROVIDED   AGE
csi-azuredisk-controller   Auto   15m   43690666   True       8s

kubectl describe vpa csi-azuredisk-controller -n kube-system
Name:         csi-azuredisk-controller
Namespace:    kube-system
Labels:       <none>
Annotations:  <none>
API Version:  autoscaling.k8s.io/v1
Kind:         VerticalPodAutoscaler
Metadata:
  Creation Timestamp:  2024-09-25T02:54:36Z
  Generation:          1
  Resource Version:    417864
  UID:                 6c393ae3-2f17-4efe-974c-2f0d1f4c635a
Spec:
  Resource Policy:
    Container Policies:
      Container Name:  *
      Max Allowed:
        Cpu:     15m
        Memory:  50Gi
  Target Ref:
    API Version:  apps/v1
    Kind:         Deployment
    Name:         csi-azuredisk-controller
  Update Policy:
    Update Mode:  Auto
Status:
  Conditions:
    Last Transition Time:  2024-09-25T02:55:13Z
    Status:                True
    Type:                  RecommendationProvided
  Recommendation:
    Container Recommendations:
      Container Name:  azuredisk
      Lower Bound:
        Cpu:     10m
        Memory:  43690666
      Target:
        Cpu:     15m
        Memory:  43690666
      Uncapped Target:
        Cpu:     23m
        Memory:  43690666
      Upper Bound:
        Cpu:           15m
        Memory:        55999064
      Container Name:  csi-attacher
      Lower Bound:
        Cpu:     10m
        Memory:  43690666
      Target:
        Cpu:     11m
        Memory:  43690666
      Uncapped Target:
        Cpu:     11m
        Memory:  43690666
      Upper Bound:
        Cpu:           15m
        Memory:        86115636
      Container Name:  csi-provisioner
      Lower Bound:
        Cpu:     10m
        Memory:  43690666
      Target:
        Cpu:     11m
        Memory:  43690666
      Uncapped Target:
        Cpu:     11m
        Memory:  43690666
      Upper Bound:
        Cpu:           15m
        Memory:        55999064
      Container Name:  csi-resizer
      Lower Bound:
        Cpu:     10m
        Memory:  43690666
      Target:
        Cpu:     11m
        Memory:  43690666
      Uncapped Target:
        Cpu:     11m
        Memory:  43690666
      Upper Bound:
        Cpu:           15m
        Memory:        55999064
      Container Name:  csi-snapshotter
      Lower Bound:
        Cpu:     10m
        Memory:  43690666
      Target:
        Cpu:     11m
        Memory:  43690666
      Uncapped Target:
        Cpu:     11m
        Memory:  43690666
      Upper Bound:
        Cpu:           15m
        Memory:        55999064
      Container Name:  liveness-probe
      Lower Bound:
        Cpu:     10m
        Memory:  43690666
      Target:
        Cpu:     11m
        Memory:  43690666
      Uncapped Target:
        Cpu:     11m
        Memory:  43690666
      Upper Bound:
        Cpu:     15m
        Memory:  43690666
Events:          <none>
```