package correlator

import (
	"fmt"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// EventClassification is the UI-facing summary of a CorrelatorEvent. It powers
// the dashboard's Live Correlation Stream, colour-coding pills by Kind.
type EventClassification struct {
	Kind     string // "trace" | "log" | "k8s" | "metric"
	Source   string // e.g. "Jaeger", "OTel", "RCA-Operator"
	Title    string // short label ("OOMKilled", "ERROR", "span.error")
	Msg      string // human-readable one-liner
	Resource string // ns/name of the subject (pod, service, node)
}

// ClassifyEvent maps a raw CorrelatorEvent to a UI-oriented classification.
// Kept alongside Buffer so SSE streamers can convert Entry → wire JSON without
// reaching into every event sub-type.
func ClassifyEvent(e watcher.CorrelatorEvent) EventClassification {
	switch ev := e.(type) {
	case watcher.OTelSpanErrorEvent:
		return EventClassification{
			Kind:     "trace",
			Source:   "OTel",
			Title:    "span.error",
			Msg:      fmt.Sprintf("%s %s (%s)", ev.ServiceName, ev.SpanName, ev.StatusMessage),
			Resource: ev.ServiceName,
		}
	case watcher.OTelSpanLatencySpikeEvent:
		return EventClassification{
			Kind:     "trace",
			Source:   "OTel",
			Title:    "span.latency",
			Msg:      fmt.Sprintf("%s %s exceeded latency threshold", ev.ServiceName, ev.SpanName),
			Resource: ev.ServiceName,
		}
	case watcher.OTelSpanEventEvent:
		return EventClassification{
			Kind:     "trace",
			Source:   "OTel",
			Title:    ev.EventName,
			Msg:      fmt.Sprintf("%s on %s", ev.EventName, ev.ServiceName),
			Resource: ev.ServiceName,
		}
	case watcher.OTelLogMatchEvent:
		sev := ev.Severity
		if sev == "" {
			sev = "LOG"
		}
		return EventClassification{
			Kind:     "log",
			Source:   "OTel",
			Title:    sev,
			Msg:      ev.Body,
			Resource: ev.ServiceName,
		}
	}

	// All remaining events are K8s-sourced. Pull namespace/pod/node via type
	// switch on concrete kinds that embed BaseEvent.
	kind := "k8s"
	title := string(e.Type())
	ns, pod, node := baseFields(e)
	resource := pod
	if resource == "" {
		resource = node
	}
	if ns != "" && resource != "" {
		resource = ns + "/" + resource
	}
	return EventClassification{
		Kind:     kind,
		Source:   "RCA-Operator",
		Title:    title,
		Msg:      fmt.Sprintf("%s on %s", title, resource),
		Resource: resource,
	}
}

func baseFields(e watcher.CorrelatorEvent) (ns, pod, node string) {
	switch ev := e.(type) {
	case watcher.CrashLoopBackOffEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.OOMKilledEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.ImagePullBackOffEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.PodPendingTooLongEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.GracePeriodViolationEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.PodHealthyEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.PodDeletedEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.NodeNotReadyEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.PodEvictedEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.ProbeFailureEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.StalledRolloutEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.NodePressureEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.StalledStatefulSetEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.StalledDaemonSetEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.JobFailedEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	case watcher.CronJobFailedEvent:
		return ev.Namespace, ev.PodName, ev.NodeName
	}
	return "", "", ""
}
