{{/*  */}}
{{- define "adcd.name" }}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*  */}}
{{- define "adcd.fullname" }}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*  */}}
{{- define "adcd.labels" }}
labels:
  heritage: "{{ .Release.Service }}"
  release: "{{ .Release.Name }}"
  revision: "{{ .Release.Revision }}"
  chart: "{{ .Chart.Name }}"
  chartVersion: "{{ .Chart.Version }}"
  app: {{ template "adcd.name" . }}
{{- end -}}
