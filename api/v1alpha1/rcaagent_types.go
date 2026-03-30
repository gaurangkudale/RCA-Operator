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
