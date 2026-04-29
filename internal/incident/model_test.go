package incident

import (
	"testing"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

func TestFingerprint_OTelIncidentUsesSharedTelemetryScope(t *testing.T) {
	input := Input{
		Namespace:    "rca-demo",
		IncidentType: "OTelSpanError",
		Scope: rcav1alpha1.IncidentScope{
			Level:     ScopeLevelWorkload,
			Namespace: "rca-demo",
			WorkloadRef: &rcav1alpha1.IncidentObjectRef{
				Kind:      "Service",
				Namespace: "rca-demo",
				Name:      "proxy-service",
			},
		},
	}

	got := input.Fingerprint()
	want := "Workload|rca-demo|service|proxy-service"
	if got != want {
		t.Fatalf("Fingerprint() = %q, want %q", got, want)
	}

	input.IncidentType = "OTelLogMatch"
	if got := input.Fingerprint(); got != want {
		t.Fatalf("Fingerprint() for OTelLogMatch = %q, want shared %q", got, want)
	}
}

func TestFingerprint_KubernetesIncidentExcludesType(t *testing.T) {
	input := Input{
		Namespace:    "rca-demo",
		IncidentType: "ImagePullBackOff",
		Scope: rcav1alpha1.IncidentScope{
			Level:     ScopeLevelWorkload,
			Namespace: "rca-demo",
			WorkloadRef: &rcav1alpha1.IncidentObjectRef{
				Kind:      "Deployment",
				Namespace: "rca-demo",
				Name:      "payment-service",
			},
		},
	}

	got := input.Fingerprint()
	want := "Workload|rca-demo|deployment|payment-service"
	if got != want {
		t.Fatalf("Fingerprint() = %q, want %q", got, want)
	}
}

func TestIsOTelIncidentType(t *testing.T) {
	if !IsOTelIncidentType("OTelLogMatch") {
		t.Fatal("expected OTelLogMatch to be detected as OTel incident type")
	}
	if IsOTelIncidentType("ImagePullBackOff") {
		t.Fatal("expected ImagePullBackOff to be non-OTel incident type")
	}
}
