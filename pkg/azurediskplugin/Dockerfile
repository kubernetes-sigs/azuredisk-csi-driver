FROM debian:9
RUN apt-get update && apt-get install -y util-linux e2fsprogs mount ca-certificates udev clean-install xfsprogs
LABEL maintainers="andyzhangx"
LABEL description="Azure Disk CSI Driver"

COPY ./_output/azurediskplugin /azurediskplugin
ENTRYPOINT ["/azurediskplugin"]
