# Pod Inline Volume Example

>Pod inline volume feature is alpha in k8s v1.15. If you want to use the feature, you should ensure `--feature-gates=CSIInlineVolume=true`.
>
>Kubernetes >= 1.16 no longer needs this as the feature is beta and enabled by default.

The CSIDriver should set `podInfoOnMount` to true and specify `Ephemeral` in volumeLifecycleModes so that Kubernetes allows using a CSI driver for an inline volume.

After deploy the azuredisk-csi-driver and CSIDriver, we can create the pod with inline volume. StorageClass and PVC are not required.

Here is [the inline volume example](./nginx-pod-azuredisk-inline.yaml).
