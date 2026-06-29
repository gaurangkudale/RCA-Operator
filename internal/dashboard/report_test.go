package dashboard

import (
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

// richIncident augments makeIncident with resources, signals, timeline, and
// trace/rule annotations so report rendering can be exercised end to end.
func richIncident(name, ns string, now time.Time) *rcav1alpha1.IncidentReport {
	inc := makeIncident(name, ns, "P1", "Active", "CrashLoopBackOff", now.Add(-2*time.Hour))
	inc.Status.CorrelatedSignals = []string{
		"CrashLoopBackOff pod=web-1",
		"CrashLoopBackOff pod=web-1",
		"OOMKilled message=",
	}
	inc.Status.AffectedResources = []rcav1alpha1.AffectedResource{
		{Kind: "Pod", Name: "web-1", Namespace: ns},
	}
	inc.Status.Timeline = []rcav1alpha1.TimelineEvent{
		{Time: metav1.Time{Time: now.Add(-2 * time.Hour)}, Event: "Pod entered CrashLoopBackOff"},
	}
	inc.Annotations = map[string]string{
		reporter.AnnotationFiredRule: "crashloop-plus-oom",
		reporter.AnnotationTraceID:   "abc123def456",
	}
	return inc
}

func TestIncidentReport_RendersHTML(t *testing.T) {
	now := time.Now()
	s := newTestServer(t, richIncident("web-crash", "prod", now))

	rr := httptest.NewRecorder()
	s.handleIncidentDetail(rr, httptest.NewRequest(http.MethodGet, "/api/incidents/prod/web-crash/report", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}

	body := rr.Body.String()
	for _, want := range []string{
		"web-crash", "summary web-crash", "P1", "Critical", "CrashLoopBackOff",
		"crashloop-plus-oom", "abc123def456", "web-1",
		"window.print()", "Save as PDF", "Download HTML",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("report missing %q", want)
		}
	}
	// The two identical signals collapse into a single grouped entry with count.
	if !strings.Contains(body, "x2") {
		t.Errorf("expected grouped signal count x2 in report")
	}
	// The empty "message=" fragment must be cleaned away.
	if strings.Contains(body, "message=") {
		t.Errorf("empty key= fragment was not cleaned from report")
	}
}

func TestIncidentReport_NotFound(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.handleIncidentDetail(rr, httptest.NewRequest(http.MethodGet, "/api/incidents/prod/missing/report", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestIncidentReport_RejectsNonGet(t *testing.T) {
	s := newTestServer(t, makeIncident("a", "prod", "P1", "Active", "CrashLoopBackOff", time.Now()))
	rr := httptest.NewRecorder()
	s.handleIncidentDetail(rr, httptest.NewRequest(http.MethodPost, "/api/incidents/prod/a/report", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestIncidentReport_DownloadDisposition(t *testing.T) {
	s := newTestServer(t, makeIncident("a", "prod", "P1", "Active", "CrashLoopBackOff", time.Now()))
	rr := httptest.NewRecorder()
	s.handleIncidentDetail(rr, httptest.NewRequest(http.MethodGet, "/api/incidents/prod/a/report?download=1", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	cd := rr.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, ".html") {
		t.Errorf("content-disposition = %q, want attachment ...html", cd)
	}
}

func TestIncidentReport_EscapesHTML(t *testing.T) {
	inc := makeIncident("x", "prod", "P1", "Active", "CrashLoopBackOff", time.Now())
	inc.Status.Summary = "<script>alert(1)</script>"
	s := newTestServer(t, inc)

	rr := httptest.NewRecorder()
	s.handleIncidentDetail(rr, httptest.NewRequest(http.MethodGet, "/api/incidents/prod/x/report", nil))

	body := rr.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Errorf("incident-supplied summary was not escaped")
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Errorf("expected escaped summary in report")
	}
}

func TestClusterReport_RendersHTML(t *testing.T) {
	now := time.Now()
	objs := []runtime.Object{
		makeIncident("a", "prod", "P1", "Active", "NodeNotReady", now.Add(-3*time.Hour)),
		makeIncident("b", "prod", "P2", "Detecting", "CrashLoopBackOff", now.Add(-2*time.Hour)),
		makeIncident("c", "stage", "P3", "Resolved", "ImagePullBackOff", now.Add(-time.Hour)),
	}
	s := newTestServer(t, objs...)

	rr := httptest.NewRecorder()
	s.handleClusterReport(rr, httptest.NewRequest(http.MethodGet, "/api/report", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q, want text/html", ct)
	}

	body := rr.Body.String()
	for _, want := range []string{
		"Cluster Report", "Active", "Detecting", "Resolved",
		"NodeNotReady", "CrashLoopBackOff", "ImagePullBackOff", "window.print()",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("cluster report missing %q", want)
		}
	}
}

func TestClusterReport_RejectsNonGet(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.handleClusterReport(rr, httptest.NewRequest(http.MethodPost, "/api/report", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// TestReportRoutesWiredInMux confirms both report endpoints are registered and
// routed (cluster at /api/report, per-incident as a /report sub-resource).
func TestReportRoutesWiredInMux(t *testing.T) {
	s := newTestServer(t, makeIncident("a", "prod", "P1", "Active", "CrashLoopBackOff", time.Now()))
	mux, err := s.newMux()
	if err != nil {
		t.Fatalf("newMux: %v", err)
	}
	for _, path := range []string{"/api/report", "/api/incidents/prod/a/report"} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Errorf("%s: status = %d, body = %s", path, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "window.print()") {
			t.Errorf("%s: response is not a report page", path)
		}
	}
}

func TestCleanSignalGo(t *testing.T) {
	cases := map[string]string{
		"CrashLoopBackOff message=": "CrashLoopBackOff",
		"  spaced   out   ":         "spaced out",
		"key=value stays":           "key=value stays",
		"a=b= kept":                 "a=b= kept",
		"reason= detail=":           "",
	}
	for in, want := range cases {
		if got := cleanSignalGo(in); got != want {
			t.Errorf("cleanSignalGo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGroupSignals(t *testing.T) {
	groups, unique, total := groupSignals([]string{"a", "a", "b", "noise message="})
	if unique != 3 || total != 4 {
		t.Fatalf("unique=%d total=%d, want 3/4", unique, total)
	}
	// Most frequent first.
	if groups[0].Text != "a" || groups[0].Count != 2 {
		t.Errorf("groups[0] = %+v, want {a 2}", groups[0])
	}
}
