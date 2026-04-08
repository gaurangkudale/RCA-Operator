package rca

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/telemetry"
)

// mockQuerier is a minimal TelemetryQuerier for testing the investigator.
type mockQuerier struct {
	metrics      []telemetry.MetricSeries
	logs         []telemetry.LogEntry
	trace        *telemetry.Trace
	svcMetrics   *telemetry.ServiceMetrics
	dependencies []telemetry.DependencyEdge
}

func (q *mockQuerier) FindTracesByService(_ context.Context, _ string, _, _ time.Time, _ int) ([]telemetry.TraceSummary, error) {
	return nil, nil
}
func (q *mockQuerier) GetTrace(_ context.Context, _ string) (*telemetry.Trace, error) {
	return q.trace, nil
}
func (q *mockQuerier) FindErrorTraces(_ context.Context, _ string, _ time.Duration) ([]telemetry.TraceSummary, error) {
	return nil, nil
}
func (q *mockQuerier) QueryMetric(_ context.Context, _ string, _, _ time.Time, _ time.Duration) ([]telemetry.MetricSeries, error) {
	return q.metrics, nil
}
func (q *mockQuerier) GetServiceMetrics(_ context.Context, _ string, _ time.Duration) (*telemetry.ServiceMetrics, error) {
	return q.svcMetrics, nil
}
func (q *mockQuerier) SearchLogs(_ context.Context, _ telemetry.LogFilter) ([]telemetry.LogEntry, error) {
	return q.logs, nil
}
func (q *mockQuerier) GetDependencies(_ context.Context, _ time.Duration) ([]telemetry.DependencyEdge, error) {
	return q.dependencies, nil
}
func (q *mockQuerier) CorrelateByTraceID(_ context.Context, _ string) (*telemetry.CorrelatedSignals, error) {
	return nil, nil
}

func TestParseRCAResponse_Valid(t *testing.T) {
	input := `{"rootCause":"Memory leak in payment-svc","confidence":"0.92","playbook":["kubectl rollout undo"],"evidence":["trace-abc"]}`
	result, err := parseRCAResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RootCause != "Memory leak in payment-svc" {
		t.Errorf("unexpected rootCause: %s", result.RootCause)
	}
	if result.Confidence != "0.92" {
		t.Errorf("unexpected confidence: %s", result.Confidence)
	}
	if len(result.Playbook) != 1 || result.Playbook[0] != "kubectl rollout undo" {
		t.Errorf("unexpected playbook: %v", result.Playbook)
	}
	if result.InvestigatedAt == nil {
		t.Error("expected non-nil investigatedAt")
	}
}

func TestParseRCAResponse_InvalidJSON(t *testing.T) {
	_, err := parseRCAResponse("not json")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestParseRCAResponse_EmptyRootCause(t *testing.T) {
	_, err := parseRCAResponse(`{"rootCause":"","confidence":"0.5"}`)
	if err == nil {
		t.Error("expected error for empty rootCause")
	}
}

func TestBuildUserPrompt(t *testing.T) {
	ctx := IncidentContext{
		Namespace:    "production",
		Name:         "inc-payment-crash",
		IncidentType: "CrashLoopBackOff",
		Severity:     "P1",
		Phase:        "Active",
		Summary:      "Pod payment-svc is crash-looping",
		AffectedPod:  "payment-svc",
		Signals:      []string{"CrashLoopBackOff (restarts: 5)", "OOMKilled (exit code 137)"},
		Timeline: []string{
			"[2026-04-08T10:00:00Z] Pod entered CrashLoopBackOff",
			"[2026-04-08T10:01:00Z] OOMKilled detected",
		},
		RelatedTraces: []string{"trace-abc", "trace-def"},
		BlastRadius:   []string{"api-gateway", "frontend"},
	}

	prompt := BuildUserPrompt(ctx)

	checks := []string{
		"production",
		"inc-payment-crash",
		"CrashLoopBackOff",
		"P1",
		"payment-svc",
		"OOMKilled",
		"trace-abc",
		"api-gateway",
		"2 services",
	}
	for _, check := range checks {
		if !strings.Contains(prompt, check) {
			t.Errorf("prompt missing expected content %q", check)
		}
	}
}

func TestBuildUserPrompt_Minimal(t *testing.T) {
	ctx := IncidentContext{
		Namespace:    "default",
		Name:         "inc-minimal",
		IncidentType: "ImagePullBackOff",
		Severity:     "P3",
		Phase:        "Detecting",
		Summary:      "Image pull failing",
	}

	prompt := BuildUserPrompt(ctx)
	if !strings.Contains(prompt, "ImagePullBackOff") {
		t.Error("prompt missing incident type")
	}
	// Should not have Affected Pod section
	if strings.Contains(prompt, "Affected Pod") {
		t.Error("prompt should not have Affected Pod for minimal context")
	}
}

func TestExecuteToolCall_QueryMetrics(t *testing.T) {
	querier := &mockQuerier{
		metrics: []telemetry.MetricSeries{
			{Labels: map[string]string{"service": "pay"}, Datapoints: []telemetry.Datapoint{{Value: 42.0}}},
		},
	}
	inv := NewInvestigator(nil, querier, nil, logr.Discard())

	result := inv.executeToolCall(context.Background(), ToolCallEntry{
		Function: FunctionCall{
			Name:      "query_metrics",
			Arguments: `{"query":"rate(http_requests_total[5m])"}`,
		},
	})

	if !strings.Contains(result, "42") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestExecuteToolCall_SearchLogs(t *testing.T) {
	querier := &mockQuerier{
		logs: []telemetry.LogEntry{
			{Body: "OutOfMemoryError", Severity: "ERROR", ServiceName: "payment-svc"},
		},
	}
	inv := NewInvestigator(nil, querier, nil, logr.Discard())

	result := inv.executeToolCall(context.Background(), ToolCallEntry{
		Function: FunctionCall{
			Name:      "search_logs",
			Arguments: `{"service":"payment-svc","severity":"ERROR"}`,
		},
	})

	if !strings.Contains(result, "OutOfMemoryError") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestExecuteToolCall_GetTrace(t *testing.T) {
	querier := &mockQuerier{
		trace: &telemetry.Trace{
			TraceID: "abc123",
			Spans: []telemetry.Span{
				{SpanID: "span1", OperationName: "/checkout", ServiceName: "payment-svc"},
			},
		},
	}
	inv := NewInvestigator(nil, querier, nil, logr.Discard())

	result := inv.executeToolCall(context.Background(), ToolCallEntry{
		Function: FunctionCall{
			Name:      "get_trace",
			Arguments: `{"trace_id":"abc123"}`,
		},
	})

	if !strings.Contains(result, "checkout") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestExecuteToolCall_GetServiceMetrics(t *testing.T) {
	querier := &mockQuerier{
		svcMetrics: &telemetry.ServiceMetrics{
			ServiceName: "payment-svc",
			RequestRate: 100.5,
			ErrorRate:   5.2,
		},
	}
	inv := NewInvestigator(nil, querier, nil, logr.Discard())

	result := inv.executeToolCall(context.Background(), ToolCallEntry{
		Function: FunctionCall{
			Name:      "get_service_metrics",
			Arguments: `{"service":"payment-svc","window":"30m"}`,
		},
	})

	if !strings.Contains(result, "100.5") {
		t.Errorf("unexpected result: %s", result)
	}
}

func TestExecuteToolCall_GetDependencies(t *testing.T) {
	querier := &mockQuerier{
		dependencies: []telemetry.DependencyEdge{
			{Parent: "gateway", Child: "payment-svc", CallCount: 100},
			{Parent: "payment-svc", Child: "database", CallCount: 50},
			{Parent: "gateway", Child: "auth-svc", CallCount: 80},
		},
	}
	inv := NewInvestigator(nil, querier, nil, logr.Discard())

	// With service filter
	result := inv.executeToolCall(context.Background(), ToolCallEntry{
		Function: FunctionCall{
			Name:      "get_dependencies",
			Arguments: `{"service":"payment-svc"}`,
		},
	})

	// Should include gateway->payment-svc and payment-svc->database but not gateway->auth-svc
	if !strings.Contains(result, "gateway") {
		t.Error("expected gateway in filtered dependencies")
	}
	if strings.Contains(result, "auth-svc") {
		t.Error("auth-svc should be filtered out")
	}
}

func TestExecuteToolCall_Unknown(t *testing.T) {
	inv := NewInvestigator(nil, &mockQuerier{}, nil, logr.Discard())
	result := inv.executeToolCall(context.Background(), ToolCallEntry{
		Function: FunctionCall{Name: "nonexistent", Arguments: "{}"},
	})
	if !strings.Contains(result, "unknown tool") {
		t.Errorf("expected unknown tool error: %s", result)
	}
}

func TestExecuteToolCall_InvalidArgs(t *testing.T) {
	inv := NewInvestigator(nil, &mockQuerier{}, nil, logr.Discard())

	tools := []string{"query_metrics", "search_logs", "get_trace", "get_service_metrics", "get_dependencies"}
	for _, tool := range tools {
		t.Run(tool, func(t *testing.T) {
			result := inv.executeToolCall(context.Background(), ToolCallEntry{
				Function: FunctionCall{Name: tool, Arguments: "not-json"},
			})
			if !strings.Contains(result, "invalid arguments") {
				t.Errorf("expected invalid arguments error for %s: %s", tool, result)
			}
		})
	}
}

func TestInvestigator_Investigate_NoToolCalls(t *testing.T) {
	// Mock LLM server that returns a direct response.
	rcaJSON := `{"rootCause":"Memory leak in payment-svc","confidence":"0.92","playbook":["kubectl rollout undo deployment/payment-svc"],"evidence":["OOMKilled events"]}`
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := ChatResponse{
			Choices: []ChatChoice{
				{Message: ChatMessage{Role: "assistant", Content: rcaJSON}, FinishReason: "stop"},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}))
	defer llmServer.Close()

	llm := NewLLMClient(llmServer.URL, "key", "gpt-4o")
	inv := NewInvestigator(llm, &mockQuerier{}, nil, logr.Discard())

	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: "inc-1", Namespace: "prod"},
		Spec: rcav1alpha1.IncidentReportSpec{
			IncidentType: "CrashLoopBackOff",
			Scope: rcav1alpha1.IncidentScope{
				WorkloadRef: &rcav1alpha1.IncidentObjectRef{Name: "payment-svc"},
			},
		},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:             "Active",
			Severity:          "P1",
			Summary:           "Pod crash-looping",
			CorrelatedSignals: []string{"CrashLoopBackOff", "OOMKilled"},
		},
	}

	// Investigate will fail at persistResult since we have no real k8s client,
	// but we can verify the LLM interaction worked by checking the error message.
	err := inv.Investigate(context.Background(), report)
	if err == nil {
		t.Fatal("expected error (no k8s client)")
	}
	// The error should be about persisting, not about LLM call.
	if !strings.Contains(err.Error(), "get latest IncidentReport") {
		t.Errorf("unexpected error (expected persist error): %v", err)
	}
}

func TestInvestigator_Investigate_WithToolCalls(t *testing.T) {
	callCount := 0
	llmServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var resp ChatResponse

		if callCount == 1 {
			// First call: LLM wants to use a tool.
			resp = ChatResponse{
				Choices: []ChatChoice{
					{
						Message: ChatMessage{
							Role: "assistant",
							ToolCalls: []ToolCallEntry{
								{
									ID:   "call_1",
									Type: "function",
									Function: FunctionCall{
										Name:      "get_service_metrics",
										Arguments: `{"service":"auth-svc"}`,
									},
								},
							},
						},
						FinishReason: "tool_calls",
					},
				},
			}
		} else {
			// Second call: LLM returns final answer.
			resp = ChatResponse{
				Choices: []ChatChoice{
					{
						Message: ChatMessage{
							Role:    "assistant",
							Content: `{"rootCause":"High error rate on auth-svc","confidence":"0.85","playbook":["check auth config"],"evidence":["error rate 15%"]}`,
						},
						FinishReason: "stop",
					},
				},
			}
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode: %v", err)
		}
	}))
	defer llmServer.Close()

	querier := &mockQuerier{
		svcMetrics: &telemetry.ServiceMetrics{
			ServiceName: "auth-svc",
			ErrorRate:   15.0,
		},
	}
	llm := NewLLMClient(llmServer.URL, "key", "gpt-4o")
	inv := NewInvestigator(llm, querier, nil, logr.Discard())

	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: "inc-auth", Namespace: "staging"},
		Spec: rcav1alpha1.IncidentReportSpec{
			IncidentType: "CrashLoopBackOff",
		},
		Status: rcav1alpha1.IncidentReportStatus{Phase: "Active", Severity: "P2", Summary: "Auth service crashing"},
	}

	err := inv.Investigate(context.Background(), report)
	// Will fail at persist, but should have made 2 LLM calls.
	if err == nil {
		t.Fatal("expected error (no k8s client)")
	}
	if callCount != 2 {
		t.Errorf("expected 2 LLM calls (1 tool + 1 final), got %d", callCount)
	}
}

func TestInvestigator_BuildContext(t *testing.T) {
	inv := NewInvestigator(nil, nil, nil, logr.Discard())

	now := metav1.Now()
	report := &rcav1alpha1.IncidentReport{
		ObjectMeta: metav1.ObjectMeta{Name: "inc-ctx", Namespace: "prod"},
		Spec: rcav1alpha1.IncidentReportSpec{
			IncidentType: "NodeFailure",
			Scope: rcav1alpha1.IncidentScope{
				WorkloadRef: &rcav1alpha1.IncidentObjectRef{Name: "web-app"},
				ResourceRef: &rcav1alpha1.IncidentObjectRef{Kind: "Node", Name: "node-1"},
			},
		},
		Status: rcav1alpha1.IncidentReportStatus{
			Phase:             "Active",
			Severity:          "P1",
			Summary:           "Node not ready",
			CorrelatedSignals: []string{"NodeNotReady", "PodEvicted"},
			RelatedTraces:     []string{"trace-1"},
			BlastRadius:       []string{"frontend"},
			Timeline: []rcav1alpha1.TimelineEvent{
				{Time: now, Event: "Node became NotReady"},
			},
		},
	}

	ctx := inv.buildContext(report)
	if ctx.AffectedPod != "web-app" {
		t.Errorf("unexpected affectedPod: %s", ctx.AffectedPod)
	}
	if ctx.AffectedNode != "node-1" {
		t.Errorf("unexpected affectedNode: %s", ctx.AffectedNode)
	}
	if len(ctx.Signals) != 2 {
		t.Errorf("expected 2 signals, got %d", len(ctx.Signals))
	}
	if len(ctx.Timeline) != 1 {
		t.Errorf("expected 1 timeline entry, got %d", len(ctx.Timeline))
	}
}

func TestInvestigationTools(t *testing.T) {
	tools := InvestigationTools()
	if len(tools) != 5 {
		t.Fatalf("expected 5 tools, got %d", len(tools))
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		if tool.Type != "function" {
			t.Errorf("unexpected tool type: %s", tool.Type)
		}
		names[tool.Function.Name] = true
		// Verify parameters are valid JSON.
		var params map[string]any
		if err := json.Unmarshal(tool.Function.Parameters, &params); err != nil {
			t.Errorf("invalid parameters JSON for %s: %v", tool.Function.Name, err)
		}
	}

	expected := []string{"query_metrics", "search_logs", "get_trace", "get_service_metrics", "get_dependencies"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestJsonError(t *testing.T) {
	result := jsonError("something went wrong")
	var m map[string]string
	if err := json.Unmarshal([]byte(result), &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if m["error"] != "something went wrong" {
		t.Errorf("unexpected error message: %s", m["error"])
	}
}

func TestMustMarshal(t *testing.T) {
	result := mustMarshal(map[string]int{"count": 5})
	if !strings.Contains(result, `"count":5`) {
		t.Errorf("unexpected result: %s", result)
	}
}
