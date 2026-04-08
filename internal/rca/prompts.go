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
	"fmt"
	"strings"
)

const systemPrompt = `You are a Kubernetes incident root cause analysis expert. You are investigating an active incident detected by the RCA Operator.

Your task is to determine the root cause of the incident by analyzing the available evidence: Kubernetes events, pod status, related traces, metrics, and logs. You have access to tools that let you query observability backends for additional data.

Guidelines:
- Be concise and actionable in your analysis
- Focus on the most likely root cause based on evidence
- Provide specific remediation steps (kubectl commands, config changes)
- Rate your confidence from 0.0 to 1.0
- Only use tools when the initial evidence is insufficient
- Do not speculate without evidence

Respond in this exact JSON format:
{
  "rootCause": "Brief description of the root cause",
  "confidence": "0.85",
  "playbook": ["step 1", "step 2"],
  "evidence": ["evidence item 1", "evidence item 2"]
}`

// IncidentContext holds the data assembled for an AI investigation.
type IncidentContext struct {
	Namespace     string
	Name          string
	IncidentType  string
	Severity      string
	Phase         string
	Summary       string
	AffectedPod   string
	AffectedNode  string
	Signals       []string
	Timeline      []string
	RelatedTraces []string
	BlastRadius   []string
}

// BuildUserPrompt constructs the investigation prompt from incident context.
func BuildUserPrompt(ctx IncidentContext) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Incident: %s/%s\n\n", ctx.Namespace, ctx.Name)
	fmt.Fprintf(&b, "- **Type**: %s\n", ctx.IncidentType)
	fmt.Fprintf(&b, "- **Severity**: %s\n", ctx.Severity)
	fmt.Fprintf(&b, "- **Phase**: %s\n", ctx.Phase)
	fmt.Fprintf(&b, "- **Summary**: %s\n", ctx.Summary)

	if ctx.AffectedPod != "" {
		fmt.Fprintf(&b, "- **Affected Pod**: %s\n", ctx.AffectedPod)
	}
	if ctx.AffectedNode != "" {
		fmt.Fprintf(&b, "- **Affected Node**: %s\n", ctx.AffectedNode)
	}

	if len(ctx.Signals) > 0 {
		b.WriteString("\n### Correlated Signals\n")
		for _, s := range ctx.Signals {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}

	if len(ctx.Timeline) > 0 {
		b.WriteString("\n### Timeline\n")
		for _, t := range ctx.Timeline {
			fmt.Fprintf(&b, "- %s\n", t)
		}
	}

	if len(ctx.RelatedTraces) > 0 {
		fmt.Fprintf(&b, "\n### Related Traces (%d)\n", len(ctx.RelatedTraces))
		for _, t := range ctx.RelatedTraces {
			fmt.Fprintf(&b, "- %s\n", t)
		}
	}

	if len(ctx.BlastRadius) > 0 {
		fmt.Fprintf(&b, "\n### Blast Radius (%d services)\n", len(ctx.BlastRadius))
		for _, s := range ctx.BlastRadius {
			fmt.Fprintf(&b, "- %s\n", s)
		}
	}

	b.WriteString("\nAnalyze this incident and determine the root cause. Use the available tools if you need additional telemetry data.")

	return b.String()
}
