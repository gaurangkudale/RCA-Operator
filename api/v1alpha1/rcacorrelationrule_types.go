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

// RCACorrelationRuleSpec defines a declarative correlation rule evaluated by the
// generic rule engine. Rules are loaded dynamically — no operator redeploy needed.
type RCACorrelationRuleSpec struct {
	// priority controls evaluation order. Higher values are evaluated first.
	// The first rule whose conditions match wins.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=100
	Priority int `json:"priority"`

	// agentSelector restricts which RCAAgents this rule applies to.
	// A nil selector matches all agents.
	// +optional
	AgentSelector *metav1.LabelSelector `json:"agentSelector,omitempty"`

	// trigger is the event type that initiates rule evaluation.
	// +kubebuilder:validation:Required
	Trigger RuleTrigger `json:"trigger"`

	// conditions are additional signals that must be present in the correlation
	// buffer for the rule to fire. All conditions must match (AND logic).
	// +optional
	Conditions []RuleCondition `json:"conditions,omitempty"`

	// fires defines the incident properties when this rule matches.
	// +kubebuilder:validation:Required
	Fires RuleFires `json:"fires"`
}

// RuleTrigger identifies the event that starts rule evaluation.
type RuleTrigger struct {
	// eventType is one of the watcher EventType constants (e.g. CrashLoopBackOff, OOMKilled).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	EventType string `json:"eventType"`
}

// RuleCondition specifies an additional signal that must be present in the buffer.
type RuleCondition struct {
	// eventType is the signal type that must be present.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	EventType string `json:"eventType"`

	// scope defines the relationship between the trigger event and this condition.
	// +kubebuilder:validation:Enum=samePod;sameNode;sameNamespace;any
	// +kubebuilder:default=samePod
	Scope string `json:"scope"`

	// negate inverts the condition: when true the rule fires only if this signal
	// is NOT present in the buffer.
	// +optional
	Negate bool `json:"negate,omitempty"`
}

// RuleFires defines the incident output when a rule matches.
type RuleFires struct {
	// incidentType is the canonical incident category.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	IncidentType string `json:"incidentType"`

	// severity is the incident priority level.
	// +kubebuilder:validation:Enum=P1;P2;P3;P4
	// +kubebuilder:validation:Required
	Severity string `json:"severity"`

	// summary is a Go text/template rendered with event context.
	// Available variables: {{.PodName}}, {{.Namespace}}, {{.NodeName}}, {{.EventType}}.
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	Summary string `json:"summary"`

	// resource overrides the default resource for incident dedup.
	// Use "node" for node-scoped, "deployment" for deployment-scoped, or leave empty for pod-scoped.
	// +optional
	Resource string `json:"resource,omitempty"`

	// scope overrides the incident scope level.
	// +kubebuilder:validation:Enum=Pod;Workload;Namespace;Cluster
	// +optional
	Scope string `json:"scope,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Cluster,shortName=rcr
// +kubebuilder:printcolumn:name="Priority",type=integer,JSONPath=".spec.priority",description="Evaluation priority"
// +kubebuilder:printcolumn:name="Trigger",type=string,JSONPath=".spec.trigger.eventType",description="Trigger event type"
// +kubebuilder:printcolumn:name="Fires",type=string,JSONPath=".spec.fires.incidentType",description="Incident type produced"
// +kubebuilder:printcolumn:name="Severity",type=string,JSONPath=".spec.fires.severity",description="Incident severity"
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=".metadata.creationTimestamp"

// RCACorrelationRule is a cluster-scoped CRD that defines a declarative
// correlation rule for the RCA Operator rule engine.
type RCACorrelationRule struct {
	metav1.TypeMeta `json:",inline"`

	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// +required
	Spec RCACorrelationRuleSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// RCACorrelationRuleList contains a list of RCACorrelationRule.
type RCACorrelationRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []RCACorrelationRule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RCACorrelationRule{}, &RCACorrelationRuleList{})
}
