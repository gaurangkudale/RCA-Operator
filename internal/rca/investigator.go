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

package rca

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/telemetry"
)

// maxToolRounds limits the number of tool-use round trips with the LLM.
const maxToolRounds = 5

// Investigator orchestrates AI-powered root cause analysis for incidents.
// It assembles incident context, sends it to an LLM, handles tool calls,
// and writes the result back to the IncidentReport CR.
type Investigator struct {
	llm      *LLMClient
	querier  telemetry.TelemetryQuerier
	k8s      client.Client
	redactor *Redactor
	log      logr.Logger
}

// NewInvestigator creates an Investigator with the given LLM client and telemetry querier.
func NewInvestigator(llm *LLMClient, querier telemetry.TelemetryQuerier, k8s client.Client, log logr.Logger) *Investigator {
	return &Investigator{
		llm:      llm,
		querier:  querier,
		k8s:      k8s,
		redactor: NewRedactor(),
		log:      log.WithName("investigator"),
	}
}

// Investigate runs an AI investigation for the given IncidentReport.
// It assembles context, queries the LLM (with optional tool use rounds),
// parses the result, and updates the CR status.
func (inv *Investigator) Investigate(ctx context.Context, report *rcav1alpha1.IncidentReport) error {
	incCtx := inv.buildContext(report)
	userPrompt := BuildUserPrompt(incCtx)

	// Redact PII from the prompt before sending to LLM.
	userPrompt = inv.redactor.Redact(userPrompt)

	messages := []ChatMessage{
		{Role: "system", Content: systemPrompt},
		{Role: "user", Content: userPrompt},
	}
	tools := InvestigationTools()

	// Agentic loop: LLM may request tool calls, we execute them and feed results back.
	var finalContent string
	for round := range maxToolRounds {
		resp, err := inv.llm.Complete(ctx, messages, tools)
		if err != nil {
			return fmt.Errorf("LLM call (round %d): %w", round, err)
		}

		if !resp.HasToolCalls() {
			finalContent = resp.FirstContent()
			break
		}

		// Append assistant's tool call message.
		messages = append(messages, resp.Choices[0].Message)

		// Execute each tool call and append results.
		for _, tc := range resp.Choices[0].Message.ToolCalls {
			result := inv.executeToolCall(ctx, tc)
			messages = append(messages, ChatMessage{
				Role:       "tool",
				Content:    inv.redactor.Redact(result),
				ToolCallID: tc.ID,
			})
		}
	}

	if finalContent == "" {
		return fmt.Errorf("LLM did not produce a final response after %d rounds", maxToolRounds)
	}

	// Parse the structured response.
	rcaResult, err := parseRCAResponse(finalContent)
	if err != nil {
		// If parsing fails, store the raw content as root cause.
		inv.log.V(1).Info("failed to parse structured RCA response, using raw", "error", err)
		rcaResult = &rcav1alpha1.RCAResult{
			RootCause:      finalContent,
			Confidence:     "0.5",
			InvestigatedAt: &metav1.Time{Time: time.Now()},
		}
	}

	// Persist the result to the IncidentReport CR.
	return inv.persistResult(ctx, report, rcaResult)
}

// buildContext assembles the incident context from the CR.
func (inv *Investigator) buildContext(report *rcav1alpha1.IncidentReport) IncidentContext {
	ctx := IncidentContext{
		Namespace:     report.Namespace,
		Name:          report.Name,
		IncidentType:  report.Spec.IncidentType,
		Severity:      report.Status.Severity,
		Phase:         report.Status.Phase,
		Summary:       report.Spec.IncidentType + " on " + report.Spec.Fingerprint,
		Signals:       report.Status.CorrelatedSignals,
		RelatedTraces: report.Status.RelatedTraces,
		BlastRadius:   report.Status.BlastRadius,
	}

	if report.Spec.Scope.WorkloadRef != nil {
		ctx.AffectedPod = report.Spec.Scope.WorkloadRef.Name
	}
	if report.Spec.Scope.ResourceRef != nil && report.Spec.Scope.ResourceRef.Kind == "Node" {
		ctx.AffectedNode = report.Spec.Scope.ResourceRef.Name
	}

	for _, evt := range report.Status.Timeline {
		ctx.Timeline = append(ctx.Timeline, fmt.Sprintf("[%s] %s", evt.Time.Format(time.RFC3339), evt.Event))
	}

	return ctx
}

// executeToolCall runs a single tool call against the telemetry backend.
func (inv *Investigator) executeToolCall(ctx context.Context, tc ToolCallEntry) string {
	switch tc.Function.Name {
	case "query_metrics":
		return inv.toolQueryMetrics(ctx, tc.Function.Arguments)
	case "search_logs":
		return inv.toolSearchLogs(ctx, tc.Function.Arguments)
	case "get_trace":
		return inv.toolGetTrace(ctx, tc.Function.Arguments)
	case "get_service_metrics":
		return inv.toolGetServiceMetrics(ctx, tc.Function.Arguments)
	case "get_dependencies":
		return inv.toolGetDependencies(ctx, tc.Function.Arguments)
	default:
		return fmt.Sprintf(`{"error": "unknown tool: %s"}`, tc.Function.Name)
	}
}

func (inv *Investigator) toolQueryMetrics(ctx context.Context, argsJSON string) string {
	var args QueryMetricsArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}

	end := time.Now()
	start := end.Add(-15 * time.Minute)
	if args.Start != "" {
		if t, err := time.Parse(time.RFC3339, args.Start); err == nil {
			start = t
		}
	}
	if args.End != "" {
		if t, err := time.Parse(time.RFC3339, args.End); err == nil {
			end = t
		}
	}

	series, err := inv.querier.QueryMetric(ctx, args.Query, start, end, 30*time.Second)
	if err != nil {
		return jsonError("query_metrics failed: " + err.Error())
	}
	return mustMarshal(series)
}

func (inv *Investigator) toolSearchLogs(ctx context.Context, argsJSON string) string {
	var args SearchLogsArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}

	window := 15 * time.Minute
	if args.Window != "" {
		if d, err := time.ParseDuration(args.Window); err == nil {
			window = d
		}
	}

	end := time.Now()
	start := end.Add(-window)

	logs, err := inv.querier.SearchLogs(ctx, telemetry.LogFilter{
		ServiceName: args.Service,
		Severity:    args.Severity,
		Keyword:     args.Keyword,
		Start:       start,
		End:         end,
		Limit:       50,
	})
	if err != nil {
		return jsonError("search_logs failed: " + err.Error())
	}
	return mustMarshal(logs)
}

func (inv *Investigator) toolGetTrace(ctx context.Context, argsJSON string) string {
	var args GetTraceArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}

	trace, err := inv.querier.GetTrace(ctx, args.TraceID)
	if err != nil {
		return jsonError("get_trace failed: " + err.Error())
	}
	if trace == nil {
		return `{"error": "trace not found"}`
	}
	return mustMarshal(trace)
}

func (inv *Investigator) toolGetServiceMetrics(ctx context.Context, argsJSON string) string {
	var args GetServiceMetricsArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}

	window := 15 * time.Minute
	if args.Window != "" {
		if d, err := time.ParseDuration(args.Window); err == nil {
			window = d
		}
	}

	metrics, err := inv.querier.GetServiceMetrics(ctx, args.Service, window)
	if err != nil {
		return jsonError("get_service_metrics failed: " + err.Error())
	}
	if metrics == nil {
		return `{"error": "no metrics available"}`
	}
	return mustMarshal(metrics)
}

func (inv *Investigator) toolGetDependencies(ctx context.Context, argsJSON string) string {
	var args GetDependenciesArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return jsonError("invalid arguments: " + err.Error())
	}

	deps, err := inv.querier.GetDependencies(ctx, 15*time.Minute)
	if err != nil {
		return jsonError("get_dependencies failed: " + err.Error())
	}

	// Filter to specific service if requested.
	if args.Service != "" {
		var filtered []telemetry.DependencyEdge
		for _, d := range deps {
			if d.Parent == args.Service || d.Child == args.Service {
				filtered = append(filtered, d)
			}
		}
		deps = filtered
	}
	return mustMarshal(deps)
}

// rcaResponse is the expected JSON structure from the LLM.
type rcaResponse struct {
	RootCause  string   `json:"rootCause"`
	Confidence string   `json:"confidence"`
	Playbook   []string `json:"playbook"`
	Evidence   []string `json:"evidence"`
}

// parseRCAResponse extracts the structured RCA result from LLM output.
func parseRCAResponse(content string) (*rcav1alpha1.RCAResult, error) {
	var resp rcaResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil {
		return nil, fmt.Errorf("parse RCA JSON: %w", err)
	}

	if resp.RootCause == "" {
		return nil, fmt.Errorf("empty rootCause in response")
	}

	now := metav1.Now()
	return &rcav1alpha1.RCAResult{
		RootCause:      resp.RootCause,
		Confidence:     resp.Confidence,
		Playbook:       resp.Playbook,
		Evidence:       resp.Evidence,
		InvestigatedAt: &now,
	}, nil
}

// persistResult writes the RCA result to the IncidentReport CR status.
func (inv *Investigator) persistResult(ctx context.Context, report *rcav1alpha1.IncidentReport, result *rcav1alpha1.RCAResult) error {
	if inv.k8s == nil {
		return fmt.Errorf("get latest IncidentReport: kubernetes client is nil")
	}

	// Re-read the latest version to avoid conflict.
	latest := &rcav1alpha1.IncidentReport{}
	key := client.ObjectKeyFromObject(report)
	if err := inv.k8s.Get(ctx, key, latest); err != nil {
		return fmt.Errorf("get latest IncidentReport: %w", err)
	}

	latest.Status.RCA = result
	if err := inv.k8s.Status().Update(ctx, latest); err != nil {
		return fmt.Errorf("update IncidentReport status: %w", err)
	}

	inv.log.Info("AI investigation complete",
		"incident", key,
		"rootCause", result.RootCause,
		"confidence", result.Confidence,
	)
	return nil
}

func jsonError(msg string) string {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return string(b)
}

func mustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return jsonError("marshal error: " + err.Error())
	}
	return string(b)
}
