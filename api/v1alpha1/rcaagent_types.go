/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// RCAAgentSpec defines the desired state of RCAAgent
type RCAAgentSpec struct {
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// The following markers will use OpenAPI v3 schema to validate the value
	// More info: https://book.kubebuilder.io/reference/markers/crd-validation.html

	// watchNamespaces specifies the namespaces that the RCAAgent should monitor.
	// +kubebuilder:validation:Required
	// +kubebuilder:default={"default"}
	// +kubebuilder:example={"production","staging"}
	WatchNamespaces []string `json:"watchNamespaces,omitempty"`

	// Notifications holds the configuration for sending incident notifications.
	// +optional
	Notifications *NotificationsConfig `json:"notifications,omitempty"`

	// IncidentRetention specifies how long to keep Resolved IncidentReport CRs before pruning.
	// Supported suffixes: m (minutes), h (hours), d (days), for example "5m", "12h", "30d".
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*(m|h|d)$`
	// +kubebuilder:default="30d"
	// +optional
	IncidentRetention string `json:"incidentRetention,omitempty"`

	// IncidentRetentionDays is deprecated. Use incidentRetention instead.
	// This field is retained for backward compatibility.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=30
	// +optional
	IncidentRetentionDays int `json:"incidentRetentionDays,omitempty"`

	// OTel holds optional OpenTelemetry configuration for exporting traces and metrics to SigNoz.
	// +optional
	OTel *OTelConfig `json:"otel,omitempty"`

	// SignalMappings allows overriding the default event-type → incident-type mapping.
	// +optional
	SignalMappings []SignalMappingConfig `json:"signalMappings,omitempty"`

	// Telemetry configures connections to external observability backends
	// for cross-signal incident correlation (traces, metrics, logs).
	// +optional
	Telemetry *TelemetryConfig `json:"telemetry,omitempty"`

	// AI configures AI/LLM-driven root cause analysis.
	// +optional
	AI *AIConfig `json:"ai,omitempty"`
}

// OTelConfig holds OpenTelemetry export settings.
type OTelConfig struct {
	// Endpoint is the OTLP gRPC collector address (e.g. "signoz-collector:4317").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// ServiceName is the service.name resource attribute. Defaults to "rca-operator".
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// SamplingRate is the trace sampling ratio as a string (e.g. "1.0"). Defaults to "1.0".
	// +optional
	SamplingRate string `json:"samplingRate,omitempty"`

	// Insecure disables TLS on the gRPC connection (typical for in-cluster collectors).
	// +optional
	Insecure bool `json:"insecure,omitempty"`
}

// SignalMappingConfig overrides the default event→incident mapping for a single event type.
type SignalMappingConfig struct {
	// EventType is the watcher event type to override (e.g. "CrashLoopBackOff").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	EventType string `json:"eventType"`

	// IncidentType overrides the default incident type.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	IncidentType string `json:"incidentType"`

	// Severity overrides the default severity.
	// +kubebuilder:validation:Enum=P1;P2;P3;P4
	// +optional
	Severity string `json:"severity,omitempty"`

	// Scope overrides the default scope level.
	// +kubebuilder:validation:Enum=Pod;Workload;Namespace;Cluster
	// +optional
	Scope string `json:"scope,omitempty"`
}

// TelemetryConfig configures connections to external observability backends.
type TelemetryConfig struct {
	// Backend selects the telemetry backend type.
	// "signoz" uses SigNoz as a unified traces+logs+metrics backend.
	// "jaeger" uses Jaeger for traces (combine with Prometheus for metrics).
	// "composite" delegates to separate backends per signal type.
	// +kubebuilder:validation:Enum=signoz;jaeger;composite
	// +kubebuilder:default=composite
	// +optional
	Backend string `json:"backend,omitempty"`

	// SigNoz holds SigNoz query service configuration.
	// +optional
	SigNoz *SigNozConfig `json:"signoz,omitempty"`

	// Jaeger holds Jaeger query API configuration.
	// +optional
	Jaeger *JaegerConfig `json:"jaeger,omitempty"`

	// Prometheus holds Prometheus query API configuration.
	// +optional
	Prometheus *PrometheusConfig `json:"prometheus,omitempty"`
}

// SigNozConfig holds SigNoz-specific connection settings.
type SigNozConfig struct {
	// Endpoint is the SigNoz query service URL (e.g. "http://signoz-query-service:8080").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`
}

// JaegerConfig holds Jaeger query API connection settings.
type JaegerConfig struct {
	// Endpoint is the Jaeger query HTTP API URL (e.g. "http://jaeger-query:16686").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`

	// GRPCEndpoint is the Jaeger gRPC query endpoint (e.g. "jaeger-query:16685").
	// +optional
	GRPCEndpoint string `json:"grpcEndpoint,omitempty"`
}

// PrometheusConfig holds Prometheus query API connection settings.
type PrometheusConfig struct {
	// Endpoint is the Prometheus HTTP API URL (e.g. "http://prometheus:9090").
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Endpoint string `json:"endpoint"`
}

// AIConfig configures AI/LLM-driven root cause analysis.
type AIConfig struct {
	// Enabled toggles AI-driven RCA analysis.
	// +kubebuilder:default=false
	// +optional
	Enabled bool `json:"enabled,omitempty"`

	// Endpoint is the OpenAI-compatible API endpoint (e.g. "https://api.openai.com/v1").
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// Model is the LLM model name (e.g. "gpt-4o", "llama3").
	// +optional
	Model string `json:"model,omitempty"`

	// SecretRef is the name of the Secret containing the API key.
	// The secret must have a key named "apiKey".
	// +optional
	SecretRef string `json:"secretRef,omitempty"`

	// AutoInvestigate triggers AI analysis automatically when an incident
	// transitions to Active phase.
	// +kubebuilder:default=false
	// +optional
	AutoInvestigate bool `json:"autoInvestigate,omitempty"`
}

// NotificationsConfig defines where to send RCAAgent notifications.
type NotificationsConfig struct {
	// Slack holds the configuration for Slack notifications.
	// +optional
	Slack *SlackConfig `json:"slack,omitempty"`

	// PagerDuty holds the configuration for PagerDuty notifications.
	// +optional
	PagerDuty *PagerDutyConfig `json:"pagerduty,omitempty"`
}

// SlackConfig holds Slack-specific notification settings.
type SlackConfig struct {
	// WebhookSecretRef is the name of the Kubernetes Secret containing the Slack webhook URL.
	// The secret must have a key named "webhookURL".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example=slack-webhook
	WebhookSecretRef string `json:"webhookSecretRef"`

	// Channel is the Slack channel to post notifications to (e.g. #incidents).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example="#incidents"
	Channel string `json:"channel"`

	// MentionOnP1 is the Slack user or group handle to mention on P1 incidents (e.g. @oncall).
	// +optional
	// +kubebuilder:example="@oncall"
	MentionOnP1 string `json:"mentionOnP1,omitempty"`
}

// PagerDutyConfig holds PagerDuty-specific notification settings.
type PagerDutyConfig struct {
	// SecretRef is the name of the Kubernetes Secret containing the PagerDuty Events API v2 key.
	// The secret must have a key named "apiKey".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example=pd-api-key
	SecretRef string `json:"secretRef"`

	// Severity is the minimum incident severity that triggers a PagerDuty page.
	// +kubebuilder:validation:Enum=P1;P2;P3;P4
	// +kubebuilder:default=P2
	// +optional
	Severity string `json:"severity,omitempty"`
}

// RCAAgentStatus defines the observed state of RCAAgent.
type RCAAgentStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file

	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// conditions represent the current state of the RCAAgent resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=".status.conditions[?(@.type=='Available')].status",description="Overall status based on conditions"
// +kubebuilder:printcolumn:name="Retention",type=string,JSONPath=".spec.incidentRetention",description="Incident retention duration"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// RCAAgent is the Schema for the rcaagents API
type RCAAgent struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of RCAAgent
	// +required
	Spec RCAAgentSpec `json:"spec"`

	// status defines the observed state of RCAAgent
	// +optional
	Status RCAAgentStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// RCAAgentList contains a list of RCAAgent
type RCAAgentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []RCAAgent `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RCAAgent{}, &RCAAgentList{})
}
