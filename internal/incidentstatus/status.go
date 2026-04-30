package incidentstatus

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/metrics"
)

const (
	MaxTimelineEntries = 50
	PhaseDetecting     = "Detecting"
	PhaseActive        = "Active"
	PhaseResolved      = "Resolved"
)

func AppendTimeline(tl []rcav1alpha1.TimelineEvent, t metav1.Time, msg string) []rcav1alpha1.TimelineEvent {
	tl = append(tl, rcav1alpha1.TimelineEvent{Time: t, Event: msg})
	if len(tl) > MaxTimelineEntries {
		tl = tl[len(tl)-MaxTimelineEntries:]
	}
	return tl
}

func MarkActive(report *rcav1alpha1.IncidentReport, now metav1.Time, reason string) {
	priorPhase := report.Status.Phase
	report.Status.Phase = PhaseActive
	report.Status.ActiveAt = &now
	report.Status.Timeline = AppendTimeline(report.Status.Timeline, now, reason)

	// Only count the transition once per incident — re-entering Active from
	// a resurrection path would otherwise double-count.
	if priorPhase != PhaseActive {
		var dt float64
		if start := EffectiveStartTime(report.Status); start != nil {
			dt = now.Sub(start.Time).Seconds()
		}
		metrics.RecordIncidentActivated(report.Status.IncidentType, report.Status.Severity, dt)
	}
}

func MarkResolved(report *rcav1alpha1.IncidentReport, now metav1.Time, reason string) {
	priorPhase := report.Status.Phase
	report.Status.Phase = PhaseResolved
	report.Status.ResolvedAt = &now
	report.Status.ResolvedTime = &now
	report.Status.Timeline = AppendTimeline(report.Status.Timeline, now, reason)

	if priorPhase != PhaseResolved {
		var dt float64
		// Prefer time spent in the immediately-prior phase: ActiveAt for
		// Active→Resolved, FirstObservedAt for Detecting→Resolved.
		switch priorPhase {
		case PhaseActive:
			if report.Status.ActiveAt != nil {
				dt = now.Sub(report.Status.ActiveAt.Time).Seconds()
			}
		default:
			if start := EffectiveStartTime(report.Status); start != nil {
				dt = now.Sub(start.Time).Seconds()
			}
		}
		metrics.RecordIncidentResolved(report.Status.IncidentType, report.Status.Severity, priorPhase, dt)
	}
}

func EffectiveStartTime(status rcav1alpha1.IncidentReportStatus) *metav1.Time {
	if status.FirstObservedAt != nil {
		return status.FirstObservedAt
	}
	return status.StartTime
}

func EffectiveResolvedTime(status rcav1alpha1.IncidentReportStatus) *metav1.Time {
	if status.ResolvedAt != nil {
		return status.ResolvedAt
	}
	return status.ResolvedTime
}
