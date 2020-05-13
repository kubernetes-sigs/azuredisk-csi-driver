FROM --platform=$BUILDPLATFORM golang:1.13.10-alpine3.10 as builder
WORKDIR /go/src/sigs.k8s.io/azuredisk-csi-driver
ADD . .
ARG TARGETARCH
ARG TARGETOS
ARG LDFLAGS
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -a -ldflags "${LDFLAGS:--X sigs.k8s.io/azuredisk-csi-driver/pkg/azuredisk.driverVersion=latest}" -o _output/azurediskplugin.exe ./pkg/azurediskplugin

FROM mcr.microsoft.com/windows/servercore:1809 as core

FROM mcr.microsoft.com/windows/nanoserver:1809
LABEL description="CSI Azure disk plugin"

COPY --from=builder /go/src/sigs.k8s.io/azuredisk-csi-driver/_output/azurediskplugin.exe /azurediskplugin.exe
COPY --from=core /Windows/System32/netapi32.dll /Windows/System32/netapi32.dll
USER ContainerAdministrator
ENTRYPOINT ["/azurediskplugin.exe"]
