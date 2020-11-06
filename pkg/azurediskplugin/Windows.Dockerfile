ARG OSVERSION
FROM mcr.microsoft.com/windows/servercore:${OSVERSION} as core

FROM mcr.microsoft.com/windows/nanoserver:${OSVERSION}
LABEL description="CSI Azure disk plugin"

COPY ./_output/azurediskplugin.exe /azurediskplugin.exe
COPY --from=core /Windows/System32/netapi32.dll /Windows/System32/netapi32.dll
USER ContainerAdministrator
ENTRYPOINT ["/azurediskplugin.exe"]
