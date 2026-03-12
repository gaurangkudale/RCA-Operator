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

// IncidentReportSpec defines the desired state of IncidentReport.
// In Phase 1 the CR is written by the operator — users do not submit specs directly.
type IncidentReportSpec struct {
	// agentRef is the name of the RCAAgent that created this report.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	AgentRef string `json:"agentRef"`
}

// AffectedResource identifies a Kubernetes resource involved in an incident.
type AffectedResource struct {
	// kind is the resource kind (e.g. Deployment, Pod, Node).
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`

	// name is the resource name.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// namespace is the resource namespace. Empty for cluster-scoped resources.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// TimelineEvent is a single timestamped entry in the incident timeline.
type TimelineEvent struct {
	// time is the wall-clock time of the event (RFC3339).
	// +kubebuilder:validation:Required
	Time metav1.Time `json:"time"`

	// event is a human-readable description of what happened.
	// +kubebuilder:validation:Required
	Event string `json:"event"`
}

// IncidentReportStatus defines the observed state of IncidentReport.
type IncidentReportStatus struct {
	// severity is the incident severity level assigned by the correlator.
	// +kubebuilder:validation:Enum=P1;P2;P3;P4
	// +required
	Severity string `json:"severity,omitempty"`

	// phase is the current lifecycle phase of the incident.
	// +kubebuilder:validation:Enum=Detecting;Active;Resolved
	// +required
	Phase string `json:"phase,omitempty"`

	// incidentType is the category of incident detected by the correlator.
	// +kubebuilder:validation:Enum=CrashLoop;OOM;BadDeploy;NodeFailure;Registry;ExitCode;GracePeriodViolation
	// +required
	IncidentType string `json:"incidentType,omitempty"`

	// startTime is when the incident was first detected.
	// +required
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// resolvedTime is when the incident was resolved. Empty while still active.
	// +optional
	ResolvedTime *metav1.Time `json:"resolvedTime,omitempty"`

	// notified indicates whether the notification layer (Slack / PagerDuty) has
	// already fired for this incident. Used to suppress duplicate alerts.
	// +optional
	Notified bool `json:"notified,omitempty"`

	// affectedResources lists the Kubernetes resources involved in this incident.
	// +required
	// +listType=atomic
	AffectedResources []AffectedResource `json:"affectedResources,omitempty"`

	// correlatedSignals is the list of raw signals that triggered this incident
	// (e.g. "CrashLoopBackOff (restarts: 8)", "OOMKilled (exit code 137)").
	// +required
	// +listType=atomic
	CorrelatedSignals []string `json:"correlatedSignals,omitempty"`

	// timeline is the ordered sequence of events that make up this incident.
	// +required
	// +listType=atomic
	Timeline []TimelineEvent `json:"timeline,omitempty"`

	// rootCause is a human-readable summary of the root cause.
	// Stub in Phase 1 — populated by the RCA engine in Phase 2.
	// +optional
	RootCause string `json:"rootCause,omitempty"`

	// conditions represent the current state of the IncidentReport resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Severity",type=string,JSONPath=".status.severity",description="Incident severity"
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=".status.phase",description="Lifecycle phase"
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=".status.incidentType",description="Incident type"
// +kubebuilder:printcolumn:name="Notified",type=boolean,JSONPath=".status.notified",description="Notifications sent"
// +kubebuilder:printcolumn:name="Start",type=date,JSONPath=".status.startTime",description="When detected"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// IncidentReport is the Schema for the incidentreports API
type IncidentReport struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of IncidentReport
	// +required
	Spec IncidentReportSpec `json:"spec"`

	// status defines the observed state of IncidentReport
	// +optional
	Status IncidentReportStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// IncidentReportList contains a list of IncidentReport
type IncidentReportList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []IncidentReport `json:"items"`
}

func init() {
	SchemeBuilder.Register(&IncidentReport{}, &IncidentReportList{})
}
