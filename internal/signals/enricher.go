package signals

import (
	"context"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/incident"
)

// Enricher adds Kubernetes metadata (owner references, workload refs) to signals.
type Enricher struct {
	resolver *incident.Resolver
	log      logr.Logger
}

// NewEnricher creates an Enricher backed by the given Kubernetes client.
func NewEnricher(c client.Client, logger logr.Logger) *Enricher {
	return &Enricher{
		resolver: incident.NewResolver(c),
		log:      logger.WithName("signal-enricher"),
	}
}

// Enrich resolves pod owner references and populates scope/affectedResources.
// For non-pod-scoped signals (node-level, workload-level), the signal passes through unchanged.
// When an owner workload is discovered, pod-scoped signals are promoted to
// workload scope so that all signals for the same workload share a single
// incident fingerprint regardless of the originating event type.
func (e *Enricher) Enrich(ctx context.Context, sig NormalizedSignal) NormalizedSignal {
	if sig.Scope.Level != incident.ScopeLevelPod {
		return sig
	}

	podName := ""
	if sig.Scope.ResourceRef != nil {
		podName = sig.Scope.ResourceRef.Name
	}
	if podName == "" {
		return sig
	}

	scope, affectedResources, err := e.resolver.ResolvePodScope(ctx, sig.Namespace, podName)
	if err != nil {
		e.log.V(1).Info("Falling back to pod scope",
			"namespace", sig.Namespace,
			"pod", podName,
			"error", err.Error(),
		)
		// ResolvePodScope returns usable fallback scope/affected even on error.
		sig.Scope = scope
		sig.AffectedResources = affectedResources
		// Even on fallback, attempt workload promotion using pod name heuristics.
		sig.Input = promoteToWorkloadScope(sig.Input, sig.Namespace, podName)
		return sig
	}

	sig.Scope = scope
	sig.AffectedResources = affectedResources

	// Promote to workload scope if we resolved an owner.
	sig.Input = promoteToWorkloadScope(sig.Input, sig.Namespace, podName)

	return sig
}

// promoteToWorkloadScope promotes a pod-scoped incident to workload scope when
// the owner workload is known (from resolved owner refs) or can be guessed from
// the pod name. This ensures all signals for the same deployment share a single
// fingerprint and therefore a single incident, regardless of event type.
func promoteToWorkloadScope(input incident.Input, namespace, podName string) incident.Input {
	if input.Scope.WorkloadRef != nil && input.Scope.WorkloadRef.Name != "" {
		input.Scope.Level = incident.ScopeLevelWorkload
		input.Scope.Namespace = namespace
		input.Scope.ResourceRef = input.Scope.WorkloadRef
		return input
	}

	workloadName := GuessDeploymentNameFromPod(podName)
	if workloadName == "" {
		// Cannot determine workload — keep pod scope.
		return input
	}

	ref := &rcav1alpha1.IncidentObjectRef{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Namespace:  namespace,
		Name:       workloadName,
	}
	input.Scope.Level = incident.ScopeLevelWorkload
	input.Scope.Namespace = namespace
	input.Scope.WorkloadRef = ref
	input.Scope.ResourceRef = ref
	input.AffectedResources = mergeAffectedResources(input.AffectedResources, []rcav1alpha1.AffectedResource{
		{APIVersion: "apps/v1", Kind: "Deployment", Namespace: namespace, Name: workloadName},
	})
	return input
}

func mergeAffectedResources(existing, incoming []rcav1alpha1.AffectedResource) []rcav1alpha1.AffectedResource {
	out := append([]rcav1alpha1.AffectedResource{}, existing...)
	for _, candidate := range incoming {
		found := false
		for _, current := range out {
			if current.Kind == candidate.Kind && current.Namespace == candidate.Namespace && current.Name == candidate.Name {
				found = true
				break
			}
		}
		if !found {
			out = append(out, candidate)
		}
	}
	return out
}
