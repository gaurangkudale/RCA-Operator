package dashboard

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/reporter"
)

//go:embed templates/report.html.tmpl
var reportTemplateFS embed.FS

// reportTemplates holds the incident + cluster report HTML templates. Parsed
// once at package init; html/template auto-escapes all interpolated values so
// incident-supplied text (summaries, signals) cannot inject markup.
var reportTemplates = template.Must(template.ParseFS(reportTemplateFS, "templates/report.html.tmpl"))

// maxClusterReportRows bounds the incident table in the cluster report so a
// cluster with thousands of historical incidents still renders a usable page.
const maxClusterReportRows = 200

// ── View models ─────────────────────────────────────────────────────────────

type reportMeta struct {
	Title        string // <title> / browser tab text
	Heading      string // visible report heading
	GeneratedAt  string // formatted generation timestamp
	DownloadName string // suggested filename for "Download HTML"
}

type resourceView struct {
	Kind, Name, Namespace string
}

type signalView struct {
	Text  string
	Count int
}

type timelineView struct {
	Time  string
	Rel   string
	Event string
}

type incidentReportView struct {
	Meta reportMeta

	Name          string
	Namespace     string
	Title         string
	Severity      string
	SeverityLabel string
	Phase         string
	IncidentType  string
	Summary       string
	Message       string
	Reason        string
	FiredRule     string
	AgentRef      string
	PodName       string
	SignalCount   int64
	Notified      bool

	FirstObservedAt string
	ActiveAt        string
	LastObservedAt  string
	ResolvedAt      string
	Duration        string

	Resources     []resourceView
	Signals       []signalView
	SignalsUnique int
	SignalsTotal  int
	Timeline      []timelineView
	TraceIDs      []string
}

type agentView struct {
	Name       string
	Healthy    bool
	Namespaces []string
}

type incidentRowView struct {
	Severity        string
	Phase           string
	IncidentType    string
	Title           string
	Namespace       string
	FirstObservedAt string
	ts              time.Time // sort key only; not rendered
}

type clusterReportView struct {
	Meta reportMeta

	Active    int
	Detecting int
	Resolved  int
	Total     int

	P1     int
	P2     int
	P3     int
	POther int

	AgentCount     int
	NamespaceCount int
	Agents         []agentView

	Incidents     []incidentRowView
	IncidentTotal int
	Capped        bool
}

// ── Handlers ─────────────────────────────────────────────────────────────────

// handleClusterReport renders the cluster-wide summary report (all incidents +
// stats) as a self-contained, print-optimized HTML page.
func (s *Server) handleClusterReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	list := &rcav1alpha1.IncidentReportList{}
	if err := s.client.List(r.Context(), list); err != nil {
		s.log.Error(err, "Failed to list IncidentReports for report")
		http.Error(w, "failed to list incidents", http.StatusInternalServerError)
		return
	}
	stats, err := s.computeStats(r.Context())
	if err != nil {
		s.log.Error(err, "Failed to compute stats for report")
		http.Error(w, "failed to compute stats", http.StatusInternalServerError)
		return
	}

	view := buildClusterReportView(stats, list.Items, time.Now())
	s.renderReport(w, r, "cluster", view.Meta.DownloadName, view)
}

// writeIncidentReport renders the single-incident report. Called from
// handleIncidentDetail, which has already fetched and authorized the item.
func (s *Server) writeIncidentReport(w http.ResponseWriter, r *http.Request, item *rcav1alpha1.IncidentReport) {
	view := buildIncidentReportView(item, time.Now())
	s.renderReport(w, r, "incident", view.Meta.DownloadName, view)
}

// renderReport executes the named template into a buffer first so a render
// failure produces a clean 500 instead of a half-written page, then writes the
// result. With ?download=1 the page is served as a file attachment.
func (s *Server) renderReport(w http.ResponseWriter, r *http.Request, tmplName, downloadName string, data any) {
	var buf bytes.Buffer
	if err := reportTemplates.ExecuteTemplate(&buf, tmplName, data); err != nil {
		s.log.Error(err, "Failed to render report", "template", tmplName)
		http.Error(w, "failed to render report", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.URL.Query().Get("download") == "1" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", downloadName))
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		s.log.V(1).Info("failed to write report", "error", err.Error())
	}
}

// ── View builders ────────────────────────────────────────────────────────────

func buildIncidentReportView(item *rcav1alpha1.IncidentReport, now time.Time) incidentReportView {
	base := toIncidentResponse(item)
	signals, unique, total := groupSignals(base.CorrelatedSignals)

	v := incidentReportView{
		Meta: reportMeta{
			Title:        "RCA Incident Report — " + item.Name,
			Heading:      "Incident Report",
			GeneratedAt:  fmtTime(&now),
			DownloadName: incidentDownloadName(item.Namespace, item.Name, now),
		},
		Name:            base.Name,
		Namespace:       base.Namespace,
		Title:           incidentTitleGo(base),
		Severity:        base.Severity,
		SeverityLabel:   severityLabel(base.Severity),
		Phase:           base.Phase,
		IncidentType:    base.IncidentType,
		Summary:         cleanSignalGo(base.Summary),
		Reason:          base.Reason,
		FiredRule:       base.FiredRule,
		AgentRef:        base.AgentRef,
		PodName:         base.PodName,
		SignalCount:     base.SignalCount,
		Notified:        base.Notified,
		FirstObservedAt: fmtTime(base.FirstObservedAt),
		ActiveAt:        fmtTime(base.ActiveAt),
		LastObservedAt:  fmtTime(base.LastObservedAt),
		ResolvedAt:      fmtTime(base.ResolvedAt),
		Duration:        incidentDuration(base, now),
		Signals:         signals,
		SignalsUnique:   unique,
		SignalsTotal:    total,
		TraceIDs:        collectTraceIDs(item.Annotations),
	}

	// Only show the message line when it adds information beyond the summary.
	if msg := cleanSignalGo(base.Message); msg != "" && msg != v.Summary {
		v.Message = msg
	}

	v.Resources = make([]resourceView, 0, len(base.AffectedResources))
	for _, res := range base.AffectedResources {
		v.Resources = append(v.Resources, resourceView{Kind: res.Kind, Name: res.Name, Namespace: res.Namespace})
	}

	entries := append([]timelineEntry(nil), base.Timeline...)
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Time == nil || entries[j].Time == nil {
			return entries[i].Time != nil
		}
		return entries[i].Time.Before(*entries[j].Time)
	})
	v.Timeline = make([]timelineView, 0, len(entries))
	for _, e := range entries {
		v.Timeline = append(v.Timeline, timelineView{
			Time:  fmtTime(e.Time),
			Rel:   relTimeGo(e.Time, now),
			Event: cleanSignalGo(e.Event),
		})
	}

	return v
}

func buildClusterReportView(stats statsResponse, items []rcav1alpha1.IncidentReport, now time.Time) clusterReportView {
	v := clusterReportView{
		Meta: reportMeta{
			Title:        "RCA Cluster Report",
			Heading:      "Cluster Report",
			GeneratedAt:  fmtTime(&now),
			DownloadName: clusterDownloadName(now),
		},
		Active:         stats.Active,
		Detecting:      stats.Detecting,
		Resolved:       stats.Resolved,
		Total:          stats.Active + stats.Detecting + stats.Resolved,
		AgentCount:     len(stats.Agents),
		NamespaceCount: len(stats.Namespaces),
	}

	v.Agents = make([]agentView, 0, len(stats.Agents))
	for _, a := range stats.Agents {
		v.Agents = append(v.Agents, agentView{Name: a.Name, Healthy: a.Healthy, Namespaces: a.WatchNamespaces})
	}
	sort.SliceStable(v.Agents, func(i, j int) bool { return v.Agents[i].Name < v.Agents[j].Name })

	rows := make([]incidentRowView, 0, len(items))
	for i := range items {
		base := toIncidentResponse(&items[i])
		if base.Phase != reporter.PhaseResolved {
			switch base.Severity {
			case "P1":
				v.P1++
			case "P2":
				v.P2++
			case "P3":
				v.P3++
			default:
				v.POther++
			}
		}
		rows = append(rows, incidentRowView{
			Severity:        base.Severity,
			Phase:           base.Phase,
			IncidentType:    base.IncidentType,
			Title:           incidentTitleGo(base),
			Namespace:       base.Namespace,
			FirstObservedAt: fmtTime(base.FirstObservedAt),
			ts:              incidentTimestamp(base),
		})
	}

	// Most severe first, then most recent.
	sort.SliceStable(rows, func(i, j int) bool {
		ri, rj := severityRank(rows[i].Severity), severityRank(rows[j].Severity)
		if ri != rj {
			return ri > rj
		}
		return rows[i].ts.After(rows[j].ts)
	})

	v.IncidentTotal = len(rows)
	if len(rows) > maxClusterReportRows {
		rows = rows[:maxClusterReportRows]
		v.Capped = true
	}
	v.Incidents = rows

	return v
}

// ── Helpers ──────────────────────────────────────────────────────────────────

// incidentTitleGo mirrors the dashboard's incidentTitle(): prefer the pod name,
// then the first affected resource, then the incident type, then the CR name.
func incidentTitleGo(base incidentResponse) string {
	if pod := strings.TrimSpace(base.PodName); pod != "" && !strings.EqualFold(pod, "unknown") {
		return pod
	}
	for _, r := range base.AffectedResources {
		if r.Name != "" {
			return r.Name
		}
	}
	if base.IncidentType != "" {
		return base.IncidentType
	}
	if base.Name != "" {
		return base.Name
	}
	return "—"
}

// groupSignals collapses correlated signals into unique entries with counts,
// mirroring the dashboard's renderIncidentDetail grouping. Returns the grouped
// list (most frequent first), the unique count, and the total count.
func groupSignals(raw []string) (groups []signalView, unique, total int) {
	counts := make(map[string]int, len(raw))
	order := make([]string, 0, len(raw))
	for _, s := range raw {
		c := cleanSignalGo(s)
		if c == "" {
			continue
		}
		total++
		if _, ok := counts[c]; !ok {
			order = append(order, c)
		}
		counts[c]++
	}
	groups = make([]signalView, 0, len(order))
	for _, t := range order {
		groups = append(groups, signalView{Text: t, Count: counts[t]})
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].Count != groups[j].Count {
			return groups[i].Count > groups[j].Count
		}
		return groups[i].Text < groups[j].Text
	})
	return groups, len(order), total
}

// cleanSignalGo mirrors the dashboard's cleanSignal(): drop empty "key="
// fragments (e.g. a trailing "message=") and collapse whitespace.
func cleanSignalGo(s string) string {
	fields := strings.Fields(s)
	out := fields[:0]
	for _, f := range fields {
		if isEmptyKeyFragment(f) {
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// isEmptyKeyFragment reports whether tok is an identifier immediately followed
// by "=" with no value (e.g. "message=", "trace.id=").
func isEmptyKeyFragment(tok string) bool {
	if !strings.HasSuffix(tok, "=") {
		return false
	}
	key := tok[:len(tok)-1]
	if key == "" {
		return false
	}
	for i, r := range key {
		switch {
		case r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
			// always valid
		case i > 0 && (r == '.' || r == '-' || (r >= '0' && r <= '9')):
			// valid after the first character
		default:
			return false
		}
	}
	return true
}

func severityLabel(sev string) string {
	switch sev {
	case "P1":
		return "Critical"
	case "P2":
		return "High"
	case "P3":
		return "Medium"
	default:
		return ""
	}
}

func fmtTime(t *time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05 UTC")
}

func relTimeGo(t *time.Time, now time.Time) string {
	if t == nil || t.IsZero() {
		return ""
	}
	d := max(now.Sub(*t), 0)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours())/24)
	}
}

// incidentDuration returns a human duration from incident start (activated, or
// first observed) to resolution (or now if still open).
func incidentDuration(base incidentResponse, now time.Time) string {
	start := base.ActiveAt
	if start == nil {
		start = base.FirstObservedAt
	}
	if start == nil {
		return ""
	}
	end := now
	if base.ResolvedAt != nil {
		end = *base.ResolvedAt
	}
	return humanDuration(max(end.Sub(*start), 0))
}

func humanDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

func incidentDownloadName(ns, name string, now time.Time) string {
	return fmt.Sprintf("rca-incident-%s-%s-%s.html", sanitizeFilePart(ns), sanitizeFilePart(name), now.UTC().Format("20060102-150405"))
}

func clusterDownloadName(now time.Time) string {
	return fmt.Sprintf("rca-cluster-report-%s.html", now.UTC().Format("20060102-150405"))
}

func sanitizeFilePart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "report"
	}
	return b.String()
}
