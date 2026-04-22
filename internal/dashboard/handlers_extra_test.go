package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/correlator"
	"github.com/gaurangkudale/rca-operator/internal/correlator/graph"
	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

func newTestServer(t *testing.T, objs ...runtime.Object) *Server {
	t.Helper()
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = appsv1.AddToScheme(scheme)
	_ = rcav1alpha1.AddToScheme(scheme)

	cb := fake.NewClientBuilder().WithScheme(scheme)
	for _, o := range objs {
		cb = cb.WithRuntimeObjects(o)
	}
	return &Server{client: cb.Build(), cache: newJSONCache(defaultCacheTTL)}
}

func ptrInt32(v int32) *int32 { return &v }

func TestHandleTopology_EmptyCluster(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.handleTopology(rr, httptest.NewRequest(http.MethodGet, "/api/topology", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got graph.ClusterGraph
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Nodes) != 0 {
		t.Fatalf("expected empty graph, got %d nodes", len(got.Nodes))
	}
}

func TestHandleTopology_WithDeploymentAndPod(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       appsv1.DeploymentSpec{Replicas: ptrInt32(2)},
		Status:     appsv1.DeploymentStatus{Replicas: 2, ReadyReplicas: 2},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-abc", Namespace: "prod"},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-1"},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	}

	s := newTestServer(t, dep, pod, node)
	rr := httptest.NewRecorder()
	s.handleTopology(rr, httptest.NewRequest(http.MethodGet, "/api/topology?view=detail", nil))

	var got graph.ClusterGraph
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Nodes) < 3 {
		t.Fatalf("expected >=3 nodes, got %d", len(got.Nodes))
	}
	found := false
	for _, e := range got.Edges {
		if e.Kind == graph.EdgeKindScheduledOn {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected scheduled_on edge")
	}
}

func TestHandleAgents(t *testing.T) {
	ag := &rcav1alpha1.RCAAgent{
		ObjectMeta: metav1.ObjectMeta{Name: "agent-1"},
		Spec:       rcav1alpha1.RCAAgentSpec{WatchNamespaces: []string{"prod", "staging"}},
	}
	s := newTestServer(t, ag)
	rr := httptest.NewRecorder()
	s.handleAgents(rr, httptest.NewRequest(http.MethodGet, "/api/agents", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var out []flatAgent
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(out) != 1 || out[0].Name != "agent-1" {
		t.Fatalf("unexpected agents: %+v", out)
	}
}

func TestHandleResource_Deployment(t *testing.T) {
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod", CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Hour))},
		Spec:       appsv1.DeploymentSpec{Replicas: ptrInt32(3)},
		Status:     appsv1.DeploymentStatus{Replicas: 3, ReadyReplicas: 2},
	}
	s := newTestServer(t, dep)
	s.k8s = k8sfake.NewClientset()

	rr := httptest.NewRecorder()
	s.handleResource(rr, httptest.NewRequest(http.MethodGet, "/api/resources/prod/Deployment/api", nil))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var got resourceResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.DesiredReplicas != 3 || got.ReadyReplicas != 2 {
		t.Fatalf("replicas: %+v", got)
	}
}

func TestHandleResource_BadPath(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.handleResource(rr, httptest.NewRequest(http.MethodGet, "/api/resources/only-two/parts", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestIncidentReferences_ServiceMatchesWorkloadResource(t *testing.T) {
	inc := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod"},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase: "Active",
			AffectedResources: []rcav1alpha1.AffectedResource{
				{Kind: "Deployment", Namespace: "prod", Name: "cart"},
			},
		},
	}

	if !incidentReferences(inc, "Service", "prod", "cart") {
		t.Fatalf("expected Service/cart in prod to match workload-scoped incident")
	}
}

func TestHandleLogs_MissingFilters(t *testing.T) {
	s := newTestServer(t)
	s.k8s = k8sfake.NewClientset()
	rr := httptest.NewRecorder()
	s.handleLogs(rr, httptest.NewRequest(http.MethodGet, "/api/logs?ns=prod", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestHandleStream_NoBuffer(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.handleStream(rr, httptest.NewRequest(http.MethodGet, "/api/stream", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestHandleTopology_ETag304(t *testing.T) {
	s := newTestServer(t)

	rr1 := httptest.NewRecorder()
	s.handleTopology(rr1, httptest.NewRequest(http.MethodGet, "/api/topology", nil))
	if rr1.Code != http.StatusOK {
		t.Fatalf("first status = %d", rr1.Code)
	}
	etag := rr1.Header().Get("ETag")
	if etag == "" {
		t.Fatalf("expected ETag header on first response")
	}

	rr2 := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/topology", nil)
	req.Header.Set("If-None-Match", etag)
	s.handleTopology(rr2, req)
	if rr2.Code != http.StatusNotModified {
		t.Fatalf("second status = %d, want 304", rr2.Code)
	}
	if rr2.Body.Len() != 0 {
		t.Fatalf("304 response must have empty body, got %d bytes", rr2.Body.Len())
	}
}

func TestHandleStream_EmitsSnapshot(t *testing.T) {
	s := newTestServer(t)
	buf := correlator.NewBuffer(time.Minute)
	buf.Add(watcher.OTelLogMatchEvent{ServiceName: "svc", Severity: "ERROR", Body: "boom"})
	s.buffer = buf

	rr := httptest.NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/stream", nil).WithContext(ctx)

	s.handleStream(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "event: correlation") || !strings.Contains(body, `"kind":"log"`) {
		t.Fatalf("missing SSE payload: %s", body)
	}
}
