ARG ARCH=amd64
ARG OSVERSION
FROM --platform=linux/${ARCH} gcr.io/k8s-staging-e2e-test-images/windows-servercore-cache:1.0-linux-${ARCH}-${OSVERSION} as core

FROM mcr.microsoft.com/windows/nanoserver:${OSVERSION}
COPY --from=core /Windows/System32/netapi32.dll /Windows/System32/netapi32.dll

USER ContainerAdministrator
# Delete DiagTrack service as it causes CPU spikes and delays pod/container startup times.
RUN sc.exe delete diagtrack -f

LABEL description="CSI Azure disk plugin"

ARG ARCH
ARG PLUGIN_NAME=azurediskplugin
ARG binary=./_output/${ARCH}/${PLUGIN_NAME}.exe
COPY ${binary} /azurediskplugin.exe
ENTRYPOINT ["/azurediskplugin.exe"]
