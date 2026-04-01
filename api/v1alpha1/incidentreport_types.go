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

	// fingerprint is the canonical identity for an incident.
	// It is stable across repeated signals for the same underlying issue.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Fingerprint string `json:"fingerprint"`

	// incidentType is the durable incident category.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	IncidentType string `json:"incidentType"`

	// scope describes the primary object or workload the incident belongs to.
	// +optional
	Scope IncidentScope `json:"scope,omitempty"`
}

// AffectedResource identifies a Kubernetes resource involved in an incident.
type AffectedResource struct {
	// apiVersion is the resource API version when known.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`

	// kind is the resource kind (e.g. Deployment, Pod, Node).
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`

	// name is the resource name.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// namespace is the resource namespace. Empty for cluster-scoped resources.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// uid is the Kubernetes UID when known.
	// +optional
	UID string `json:"uid,omitempty"`
}

// IncidentObjectRef is a normalized reference to a Kubernetes resource.
type IncidentObjectRef struct {
	// apiVersion is the resource API version.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`

	// kind is the Kubernetes kind.
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`

	// namespace is empty for cluster-scoped resources.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// name is the Kubernetes object name.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// uid is the Kubernetes UID when known.
	// +optional
	UID string `json:"uid,omitempty"`
}

// IncidentScope identifies the primary scope for an incident.
type IncidentScope struct {
	// level is one of Cluster, Namespace, Workload, or Pod.
	// +kubebuilder:validation:Enum=Cluster;Namespace;Workload;Pod
	// +optional
	Level string `json:"level,omitempty"`

	// namespace is populated for namespace-, workload-, and pod-scoped incidents.
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// workloadRef points at the top-level workload when the issue belongs to one.
	// +optional
	WorkloadRef *IncidentObjectRef `json:"workloadRef,omitempty"`

	// resourceRef points at the primary affected object.
	// +optional
	ResourceRef *IncidentObjectRef `json:"resourceRef,omitempty"`
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
	// severity is the incident severity level assigned by the incident engine.
	// +kubebuilder:validation:Enum=P1;P2;P3;P4
	// +required
	Severity string `json:"severity,omitempty"`

	// phase is the current lifecycle phase of the incident.
	// +kubebuilder:validation:Enum=Detecting;Active;Resolved
	// +required
	Phase string `json:"phase,omitempty"`

	// incidentType is the category of incident detected by the incident engine.
	// The value is self-describing from the raw event type (e.g. CrashLoopBackOff,
	// OOMKilled, ImagePullBackOff, NodeNotReady) rather than a fixed enum.
	// +required
	IncidentType string `json:"incidentType,omitempty"`

	// summary is the short dashboard-friendly summary for the current incident state.
	// +optional
	Summary string `json:"summary,omitempty"`

	// reason is the machine-oriented Kubernetes reason when available.
	// +optional
	Reason string `json:"reason,omitempty"`

	// message is the detailed message for the most recent signal.
	// +optional
	Message string `json:"message,omitempty"`

	// firstObservedAt is when the incident fingerprint was first seen in the current lifecycle.
	// +optional
	FirstObservedAt *metav1.Time `json:"firstObservedAt,omitempty"`

	// activeAt is when the incident crossed the stabilization window and became Active.
	// +optional
	ActiveAt *metav1.Time `json:"activeAt,omitempty"`

	// lastObservedAt is when the most recent confirming signal was received.
	// +optional
	LastObservedAt *metav1.Time `json:"lastObservedAt,omitempty"`

	// startTime is when the incident was first detected.
	// Deprecated: use firstObservedAt. Retained only for compatibility with older clients.
	// +required
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// resolvedTime is when the incident was resolved. Empty while still active.
	// Deprecated: use resolvedAt. Retained only for compatibility with older clients.
	// +optional
	ResolvedTime *metav1.Time `json:"resolvedTime,omitempty"`

	// resolvedAt is when the incident was resolved.
	// +optional
	ResolvedAt *metav1.Time `json:"resolvedAt,omitempty"`

	// signalCount is the number of confirming signals recorded in the current lifecycle.
	// +optional
	SignalCount int64 `json:"signalCount,omitempty"`

	// stabilizationWindowSeconds is the required continuous observation window before Active.
	// +optional
	StabilizationWindowSeconds int64 `json:"stabilizationWindowSeconds,omitempty"`

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
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=".spec.incidentType",description="Incident type"
// +kubebuilder:printcolumn:name="Notified",type=boolean,JSONPath=".status.notified",description="Notifications sent"
// +kubebuilder:printcolumn:name="FirstSeen",type=date,JSONPath=".status.firstObservedAt",description="When first observed"
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
