package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/correlator"
	"github.com/gaurangkudale/rca-operator/internal/correlator/graph"
	"github.com/gaurangkudale/rca-operator/internal/jaeger"
)

// Option configures optional Server dependencies. Keeps NewServer backward
// compatible while adding the cluster-topology, logs, and SSE features.
const (
	kindPod         = "Pod"
	kindNode        = "Node"
	kindService     = "Service"
	kindDeployment  = "Deployment"
	kindReplicaSet  = "ReplicaSet"
	kindStatefulSet = "StatefulSet"
	kindDaemonSet   = "DaemonSet"

	defaultServiceGraphLookback = 24 * time.Hour
	maxServiceGraphLookback     = 7 * 24 * time.Hour
)

type Option func(*Server)

// WithKubernetesClient injects a typed kubernetes.Interface used for pod log
// streaming. When nil, /api/logs responds with 503.
func WithKubernetesClient(k kubernetes.Interface) Option {
	return func(s *Server) { s.k8s = k }
}

// WithBuffer injects the correlator's sliding-window buffer so the SSE stream
// handler can subscribe to live events. When nil, /api/stream responds with 503.
func WithBuffer(b *correlator.Buffer) Option {
	return func(s *Server) { s.buffer = b }
}

// WithJaegerClient injects a Jaeger query client used to build the
// service-dependency graph. When nil, /api/service-graph returns an empty graph
// with Meta.source="unavailable" (handler still 200s so the UI can render).
func WithJaegerClient(c *jaeger.Client) Option {
	return func(s *Server) { s.jc = c }
}

// WithOptions applies the given options. Exposed so NewServer can stay with a
// minimal signature; callers use this to attach k8s + buffer after construction.
func (s *Server) WithOptions(opts ...Option) *Server {
	for _, o := range opts {
		o(s)
	}
	return s
}

// ── /api/topology ─────────────────────────────────────────────────────────────

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	opts := graph.BuildOptions{Summary: r.URL.Query().Get("view") != "detail"}
	key := "topology|summary=" + strconv.FormatBool(opts.Summary)
	body, etag, err := s.cache.Fetch(key, func() (any, error) {
		b := graph.NewClusterBuilder(s.client, s.log)
		return b.BuildWithOptions(r.Context(), opts)
	})
	if err != nil {
		s.log.Error(err, "build cluster topology")
		http.Error(w, "failed to build topology", http.StatusInternalServerError)
		return
	}
	writeCachedJSON(w, r, body, etag)
}

// ── /api/service-graph ────────────────────────────────────────────────────────

func (s *Server) handleServiceGraph(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	lookback := defaultServiceGraphLookback
	if v := r.URL.Query().Get("lookback"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 && d <= maxServiceGraphLookback {
			lookback = d
		}
	}
	key := "service-graph|lookback=" + lookback.String()
	body, etag, err := s.cache.Fetch(key, func() (any, error) {
		var fetcher graph.ServiceDependencyFetcher
		if s.jc != nil {
			fetcher = s.jc
		}
		b := graph.NewServiceGraphBuilder(s.client, fetcher, s.log)
		return b.Build(r.Context(), lookback)
	})
	if err != nil {
		s.log.Error(err, "build service graph")
		http.Error(w, "failed to build service graph", http.StatusInternalServerError)
		return
	}
	writeCachedJSON(w, r, body, etag)
}

// ── /api/resources/{ns}/{kind}/{name} ─────────────────────────────────────────

type resourceResponse struct {
	Kind            string             `json:"kind"`
	Namespace       string             `json:"namespace"`
	Name            string             `json:"name"`
	Age             string             `json:"age,omitempty"`
	DesiredReplicas int32              `json:"desiredReplicas,omitempty"`
	CurrentReplicas int32              `json:"currentReplicas,omitempty"`
	ReadyReplicas   int32              `json:"readyReplicas,omitempty"`
	RestartCount    int32              `json:"restartCount,omitempty"`
	Phase           string             `json:"phase,omitempty"`
	NodeName        string             `json:"nodeName,omitempty"`
	OpenIncidents   []incidentResponse `json:"openIncidents"`
	RecentEvents    []eventEntry       `json:"recentEvents"`
	NodeReady       *bool              `json:"nodeReady,omitempty"`
}

type eventEntry struct {
	Time    *time.Time `json:"time"`
	Type    string     `json:"type"`
	Reason  string     `json:"reason"`
	Message string     `json:"message"`
	Source  string     `json:"source,omitempty"`
}

func (s *Server) handleResource(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/resources/"), "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		http.Error(w, "path must be /api/resources/{namespace}/{kind}/{name}", http.StatusBadRequest)
		return
	}
	ns, kind, name := parts[0], parts[1], parts[2]

	resp := resourceResponse{Kind: kind, Namespace: ns, Name: name}

	switch kind {
	case kindPod:
		var p corev1.Pod
		if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: ns, Name: name}, &p); err != nil {
			writeResourceNotFound(w, err)
			return
		}
		resp.Age = ageString(p.CreationTimestamp.Time)
		resp.Phase = string(p.Status.Phase)
		resp.NodeName = p.Spec.NodeName
		for _, cs := range p.Status.ContainerStatuses {
			resp.RestartCount += cs.RestartCount
		}
	case kindDeployment:
		var d appsv1.Deployment
		if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: ns, Name: name}, &d); err != nil {
			writeResourceNotFound(w, err)
			return
		}
		resp.Age = ageString(d.CreationTimestamp.Time)
		if d.Spec.Replicas != nil {
			resp.DesiredReplicas = *d.Spec.Replicas
		}
		resp.CurrentReplicas = d.Status.Replicas
		resp.ReadyReplicas = d.Status.ReadyReplicas
	case kindNode:
		var n corev1.Node
		if err := s.client.Get(r.Context(), client.ObjectKey{Name: name}, &n); err != nil {
			writeResourceNotFound(w, err)
			return
		}
		resp.Age = ageString(n.CreationTimestamp.Time)
		ready := false
		for _, c := range n.Status.Conditions {
			if c.Type == corev1.NodeReady {
				ready = c.Status == corev1.ConditionTrue
				break
			}
		}
		resp.NodeReady = &ready
	case kindService:
		var svc corev1.Service
		if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: ns, Name: name}, &svc); err != nil {
			writeResourceNotFound(w, err)
			return
		}
		resp.Age = ageString(svc.CreationTimestamp.Time)
		resp.Phase = string(svc.Spec.Type)
	default:
		http.Error(w, "unsupported kind", http.StatusBadRequest)
		return
	}

	// Overlay open IncidentReports referencing this resource.
	var incidents rcav1alpha1.IncidentReportList
	if err := s.client.List(r.Context(), &incidents); err == nil {
		for i := range incidents.Items {
			inc := &incidents.Items[i]
			if strings.EqualFold(inc.Status.Phase, "resolved") {
				continue
			}
			if !incidentReferences(inc, kind, ns, name) {
				continue
			}
			resp.OpenIncidents = append(resp.OpenIncidents, toIncidentResponse(inc))
		}
	}

	// Recent k8s events via typed client when available.
	if s.k8s != nil {
		evScope := ns
		if kind == kindNode {
			evScope = ""
		}
		fs := fmt.Sprintf("involvedObject.kind=%s,involvedObject.name=%s", kind, name)
		list, err := s.k8s.CoreV1().Events(evScope).List(r.Context(), metav1.ListOptions{FieldSelector: fs, Limit: 10})
		if err == nil {
			for _, e := range list.Items {
				t := e.LastTimestamp.Time
				if t.IsZero() {
					t = e.EventTime.Time
				}
				resp.RecentEvents = append(resp.RecentEvents, eventEntry{
					Time:    &t,
					Type:    e.Type,
					Reason:  e.Reason,
					Message: e.Message,
					Source:  e.Source.Component,
				})
			}
		}
	}

	if resp.OpenIncidents == nil {
		resp.OpenIncidents = []incidentResponse{}
	}
	resp.OpenIncidents = collapseDuplicateOTelIncidents(resp.OpenIncidents)
	if resp.RecentEvents == nil {
		resp.RecentEvents = []eventEntry{}
	}
	writeJSON(w, resp)
}

func incidentReferences(inc *rcav1alpha1.IncidentReport, kind, ns, name string) bool {
	if kind == kindPod && inc.Namespace == ns && inc.Labels["rca.rca-operator.tech/pod"] == name {
		return true
	}
	if kind == kindService {
		for _, r := range inc.Status.AffectedResources {
			if r.Name != name {
				continue
			}
			if r.Namespace != "" && r.Namespace != ns {
				continue
			}
			switch r.Kind {
			case kindService, kindDeployment, kindStatefulSet, kindDaemonSet, kindReplicaSet, kindPod:
				if r.Namespace == "" && inc.Namespace != ns {
					continue
				}
				return true
			}
		}
		if pod := inc.Labels["rca.rca-operator.tech/pod"]; pod != "" && inc.Namespace == ns {
			if strings.HasPrefix(pod, name+"-") {
				return true
			}
		}
	}
	for _, r := range inc.Status.AffectedResources {
		if r.Kind == kind && r.Name == name {
			if kind == kindNode || inc.Namespace == ns {
				return true
			}
		}
	}
	return false
}

func writeResourceNotFound(w http.ResponseWriter, err error) {
	if apierrors.IsNotFound(err) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, "failed to get resource", http.StatusInternalServerError)
}

// ── /api/logs ─────────────────────────────────────────────────────────────────

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.k8s == nil {
		http.Error(w, "logs unavailable: k8s client not configured", http.StatusServiceUnavailable)
		return
	}
	q := r.URL.Query()
	ns := q.Get("ns")
	pod := q.Get("pod")
	deployment := q.Get("deployment")
	container := q.Get("container")
	if ns == "" {
		http.Error(w, "ns is required", http.StatusBadRequest)
		return
	}
	if pod == "" && deployment == "" {
		http.Error(w, "pod or deployment is required", http.StatusBadRequest)
		return
	}

	tail := int64(500)
	if v, err := strconv.ParseInt(q.Get("tailLines"), 10, 64); err == nil && v > 0 {
		if v > 5000 {
			v = 5000
		}
		tail = v
	}
	sinceSeconds := int64(3600)
	if s := q.Get("since"); s != "" {
		if d, err := time.ParseDuration(s); err == nil && d > 0 {
			sinceSeconds = int64(d.Seconds())
		}
	}

	if pod == "" {
		// Resolve deployment → pods via label selector.
		var d appsv1.Deployment
		if err := s.client.Get(r.Context(), client.ObjectKey{Namespace: ns, Name: deployment}, &d); err != nil {
			http.Error(w, "deployment not found", http.StatusNotFound)
			return
		}
		sel := labels.SelectorFromSet(d.Spec.Selector.MatchLabels).String()
		podList, err := s.k8s.CoreV1().Pods(ns).List(r.Context(), metav1.ListOptions{LabelSelector: sel, Limit: 1})
		if err != nil || len(podList.Items) == 0 {
			http.Error(w, "no pods found for deployment", http.StatusNotFound)
			return
		}
		pod = podList.Items[0].Name
	}

	opts := &corev1.PodLogOptions{
		TailLines:    &tail,
		SinceSeconds: &sinceSeconds,
	}
	if container != "" {
		opts.Container = container
	}
	req := s.k8s.CoreV1().Pods(ns).GetLogs(pod, opts)
	stream, err := req.Stream(r.Context())
	if err != nil {
		s.log.Error(err, "pod logs stream", "ns", ns, "pod", pod)
		http.Error(w, "failed to fetch logs", http.StatusBadGateway)
		return
	}
	defer func() { _ = stream.Close() }()

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	// Cap response at 4 MiB so a misbehaving pod can't stream unbounded logs
	// through the dashboard. Well above the 5000-line tail limit for typical
	// log densities.
	const maxLogBytes = 4 << 20
	_, _ = io.Copy(w, io.LimitReader(stream, maxLogBytes))
}

// ── /api/pods ─────────────────────────────────────────────────────────────────

type podSummary struct {
	Name       string `json:"name"`
	Deployment string `json:"deployment,omitempty"`
	Phase      string `json:"phase,omitempty"`
}

func (s *Server) handlePods(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	ns := r.URL.Query().Get("ns")
	if ns == "" {
		http.Error(w, "ns is required", http.StatusBadRequest)
		return
	}
	var list corev1.PodList
	if err := s.client.List(r.Context(), &list, client.InNamespace(ns)); err != nil {
		http.Error(w, "failed to list pods", http.StatusInternalServerError)
		return
	}
	var deps appsv1.DeploymentList
	_ = s.client.List(r.Context(), &deps, client.InNamespace(ns))

	out := make([]podSummary, 0, len(list.Items))
	for i := range list.Items {
		p := &list.Items[i]
		ps := podSummary{Name: p.Name, Phase: string(p.Status.Phase)}
		for _, o := range p.OwnerReferences {
			if o.Kind != kindReplicaSet {
				continue
			}
			for _, d := range deps.Items {
				if strings.HasPrefix(o.Name, d.Name+"-") {
					ps.Deployment = d.Name
					break
				}
			}
		}
		out = append(out, ps)
	}
	writeJSON(w, out)
}

// ── /api/agents ───────────────────────────────────────────────────────────────

type flatAgent struct {
	Name            string    `json:"name"`
	WatchNamespaces []string  `json:"watchNamespaces"`
	Status          string    `json:"status"`
	LastSync        time.Time `json:"lastSync"`
	Healthy         bool      `json:"healthy"`
	Retention       string    `json:"retention,omitempty"`
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, etag, err := s.cache.Fetch("agents", func() (any, error) {
		var list rcav1alpha1.RCAAgentList
		if err := s.client.List(r.Context(), &list); err != nil {
			return nil, err
		}
		out := make([]flatAgent, 0, len(list.Items))
		for i := range list.Items {
			a := &list.Items[i]
			nss := a.Spec.WatchNamespaces
			if nss == nil {
				nss = []string{}
			}
			agent := flatAgent{
				Name:            a.Name,
				WatchNamespaces: nss,
				Status:          "Unknown",
				Healthy:         false,
				LastSync:        a.CreationTimestamp.Time,
				Retention:       a.Spec.IncidentRetention,
			}
			for _, c := range a.Status.Conditions {
				if c.Type == "Available" {
					agent.Status = string(c.Status)
					agent.Healthy = c.Status == "True"
					if !c.LastTransitionTime.IsZero() {
						agent.LastSync = c.LastTransitionTime.Time
					}
					break
				}
			}
			out = append(out, agent)
		}
		return out, nil
	})
	if err != nil {
		http.Error(w, "failed to list agents", http.StatusInternalServerError)
		return
	}
	writeCachedJSON(w, r, body, etag)
}

// ── /api/stream (SSE) ─────────────────────────────────────────────────────────

type streamPayload struct {
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"`
	Source   string    `json:"source"`
	Title    string    `json:"title"`
	Msg      string    `json:"msg"`
	Resource string    `json:"resource,omitempty"`
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.buffer == nil {
		http.Error(w, "stream unavailable: buffer not configured", http.StatusServiceUnavailable)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Replay last N entries on connect so the UI has immediate context.
	snap := s.buffer.Snapshot()
	start := 0
	if n := len(snap); n > 20 {
		start = n - 20
	}
	bw := bufio.NewWriter(w)
	for _, e := range snap[start:] {
		writeSSEEntry(bw, e)
	}
	_ = bw.Flush()
	flusher.Flush()

	ch := make(chan correlator.Entry, 32)
	unsub := s.buffer.Subscribe(ch)
	defer unsub()

	ctx := r.Context()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case e := <-ch:
			writeSSEEntry(bw, e)
			if err := bw.Flush(); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := bw.WriteString(": keepalive\n\n"); err != nil {
				return
			}
			if err := bw.Flush(); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSEEntry(w io.Writer, e correlator.Entry) {
	c := correlator.ClassifyEvent(e.Event)
	p := streamPayload{
		Time:     e.AddedAt,
		Kind:     c.Kind,
		Source:   c.Source,
		Title:    c.Title,
		Msg:      c.Msg,
		Resource: c.Resource,
	}
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(w, "event: correlation\ndata: %s\n\n", b)
}

// ageString renders a concise "5m", "3h", "12d" age from t until now.
func ageString(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// appease unused-import check if feature flags trim paths.
var _ = context.Canceled
