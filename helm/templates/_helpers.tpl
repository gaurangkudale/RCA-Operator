{{/*
Expand the name of the chart.
*/}}
{{- define "rca-operator.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "rca-operator.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Create chart name and version as used by the chart label.
*/}}
{{- define "rca-operator.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "rca-operator.labels" -}}
helm.sh/chart: {{ include "rca-operator.chart" . }}
{{ include "rca-operator.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "rca-operator.selectorLabels" -}}
app.kubernetes.io/name: {{ include "rca-operator.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
control-plane: controller-manager
{{- end }}

{{/*
Create the name of the service account to use
*/}}
{{- define "rca-operator.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (printf "%s-controller-manager" (include "rca-operator.fullname" .)) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
Create the name of the namespace
*/}}
{{- define "rca-operator.namespace" -}}
{{- .Release.Namespace }}
{{- end }}

{{/*
Name for the OpenTelemetry Collector ServiceAccount, ClusterRole,
ClusterRoleBinding, and OpenTelemetryCollector CR.
Keeps all collector RBAC resources consistently named.
*/}}
{{- define "rca-operator.otelCollectorName" -}}
{{- printf "%s-otel-collector" (include "rca-operator.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Jaeger OTLP gRPC endpoint used by the OTel Collector exporter.
Priority:
  1. otelCollector.jaegerEndpoint (explicit override)
  2. Auto-computed: <release-name>-jaeger:<port>
     The Jaeger sub-chart creates a service named "<release-name>-jaeger"
     that listens on port 4317 for OTLP gRPC.
*/}}
{{- define "rca-operator.jaegerEndpoint" -}}
{{- if .Values.otelCollector.jaegerEndpoint -}}
{{- .Values.otelCollector.jaegerEndpoint }}
{{- else -}}
{{- printf "%s-jaeger:4317" .Release.Name }}
{{- end -}}
{{- end }}

{{/*
OTLP HTTP endpoint that auto-instrumented workloads send spans to.
Points at the OpenTelemetryCollector DaemonSet service (port 4318).
Priority:
  1. instrumentation.otlpEndpoint (explicit override)
  2. Auto-computed: http://<release-name>-otel-collector:<port>
     The OTel Operator creates a service for the collector named
     "<collector-cr-name>-collector" by convention. We use the full
     CR name (rca-operator.fullname + "-otel") to derive the service name.
*/}}
{{- define "rca-operator.otlpEndpoint" -}}
{{- if .Values.instrumentation.otlpEndpoint -}}
{{- .Values.instrumentation.otlpEndpoint }}
{{- else -}}
{{- printf "http://%s-otel-collector:4318" (include "rca-operator.fullname" .) }}
{{- end -}}
{{- end }}
