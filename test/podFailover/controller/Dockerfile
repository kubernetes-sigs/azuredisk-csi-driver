FROM gcr.io/distroless/static:latest
LABEL description="Controller container for pod failover testing"

ARG ARCH=amd64
COPY ./_output/${ARCH}/controllerPod /controllerPod
ENTRYPOINT ["/controllerPod"]