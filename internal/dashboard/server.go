package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/reporter"
)

// Server serves the incident dashboard UI and its REST API.
// It implements manager.Runnable so it can be registered with mgr.Add().
type Server struct {
	client client.Client
	addr   string
	log    logr.Logger
}

// NewServer returns a dashboard server that will listen on addr.
func NewServer(c client.Client, addr string, logger logr.Logger) *Server {
	return &Server{
		client: c,
		addr:   addr,
		log:    logger.WithName("dashboard"),
	}
}

// Start implements manager.Runnable. It blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Serve embedded static files at /
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return fmt.Errorf("dashboard: embed sub failed: %w", err)
	}
	mux.Handle("/", http.FileServer(http.FS(sub)))

	// API endpoints
	mux.HandleFunc("/api/incidents", s.handleIncidents)
	mux.HandleFunc("/api/stats", s.handleStats)

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(_ net.Listener) context.Context { return ctx },
	}

	// Graceful shutdown when the manager context is cancelled.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	s.log.Info("Starting dashboard server", "addr", s.addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("dashboard server failed: %w", err)
	}
	return nil
}

// ── API types ─────────────────────────────────────────────────────────────────

type incidentResponse struct {
	Name              string                         `json:"name"`
	Namespace         string                         `json:"namespace"`
	Fingerprint       string                         `json:"fingerprint"`
	PodName           string                         `json:"podName"`
	Severity          string                         `json:"severity"`
	Phase             string                         `json:"phase"`
	IncidentType      string                         `json:"incidentType"`
	Summary           string                         `json:"summary"`
	Reason            string                         `json:"reason"`
	Message           string                         `json:"message"`
	Notified          bool                           `json:"notified"`
	FirstObservedAt   *time.Time                     `json:"firstObservedAt"`
	ActiveAt          *time.Time                     `json:"activeAt"`
	LastObservedAt    *time.Time                     `json:"lastObservedAt"`
	StartTime         *time.Time                     `json:"startTime"`
	ResolvedTime      *time.Time                     `json:"resolvedTime"`
	ResolvedAt        *time.Time                     `json:"resolvedAt"`
	SignalCount       int64                          `json:"signalCount"`
	Scope             rcav1alpha1.IncidentScope      `json:"scope"`
	AffectedResources []rcav1alpha1.AffectedResource `json:"affectedResources"`
	CorrelatedSignals []string                       `json:"correlatedSignals"`
	Timeline          []timelineEntry                `json:"timeline"`
	AgentRef          string                         `json:"agentRef"`
	LastSeen          string                         `json:"lastSeen"`
	RootCause         string                         `json:"rootCause,omitempty"`
}

type timelineEntry struct {
	Time  *time.Time `json:"time"`
	Event string     `json:"event"`
}

type statsResponse struct {
	Active     int                       `json:"active"`
	Detecting  int                       `json:"detecting"`
	Resolved   int                       `json:"resolved"`
	Namespaces map[string]namespaceStats `json:"namespaces"`
	Agents     []agentInfo               `json:"agents"`
}

type namespaceStats struct {
	Active    int  `json:"active"`
	Monitored bool `json:"monitored"`
}

type agentInfo struct {
	Name            string   `json:"name"`
	WatchNamespaces []string `json:"watchNamespaces"`
	Healthy         bool     `json:"healthy"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	list := &rcav1alpha1.IncidentReportList{}
	opts := []client.ListOption{}

	// Optional namespace filter.
	if ns := r.URL.Query().Get("namespace"); ns != "" {
		opts = append(opts, client.InNamespace(ns))
	}

	if err := s.client.List(r.Context(), list, opts...); err != nil {
		s.log.Error(err, "Failed to list IncidentReports")
		http.Error(w, "failed to list incidents", http.StatusInternalServerError)
		return
	}

	// Optional phase and severity filters (applied in-memory).
	phaseFilter := r.URL.Query().Get("phase")
	sevFilter := r.URL.Query().Get("severity")

	var result []incidentResponse
	for i := range list.Items {
		item := &list.Items[i]
		if phaseFilter != "" && item.Status.Phase != phaseFilter {
			continue
		}
		if sevFilter != "" && item.Status.Severity != sevFilter {
			continue
		}
		result = append(result, toIncidentResponse(item))
	}

	if result == nil {
		result = []incidentResponse{}
	}

	writeJSON(w, result)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := s.client.List(r.Context(), list); err != nil {
		s.log.Error(err, "Failed to list IncidentReports for stats")
		http.Error(w, "failed to list incidents", http.StatusInternalServerError)
		return
	}

	resp := statsResponse{
		Namespaces: make(map[string]namespaceStats),
	}
	agentSet := make(map[string]bool)

	for i := range list.Items {
		item := &list.Items[i]
		switch item.Status.Phase {
		case reporter.PhaseActive:
			resp.Active++
		case reporter.PhaseDetecting:
			resp.Detecting++
		case reporter.PhaseResolved:
			resp.Resolved++
		}

		ns := item.Namespace
		if item.Status.Phase == reporter.PhaseActive || item.Status.Phase == reporter.PhaseDetecting {
			entry := resp.Namespaces[ns]
			entry.Active++
			resp.Namespaces[ns] = entry
		} else if _, ok := resp.Namespaces[ns]; !ok {
			resp.Namespaces[ns] = namespaceStats{}
		}

		if agent := item.Spec.AgentRef; agent != "" {
			agentSet[agent] = true
		}
	}

	// Also list RCAAgent CRDs directly so agents without incidents still appear.
	agentList := &rcav1alpha1.RCAAgentList{}
	agentMap := make(map[string]*rcav1alpha1.RCAAgent)
	if err := s.client.List(r.Context(), agentList); err != nil {
		s.log.Error(err, "Failed to list RCAAgents for stats")
	} else {
		for i := range agentList.Items {
			a := &agentList.Items[i]
			agentSet[a.Name] = true
			agentMap[a.Name] = a
			// Add watched namespaces so they always appear in the namespace list.
			for _, ns := range a.Spec.WatchNamespaces {
				if _, ok := resp.Namespaces[ns]; !ok {
					resp.Namespaces[ns] = namespaceStats{Monitored: true}
				} else {
					entry := resp.Namespaces[ns]
					entry.Monitored = true
					resp.Namespaces[ns] = entry
				}
			}
		}
	}

	// Mark all namespaces that are watched by at least one agent.
	for _, a := range agentList.Items {
		for _, ns := range a.Spec.WatchNamespaces {
			entry := resp.Namespaces[ns]
			entry.Monitored = true
			resp.Namespaces[ns] = entry
		}
	}

	resp.Agents = make([]agentInfo, 0, len(agentSet))
	for name := range agentSet {
		ai := agentInfo{Name: name, Healthy: true}
		if agent, ok := agentMap[name]; ok {
			ai.WatchNamespaces = agent.Spec.WatchNamespaces
			// Check conditions for health.
			for _, c := range agent.Status.Conditions {
				if c.Type == "Available" {
					ai.Healthy = c.Status == "True"
					break
				}
			}
		}
		resp.Agents = append(resp.Agents, ai)
	}

	writeJSON(w, resp)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func toIncidentResponse(item *rcav1alpha1.IncidentReport) incidentResponse {
	resp := incidentResponse{
		Name:              item.Name,
		Namespace:         item.Namespace,
		Fingerprint:       item.Spec.Fingerprint,
		PodName:           item.Labels[reporter.LabelPodName],
		Severity:          item.Status.Severity,
		Phase:             item.Status.Phase,
		IncidentType:      item.Spec.IncidentType,
		Summary:           item.Status.Summary,
		Reason:            item.Status.Reason,
		Message:           item.Status.Message,
		Notified:          item.Status.Notified,
		Scope:             item.Spec.Scope,
		AffectedResources: item.Status.AffectedResources,
		CorrelatedSignals: item.Status.CorrelatedSignals,
		AgentRef:          item.Spec.AgentRef,
		LastSeen:          item.Annotations[reporter.AnnotationLastSeen],
		SignalCount:       item.Status.SignalCount,
		RootCause:         item.Status.RootCause,
	}
	if item.Status.FirstObservedAt != nil {
		t := item.Status.FirstObservedAt.Time
		resp.FirstObservedAt = &t
	}
	if item.Status.ActiveAt != nil {
		t := item.Status.ActiveAt.Time
		resp.ActiveAt = &t
	}
	if item.Status.LastObservedAt != nil {
		t := item.Status.LastObservedAt.Time
		resp.LastObservedAt = &t
	}
	if item.Status.StartTime != nil {
		t := item.Status.StartTime.Time
		resp.StartTime = &t
	}
	if item.Status.ResolvedTime != nil {
		t := item.Status.ResolvedTime.Time
		resp.ResolvedTime = &t
	}
	if item.Status.ResolvedAt != nil {
		t := item.Status.ResolvedAt.Time
		resp.ResolvedAt = &t
	}
	if resp.AffectedResources == nil {
		resp.AffectedResources = []rcav1alpha1.AffectedResource{}
	}
	if resp.CorrelatedSignals == nil {
		resp.CorrelatedSignals = []string{}
	}

	resp.Timeline = make([]timelineEntry, 0, len(item.Status.Timeline))
	for _, e := range item.Status.Timeline {
		t := e.Time.Time
		resp.Timeline = append(resp.Timeline, timelineEntry{Time: &t, Event: e.Event})
	}
	return resp
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, "json encode failed", http.StatusInternalServerError)
	}
}
