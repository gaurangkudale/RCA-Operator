package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/incidentstatus"
	"github.com/gaurangkudale/rca-operator/internal/reporter"
	"github.com/gaurangkudale/rca-operator/internal/telemetry"
	"github.com/gaurangkudale/rca-operator/internal/topology"
)

// Server serves the incident dashboard UI and its REST API.
// It implements manager.Runnable so it can be registered with mgr.Add().
type Server struct {
	client    client.Client
	addr      string
	log       logr.Logger
	querier   telemetry.TelemetryQuerier
	topoCache *topology.Cache
	sseHub    *SSEHub
}

// ServerOption configures the dashboard server.
type ServerOption func(*Server)

// WithTelemetryQuerier sets the telemetry querier for trace/metric/log API endpoints.
func WithTelemetryQuerier(q telemetry.TelemetryQuerier) ServerOption {
	return func(s *Server) { s.querier = q }
}

// WithTopologyCache sets the topology cache for the topology API endpoint.
func WithTopologyCache(c *topology.Cache) ServerOption {
	return func(s *Server) { s.topoCache = c }
}

// NewServer returns a dashboard server that will listen on addr.
func NewServer(c client.Client, addr string, logger logr.Logger, opts ...ServerOption) *Server {
	s := &Server{
		client: c,
		addr:   addr,
		log:    logger.WithName("dashboard"),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.sseHub = NewSSEHub(s.log)
	return s
}

// SSEHub returns the SSE hub for broadcasting events from outside the dashboard.
func (s *Server) SSEHub() *SSEHub {
	return s.sseHub
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
	mux.HandleFunc("/api/incidents/", s.handleIncidentDetail)
	mux.HandleFunc("/api/rules", s.handleRules)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/timeline", s.handleTimeline)

	// Phase 2: Topology and telemetry endpoints
	mux.HandleFunc("/api/topology", s.handleTopology)
	mux.HandleFunc("/api/topology/blast", s.handleBlastRadius)
	mux.HandleFunc("/api/services", s.handleServices)
	mux.HandleFunc("/api/services/", s.handleServiceDetail)

	// SSE live streams
	mux.HandleFunc("/api/stream/topology", func(w http.ResponseWriter, r *http.Request) {
		s.sseHub.ServeHTTP(w, r, "topology")
	})
	mux.HandleFunc("/api/stream/correlation", func(w http.ResponseWriter, r *http.Request) {
		s.sseHub.ServeHTTP(w, r, "correlation")
	})

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
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
	ResolvedAt        *time.Time                     `json:"resolvedAt"`
	SignalCount       int64                          `json:"signalCount"`
	Scope             rcav1alpha1.IncidentScope      `json:"scope"`
	AffectedResources []rcav1alpha1.AffectedResource `json:"affectedResources"`
	CorrelatedSignals []string                       `json:"correlatedSignals"`
	Timeline          []timelineEntry                `json:"timeline"`
	AgentRef          string                         `json:"agentRef"`
	LastSeen          string                         `json:"lastSeen"`
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

type ruleResponse struct {
	Name          string   `json:"name"`
	Priority      int      `json:"priority"`
	TriggerEvent  string   `json:"triggerEvent"`
	Conditions    []string `json:"conditions"`
	FiresType     string   `json:"firesType"`
	FiresSeverity string   `json:"firesSeverity"`
	AgentSelector string   `json:"agentSelector"`
	Age           string   `json:"age"`
	AutoGenerated bool     `json:"autoGenerated"`
}

type incidentDetailResponse struct {
	incidentResponse
	TraceID   string `json:"traceId"`
	FiredRule string `json:"firedRule"`
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
	typeFilter := r.URL.Query().Get("type")
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query")))
	limit := parsePositiveInt(r.URL.Query().Get("limit"), 500)
	offset := parsePositiveInt(r.URL.Query().Get("offset"), 0)
	sortBy := r.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "newest"
	}

	result := make([]incidentResponse, 0, len(list.Items))
	for i := range list.Items {
		item := &list.Items[i]
		if phaseFilter != "" && item.Status.Phase != phaseFilter {
			continue
		}
		if sevFilter != "" && item.Status.Severity != sevFilter {
			continue
		}
		if typeFilter != "" && item.Spec.IncidentType != typeFilter {
			continue
		}
		if query != "" && !matchesIncidentQuery(item, query) {
			continue
		}
		result = append(result, toIncidentResponse(item))
	}

	sortIncidentResponses(result, sortBy)

	if offset > len(result) {
		offset = len(result)
	}
	end := len(result)
	if limit > 0 && offset+limit < end {
		end = offset + limit
	}

	writeJSON(w, result[offset:end])
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

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	list := &rcav1alpha1.RCACorrelationRuleList{}
	if err := s.client.List(r.Context(), list); err != nil {
		s.log.Error(err, "Failed to list RCACorrelationRules")
		http.Error(w, "failed to list rules", http.StatusInternalServerError)
		return
	}

	result := make([]ruleResponse, 0, len(list.Items))
	for i := range list.Items {
		rule := &list.Items[i]
		conditions := make([]string, 0, len(rule.Spec.Conditions))
		for _, cond := range rule.Spec.Conditions {
			desc := cond.EventType + " on " + cond.Scope
			if cond.Negate {
				desc = "NOT " + desc
			}
			conditions = append(conditions, desc)
		}
		agentSel := "all"
		if rule.Spec.AgentSelector != nil {
			parts := make([]string, 0)
			for k, v := range rule.Spec.AgentSelector.MatchLabels {
				parts = append(parts, k+"="+v)
			}
			if len(parts) > 0 {
				agentSel = strings.Join(parts, ",")
			}
		}
		age := time.Since(rule.CreationTimestamp.Time).Truncate(time.Minute).String()
		autoGen := rule.Labels["rca.rca-operator.tech/auto-generated"] == "true"
		result = append(result, ruleResponse{
			Name:          rule.Name,
			Priority:      rule.Spec.Priority,
			TriggerEvent:  rule.Spec.Trigger.EventType,
			Conditions:    conditions,
			FiresType:     rule.Spec.Fires.IncidentType,
			FiresSeverity: rule.Spec.Fires.Severity,
			AgentSelector: agentSel,
			Age:           age,
			AutoGenerated: autoGen,
		})
	}

	// Sort by priority descending.
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Priority > result[j].Priority
	})

	writeJSON(w, result)
}

func (s *Server) handleIncidentDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract incident name from path: /api/incidents/{namespace}/{name}
	path := strings.TrimPrefix(r.URL.Path, "/api/incidents/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "path must be /api/incidents/{namespace}/{name}", http.StatusBadRequest)
		return
	}
	namespace, name := parts[0], parts[1]

	item := &rcav1alpha1.IncidentReport{}
	if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: namespace, Name: name}, item); err != nil {
		s.log.Error(err, "Failed to get IncidentReport", "namespace", namespace, "name", name)
		http.Error(w, "incident not found", http.StatusNotFound)
		return
	}

	base := toIncidentResponse(item)
	detail := incidentDetailResponse{
		incidentResponse: base,
	}
	if item.Annotations != nil {
		detail.TraceID = item.Annotations["rca.rca-operator.tech/trace-id"]
		detail.FiredRule = item.Annotations["rca.rca-operator.tech/fired-rule"]
	}

	writeJSON(w, detail)
}

func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	fingerprint := r.URL.Query().Get("fingerprint")
	if fingerprint == "" {
		http.Error(w, "fingerprint query parameter is required", http.StatusBadRequest)
		return
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := s.client.List(r.Context(), list); err != nil {
		s.log.Error(err, "Failed to list IncidentReports for timeline")
		http.Error(w, "failed to list incidents", http.StatusInternalServerError)
		return
	}

	// Collect all timeline entries from incidents matching the fingerprint, across
	// all lifecycle phases (Detecting, Active, Resolved). This gives a unified
	// chronological view of an incident's full history, including reopens.
	type fullTimelineEntry struct {
		Time         *time.Time `json:"time"`
		Event        string     `json:"event"`
		Phase        string     `json:"phase"`
		IncidentName string     `json:"incidentName"`
		Namespace    string     `json:"namespace"`
	}

	entries := make([]fullTimelineEntry, 0)
	for i := range list.Items {
		item := &list.Items[i]
		if item.Spec.Fingerprint != fingerprint {
			continue
		}

		for _, te := range item.Status.Timeline {
			t := te.Time.Time
			entries = append(entries, fullTimelineEntry{
				Time:         &t,
				Event:        te.Event,
				Phase:        item.Status.Phase,
				IncidentName: item.Name,
				Namespace:    item.Namespace,
			})
		}

		// Add lifecycle transition events that may not be in the timeline.
		if item.Status.FirstObservedAt != nil {
			t := item.Status.FirstObservedAt.Time
			entries = append(entries, fullTimelineEntry{
				Time:         &t,
				Event:        "Incident detected",
				Phase:        reporter.PhaseDetecting,
				IncidentName: item.Name,
				Namespace:    item.Namespace,
			})
		}
		if item.Status.ActiveAt != nil {
			t := item.Status.ActiveAt.Time
			entries = append(entries, fullTimelineEntry{
				Time:         &t,
				Event:        "Incident activated",
				Phase:        reporter.PhaseActive,
				IncidentName: item.Name,
				Namespace:    item.Namespace,
			})
		}
		if resolvedAt := incidentstatus.EffectiveResolvedTime(item.Status); resolvedAt != nil {
			t := resolvedAt.Time
			entries = append(entries, fullTimelineEntry{
				Time:         &t,
				Event:        "Incident resolved",
				Phase:        reporter.PhaseResolved,
				IncidentName: item.Name,
				Namespace:    item.Namespace,
			})
		}
	}

	// Sort chronologically, oldest first.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Time == nil || entries[j].Time == nil {
			return entries[i].Time != nil
		}
		return entries[i].Time.Before(*entries[j].Time)
	})

	// Deduplicate entries with the same timestamp and event text.
	if len(entries) > 1 {
		deduped := entries[:1]
		for _, e := range entries[1:] {
			prev := deduped[len(deduped)-1]
			if e.Time != nil && prev.Time != nil && e.Time.Equal(*prev.Time) && e.Event == prev.Event && e.IncidentName == prev.IncidentName {
				continue
			}
			deduped = append(deduped, e)
		}
		entries = deduped
	}

	writeJSON(w, entries)
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
	if startAt := incidentstatus.EffectiveStartTime(item.Status); startAt != nil {
		t := startAt.Time
		resp.FirstObservedAt = &t
	}
	if resolvedAt := incidentstatus.EffectiveResolvedTime(item.Status); resolvedAt != nil {
		t := resolvedAt.Time
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

func parsePositiveInt(raw string, fallback int) int {
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return fallback
	}
	return n
}

func matchesIncidentQuery(item *rcav1alpha1.IncidentReport, query string) bool {
	fields := make([]string, 0, 7+len(item.Status.AffectedResources)*3)
	fields = append(fields,
		item.Name,
		item.Namespace,
		item.Spec.AgentRef,
		item.Spec.IncidentType,
		item.Status.Summary,
		item.Status.Message,
		item.Status.Reason,
	)
	for _, res := range item.Status.AffectedResources {
		fields = append(fields, res.Kind, res.Name, res.Namespace)
	}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}

func sortIncidentResponses(items []incidentResponse, sortBy string) {
	sort.SliceStable(items, func(i, j int) bool {
		left := incidentTimestamp(items[i])
		right := incidentTimestamp(items[j])
		switch sortBy {
		case "oldest":
			return left.Before(right)
		case "severity":
			lv := severityRank(items[i].Severity)
			rv := severityRank(items[j].Severity)
			if lv == rv {
				return right.Before(left)
			}
			return lv > rv
		default:
			return right.Before(left)
		}
	})
}

func incidentTimestamp(item incidentResponse) time.Time {
	if item.FirstObservedAt != nil {
		return *item.FirstObservedAt
	}
	if item.ResolvedAt != nil {
		return *item.ResolvedAt
	}
	return time.Time{}
}

func severityRank(severity string) int {
	switch severity {
	case "P1":
		return 4
	case "P2":
		return 3
	case "P3":
		return 2
	default:
		return 1
	}
}

// ── Phase 2: Topology & Telemetry Handlers ──────────────────────────────────

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.topoCache == nil {
		writeJSON(w, map[string]any{"nodes": map[string]any{}, "edges": []any{}})
		return
	}

	graph, err := s.topoCache.Get(r.Context())
	if err != nil {
		s.log.Error(err, "Failed to get topology graph")
		http.Error(w, "failed to get topology", http.StatusInternalServerError)
		return
	}
	writeJSON(w, graph)
}

func (s *Server) handleBlastRadius(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	service := r.URL.Query().Get("service")
	if service == "" {
		http.Error(w, "service query parameter is required", http.StatusBadRequest)
		return
	}

	if s.topoCache == nil {
		writeJSON(w, []string{})
		return
	}

	graph, err := s.topoCache.Get(r.Context())
	if err != nil {
		s.log.Error(err, "Failed to get topology graph for blast radius")
		http.Error(w, "failed to get topology", http.StatusInternalServerError)
		return
	}

	affected := topology.ComputeBlastRadius(graph, service)
	if affected == nil {
		affected = []string{}
	}
	writeJSON(w, affected)
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.topoCache == nil {
		writeJSON(w, []any{})
		return
	}

	graph, err := s.topoCache.Get(r.Context())
	if err != nil {
		s.log.Error(err, "Failed to get topology graph for services")
		http.Error(w, "failed to get topology", http.StatusInternalServerError)
		return
	}

	type serviceEntry struct {
		Name   string                 `json:"name"`
		Status telemetry.HealthStatus `json:"status"`
		Icon   string                 `json:"icon,omitempty"`
	}
	services := make([]serviceEntry, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		services = append(services, serviceEntry{
			Name:   node.Name,
			Status: node.Status,
			Icon:   node.Icon,
		})
	}
	writeJSON(w, services)
}

func (s *Server) handleServiceDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract service name and sub-resource from path: /api/services/{name}/{sub}
	path := strings.TrimPrefix(r.URL.Path, "/api/services/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "path must be /api/services/{name} or /api/services/{name}/{sub}", http.StatusBadRequest)
		return
	}
	serviceName := parts[0]
	subResource := ""
	if len(parts) == 2 {
		subResource = parts[1]
	}

	if s.querier == nil {
		writeJSON(w, map[string]any{})
		return
	}

	switch subResource {
	case "metrics":
		metrics, err := s.querier.GetServiceMetrics(r.Context(), serviceName, 15*time.Minute)
		if err != nil {
			s.log.Error(err, "Failed to get service metrics", "service", serviceName)
			http.Error(w, "failed to get metrics", http.StatusInternalServerError)
			return
		}
		if metrics == nil {
			metrics = &telemetry.ServiceMetrics{ServiceName: serviceName}
		}
		writeJSON(w, metrics)

	case "traces":
		end := time.Now()
		start := end.Add(-15 * time.Minute)
		limit := parsePositiveInt(r.URL.Query().Get("limit"), 20)
		traces, err := s.querier.FindTracesByService(r.Context(), serviceName, start, end, limit)
		if err != nil {
			s.log.Error(err, "Failed to get traces", "service", serviceName)
			http.Error(w, "failed to get traces", http.StatusInternalServerError)
			return
		}
		if traces == nil {
			traces = []telemetry.TraceSummary{}
		}
		writeJSON(w, traces)

	case "logs":
		end := time.Now()
		start := end.Add(-15 * time.Minute)
		limit := parsePositiveInt(r.URL.Query().Get("limit"), 100)
		severity := r.URL.Query().Get("severity")
		logs, err := s.querier.SearchLogs(r.Context(), telemetry.LogFilter{
			ServiceName: serviceName,
			Severity:    severity,
			Start:       start,
			End:         end,
			Limit:       limit,
		})
		if err != nil {
			s.log.Error(err, "Failed to get logs", "service", serviceName)
			http.Error(w, "failed to get logs", http.StatusInternalServerError)
			return
		}
		if logs == nil {
			logs = []telemetry.LogEntry{}
		}
		writeJSON(w, logs)

	default:
		// Return service overview from topology
		if s.topoCache != nil {
			graph, err := s.topoCache.Get(r.Context())
			if err == nil {
				if node, ok := graph.Nodes[serviceName]; ok {
					writeJSON(w, node)
					return
				}
			}
		}
		http.Error(w, "service not found", http.StatusNotFound)
	}
}
