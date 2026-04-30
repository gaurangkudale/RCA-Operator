package webhook

import (
	"context"
	"strings"
	"testing"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
)

func TestRCAAgentWebhook_Default_FillsRetentionAndOTelDefaults(t *testing.T) {
	w := &RCAAgentWebhook{}

	t.Run("retention default is 30d when unset", func(t *testing.T) {
		a := &rcav1alpha1.RCAAgent{}
		if err := w.Default(context.Background(), a); err != nil {
			t.Fatalf("Default returned error: %v", err)
		}
		if a.Spec.IncidentRetention != "30d" {
			t.Errorf("want IncidentRetention=30d, got %q", a.Spec.IncidentRetention)
		}
	})

	t.Run("retention is preserved when set", func(t *testing.T) {
		a := &rcav1alpha1.RCAAgent{Spec: rcav1alpha1.RCAAgentSpec{IncidentRetention: "7d"}}
		if err := w.Default(context.Background(), a); err != nil {
			t.Fatalf("Default returned error: %v", err)
		}
		if a.Spec.IncidentRetention != "7d" {
			t.Errorf("retention should be unchanged, got %q", a.Spec.IncidentRetention)
		}
	})

	t.Run("otel defaults populate service name and sampling", func(t *testing.T) {
		a := &rcav1alpha1.RCAAgent{Spec: rcav1alpha1.RCAAgentSpec{OTel: &rcav1alpha1.OTelConfig{}}}
		if err := w.Default(context.Background(), a); err != nil {
			t.Fatalf("Default returned error: %v", err)
		}
		if a.Spec.OTel.ServiceName != "rca-operator" {
			t.Errorf("want OTel.ServiceName=rca-operator, got %q", a.Spec.OTel.ServiceName)
		}
		if a.Spec.OTel.SamplingRate != "1.0" {
			t.Errorf("want OTel.SamplingRate=1.0, got %q", a.Spec.OTel.SamplingRate)
		}
	})

	t.Run("otel defaults are not applied when otel is nil", func(t *testing.T) {
		a := &rcav1alpha1.RCAAgent{}
		if err := w.Default(context.Background(), a); err != nil {
			t.Fatalf("Default returned error: %v", err)
		}
		if a.Spec.OTel != nil {
			t.Errorf("expected OTel to remain nil, got %+v", a.Spec.OTel)
		}
	})

	t.Run("otel preserves explicit values", func(t *testing.T) {
		a := &rcav1alpha1.RCAAgent{Spec: rcav1alpha1.RCAAgentSpec{OTel: &rcav1alpha1.OTelConfig{ServiceName: "custom", SamplingRate: "0.1"}}}
		if err := w.Default(context.Background(), a); err != nil {
			t.Fatalf("Default returned error: %v", err)
		}
		if a.Spec.OTel.ServiceName != "custom" || a.Spec.OTel.SamplingRate != "0.1" {
			t.Errorf("explicit OTel values overwritten: %+v", a.Spec.OTel)
		}
	})
}

func TestRCAAgentWebhook_ValidateCreate(t *testing.T) {
	w := &RCAAgentWebhook{}
	ctx := context.Background()

	cases := []struct {
		name    string
		agent   *rcav1alpha1.RCAAgent
		wantErr string // "" → expect success; otherwise the error must contain this substring
	}{
		{
			name:    "empty watch namespaces is rejected",
			agent:   &rcav1alpha1.RCAAgent{},
			wantErr: "spec.watchNamespaces must not be empty",
		},
		{
			name: "valid agent with single namespace",
			agent: &rcav1alpha1.RCAAgent{
				Spec: rcav1alpha1.RCAAgentSpec{WatchNamespaces: []string{"default"}},
			},
		},
		{
			name: "otel endpoint missing port is rejected",
			agent: &rcav1alpha1.RCAAgent{
				Spec: rcav1alpha1.RCAAgentSpec{
					WatchNamespaces: []string{"default"},
					OTel:            &rcav1alpha1.OTelConfig{Endpoint: "otel-collector.observability"},
				},
			},
			wantErr: "spec.otel.endpoint must be a valid host:port",
		},
		{
			name: "otel endpoint with valid host:port passes",
			agent: &rcav1alpha1.RCAAgent{
				Spec: rcav1alpha1.RCAAgentSpec{
					WatchNamespaces: []string{"default"},
					OTel:            &rcav1alpha1.OTelConfig{Endpoint: "otel-collector.observability:4317"},
				},
			},
		},
		{
			name: "otel endpoint empty string is allowed (collector resolves later)",
			agent: &rcav1alpha1.RCAAgent{
				Spec: rcav1alpha1.RCAAgentSpec{
					WatchNamespaces: []string{"default"},
					OTel:            &rcav1alpha1.OTelConfig{Endpoint: ""},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.ValidateCreate(ctx, tc.agent)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}

func TestRCAAgentWebhook_ValidateUpdate_ReusesValidator(t *testing.T) {
	w := &RCAAgentWebhook{}
	old := &rcav1alpha1.RCAAgent{Spec: rcav1alpha1.RCAAgentSpec{WatchNamespaces: []string{"default"}}}
	bad := &rcav1alpha1.RCAAgent{}
	if _, err := w.ValidateUpdate(context.Background(), old, bad); err == nil {
		t.Fatalf("update with empty watchNamespaces should fail")
	}
}

func TestRCAAgentWebhook_ValidateDelete_AlwaysAllows(t *testing.T) {
	w := &RCAAgentWebhook{}
	if _, err := w.ValidateDelete(context.Background(), &rcav1alpha1.RCAAgent{}); err != nil {
		t.Fatalf("ValidateDelete should never error, got %v", err)
	}
}
