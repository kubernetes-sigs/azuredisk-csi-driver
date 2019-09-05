FROM mcr.microsoft.com/windows/servercore:1809 as core

FROM mcr.microsoft.com/windows/nanoserver:1809
LABEL description="CSI Azure disk plugin"

COPY ./_output/azurediskplugin.exe /azurediskplugin.exe
COPY --from=core /Windows/System32/netapi32.dll /Windows/System32/netapi32.dll
USER ContainerAdministrator
ENTRYPOINT ["/azurediskplugin.exe"]