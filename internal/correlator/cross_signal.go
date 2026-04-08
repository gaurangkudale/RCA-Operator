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

package correlator

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/telemetry"
	"github.com/gaurangkudale/rca-operator/internal/topology"
)

// CrossSignalEnricher queries external telemetry backends to enrich incidents
// with related traces, correlated logs, and blast radius data. Enrichment is
// best-effort: if the telemetry backend is unavailable, the incident still
// fires with K8s-only context (backward compatible).
type CrossSignalEnricher struct {
	querier   telemetry.TelemetryQuerier
	topoCache *topology.Cache
	client    client.Client
	log       logr.Logger

	// lookbackWindow defines how far back to search for traces and logs
	// relative to the incident's observed time.
	lookbackWindow time.Duration

	// maxTraces limits the number of related trace IDs stored per incident.
	maxTraces int
}

// CrossSignalOption configures the enricher.
type CrossSignalOption func(*CrossSignalEnricher)

// WithLookbackWindow sets the time window for telemetry queries.
func WithLookbackWindow(d time.Duration) CrossSignalOption {
	return func(e *CrossSignalEnricher) { e.lookbackWindow = d }
}

// WithMaxTraces sets the maximum number of related traces stored per incident.
func WithMaxTraces(n int) CrossSignalOption {
	return func(e *CrossSignalEnricher) { e.maxTraces = n }
}

// NewCrossSignalEnricher creates an enricher that queries telemetry backends.
// If querier is nil, a NoopQuerier is used (no enrichment).
func NewCrossSignalEnricher(
	querier telemetry.TelemetryQuerier,
	topoCache *topology.Cache,
	k8sClient client.Client,
	log logr.Logger,
	opts ...CrossSignalOption,
) *CrossSignalEnricher {
	if querier == nil {
		querier = &telemetry.NoopQuerier{}
	}
	e := &CrossSignalEnricher{
		querier:        querier,
		topoCache:      topoCache,
		client:         k8sClient,
		log:            log.WithName("cross-signal"),
		lookbackWindow: 15 * time.Minute,
		maxTraces:      10,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// EnrichmentResult holds the cross-signal data to be written to an IncidentReport.
type EnrichmentResult struct {
	RelatedTraces []string
	BlastRadius   []string
}

// Enrich queries telemetry backends for traces and topology data related to
// the given incident. It returns enrichment data without modifying the CR
// directly. The caller is responsible for persisting the result.
//
// The method is safe to call concurrently and never returns an error - failures
// are logged and the result is returned with whatever data was available.
func (e *CrossSignalEnricher) Enrich(ctx context.Context, report *rcav1alpha1.IncidentReport) EnrichmentResult {
	result := EnrichmentResult{}
	serviceName := extractServiceName(report)
	if serviceName == "" {
		return result
	}

	// Query for error traces related to this service.
	traces, err := e.querier.FindErrorTraces(ctx, serviceName, e.lookbackWindow)
	if err != nil {
		e.log.V(1).Info("failed to find error traces", "service", serviceName, "error", err)
	} else {
		seen := make(map[string]bool)
		for _, t := range traces {
			if !seen[t.TraceID] {
				seen[t.TraceID] = true
				result.RelatedTraces = append(result.RelatedTraces, t.TraceID)
			}
		}
		if len(result.RelatedTraces) > e.maxTraces {
			result.RelatedTraces = result.RelatedTraces[:e.maxTraces]
		}
	}

	// Compute blast radius from topology.
	if e.topoCache != nil {
		graph, err := e.topoCache.Get(ctx)
		if err != nil {
			e.log.V(1).Info("failed to get topology graph", "error", err)
		} else if graph != nil {
			affected := topology.ComputeUpstreamBlastRadius(graph, serviceName)
			if affected != nil {
				sort.Strings(affected)
				result.BlastRadius = affected
			}
		}
	}

	return result
}

// ApplyEnrichment writes the enrichment result to an IncidentReport's status.
// It merges new trace IDs with existing ones (deduplicating) and replaces
// the blast radius. Returns true if the status was actually modified.
func ApplyEnrichment(report *rcav1alpha1.IncidentReport, result EnrichmentResult) bool {
	modified := false

	// Merge related traces (deduplicate).
	if len(result.RelatedTraces) > 0 {
		existing := make(map[string]bool)
		for _, t := range report.Status.RelatedTraces {
			existing[t] = true
		}
		for _, t := range result.RelatedTraces {
			if !existing[t] {
				report.Status.RelatedTraces = append(report.Status.RelatedTraces, t)
				existing[t] = true
				modified = true
			}
		}
	}

	// Replace blast radius (topology is always current).
	if len(result.BlastRadius) > 0 {
		if !stringSliceEqual(report.Status.BlastRadius, result.BlastRadius) {
			report.Status.BlastRadius = result.BlastRadius
			modified = true
		}
	}

	return modified
}

// PersistEnrichment writes enrichment data to the given IncidentReport CR.
// It reads the latest version, applies the enrichment, and patches the status.
func (e *CrossSignalEnricher) PersistEnrichment(ctx context.Context, key client.ObjectKey, result EnrichmentResult) error {
	if len(result.RelatedTraces) == 0 && len(result.BlastRadius) == 0 {
		return nil
	}

	report := &rcav1alpha1.IncidentReport{}
	if err := e.client.Get(ctx, key, report); err != nil {
		return err
	}

	if !ApplyEnrichment(report, result) {
		return nil // Nothing changed.
	}

	return e.client.Status().Update(ctx, report)
}

// extractServiceName derives a service name from the incident for telemetry queries.
// It uses the workload or resource ref name, falling back to the pod label.
func extractServiceName(report *rcav1alpha1.IncidentReport) string {
	if report.Spec.Scope.WorkloadRef != nil && report.Spec.Scope.WorkloadRef.Name != "" {
		return report.Spec.Scope.WorkloadRef.Name
	}
	if report.Spec.Scope.ResourceRef != nil && report.Spec.Scope.ResourceRef.Name != "" {
		return report.Spec.Scope.ResourceRef.Name
	}
	if podName, ok := report.Labels[labelPodName]; ok && podName != "" && podName != valueUnknown {
		// Strip common suffixes to get the service name (e.g., "payment-svc-abc123" -> "payment-svc")
		return stripPodSuffix(podName)
	}
	return ""
}

// stripPodSuffix removes the pod hash suffix from a pod name to approximate
// the service/deployment name (e.g., "payment-svc-7d4b8c6f5-x2k9n" -> "payment-svc").
func stripPodSuffix(podName string) string {
	parts := strings.Split(podName, "-")
	if len(parts) <= 2 {
		return podName
	}
	// Walk backwards stripping hash-like segments (5+ char alphanumeric).
	for len(parts) > 1 {
		last := parts[len(parts)-1]
		if len(last) >= 5 && isAlphanumeric(last) {
			parts = parts[:len(parts)-1]
		} else {
			break
		}
	}
	return strings.Join(parts, "-")
}

func isAlphanumeric(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
