# azuredisk-csi-driver helm chart repository

This branch hosts the Helm chart repository for azuredisk-csi-driver via
GitHub Pages. Published automatically by the
`Publish Helm Chart to GitHub Pages` workflow.

To use:

    helm repo add azuredisk-csi-driver https://kubernetes-sigs.github.io/azuredisk-csi-driver
    helm repo update azuredisk-csi-driver
    helm install azuredisk-csi-driver azuredisk-csi-driver/azuredisk-csi-driver \
      --namespace kube-system --version <x.y.z>
