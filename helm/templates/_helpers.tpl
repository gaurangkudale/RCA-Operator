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
OTel Collector Service name.
The OpenTelemetry Operator creates a Service for every OpenTelemetryCollector CR
using the pattern: <CR-name>-collector.
  CR name  = <fullname>-otel         (set in otel-collector.yaml)
  Service  = <fullname>-otel-collector
This helper is used both internally (otlpEndpoint) and in NOTES.txt.
*/}}
{{- define "rca-operator.collectorServiceName" -}}
{{- printf "%s-otel-collector" (include "rca-operator.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Jaeger OTLP gRPC endpoint used by the OTel Collector exporter.
Priority:
  1. otelCollector.jaegerEndpoint  — explicit full override (host:port)
  2. Auto-computed: <release-name>-jaeger:4317
     The jaeger sub-chart (named "jaeger") creates a Service whose fullname
     follows Helm's standard convention: <release-name>-jaeger.
     Port 4317 is Jaeger's OTLP gRPC receiver.
*/}}
{{- define "rca-operator.jaegerOtlpEndpoint" -}}
{{- if .Values.otelCollector.jaegerEndpoint -}}
{{- .Values.otelCollector.jaegerEndpoint }}
{{- else -}}
{{- printf "%s-jaeger:4317" .Release.Name }}
{{- end -}}
{{- end }}

{{/*
Keep the old alias so any external templates or tests that still call
rca-operator.jaegerEndpoint continue to work.
*/}}
{{- define "rca-operator.jaegerEndpoint" -}}
{{- include "rca-operator.jaegerOtlpEndpoint" . }}
{{- end }}

{{/*
OTLP HTTP endpoint that auto-instrumented workloads send spans to.
Points at the OTel Collector DaemonSet service (HTTP port 4318).

IMPORTANT: Must use the fully-qualified cluster DNS name so that pods in
OTHER namespaces (e.g. rca-demo) can resolve the service that lives in the
Helm release namespace (e.g. rca-system). A bare service name like
"rca-operator-otel-collector:4318" resolves only within the same namespace.

Priority:
  1. instrumentation.otlpEndpoint — explicit full override (http://host:port)
  2. Auto-computed FQDN:
     http://<collectorServiceName>.<releaseNamespace>.svc.cluster.local:4318
*/}}
{{- define "rca-operator.otlpEndpoint" -}}
{{- if .Values.instrumentation.otlpEndpoint -}}
{{- .Values.instrumentation.otlpEndpoint }}
{{- else -}}
{{- printf "http://%s.%s.svc.cluster.local:4318" (include "rca-operator.collectorServiceName" .) (include "rca-operator.namespace" .) }}
{{- end -}}
{{- end }}

{{/*
  rca-operator.ingestServiceName — ClusterIP service that fronts the operator's
  OTLP/HTTP ingest port. Named separately from the controller-manager Deployment
  so traffic from the OTel Collector DaemonSet can be routed without colliding
  with dashboard or health ports.
*/}}
{{- define "rca-operator.ingestServiceName" -}}
{{- printf "%s-otel-ingest" (include "rca-operator.fullname" .) | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
  rca-operator.ingestEndpoint — fully-qualified OTLP/HTTP URL for the operator's
  ingest port. Used by the DaemonSet collector's otlphttp/rca-operator exporter.
  Priority:
    1. .Values.rcaIngest.endpoint — explicit override
    2. Auto-computed FQDN: http://<ingestServiceName>.<releaseNamespace>.svc.cluster.local:<bindPort>
*/}}
{{- define "rca-operator.ingestEndpoint" -}}
{{- if .Values.rcaIngest.endpoint -}}
{{- .Values.rcaIngest.endpoint }}
{{- else -}}
{{- printf "http://%s.%s.svc.cluster.local:%d" (include "rca-operator.ingestServiceName" .) (include "rca-operator.namespace" .) (int .Values.rcaIngest.bindPort) }}
{{- end -}}
{{- end }}
