package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/reporter"
)

// makeIncident builds a minimal IncidentReport for handler-level tests.
func makeIncident(name, ns, severity, phase, incidentType string, firstSeen time.Time) *rcav1alpha1.IncidentReport {
	return &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Labels: map[string]string{
				reporter.LabelSeverity:     severity,
				reporter.LabelIncidentType: incidentType,
			},
		},
		Spec: rcav1alpha1.IncidentReportSpec{
			AgentRef:     "agent",
			IncidentType: incidentType,
		},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:           phase,
			Severity:        severity,
			IncidentType:    incidentType,
			Summary:         "summary " + name,
			FirstObservedAt: &metav1.Time{Time: firstSeen},
		},
	}
}

func rcaWorkloadScope(ns, name string) rcav1alpha1.IncidentScope {
	ref := &rcav1alpha1.IncidentObjectRef{
		APIVersion: "apps/v1",
		Kind:       "Deployment",
		Namespace:  ns,
		Name:       name,
	}
	return rcav1alpha1.IncidentScope{
		Level:       "Workload",
		Namespace:   ns,
		ResourceRef: ref,
		WorkloadRef: ref,
	}
}

// --- handleIncidents -------------------------------------------------------

func TestHandleIncidents_RejectsNonGet(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.handleIncidents(rr, httptest.NewRequest(http.MethodPost, "/api/incidents", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", rr.Code)
	}
}

func TestDashboardStaticAssetsServedUnderStaticPrefix(t *testing.T) {
	s := newTestServer(t)
	mux, err := s.newMux()
	if err != nil {
		t.Fatalf("newMux: %v", err)
	}

	for _, path := range []string{"/static/dashboard.css", "/static/lucide-local.js"} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body=%s", path, rr.Code, rr.Body.String())
		}
		if rr.Body.Len() == 0 {
			t.Fatalf("%s returned empty body", path)
		}
	}
}

func TestHandleIncidents_FiltersByPhaseSeverityAndQuery(t *testing.T) {
	now := time.Now()
	objs := []runtime.Object{
		makeIncident("a", "prod", "P1", "Active", "NodeNotReady", now.Add(-3*time.Hour)),
		makeIncident("b", "prod", "P2", "Detecting", "CrashLoopBackOff", now.Add(-2*time.Hour)),
		makeIncident("c", "stage", "P3", "Resolved", "ImagePullBackOff", now.Add(-time.Hour)),
	}
	s := newTestServer(t, objs...)

	count := func(t *testing.T, q string) int {
		t.Helper()
		rr := httptest.NewRecorder()
		s.handleIncidents(rr, httptest.NewRequest(http.MethodGet, "/api/incidents?"+q, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("query=%q: status=%d body=%s", q, rr.Code, rr.Body.String())
		}
		var items []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
			t.Fatalf("query=%q: unmarshal: %v body=%s", q, err, rr.Body.String())
		}
		return len(items)
	}

	if got := count(t, "severity=P1"); got != 1 {
		t.Errorf("severity=P1: got %d, want 1", got)
	}
	if got := count(t, "phase=Resolved"); got != 1 {
		t.Errorf("phase=Resolved: got %d, want 1", got)
	}
	if got := count(t, "namespace=prod"); got != 2 {
		t.Errorf("namespace=prod: got %d, want 2", got)
	}
	if got := count(t, "query=summary%20a"); got != 1 {
		t.Errorf("query=summary a: got %d, want 1", got)
	}
}

func TestHandleIncidents_LimitOffsetPaginates(t *testing.T) {
	now := time.Now()
	objs := []runtime.Object{
		makeIncident("a", "prod", "P1", "Active", "T", now.Add(-3*time.Hour)),
		makeIncident("b", "prod", "P2", "Active", "T", now.Add(-2*time.Hour)),
		makeIncident("c", "prod", "P3", "Active", "T", now.Add(-1*time.Hour)),
	}
	s := newTestServer(t, objs...)
	rr := httptest.NewRecorder()
	s.handleIncidents(rr, httptest.NewRequest(http.MethodGet, "/api/incidents?limit=2&offset=1", nil))
	var items []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 2 {
		t.Errorf("returned items = %d, want 2 (limit applied)", len(items))
	}
}

// --- handleStats -----------------------------------------------------------

func TestHandleStats_AggregatesByPhase(t *testing.T) {
	now := time.Now()
	objs := []runtime.Object{
		makeIncident("a", "prod", "P1", "Active", "T1", now),
		makeIncident("b", "prod", "P2", "Active", "T2", now),
		makeIncident("c", "stage", "P3", "Detecting", "T3", now),
		makeIncident("d", "stage", "P4", "Resolved", "T4", now),
	}
	s := newTestServer(t, objs...)
	rr := httptest.NewRecorder()
	s.handleStats(rr, httptest.NewRequest(http.MethodGet, "/api/stats", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Smoke check: response is a non-empty JSON object.
	if len(got) == 0 {
		t.Errorf("stats response empty: %s", rr.Body.String())
	}
}

// --- handleIncidentDetail --------------------------------------------------

func TestHandleIncidentDetail_ReturnsDetail(t *testing.T) {
	now := time.Now()
	inc := makeIncident("foo", "prod", "P2", "Active", "CrashLoopBackOff", now)
	inc.Annotations = map[string]string{
		"rca.rca-operator.tech/trace-id":   "trace1",
		"rca.rca-operator.tech/trace-ids":  "trace0,trace1",
		"rca.rca-operator.tech/fired-rule": "auto-rule",
	}
	s := newTestServer(t, inc)
	rr := httptest.NewRecorder()
	s.handleIncidentDetail(rr, httptest.NewRequest(http.MethodGet, "/api/incidents/prod/foo", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["traceId"] != "trace1" {
		t.Errorf("traceId = %v", got["traceId"])
	}
	if got["firedRule"] != "auto-rule" {
		t.Errorf("firedRule = %v", got["firedRule"])
	}
}

func TestHandleIncidents_HasTopologyOnlyForServiceCallGraph(t *testing.T) {
	now := time.Now()
	withCalls := makeIncident("with-calls", "prod", "P3", "Active", "OTelLogMatch", now)
	withCalls.Status.IncidentGraph = &runtime.RawExtension{Raw: []byte(`{
		"nodes":[
			{"id":"svc:frontend","kind":"Service","name":"frontend"},
			{"id":"svc:checkout","kind":"Service","name":"checkout"}
		],
		"edges":[{"from":"svc:frontend","to":"svc:checkout","kind":"calls"}]
	}`)}

	withoutCalls := makeIncident("without-calls", "prod", "P3", "Active", "OTelLogMatch", now)
	withoutCalls.Status.IncidentGraph = &runtime.RawExtension{Raw: []byte(`{
		"nodes":[{"id":"svc:frontend","kind":"Service","name":"frontend"}],
		"edges":[]
	}`)}

	s := newTestServer(t, withCalls, withoutCalls)
	rr := httptest.NewRecorder()
	s.handleIncidents(rr, httptest.NewRequest(http.MethodGet, "/api/incidents", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var items []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := map[string]bool{}
	for _, item := range items {
		got[item["name"].(string)] = item["hasTopology"].(bool)
	}
	if !got["with-calls"] {
		t.Fatalf("with-calls hasTopology=false, want true")
	}
	if got["without-calls"] {
		t.Fatalf("without-calls hasTopology=true, want false")
	}
}

func TestHandleIncidents_CollapsesDuplicateOTelFingerprints(t *testing.T) {
	now := time.Now()
	span := makeIncident("span", "prod", "P2", "Resolved", "OTelSpanError", now.Add(-time.Minute))
	span.Spec.Fingerprint = "Workload|prod|deployment|frontend|type|OTelSpanError"
	span.Spec.Scope = rcaWorkloadScope("prod", "frontend")
	logMatch := makeIncident("log", "prod", "P3", "Resolved", "OTelLogMatch", now)
	logMatch.Spec.Fingerprint = "Workload|prod|deployment|frontend|type|OTelLogMatch"
	logMatch.Spec.Scope = rcaWorkloadScope("prod", "frontend")

	s := newTestServer(t, span, logMatch)
	rr := httptest.NewRecorder()
	s.handleIncidents(rr, httptest.NewRequest(http.MethodGet, "/api/incidents", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var items []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 collapsed OTel incident: %s", len(items), rr.Body.String())
	}
	if got := items[0]["fingerprint"]; got != "Workload|prod|deployment|frontend" {
		t.Fatalf("fingerprint = %v", got)
	}
}

func TestHandleIncidentDetail_404OnMissingIncident(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.handleIncidentDetail(rr, httptest.NewRequest(http.MethodGet, "/api/incidents/nope/nope", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d, want 404", rr.Code)
	}
}

func TestHandleIncidentDetail_BadPath(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.handleIncidentDetail(rr, httptest.NewRequest(http.MethodGet, "/api/incidents/onlyone", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d, want 400", rr.Code)
	}
}

func TestHandleIncidentDetail_GraphSubresource(t *testing.T) {
	now := time.Now()
	inc := makeIncident("foo", "prod", "P2", "Active", "CrashLoopBackOff", now)
	inc.Status.IncidentGraph = &runtime.RawExtension{Raw: []byte(`{"nodes":[]}`)}
	s := newTestServer(t, inc)
	rr := httptest.NewRecorder()
	s.handleIncidentDetail(rr, httptest.NewRequest(http.MethodGet, "/api/incidents/prod/foo/graph", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"nodes"`) {
		t.Errorf("graph body missing payload, got %q", rr.Body.String())
	}
}

func TestHandleIncidentDetail_RejectsNonGet(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.handleIncidentDetail(rr, httptest.NewRequest(http.MethodPut, "/api/incidents/prod/foo", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d, want 405", rr.Code)
	}
}

// --- handleRules -----------------------------------------------------------

func TestHandleRules_ListsCRDs(t *testing.T) {
	rule := &rcav1alpha1.RCACorrelationRule{
		ObjectMeta: metav1.ObjectMeta{Name: "node-plus-eviction"},
		Spec: rcav1alpha1.RCACorrelationRuleSpec{
			Priority:   100,
			Trigger:    rcav1alpha1.RuleTrigger{EventType: "NodeNotReady"},
			Conditions: []rcav1alpha1.RuleCondition{{EventType: "PodEvicted", Scope: "sameNode"}},
			Fires: rcav1alpha1.RuleFires{
				IncidentType: "NodeFailure",
				Severity:     "P1",
				Summary:      "node failure",
			},
		},
	}
	s := newTestServer(t, rule)
	rr := httptest.NewRecorder()
	s.handleRules(rr, httptest.NewRequest(http.MethodGet, "/api/rules", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "node-plus-eviction") {
		t.Errorf("rules response missing rule name; got %s", rr.Body.String())
	}
}

func TestHandleRules_RejectsNonGet(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.handleRules(rr, httptest.NewRequest(http.MethodPost, "/api/rules", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status=%d, want 405", rr.Code)
	}
}

// --- handleTimeline --------------------------------------------------------

func TestHandleTimeline_RequiresFingerprint(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.handleTimeline(rr, httptest.NewRequest(http.MethodGet, "/api/timeline", nil))
	if rr.Code == http.StatusOK {
		t.Errorf("missing fingerprint should fail; got 200")
	}
}

// --- writeIncidentGraph ---------------------------------------------------

func TestWriteIncidentGraph_ReturnsBodyWhenSet(t *testing.T) {
	raw := []byte(`{"nodes":[{"id":"x"}]}`)
	item := &rcav1alpha1.IncidentReport{
		Status: rcav1alpha1.IncidentReportStatus{
			IncidentGraph: &runtime.RawExtension{Raw: raw},
		},
	}
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.writeIncidentGraph(rr, item)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"nodes"`) {
		t.Errorf("body should contain graph payload, got %q", rr.Body.String())
	}
}

func TestWriteIncidentGraph_204WhenEmpty(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.writeIncidentGraph(rr, &rcav1alpha1.IncidentReport{})
	if rr.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204", rr.Code)
	}
}

// --- helpers --------------------------------------------------------------

func TestCollectTraceIDs_MergesSingleAndCSV(t *testing.T) {
	got := collectTraceIDs(map[string]string{
		reporter.AnnotationTraceIDs: "a,b,c",
		reporter.AnnotationTraceID:  "d",
	})
	want := []string{"a", "b", "c", "d"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("idx %d: got %q want %q", i, got[i], w)
		}
	}
}

func TestCollectTraceIDs_DedupsSingleAlreadyInList(t *testing.T) {
	got := collectTraceIDs(map[string]string{
		reporter.AnnotationTraceIDs: "a,b",
		reporter.AnnotationTraceID:  "b",
	})
	if len(got) != 2 {
		t.Errorf("expected dedup to 2 IDs, got %v", got)
	}
}

func TestCollectTraceIDs_NilOrEmpty(t *testing.T) {
	if got := collectTraceIDs(nil); got != nil {
		t.Errorf("nil annotations should return nil, got %v", got)
	}
	if got := collectTraceIDs(map[string]string{}); got != nil {
		t.Errorf("empty annotations should return nil, got %v", got)
	}
}

func TestParsePositiveInt(t *testing.T) {
	cases := map[string]int{
		"":    10,
		"abc": 10,
		"-1":  10,
		"0":   0,
		"42":  42,
	}
	for in, want := range cases {
		if got := parsePositiveInt(in, 10); got != want {
			t.Errorf("parsePositiveInt(%q,10) = %d, want %d", in, got, want)
		}
	}
}

func TestSeverityRank(t *testing.T) {
	if severityRank("P1") <= severityRank("P2") {
		t.Errorf("P1 should rank higher than P2")
	}
	if severityRank("P2") <= severityRank("P3") {
		t.Errorf("P2 should rank higher than P3")
	}
	if severityRank("Pwhatever") != severityRank("P4") && severityRank("Pwhatever") != 1 {
		t.Errorf("unknown severity should fall to bucket 1")
	}
}

func TestSortIncidentResponses_ByNewestOldestSeverity(t *testing.T) {
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	build := func() []incidentResponse {
		return []incidentResponse{
			{Name: "a", Severity: "P3", FirstObservedAt: &t1},
			{Name: "b", Severity: "P1", FirstObservedAt: &t2},
			{Name: "c", Severity: "P2", FirstObservedAt: &t3},
		}
	}

	t.Run("newest (default)", func(t *testing.T) {
		got := build()
		sortIncidentResponses(got, "")
		if got[0].Name != "c" || got[2].Name != "a" {
			t.Errorf("newest order wrong: %v %v %v", got[0].Name, got[1].Name, got[2].Name)
		}
	})
	t.Run("oldest", func(t *testing.T) {
		got := build()
		sortIncidentResponses(got, "oldest")
		if got[0].Name != "a" || got[2].Name != "c" {
			t.Errorf("oldest order wrong")
		}
	})
	t.Run("severity (highest first)", func(t *testing.T) {
		got := build()
		sortIncidentResponses(got, "severity")
		if got[0].Name != "b" {
			t.Errorf("P1 should be first by severity, got %q", got[0].Name)
		}
	})
}

func TestMatchesIncidentQuery_CaseInsensitiveAcrossFields(t *testing.T) {
	item := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: "incident-foo", Namespace: "prod"},
		Spec: rcav1alpha1.IncidentReportSpec{
			AgentRef:     "default-agent",
			IncidentType: "CrashLoopBackOff",
		},
		Status: rcav1alpha1.IncidentReportStatus{
			Summary: "Pod is failing",
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Pod", Name: "api-1", Namespace: "prod"},
			},
		},
	}
	cases := map[string]bool{
		"crash":          true,  // matches Spec.IncidentType
		"DEFAULT-AGENT":  false, // query is matched lowercased; the query itself must already be lowercase
		"default-agent":  true,
		"failing":        true,
		"api-1":          true,
		"never-match-xy": false,
	}
	for q, want := range cases {
		if got := matchesIncidentQuery(item, q); got != want {
			t.Errorf("matchesIncidentQuery(%q) = %v, want %v", q, got, want)
		}
	}
}
