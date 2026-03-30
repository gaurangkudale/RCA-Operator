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

	// AIProviderConfig holds the configuration for the LLM backend. Phase 1: stored only — not used by the operator yet.
	// +kubebuilder:validation:Required
	AIProviderConfig *AIProviderConfig `json:"aiProviderConfig,omitempty"`

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

	// SignalProcessing configures the normalization, enrichment, and deduplication
	// stage that runs before incident correlation.
	// Phase 2+: stored now so the CRD can model the target architecture.
	// +optional
	SignalProcessing *SignalProcessingConfig `json:"signalProcessing,omitempty"`

	// Decision configures autonomy and safety policy after RCA analysis has completed.
	// Phase 2+: stored now so the CRD can model the target architecture.
	// +optional
	Decision *DecisionConfig `json:"decision,omitempty"`

	// Observability configures OTLP telemetry export for traces, metrics, and logs.
	// The API stays backend-agnostic so users can target SigNoz or any OTLP-compatible sink.
	// Phase 2+: stored now so the CRD can model the target architecture.
	// +optional
	Observability *ObservabilityConfig `json:"observability,omitempty"`
}

// AIProviderConfig holds the LLM backend configuration.
// Phase 1: stored only — not used by the operator yet.
type AIProviderConfig struct {

	// Type is the LLM provider to use.
	// TODO: add more providers as they are supported (e.g. anthropic, gemini, ollama, etc.)
	// +kubebuilder:validation:Enum=openai
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=openai
	// +kubebuilder:default=openai
	// +kubebuilder:example=openai
	Type string `json:"type"`

	// Model is the model identifier to use (e.g. gpt-4o, claude-3-opus).
	// +kubebuilder:validation:Required
	// +kubebuilder:default=gpt-4o
	// +kubebuilder:example=gpt-4o
	Model string `json:"model,omitempty"`

	// SecretRef is the name of the Kubernetes Secret containing the API key.
	// The secret must have a key named "apiKey".
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:example=rca-agent-openai-secret
	SecretRef string `json:"secretRef,omitempty"`
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

// AutonomyLevel controls how much action the operator may take without human approval.
// 0 = observe only, 1 = suggest, 2 = safe auto-remediation, 3 = full auto-remediation.
// +kubebuilder:validation:Enum=0;1;2;3
type AutonomyLevel int32

// RemediationAction is a high-level action category used by the decision layer.
// +kubebuilder:validation:Enum=restartPod;rollbackWorkload;scaleWorkload;cordonNode;drainNode
type RemediationAction string

// SignalProcessingConfig defines pre-correlation signal handling.
type SignalProcessingConfig struct {
	// DedupWindow is how long equivalent raw signals are suppressed before they can re-open correlation work.
	// Supported suffixes: s (seconds), m (minutes), h (hours), for example "30s", "2m", "1h".
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*(s|m|h)$`
	// +kubebuilder:default="2m"
	// +optional
	DedupWindow string `json:"dedupWindow,omitempty"`

	// CorrelationWindow is the maximum window used to group related normalized signals into one incident candidate.
	// Supported suffixes: s (seconds), m (minutes), h (hours), for example "30s", "5m", "1h".
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*(s|m|h)$`
	// +kubebuilder:default="5m"
	// +optional
	CorrelationWindow string `json:"correlationWindow,omitempty"`

	// MeaningfulIncidentWindow is the minimum continuous observation time before a detecting incident is promoted as meaningful.
	// Supported suffixes: s (seconds), m (minutes), h (hours), for example "30s", "5m", "1h".
	// +kubebuilder:validation:Pattern=`^[1-9][0-9]*(s|m|h)$`
	// +kubebuilder:default="5m"
	// +optional
	MeaningfulIncidentWindow string `json:"meaningfulIncidentWindow,omitempty"`

	// EnableOwnerEnrichment controls whether signals are enriched with top-level workload ownership before correlation.
	// +kubebuilder:default=true
	// +optional
	EnableOwnerEnrichment bool `json:"enableOwnerEnrichment,omitempty"`
}

// NamespaceAutonomyPolicy overrides the default autonomy level for one namespace.
type NamespaceAutonomyPolicy struct {
	// Namespace is the Kubernetes namespace this policy applies to.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// Level is the autonomy level to enforce for the namespace.
	// +kubebuilder:validation:Required
	Level AutonomyLevel `json:"level"`
}

// DecisionConfig defines autonomy and safety policy for remediation decisions.
type DecisionConfig struct {
	// DefaultAutonomy is the baseline autonomy level when no namespace override matches.
	// +kubebuilder:default=1
	// +optional
	DefaultAutonomy AutonomyLevel `json:"defaultAutonomy,omitempty"`

	// NamespaceAutonomy overrides DefaultAutonomy for specific namespaces.
	// +listType=map
	// +listMapKey=namespace
	// +optional
	NamespaceAutonomy []NamespaceAutonomyPolicy `json:"namespaceAutonomy,omitempty"`

	// RequireHumanApprovalFor lists action categories that must never execute automatically.
	// +listType=set
	// +optional
	RequireHumanApprovalFor []RemediationAction `json:"requireHumanApprovalFor,omitempty"`

	// AllowPlaybooks is the allow-list of safe playbook identifiers the operator may consider executing.
	// +listType=set
	// +optional
	AllowPlaybooks []string `json:"allowPlaybooks,omitempty"`
}

// OTLPExporterConfig defines generic OTLP telemetry export settings.
type OTLPExporterConfig struct {
	// Endpoint is the OTLP gRPC or HTTP collector endpoint.
	// Example: "http://otel-collector.observability.svc.cluster.local:4317"
	// +kubebuilder:validation:MinLength=1
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// HeadersSecretRef references a Secret in the same namespace containing OTLP headers.
	// The secret may hold keys such as "Authorization" or vendor-specific routing headers.
	// +optional
	HeadersSecretRef string `json:"headersSecretRef,omitempty"`

	// Insecure disables transport security for in-cluster development collectors.
	// +optional
	Insecure bool `json:"insecure,omitempty"`

	// ResourceAttributes adds static OpenTelemetry resource attributes to exported telemetry.
	// +optional
	ResourceAttributes map[string]string `json:"resourceAttributes,omitempty"`
}

// ObservabilityConfig defines telemetry export behavior for the operator.
type ObservabilityConfig struct {
	// OTLP enables OpenTelemetry export using a backend-agnostic OTLP endpoint.
	// +optional
	OTLP *OTLPExporterConfig `json:"otlp,omitempty"`
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
// +kubebuilder:printcolumn:name="Provider",type=string,JSONPath=".spec.aiProviderConfig.type",description="AI provider type"
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=".spec.aiProviderConfig.model",description="AI model in use"
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
