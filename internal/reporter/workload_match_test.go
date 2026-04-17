package reporter

import (
	"testing"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

func TestReportMatchesWorkloadRef_OTelRequiresSameIncidentType(t *testing.T) {
	workload := &rcav1alpha1.IncidentObjectRef{
		Kind:      "Deployment",
		Namespace: "rca-demo",
		Name:      "payment-service",
	}

	report := &rcav1alpha1.IncidentReport{
		Spec: rcav1alpha1.IncidentReportSpec{
			IncidentType: "ImagePullBackOff",
			Scope: rcav1alpha1.IncidentScope{
				WorkloadRef: workload,
			},
		},
	}

	if reportMatchesWorkloadRef(report, workload, "OTelSpanError") {
		t.Fatal("expected OTel incident type to require a matching existing incident type")
	}
}

func TestReportMatchesWorkloadRef_OTelMatchesSameIncidentType(t *testing.T) {
	workload := &rcav1alpha1.IncidentObjectRef{
		Kind:      "Deployment",
		Namespace: "rca-demo",
		Name:      "payment-service",
	}

	report := &rcav1alpha1.IncidentReport{
		Spec: rcav1alpha1.IncidentReportSpec{
			IncidentType: "OTelSpanError",
			Scope: rcav1alpha1.IncidentScope{
				WorkloadRef: workload,
			},
		},
	}

	if !reportMatchesWorkloadRef(report, workload, "OTelSpanError") {
		t.Fatal("expected OTel incident with same type and workload to match")
	}
}
