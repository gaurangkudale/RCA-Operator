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
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/correlator"
	"github.com/gaurangkudale/rca-operator/internal/correlator/graph"
	"github.com/gaurangkudale/rca-operator/internal/incident"
	"github.com/gaurangkudale/rca-operator/internal/incidentstatus"
	"github.com/gaurangkudale/rca-operator/internal/jaeger"
	"github.com/gaurangkudale/rca-operator/internal/reporter"
)

// Server serves the incident dashboard UI and its REST API.
// It implements manager.Runnable so it can be registered with mgr.Add().
type Server struct {
	client client.Client
	addr   string
	log    logr.Logger
	k8s    kubernetes.Interface
	buffer *correlator.Buffer
	jc     *jaeger.Client

	// cache is a short-TTL JSON response cache + singleflight. It collapses
	// bursts of dashboard polling onto a single k8s List per key and lets the
	// UI skip re-renders via ETag/If-None-Match.
	cache *jsonCache
}

// defaultCacheTTL is short enough to stay responsive under churn but long
// enough to collapse the typical dashboard poll pattern (multiple tabs +
// auto-refresh) onto one backend call.
const defaultCacheTTL = 3 * time.Second

// NewServer returns a dashboard server that will listen on addr.
func NewServer(c client.Client, addr string, logger logr.Logger) *Server {
	return &Server{
		client: c,
		addr:   addr,
		log:    logger.WithName("dashboard"),
		cache:  newJSONCache(defaultCacheTTL),
	}
}

// Start implements manager.Runnable. It blocks until ctx is cancelled.
func (s *Server) Start(ctx context.Context) error {
	mux, err := s.newMux()
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              s.addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		// WriteTimeout is intentionally 0: /api/stream is a long-lived SSE
		// connection. Per-request timeouts are enforced via request context.
		WriteTimeout: 0,
		IdleTimeout:  60 * time.Second,
		BaseContext:  func(_ net.Listener) context.Context { return ctx },
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

func (s *Server) newMux() (*http.ServeMux, error) {
	mux := http.NewServeMux()

	// Serve embedded static files at / and /static/. The /static/ mount keeps
	// asset URLs explicit while the root mount preserves direct index serving.
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("dashboard: embed sub failed: %w", err)
	}
	staticHandler := http.FileServer(http.FS(sub))
	mux.Handle("/static/", http.StripPrefix("/static/", staticHandler))
	mux.Handle("/", staticHandler)

	// API endpoints
	mux.HandleFunc("/api/incidents", s.handleIncidents)
	mux.HandleFunc("/api/incidents/", s.handleIncidentDetail)
	mux.HandleFunc("/api/rules", s.handleRules)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/timeline", s.handleTimeline)
	mux.HandleFunc("/api/topology", s.handleTopology)
	mux.HandleFunc("/api/service-graph", s.handleServiceGraph)
	mux.HandleFunc("/api/resources/", s.handleResource)
	mux.HandleFunc("/api/logs", s.handleLogs)
	mux.HandleFunc("/api/agents", s.handleAgents)
	mux.HandleFunc("/api/pods", s.handlePods)
	mux.HandleFunc("/api/stream", s.handleStream)
	mux.HandleFunc("/api/traces/", s.handleTrace)

	return mux, nil
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
	TraceID           string                         `json:"traceId,omitempty"`
	TraceIDs          []string                       `json:"traceIds,omitempty"`
	FiredRule         string                         `json:"firedRule,omitempty"`
	HasTopology       bool                           `json:"hasTopology"`
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
	TraceID   string   `json:"traceId"`
	TraceIDs  []string `json:"traceIds,omitempty"`
	FiredRule string   `json:"firedRule"`
}

// ── Handlers ──────────────────────────────────────────────────────────────────

func (s *Server) handleIncidents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	nsFilter := q.Get("namespace")
	phaseFilter := q.Get("phase")
	sevFilter := q.Get("severity")
	typeFilter := q.Get("type")
	query := strings.ToLower(strings.TrimSpace(q.Get("query")))
	limit := parsePositiveInt(q.Get("limit"), 500)
	offset := parsePositiveInt(q.Get("offset"), 0)
	sortBy := q.Get("sort")
	if sortBy == "" {
		sortBy = "newest"
	}

	key := "incidents|" + nsFilter + "|" + phaseFilter + "|" + sevFilter + "|" + typeFilter +
		"|" + query + "|" + sortBy + "|l=" + strconv.Itoa(limit) + "|o=" + strconv.Itoa(offset)

	body, etag, err := s.cache.Fetch(key, func() (any, error) {
		list := &rcav1alpha1.IncidentReportList{}
		opts := []client.ListOption{}
		if nsFilter != "" {
			opts = append(opts, client.InNamespace(nsFilter))
		}
		if err := s.client.List(r.Context(), list, opts...); err != nil {
			return nil, err
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
		result = collapseDuplicateOTelIncidents(result)
		if offset > len(result) {
			offset = len(result)
		}
		end := len(result)
		if limit > 0 && offset+limit < end {
			end = offset + limit
		}
		// Always return a non-nil slice so JSON renders as `[]` not `null`.
		sliced := result[offset:end]
		if sliced == nil {
			sliced = []incidentResponse{}
		}
		return sliced, nil
	})
	if err != nil {
		s.log.Error(err, "Failed to list IncidentReports")
		http.Error(w, "failed to list incidents", http.StatusInternalServerError)
		return
	}
	writeCachedJSON(w, r, body, etag)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, etag, err := s.cache.Fetch("stats", func() (any, error) {
		return s.computeStats(r.Context())
	})
	if err != nil {
		s.log.Error(err, "Failed to compute stats")
		http.Error(w, "failed to list incidents", http.StatusInternalServerError)
		return
	}
	writeCachedJSON(w, r, body, etag)
}

// computeStats builds the stats payload. Extracted so handleStats can cache it.
func (s *Server) computeStats(ctx context.Context) (statsResponse, error) {
	list := &rcav1alpha1.IncidentReportList{}
	if err := s.client.List(ctx, list); err != nil {
		return statsResponse{}, err
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
	if err := s.client.List(ctx, agentList); err != nil {
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
	if resp.Agents == nil {
		resp.Agents = []agentInfo{}
	}

	return resp, nil
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, etag, err := s.cache.Fetch("rules", func() (any, error) {
		return s.computeRules(r.Context())
	})
	if err != nil {
		s.log.Error(err, "Failed to list RCACorrelationRules")
		http.Error(w, "failed to list rules", http.StatusInternalServerError)
		return
	}
	writeCachedJSON(w, r, body, etag)
}

func (s *Server) computeRules(ctx context.Context) ([]ruleResponse, error) {
	list := &rcav1alpha1.RCACorrelationRuleList{}
	if err := s.client.List(ctx, list); err != nil {
		return nil, err
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

	return result, nil
}

func (s *Server) handleIncidentDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Supported shapes:
	//   /api/incidents/{namespace}/{name}        → detail JSON
	//   /api/incidents/{namespace}/{name}/graph  → raw IncidentGraph JSON
	path := strings.TrimPrefix(r.URL.Path, "/api/incidents/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "path must be /api/incidents/{namespace}/{name}[/graph]", http.StatusBadRequest)
		return
	}
	namespace, name := parts[0], parts[1]

	item := &rcav1alpha1.IncidentReport{}
	if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: namespace, Name: name}, item); err != nil {
		s.log.Error(err, "Failed to get IncidentReport", "namespace", namespace, "name", name)
		http.Error(w, "incident not found", http.StatusNotFound)
		return
	}

	if len(parts) >= 3 && parts[2] == "graph" {
		s.writeIncidentGraph(w, item)
		return
	}
	if len(parts) > 2 {
		http.Error(w, "unknown sub-resource; expected /graph", http.StatusNotFound)
		return
	}

	base := toIncidentResponse(item)
	detail := incidentDetailResponse{
		incidentResponse: base,
	}
	if item.Annotations != nil {
		detail.TraceID = item.Annotations["rca.rca-operator.tech/trace-id"]
		detail.TraceIDs = collectTraceIDs(item.Annotations)
		if detail.TraceID == "" && len(detail.TraceIDs) > 0 {
			detail.TraceID = detail.TraceIDs[len(detail.TraceIDs)-1]
		}
		detail.FiredRule = item.Annotations["rca.rca-operator.tech/fired-rule"]
	}

	writeJSON(w, detail)
}

func collectTraceIDs(annotations map[string]string) []string {
	if len(annotations) == 0 {
		return nil
	}

	list := parseTraceIDCSV(annotations[reporter.AnnotationTraceIDs])
	if single := strings.TrimSpace(annotations[reporter.AnnotationTraceID]); single != "" {
		seen := make(map[string]struct{}, len(list)+1)
		for _, id := range list {
			seen[id] = struct{}{}
		}
		if _, ok := seen[single]; !ok {
			list = append(list, single)
		}
	}
	return list
}

func parseTraceIDCSV(in string) []string {
	if strings.TrimSpace(in) == "" {
		return nil
	}
	parts := strings.FieldsFunc(in, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		id := strings.TrimSpace(p)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

// writeIncidentGraph streams the opaque IncidentGraph payload stored on
// status.incidentGraph. Returns 204 when the graph has not been built or was
// pruned by the retention subsystem so the UI can render an empty-state
// message without treating this as an error.
func (s *Server) writeIncidentGraph(w http.ResponseWriter, item *rcav1alpha1.IncidentReport) {
	raw := item.Status.IncidentGraph
	if raw == nil || len(raw.Raw) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(raw.Raw); err != nil {
		s.log.V(1).Info("failed to write incident graph", "error", err.Error())
	}
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
	traceIDs := collectTraceIDs(item.Annotations)
	traceID := strings.TrimSpace(item.Annotations[reporter.AnnotationTraceID])
	if traceID == "" && len(traceIDs) > 0 {
		traceID = traceIDs[len(traceIDs)-1]
	}
	firedRule := strings.TrimSpace(item.Annotations[reporter.AnnotationFiredRule])

	resp := incidentResponse{
		Name:              item.Name,
		Namespace:         item.Namespace,
		Fingerprint:       reporter.ReportFingerprint(item),
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
		TraceID:           traceID,
		TraceIDs:          traceIDs,
		FiredRule:         firedRule,
		HasTopology:       hasRenderableIncidentTopology(item),
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

func collapseDuplicateOTelIncidents(in []incidentResponse) []incidentResponse {
	if len(in) <= 1 {
		return in
	}

	out := make([]incidentResponse, 0, len(in))
	indexByKey := make(map[string]int, len(in))
	for _, item := range in {
		key := duplicateOTelIncidentKey(item)
		if key == "" {
			out = append(out, item)
			continue
		}
		if idx, ok := indexByKey[key]; ok {
			if preferIncidentListItem(item, out[idx]) {
				out[idx] = item
			}
			continue
		}
		indexByKey[key] = len(out)
		out = append(out, item)
	}
	return out
}

func duplicateOTelIncidentKey(item incidentResponse) string {
	if !incident.IsOTelIncidentType(item.IncidentType) {
		return ""
	}
	if key := incidentScopeKey(item); key != "" {
		return key
	}
	if item.Fingerprint != "" {
		return item.Namespace + "/fingerprint/" + item.Fingerprint
	}
	return ""
}

func incidentScopeKey(item incidentResponse) string {
	ref := item.Scope.WorkloadRef
	if ref == nil || ref.Name == "" {
		ref = item.Scope.ResourceRef
	}
	if ref != nil && ref.Name != "" {
		ns := ref.Namespace
		if ns == "" {
			ns = item.Scope.Namespace
		}
		if ns == "" {
			ns = item.Namespace
		}
		return strings.Join([]string{ns, "otel-target", normalizeOTelTargetName(ref.Kind, ref.Name)}, "/")
	}

	for _, res := range item.AffectedResources {
		if res.Name == "" {
			continue
		}
		switch res.Kind {
		case kindDeployment, "StatefulSet", "DaemonSet", kindReplicaSet, "Job", "CronJob", kindService, kindPod:
			ns := res.Namespace
			if ns == "" {
				ns = item.Namespace
			}
			return strings.Join([]string{ns, "otel-target", normalizeOTelTargetName(res.Kind, res.Name)}, "/")
		}
	}
	return ""
}

func normalizeOTelTargetName(kind, name string) string {
	if kind != kindPod {
		return name
	}
	parts := strings.Split(name, "-")
	if len(parts) < 3 {
		return name
	}
	replicaSetHash := parts[len(parts)-2]
	podSuffix := parts[len(parts)-1]
	if !looksLikeKubernetesPodSuffix(podSuffix) || !looksLikeReplicaSetHash(replicaSetHash) {
		return name
	}
	return strings.Join(parts[:len(parts)-2], "-")
}

func looksLikeKubernetesPodSuffix(value string) bool {
	return len(value) == 5 && isLowerAlnum(value)
}

func looksLikeReplicaSetHash(value string) bool {
	return len(value) >= 8 && len(value) <= 10 && isLowerAlnum(value)
}

func isLowerAlnum(value string) bool {
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			continue
		}
		return false
	}
	return value != ""
}

func preferIncidentListItem(candidate, current incidentResponse) bool {
	candidateOpen := candidate.Phase != reporter.PhaseResolved
	currentOpen := current.Phase != reporter.PhaseResolved
	if candidateOpen != currentOpen {
		return candidateOpen
	}
	candidateSeverity := severityRank(candidate.Severity)
	currentSeverity := severityRank(current.Severity)
	if candidateSeverity != currentSeverity {
		return candidateSeverity > currentSeverity
	}
	return incidentResponseLastTime(candidate).After(incidentResponseLastTime(current))
}

func incidentResponseLastTime(item incidentResponse) time.Time {
	for _, t := range []*time.Time{item.LastObservedAt, item.ActiveAt, item.FirstObservedAt, item.ResolvedAt} {
		if t != nil {
			return *t
		}
	}
	return time.Time{}
}

func hasRenderableIncidentTopology(item *rcav1alpha1.IncidentReport) bool {
	if item == nil || item.Status.IncidentGraph == nil || len(item.Status.IncidentGraph.Raw) == 0 {
		return false
	}

	var g graph.IncidentGraph
	if err := json.Unmarshal(item.Status.IncidentGraph.Raw, &g); err != nil {
		return false
	}

	services := make(map[string]struct{}, len(g.Nodes))
	for _, n := range g.Nodes {
		if n.Kind == graph.NodeKindService && n.ID != "" {
			services[n.ID] = struct{}{}
		}
	}
	if len(services) < 2 {
		return false
	}

	for _, e := range g.Edges {
		if e.Kind != graph.EdgeKindCalls || e.From == e.To {
			continue
		}
		if _, ok := services[e.From]; !ok {
			continue
		}
		if _, ok := services[e.To]; !ok {
			continue
		}
		return true
	}
	return false
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
