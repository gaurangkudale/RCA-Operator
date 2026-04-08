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
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
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
	"github.com/gaurangkudale/rca-operator/internal/correlator"
	"github.com/gaurangkudale/rca-operator/internal/dashboard"
	"github.com/gaurangkudale/rca-operator/internal/engine"
	"github.com/gaurangkudale/rca-operator/internal/notify"
	rcaotel "github.com/gaurangkudale/rca-operator/internal/otel"
	"github.com/gaurangkudale/rca-operator/internal/rulengine"
	"github.com/gaurangkudale/rca-operator/internal/telemetry"
	"github.com/gaurangkudale/rca-operator/internal/topology"
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

// nolint:gocyclo
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
		"Enable admission webhooks for RCAAgent and RCACorrelationRule validation.")
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

	var telemetryBackend string
	var signozEndpoint string
	var jaegerEndpoint string
	var prometheusEndpoint string
	var topologyRefreshInterval time.Duration
	var topologyDependencyWindow time.Duration
	flag.StringVar(&telemetryBackend, "telemetry-backend", "",
		"Telemetry backend type: signoz, jaeger, or composite. Empty disables telemetry queries.")
	flag.StringVar(&signozEndpoint, "signoz-endpoint", "",
		"SigNoz query service URL (e.g. http://signoz-query-service:8080). Used when telemetry-backend=signoz.")
	flag.StringVar(&jaegerEndpoint, "jaeger-endpoint", "",
		"Jaeger query HTTP API URL (e.g. http://jaeger-query:16686). Used when telemetry-backend=jaeger or composite.")
	flag.StringVar(&prometheusEndpoint, "prometheus-endpoint", "",
		"Prometheus HTTP API URL (e.g. http://prometheus:9090). Used when telemetry-backend=composite.")
	flag.DurationVar(&topologyRefreshInterval, "topology-refresh-interval", 30*time.Second,
		"How often to refresh the topology graph cache.")
	flag.DurationVar(&topologyDependencyWindow, "topology-dependency-window", 15*time.Minute,
		"Time window for querying service dependencies.")

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

	// --- OTel Setup ---
	otelEndpoint := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	otelShutdown, err := rcaotel.Setup(context.Background(), rcaotel.Config{
		Endpoint:     otelEndpoint,
		ServiceName:  "rca-operator",
		SamplingRate: 1.0,
		Insecure:     true,
	})
	if err != nil {
		setupLog.Error(err, "Failed to initialize OpenTelemetry")
		os.Exit(1)
	}
	defer func() {
		if err := otelShutdown(context.Background()); err != nil {
			setupLog.Error(err, "Failed to shutdown OpenTelemetry")
		}
	}()
	if otelEndpoint != "" {
		setupLog.Info("OpenTelemetry initialized", "endpoint", otelEndpoint)
	}

	// Initial webhook TLS options
	webhookTLSOpts := tlsOpts
	webhookServerOptions := webhook.Options{
		TLSOpts: webhookTLSOpts,
	}

	if len(webhookCertPath) > 0 {
		setupLog.Info("Initializing webhook certificate loader using provided certificates",
			"webhook-cert-path", webhookCertPath, "webhook-cert-name", webhookCertName, "webhook-cert-key", webhookCertKey)

		webhookServerOptions.CertDir = webhookCertPath
		webhookServerOptions.CertName = webhookCertName
		webhookServerOptions.KeyName = webhookCertKey
	}

	webhookServer := webhook.NewServer(webhookServerOptions)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	metricsServerOptions := metricsserver.Options{
		BindAddress:   metricsAddr,
		SecureServing: secureMetrics,
		TLSOpts:       tlsOpts,
	}

	if secureMetrics {
		metricsServerOptions.FilterProvider = filters.WithAuthenticationAndAuthorization
	}

	if len(metricsCertPath) > 0 {
		setupLog.Info("Initializing metrics certificate loader using provided certificates",
			"metrics-cert-path", metricsCertPath, "metrics-cert-name", metricsCertName, "metrics-cert-key", metricsCertKey)

		metricsServerOptions.CertDir = metricsCertPath
		metricsServerOptions.CertName = metricsCertName
		metricsServerOptions.KeyName = metricsCertKey
	}

	if enableLeaderElection && leaderElectionNamespace == "" {
		if podNamespace := os.Getenv("POD_NAMESPACE"); podNamespace != "" {
			leaderElectionNamespace = podNamespace
			setupLog.Info("Using leader election namespace from POD_NAMESPACE", "namespace", leaderElectionNamespace)
		} else if _, err := rest.InClusterConfig(); err != nil {
			// Out-of-cluster runs (for example `make run`) cannot auto-detect a
			// namespace for the lease object. Default to `default` unless a
			// namespace is explicitly provided via flag or POD_NAMESPACE.
			leaderElectionNamespace = "default"
			setupLog.Info("Defaulting leader election namespace for out-of-cluster run",
				"namespace", leaderElectionNamespace,
				"hint", "override with --leader-election-namespace",
			)
		}
	}

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

	// --- Webhooks ---
	if enableWebhooks {
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

	managerCtx := ctrl.SetupSignalHandler()

	// --- Register CRD Rule Engine Factory ---
	crdFactory := &rulengine.Factory{
		Client: mgr.GetClient(),
		Logger: ctrl.Log,
	}
	engine.RegisterRuleEngineFactory(crdFactory)

	// --- Signal channel ---
	signals := make(chan collectors.Signal, 1024)
	signalEmitter := collectors.NewChannelSignalEmitter(signals, ctrl.Log)

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
		go det.Run(managerCtx)
		setupLog.Info("Auto-detection enabled",
			"interval", adCfg.AnalysisInterval,
			"minOccurrences", adCfg.MinOccurrences,
			"minTimeSpan", adCfg.MinTimeSpan,
			"maxRules", adCfg.MaxAutoRules,
		)
	}

	// --- Telemetry Querier ---
	var querier telemetry.TelemetryQuerier
	switch telemetryBackend {
	case "signoz":
		if signozEndpoint != "" {
			querier = telemetry.NewSigNozClient(signozEndpoint, nil)
			setupLog.Info("Telemetry backend: SigNoz", "endpoint", signozEndpoint)
		}
	case "jaeger":
		if jaegerEndpoint != "" {
			querier = telemetry.NewJaegerClient(jaegerEndpoint, nil)
			setupLog.Info("Telemetry backend: Jaeger", "endpoint", jaegerEndpoint)
		}
	case "composite":
		var traces, metrics telemetry.TelemetryQuerier
		if jaegerEndpoint != "" {
			traces = telemetry.NewJaegerClient(jaegerEndpoint, nil)
			setupLog.Info("Composite: traces via Jaeger", "endpoint", jaegerEndpoint)
		}
		if prometheusEndpoint != "" {
			metrics = telemetry.NewPrometheusClient(prometheusEndpoint, nil)
			setupLog.Info("Composite: metrics via Prometheus", "endpoint", prometheusEndpoint)
		}
		querier = telemetry.NewCompositeQuerier(traces, metrics, nil)
		setupLog.Info("Telemetry backend: Composite")
	}
	if querier == nil {
		querier = &telemetry.NoopQuerier{}
	}

	// --- Topology Cache ---
	topoBuilder := topology.NewBuilder(querier, ctrl.Log.WithName("topology"))
	topoCache := topology.NewCache(topoBuilder, ctrl.Log.WithName("topology-cache"),
		topology.WithTTL(topologyRefreshInterval),
		topology.WithDependencyWindow(topologyDependencyWindow),
	)
	if telemetryBackend != "" {
		topoCache.StartBackgroundRefresh(managerCtx)
		setupLog.Info("Topology cache started",
			"refreshInterval", topologyRefreshInterval,
			"dependencyWindow", topologyDependencyWindow,
		)
	}

	dashboardServer := dashboard.NewServer(mgr.GetClient(), dashboardAddr, ctrl.Log,
		dashboard.WithTelemetryQuerier(querier),
		dashboard.WithTopologyCache(topoCache),
	)
	if err := mgr.Add(dashboardServer); err != nil {
		setupLog.Error(err, "Failed to add dashboard server")
		os.Exit(1)
	}

	// --- Cross-Signal Enricher + Incident Engine ---
	crossSignalEnricher := correlator.NewCrossSignalEnricher(
		querier, topoCache, mgr.GetClient(), ctrl.Log,
	)
	incidentEngine, err := engine.NewIncidentEngine(
		mgr.GetClient(),
		signals,
		ctrl.Log,
		engine.WithContext(managerCtx),
		engine.WithEventRecorder(mgr.GetEventRecorder("rca-incident-engine")),
		engine.WithCrossSignalEnricher(crossSignalEnricher),
	)
	if err != nil {
		setupLog.Error(err, "Failed to create incident engine")
		os.Exit(1)
	}
	setupLog.Info("Incident engine created", "ruleEngine", incidentEngine.RuleEngineName(),
		"loadedRules", crdFactory.Engine.RuleCount())
	if err := mgr.Add(incidentEngine); err != nil {
		setupLog.Error(err, "Failed to add incident engine")
		os.Exit(1)
	}

	if err := (&controller.RCAAgentReconciler{
		Client:         mgr.GetClient(),
		Scheme:         mgr.GetScheme(),
		Cache:          mgr.GetCache(),
		SignalEmitter:  signalEmitter,
		ManagerContext: managerCtx,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "Failed to create controller", "controller", "RCAAgent")
		os.Exit(1)
	}
	if err := (&controller.IncidentReportReconciler{
		Client:   mgr.GetClient(),
		Scheme:   mgr.GetScheme(),
		Recorder: mgr.GetEventRecorder("incidentreport-controller"),
		Notifier: notify.NewDispatcher(mgr.GetClient(), ctrl.Log),
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
	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "Failed to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(managerCtx); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}
