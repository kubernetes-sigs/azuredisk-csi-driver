FROM golang:1.19.13 as builder

WORKDIR /workspace

COPY Makefile Makefile
COPY go.mod go.mod
COPY go.sum go.sum
COPY cmd/ cmd/
COPY pkg/ pkg/
COPY vendor/ vendor/

RUN make azuredisk-v2

FROM alpine:3.15.1
RUN apk upgrade --available --no-cache && \
    apk add --no-cache util-linux e2fsprogs e2fsprogs-extra ca-certificates udev xfsprogs xfsprogs-extra btrfs-progs btrfs-progs-extra

LABEL maintainers="andyzhangx"
LABEL description="Azure Disk CSI Driver v2"

ARG ARCH=amd64
ARG PLUGIN_NAME=azurediskpluginv2
ARG binary=./_output/${ARCH}/${PLUGIN_NAME}
COPY --from=builder /workspace /azurediskpluginv2
ENTRYPOINT ["/azurediskpluginv2"]
