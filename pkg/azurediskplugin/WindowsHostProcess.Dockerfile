FROM mcr.microsoft.com/oss/kubernetes/windows-host-process-containers-base-image:v1.0.0
LABEL description="CSI Azure disk plugin"

ARG ARCH=amd64
ARG binary=./_output/${ARCH}/azurediskplugin.exe
COPY ${binary} /azurediskplugin.exe
ENV PATH="C:\Windows\system32;C:\Windows;C:\WINDOWS\System32\WindowsPowerShell\v1.0\;"
USER ContainerAdministrator
ENTRYPOINT ["/azurediskplugin.exe"]
