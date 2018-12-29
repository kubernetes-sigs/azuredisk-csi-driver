FROM debian:9
RUN apt-get update && apt-get install -y ca-certificates mount
LABEL maintainers="andyzhangx"
LABEL description="Azure Disk CSI Driver"

COPY ./_output/azurediskplugin /azurediskplugin
ENTRYPOINT ["/azurediskplugin"]
