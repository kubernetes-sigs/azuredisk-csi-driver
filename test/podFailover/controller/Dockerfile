FROM mcr.microsoft.com/aks/fundamental/base-ubuntu:v0.0.5
RUN apt-get update && apt-get install -y util-linux e2fsprogs mount ca-certificates udev xfsprogs
LABEL maintainers="andyzhangx"
LABEL description="Controller container for pod failover testing"

ARG ARCH=amd64
COPY ./_output/${ARCH}/controllerPod /controllerPod
ENTRYPOINT ["/controllerPod"]