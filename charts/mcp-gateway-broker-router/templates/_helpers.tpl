{{/*
Expand the name of the chart.
*/}}
{{- define "broker-router.name" -}}
{{- default "mcp-gateway" .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "broker-router.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- default "mcp-gateway" .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "broker-router.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "broker-router.labels" -}}
helm.sh/chart: {{ include "broker-router.chart" . }}
{{ include "broker-router.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
{{- end }}

{{/*
Selector labels — matches brokerRouterLabels() in broker_router.go
*/}}
{{- define "broker-router.selectorLabels" -}}
app.kubernetes.io/name: {{ include "broker-router.name" . }}
app.kubernetes.io/managed-by: mcp-gateway-controller
{{- end }}

{{/*
Docker image reference
*/}}
{{- define "broker-router.image" -}}
{{ .Values.image.repository }}:{{ .Values.image.tag | default .Chart.AppVersion }}
{{- end }}
