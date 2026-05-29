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

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"os"
	"strconv"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	rcav1alpha1 "github.com/gaurangkudale/rca-operator/api/v1alpha1"
	"github.com/gaurangkudale/rca-operator/internal/autodetect"
	"github.com/gaurangkudale/rca-operator/internal/collectors"
	"github.com/gaurangkudale/rca-operator/internal/controller"
	"github.com/gaurangkudale/rca-operator/internal/correlator/graph"
	"github.com/gaurangkudale/rca-operator/internal/dashboard"
	"github.com/gaurangkudale/rca-operator/internal/engine"
	"github.com/gaurangkudale/rca-operator/internal/jaeger"
	"github.com/gaurangkudale/rca-operator/internal/notify"
	rcaotel "github.com/gaurangkudale/rca-operator/internal/otel"
	"github.com/gaurangkudale/rca-operator/internal/otelingest"
	"github.com/gaurangkudale/rca-operator/internal/rulengine"
	rcawebhook "github.com/gaurangkudale/rca-operator/internal/webhook"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(rcav1alpha1.AddToScheme(scheme))
	// +kubebuilder:scaffold:scheme
}

func main() {
	var dashboardAddr string
	var metricsAddr string
	var metricsCertPath, metricsCertName, metricsCertKey string
	var webhookCertPath, webhookCertName, webhookCertKey string
	var enableLeaderElection bool
	var leaderElectionNamespace string
	var probeAddr string
	var secureMetrics bool
	var enableHTTP2 bool
	var enableWebhooks bool
	var tlsOpts []func(*tls.Config)
	var signalBufferSize int
	var signalEmitDedupWindow time.Duration
	flag.StringVar(&dashboardAddr, "dashboard-bind-address", ":9090", "The address the incident dashboard binds to.")
	flag.StringVar(&metricsAddr, "metrics-bind-address", "0", "The address the metrics endpoint binds to. "+
		"Use :8443 for HTTPS or :8080 for HTTP, or leave as 0 to disable the metrics service.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", true,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&leaderElectionNamespace, "leader-election-namespace", "",
		"Namespace for the leader election lease. Required when running outside a cluster (e.g. make run). "+
			"When empty, the in-cluster namespace is used automatically.")
	flag.BoolVar(&secureMetrics, "metrics-secure", true,
		"If set, the metrics endpoint is served securely via HTTPS. Use --metrics-secure=false to use HTTP instead.")
	flag.BoolVar(&enableWebhooks, "enable-webhooks", false,
		"Enable admission webhooks for RCAAgent and RCACorrelationRule validation. "+
			"Webhooks catch invalid specs at admission time rather than when the operator "+
			"tries to load them, but require webhook serving infrastructure to be provisioned "+
			"first (a TLS serving certificate at the webhook cert path plus a "+
			"ValidatingWebhookConfiguration). Off by default because that infrastructure is "+
			"not part of the base install; enabling it without certs present will crash the "+
			"manager on startup. See docs/getting-started/installation.md for how to turn it on.")
	flag.IntVar(&signalBufferSize, "signal-buffer-size", 8192,
		"Size of the shared signal channel between collectors and the incident engine.")
	flag.DurationVar(&signalEmitDedupWindow, "signal-emit-dedup-window", 2*time.Second,
		"Coalesce repeated signals with the same dedup key before enqueueing. Set 0 to disable.")
	flag.StringVar(&webhookCertPath, "webhook-cert-path", "", "The directory that contains the webhook certificate.")
	flag.StringVar(&webhookCertName, "webhook-cert-name", "tls.crt", "The name of the webhook certificate file.")
	flag.StringVar(&webhookCertKey, "webhook-cert-key", "tls.key", "The name of the webhook key file.")
	flag.StringVar(&metricsCertPath, "metrics-cert-path", "",
		"The directory that contains the metrics server certificate.")
	flag.StringVar(&metricsCertName, "metrics-cert-name", "tls.crt", "The name of the metrics server certificate file.")
	flag.StringVar(&metricsCertKey, "metrics-cert-key", "tls.key", "The name of the metrics server key file.")
	var enableAutoDetect bool
	var autoDetectMinOccurrences int
	var autoDetectMinTimeSpan time.Duration
	var autoDetectMaxRules int
	var autoDetectInterval time.Duration
	var autoDetectExpiry time.Duration
	flag.BoolVar(&enableAutoDetect, "enable-autodetect", false,
		"Enable automatic correlation rule detection from buffer patterns.")
	flag.IntVar(&autoDetectMinOccurrences, "autodetect-min-occurrences", 5,
		"Minimum pattern occurrences before auto-creating a rule.")
	flag.DurationVar(&autoDetectMinTimeSpan, "autodetect-min-timespan", 10*time.Minute,
		"Minimum time span between first and last observation before auto-creating a rule.")
	flag.IntVar(&autoDetectMaxRules, "autodetect-max-rules", 20,
		"Maximum number of auto-generated correlation rules.")
	flag.DurationVar(&autoDetectInterval, "autodetect-interval", 60*time.Second,
		"How often to analyze the buffer for patterns.")
	flag.DurationVar(&autoDetectExpiry, "autodetect-expiry", time.Hour,
		"Duration without observation before an auto-generated rule expires.")

	// OTel ingest (Phase 2 Milestone A). Empty bind address disables the server.
	var otelIngestBindAddr string
	var otelIngestErrorStatus bool
	var otelIngestHTTPStatusGte int
	var otelIngestLatencyMs int
	var otelIngestMinLogSeverity string
	var otelIngestMaxLogSignalsPerRequest int
	flag.StringVar(&otelIngestBindAddr, "otel-ingest-bind-address", "",
		"The address the OTLP/HTTP ingest server binds to (e.g. ':4319'). Empty disables the ingest server.")
	flag.BoolVar(&otelIngestErrorStatus, "otel-ingest-filter-error-status", true,
		"Emit OTelSpanError signals when a span carries STATUS_CODE_ERROR.")
	flag.IntVar(&otelIngestHTTPStatusGte, "otel-ingest-filter-http-status-gte", 500,
		"Emit OTelSpanError signals when an http.status_code attribute meets or exceeds this value (0 disables).")
	flag.IntVar(&otelIngestLatencyMs, "otel-ingest-filter-latency-ms", 5000,
		"Emit OTelSpanLatencySpike signals when span duration exceeds this threshold in milliseconds (0 disables).")
	flag.StringVar(&otelIngestMinLogSeverity, "otel-ingest-filter-min-log-severity", "WARN",
		"Minimum OTel log severity to ingest (TRACE|DEBUG|INFO|WARN|ERROR|FATAL).")
	flag.IntVar(&otelIngestMaxLogSignalsPerRequest, "otel-ingest-max-log-signals-per-request", 256,
		"Maximum OTelLogMatch signals emitted from one OTLP log request after deduplication. Set 0 to disable.")

	// Jaeger Query URL (Phase 2 Milestone D). When set, the incident graph
	// builder enriches topology with service-to-service edges resolved from
	// trace-ids captured on OTel signals. Empty disables trace enrichment;
	// the builder falls back to the signal-only graph.
	var jaegerQueryURL string
	flag.StringVar(&jaegerQueryURL, "jaeger-query-url", "",
		"Base URL of the Jaeger Query API (e.g. http://jaeger-query:16686). Empty disables trace enrichment.")

	flag.BoolVar(&enableHTTP2, "enable-http2", false,
		"If set, HTTP/2 will be enabled for the metrics and webhook servers")
	opts := zap.Options{
		Development: false,
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// if the enable-http2 flag is false (the default), http/2 should be disabled
	// due to its vulnerabilities. More specifically, disabling http/2 will
	// prevent from being vulnerable to the HTTP/2 Stream Cancellation and
	// Rapid Reset CVEs. For more information see:
	// - https://github.com/advisories/GHSA-qppj-fm5r-hxr3
	// - https://github.com/advisories/GHSA-4374-p667-p6c8
	disableHTTP2 := func(c *tls.Config) {
		setupLog.Info("Disabling HTTP/2")
		c.NextProtos = []string{"http/1.1"}
	}

	if !enableHTTP2 {
		tlsOpts = append(tlsOpts, disableHTTP2)
	}

	otelShutdown := setupOTel()
	defer func() {
		if err := otelShutdown(context.Background()); err != nil {
			setupLog.Error(err, "Failed to shutdown OpenTelemetry")
		}
	}()

	webhookServer := buildWebhookServer(tlsOpts, webhookCertPath, webhookCertName, webhookCertKey)
	metricsServerOptions := buildMetricsServerOptions(metricsAddr, secureMetrics, tlsOpts,
		metricsCertPath, metricsCertName, metricsCertKey)
	leaderElectionNamespace = resolveLeaderElectionNamespace(enableLeaderElection, leaderElectionNamespace)

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsServerOptions,
		WebhookServer:           webhookServer,
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "8faf7f69.rca-operator.tech",
		LeaderElectionNamespace: leaderElectionNamespace,
	})
	if err != nil {
		setupLog.Error(err, "Failed to start manager")
		os.Exit(1)
	}

	if enableWebhooks {
		setupWebhooks(mgr)
	} else {
		setupLog.Info("Admission webhooks disabled; invalid RCACorrelationRule specs " +
			"will not be rejected at admission time. Re-enable with --enable-webhooks=true.")
	}

	managerCtx := ctrl.SetupSignalHandler()

	// --- Register CRD Rule Engine Factory ---
	crdFactory := &rulengine.Factory{
		Client: mgr.GetClient(),
		Logger: ctrl.Log,
	}
	engine.RegisterRuleEngineFactory(crdFactory)

	// --- Signal channel + Incident Engine ---
	if signalBufferSize < 1 {
		signalBufferSize = 1
	}
	signals := make(chan collectors.Signal, signalBufferSize)
	signalEmitter := collectors.NewChannelSignalEmitterWithOptions(signals, ctrl.Log, signalEmitDedupWindow)
	incidentEngine, err := engine.NewIncidentEngine(
		mgr.GetClient(),
		signals,
		ctrl.Log,
		engine.WithContext(managerCtx),
		engine.WithEventRecorder(mgr.GetEventRecorder("rca-incident-engine")),
	)
	if err != nil {
		setupLog.Error(err, "Failed to create incident engine")
		os.Exit(1)
	}
	setupLog.Info("Incident engine created", "ruleEngine", incidentEngine.RuleEngineName(),
		"loadedRules", crdFactory.Engine.RuleCount(),
		"signalBufferSize", signalBufferSize,
		"signalEmitDedupWindow", signalEmitDedupWindow.String())
	if err := mgr.Add(incidentEngine); err != nil {
		setupLog.Error(err, "Failed to add incident engine")
		os.Exit(1)
	}

	// --- Auto-Detection ---
	if enableAutoDetect && crdFactory.Engine != nil {
		adCfg := autodetect.DefaultConfig()
		adCfg.Enabled = true
		adCfg.MinOccurrences = autoDetectMinOccurrences
		adCfg.MinTimeSpan = autoDetectMinTimeSpan
		adCfg.MaxAutoRules = autoDetectMaxRules
		adCfg.AnalysisInterval = autoDetectInterval
		adCfg.ExpiryDuration = autoDetectExpiry
		det := autodetect.NewDetector(crdFactory.Engine.Buffer(), mgr.GetClient(), adCfg, ctrl.Log)
		if err := mgr.Add(det); err != nil {
			setupLog.Error(err, "Failed to add auto-detector")
			os.Exit(1)
		}
		setupLog.Info("Auto-detection enabled",
			"interval", adCfg.AnalysisInterval,
			"minOccurrences", adCfg.MinOccurrences,
			"minTimeSpan", adCfg.MinTimeSpan,
			"maxRules", adCfg.MaxAutoRules,
		)
	}

	var jaegerClient *jaeger.Client
	if jaegerQueryURL != "" {
		jaegerClient = jaeger.New(jaegerQueryURL)
		setupLog.Info("Jaeger query enrichment enabled", "url", jaegerQueryURL)
	} else {
		setupLog.Info("Jaeger query enrichment disabled (pass --jaeger-query-url to enable)")
	}

	dashboardServer := dashboard.NewServer(mgr.GetClient(), dashboardAddr, ctrl.Log)
	if k8sClient, err := kubernetes.NewForConfig(mgr.GetConfig()); err == nil {
		dashboardServer.WithOptions(dashboard.WithKubernetesClient(k8sClient))
	} else {
		setupLog.Error(err, "Failed to build kubernetes client for dashboard; /api/logs will be unavailable")
	}
	if crdFactory.Engine != nil {
		dashboardServer.WithOptions(dashboard.WithBuffer(crdFactory.Engine.Buffer()))
	}
	if jaegerClient != nil {
		dashboardServer.WithOptions(dashboard.WithJaegerClient(jaegerClient))
	}
	if err := mgr.Add(dashboardServer); err != nil {
		setupLog.Error(err, "Failed to add dashboard server")
		os.Exit(1)
	}

	setupOTLPIngest(mgr, signalEmitter, otelIngestBindAddr, otelIngestErrorStatus,
		otelIngestHTTPStatusGte, otelIngestLatencyMs, otelIngestMinLogSeverity,
		otelIngestMaxLogSignalsPerRequest)

	var graphBuilder controller.IncidentGraphBuilder
	if crdFactory.Engine != nil {
		graphBuilder = graph.NewBuilder(crdFactory.Engine.Buffer(), jaegerClient, ctrl.Log)
	}

	setupControllers(mgr, crdFactory, signalEmitter, managerCtx, graphBuilder)
	// +kubebuilder:scaffold:builder

	setupHealthChecks(mgr)

	setupLog.Info("Starting manager")
	if err := mgr.Start(managerCtx); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}

// setupOTel initializes the OpenTelemetry SDK from environment configuration
// and returns a shutdown function the caller must defer.
func setupOTel() func(context.Context) error {
	otelEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	otelShutdown, err := rcaotel.Setup(context.Background(), rcaotel.Config{
		Endpoint:     otelEndpoint,
		ServiceName:  envOrDefault("OTEL_SERVICE_NAME", "rca-operator"),
		SamplingRate: envFloatOrDefault("OTEL_SAMPLING_RATE", 1.0),
		Insecure:     envBoolOrDefault("OTEL_EXPORTER_OTLP_INSECURE", true),
	})
	if err != nil {
		setupLog.Error(err, "Failed to initialize OpenTelemetry")
		os.Exit(1)
	}
	if otelEndpoint != "" {
		setupLog.Info("OpenTelemetry initialized", "endpoint", otelEndpoint)
	}
	return otelShutdown
}

func buildWebhookServer(tlsOpts []func(*tls.Config), certPath, certName, certKey string) webhook.Server {
	opts := webhook.Options{TLSOpts: tlsOpts}
	if len(certPath) > 0 {
		setupLog.Info("Initializing webhook certificate loader using provided certificates",
			"webhook-cert-path", certPath, "webhook-cert-name", certName, "webhook-cert-key", certKey)
		opts.CertDir = certPath
		opts.CertName = certName
		opts.KeyName = certKey
	}
	return webhook.NewServer(opts)
}

func buildMetricsServerOptions(addr string, secure bool, tlsOpts []func(*tls.Config),
	certPath, certName, certKey string) metricsserver.Options {
	opts := metricsserver.Options{
		BindAddress:   addr,
		SecureServing: secure,
		TLSOpts:       tlsOpts,
	}
	if secure {
		opts.FilterProvider = filters.WithAuthenticationAndAuthorization
	}
	if len(certPath) > 0 {
		setupLog.Info("Initializing metrics certificate loader using provided certificates",
			"metrics-cert-path", certPath, "metrics-cert-name", certName, "metrics-cert-key", certKey)
		opts.CertDir = certPath
		opts.CertName = certName
		opts.KeyName = certKey
	}
	return opts
}

// resolveLeaderElectionNamespace fills in the leader-election namespace when
// the operator is running out-of-cluster (e.g. `make run`) and no explicit
// namespace was provided via --leader-election-namespace or POD_NAMESPACE.
func resolveLeaderElectionNamespace(enable bool, ns string) string {
	if !enable || ns != "" {
		return ns
	}
	if podNamespace := os.Getenv("POD_NAMESPACE"); podNamespace != "" {
		setupLog.Info("Using leader election namespace from POD_NAMESPACE", "namespace", podNamespace)
		return podNamespace
	}
	if _, err := rest.InClusterConfig(); err != nil {
		setupLog.Info("Defaulting leader election namespace for out-of-cluster run",
			"namespace", "default",
			"hint", "override with --leader-election-namespace",
		)
		return "default"
	}
	return ns
}

func setupWebhooks(mgr ctrl.Manager) {
	if err := rcawebhook.SetupRCAAgentWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create RCAAgent webhook")
		os.Exit(1)
	}
	if err := rcawebhook.SetupRCACorrelationRuleWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create RCACorrelationRule webhook")
		os.Exit(1)
	}
	setupLog.Info("Admission webhooks enabled")
}

// setupOTLPIngest registers the OTLP/HTTP ingest server with the manager when
// a bind address is configured. The DaemonSet OTel collector fans out filtered
// error spans and warn logs to this endpoint; the ingest server turns them
// into watcher.CorrelatorEvents and writes them to the same signals channel
// as K8s-event watchers.
func setupOTLPIngest(mgr ctrl.Manager, emitter collectors.SignalEmitter, bindAddr string,
	errorStatus bool, httpStatusGte, latencyMs int, minLogSeverity string, maxLogSignals int) {
	if bindAddr == "" {
		setupLog.Info("OTLP ingest server disabled (pass --otel-ingest-bind-address to enable)")
		return
	}
	ingestCfg := otelingest.DefaultConfig()
	ingestCfg.BindAddress = bindAddr
	ingestCfg.TraceFilters.StatusCodeERROR = errorStatus
	ingestCfg.TraceFilters.HTTPStatusGte = httpStatusGte
	ingestCfg.TraceFilters.LatencyP99Ms = latencyMs
	ingestCfg.LogFilters.MinSeverity = minLogSeverity
	ingestCfg.LogFilters.MaxSignalsPerRequest = maxLogSignals
	ingestCfg.Redaction = nil
	ingestServer := otelingest.NewServer(ingestCfg, emitter, ctrl.Log)
	if err := mgr.Add(ingestServer); err != nil {
		setupLog.Error(err, "Failed to add OTLP ingest server")
		os.Exit(1)
	}
	setupLog.Info("OTLP ingest server registered",
		"bindAddress", ingestCfg.BindAddress,
		"errorStatus", ingestCfg.TraceFilters.StatusCodeERROR,
		"httpStatusGte", ingestCfg.TraceFilters.HTTPStatusGte,
		"latencyMs", ingestCfg.TraceFilters.LatencyP99Ms,
		"minLogSeverity", ingestCfg.LogFilters.MinSeverity,
		"maxLogSignalsPerRequest", ingestCfg.LogFilters.MaxSignalsPerRequest,
	)
}

func setupControllers(mgr ctrl.Manager, crdFactory *rulengine.Factory, emitter collectors.SignalEmitter,
	managerCtx context.Context, graphBuilder controller.IncidentGraphBuilder) {
	if err := (&controller.RCAAgentReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		Cache:          mgr.GetCache(),
		SignalEmitter:  emitter,
		ManagerContext: managerCtx,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "RCAAgent")
		os.Exit(1)
	}
	if err := (&controller.IncidentReportReconciler{
		Client:       mgr.GetClient(),
		Scheme:       mgr.GetScheme(),
		Recorder:     mgr.GetEventRecorder("incidentreport-controller"),
		Notifier:     notify.NewDispatcher(mgr.GetClient(), ctrl.Log),
		GraphBuilder: graphBuilder,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "IncidentReport")
		os.Exit(1)
	}
	if err := (&controller.RCACorrelationRuleReconciler{
		Client:  mgr.GetClient(),
		Factory: crdFactory,
		Log:     ctrl.Log,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "RCACorrelationRule")
		os.Exit(1)
	}
}

func setupHealthChecks(mgr ctrl.Manager) {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envFloatOrDefault(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseFloat(v, 64)
	if err != nil {
		setupLog.Info("Ignoring invalid float environment value", "name", key, "value", v)
		return fallback
	}
	return parsed
}

func envBoolOrDefault(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(v)
	if err != nil {
		setupLog.Info("Ignoring invalid boolean environment value", "name", key, "value", v)
		return fallback
	}
	return parsed
}
