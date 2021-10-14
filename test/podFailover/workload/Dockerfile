FROM gcr.io/distroless/static:latest
LABEL description="Workload container for pod failover testing"

ARG ARCH=amd64
COPY ./_output/${ARCH}/workloadPod /workloadPod
ENTRYPOINT ["/workloadPod"]