package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/correlator/graph"
)

// blockingGraphBuilder waits until ctx.Done before returning, recording the
// reason. Used to prove that buildIncidentGraph honours its own timeout
// rather than blocking the reconcile loop indefinitely.
type blockingGraphBuilder struct {
	gotErr error
}

func (b *blockingGraphBuilder) Build(ctx context.Context, _ *rcav1alpha1.IncidentReport) (*graph.IncidentGraph, error) {
	<-ctx.Done()
	b.gotErr = ctx.Err()
	return nil, ctx.Err()
}

// TestBuildIncidentGraph_TimesOut verifies that a slow GraphBuilder cannot
// stall the reconcile loop: the wrapper imposes graphBuildTimeout and returns
// nil so the Active transition proceeds without a graph.
func TestBuildIncidentGraph_TimesOut(t *testing.T) {
	original := graphBuildTimeout
	graphBuildTimeout = 50 * time.Millisecond
	defer func() { graphBuildTimeout = original }()

	b := &blockingGraphBuilder{}
	r := &IncidentReportReconciler{GraphBuilder: b}

	report := &rcav1alpha1.IncidentReport{}
	report.Name = "i1"
	report.Namespace = "ns1"

	// Parent never expires on its own — only the inner timeout should release.
	parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	got := r.buildIncidentGraph(parent, logr.Discard(), report)
	elapsed := time.Since(start)

	if got != nil {
		t.Fatalf("expected nil graph on timeout, got %+v", got)
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("buildIncidentGraph did not honour timeout: took %v", elapsed)
	}
	if !errors.Is(b.gotErr, context.DeadlineExceeded) {
		t.Fatalf("expected DeadlineExceeded inside builder, got %v", b.gotErr)
	}
}

// TestBuildIncidentGraph_NilBuilder verifies the no-op path stays cheap.
func TestBuildIncidentGraph_NilBuilder(t *testing.T) {
	r := &IncidentReportReconciler{}
	if got := r.buildIncidentGraph(context.Background(), logr.Discard(), &rcav1alpha1.IncidentReport{}); got != nil {
		t.Fatalf("expected nil with no GraphBuilder configured, got %+v", got)
	}
}
