package correlator

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

// Signal type constants for weak signal matching.
const (
	signalTypeCrashLoop            = "CrashLoop"
	signalTypeBadDeploy            = "BadDeploy"
	signalTypeGracePeriodViolation = "GracePeriodViolation"
	signalTypeOOM                  = "OOM"
	signalTypeRegistry             = "Registry"
	signalTypeNodeFailure          = "NodeFailure"
	signalTypePodEvicted           = "PodEvicted"
	signalTypeProbeFailure         = "ProbeFailure"
	signalTypeNodePressure         = "NodePressure"
	signalTypeCPUThrottling        = "CPUThrottling"
)

// AnomalyResult represents an auto-detected failure pattern.
type AnomalyResult struct {
	Detected   bool
	Category   string   // "ExitCodePattern", "ConsecutiveExitCode", "FrequencySpike", "WeakSignalCombo"
	RootCause  string   // Human-readable hypothesis
	Confidence string   // "Low", "Medium", "High"
	Severity   string   // Suggested severity (P1-P4)
	Evidence   []string // Supporting signals
	Resource   string   // Override for dedup (e.g., "ns:production" for namespace-scoped)
}

// RootCauseHypothesis maps an exit code category to a root cause explanation.
type RootCauseHypothesis struct {
	RootCause  string
	Confidence string
}

// exitCodeRootCauses maps categories (from pod_watcher.classifyExitCode) to root cause hypotheses.
// This EXTENDS the existing classification in pod_watcher.go - no duplication of exit code mapping.
var exitCodeRootCauses = map[string]RootCauseHypothesis{
	"GeneralError":      {RootCause: "Application crashed with general error - check application logs", Confidence: "Low"},
	"ShellMisuse":       {RootCause: "Invalid shell command or syntax error in entrypoint", Confidence: "Medium"},
	"PermissionDenied":  {RootCause: "Container entrypoint exists but is not executable (check file permissions)", Confidence: "High"},
	"CommandNotFound":   {RootCause: "Container entrypoint command not found (check image or PATH)", Confidence: "High"},
	"Interrupted":       {RootCause: "Process received SIGINT (manual interruption or orchestration issue)", Confidence: "Medium"},
	"Abort":             {RootCause: "Process aborted (SIGABRT) - likely assertion failure or memory corruption", Confidence: "Medium"},
	"SegmentationFault": {RootCause: "Segmentation fault (SIGSEGV) - memory access violation in application", Confidence: "High"},
	"Terminated":        {RootCause: "Process terminated by SIGTERM - check if terminationGracePeriodSeconds is sufficient", Confidence: "Medium"},
	"OutOfRange":        {RootCause: "Exit code out of valid range - possible shell or script issue", Confidence: "Low"},
	"NonZeroExit":       {RootCause: "Application exited with non-zero code - review application logs", Confidence: "Low"},
}

// weakSignalCombo defines a combination of weak signals that together indicate a root cause.
type weakSignalCombo struct {
	Signals    []string
	RootCause  string
	Confidence string
}

// weakSignalCombos defines known weak signal combinations.
var weakSignalCombos = []weakSignalCombo{
	{
		Signals:    []string{signalTypeCPUThrottling, signalTypeProbeFailure},
		RootCause:  "CPU throttling caused probe timeout - increase CPU limits or adjust probe timeouts",
		Confidence: "High",
	},
	{
		Signals:    []string{signalTypeCrashLoop, signalTypeNodePressure},
		RootCause:  "Pod crashing on resource-pressured node - consider pod anti-affinity or resource limits",
		Confidence: "Medium",
	},
	{
		Signals:    []string{signalTypeBadDeploy, signalTypeNodePressure},
		RootCause:  "Pod cannot be scheduled due to cluster resource pressure - scale cluster or reduce requests",
		Confidence: "High",
	},
}

// exitCodeEntry tracks a single exit code occurrence for consecutive analysis.
type exitCodeEntry struct {
	Code      int32
	Timestamp time.Time
	PodName   string
}

// ExitCodeStats tracks recent exit codes per pod for consecutive pattern detection.
type ExitCodeStats struct {
	mu              sync.Mutex
	recentExitCodes map[string][]exitCodeEntry // key: namespace/pod
	window          time.Duration
}

// newExitCodeStats creates a new exit code statistics tracker.
func newExitCodeStats(window time.Duration) *ExitCodeStats {
	return &ExitCodeStats{
		recentExitCodes: make(map[string][]exitCodeEntry),
		window:          window,
	}
}

// record adds an exit code occurrence for a pod.
func (s *ExitCodeStats) record(namespace, podName string, code int32, timestamp time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := namespace + "/" + podName
	s.recentExitCodes[key] = append(s.recentExitCodes[key], exitCodeEntry{
		Code:      code,
		Timestamp: timestamp,
		PodName:   podName,
	})

	// Prune old entries
	cutoff := timestamp.Add(-s.window)
	entries := s.recentExitCodes[key]
	i := 0
	for i < len(entries) && entries[i].Timestamp.Before(cutoff) {
		i++
	}
	s.recentExitCodes[key] = entries[i:]
}

// getRecent returns exit code entries within the time window for a pod.
func (s *ExitCodeStats) getRecent(namespace, podName string, now time.Time) []exitCodeEntry {
	s.mu.Lock()
	defer s.mu.Unlock()

	key := namespace + "/" + podName
	entries := s.recentExitCodes[key]
	cutoff := now.Add(-s.window)

	var result []exitCodeEntry
	for _, e := range entries {
		if !e.Timestamp.Before(cutoff) {
			result = append(result, e)
		}
	}
	return result
}

// AnomalyDetector analyzes events that don't match correlation rules to detect
// unknown failure patterns automatically.
type AnomalyDetector struct {
	buffer    *Buffer
	exitStats *ExitCodeStats
	log       logr.Logger
	now       func() time.Time
}

// NewAnomalyDetector creates a new anomaly detector that uses the given buffer
// for frequency and weak signal analysis.
func NewAnomalyDetector(buffer *Buffer, logger logr.Logger) *AnomalyDetector {
	return &AnomalyDetector{
		buffer:    buffer,
		exitStats: newExitCodeStats(5 * time.Minute),
		log:       logger.WithName("anomaly-detector"),
		now:       time.Now,
	}
}

// Analyze examines an event and returns an AnomalyResult if a pattern is detected.
// Analysis is performed in priority order:
// 1. Consecutive exit code (same code 3+ times - specific, high confidence)
// 2. Weak signal combinations (multiple signals together)
// 3. Frequency spike (namespace-wide issues)
// 4. Exit code pattern (single event classification)
func (a *AnomalyDetector) Analyze(event watcher.CorrelatorEvent) AnomalyResult {
	// Track exit codes for consecutive analysis
	a.trackExitCode(event)

	// Priority 1: Check for consecutive same exit code (most specific)
	if result := a.analyzeConsecutiveExits(event); result.Detected {
		a.log.Info("Anomaly detected: consecutive exit codes",
			"category", result.Category,
			"confidence", result.Confidence,
			"rootCause", result.RootCause,
		)
		return result
	}

	// Priority 2: Check for weak signal combinations
	if result := a.analyzeWeakSignalCombo(event); result.Detected {
		a.log.Info("Anomaly detected: weak signal combination",
			"category", result.Category,
			"confidence", result.Confidence,
			"rootCause", result.RootCause,
		)
		return result
	}

	// Priority 3: Check for frequency spike (namespace-wide)
	if result := a.analyzeFrequencySpike(event); result.Detected {
		a.log.Info("Anomaly detected: frequency spike",
			"category", result.Category,
			"confidence", result.Confidence,
			"rootCause", result.RootCause,
		)
		return result
	}

	// Priority 4: Exit code pattern (catch-all for classified exit codes)
	if result := a.analyzeExitCode(event); result.Detected {
		a.log.Info("Anomaly detected: exit code pattern",
			"category", result.Category,
			"confidence", result.Confidence,
			"rootCause", result.RootCause,
		)
		return result
	}

	return AnomalyResult{}
}

// trackExitCode records exit codes for consecutive pattern analysis.
func (a *AnomalyDetector) trackExitCode(event watcher.CorrelatorEvent) {
	crash, ok := event.(watcher.CrashLoopBackOffEvent)
	if !ok || crash.LastExitCode == 0 {
		return
	}
	a.exitStats.record(crash.Namespace, crash.PodName, crash.LastExitCode, a.now())
}

// analyzeExitCode detects failure patterns based on exit code classification.
// REUSES: ExitCodeCategory from CrashLoopBackOffEvent (already set by pod_watcher.classifyExitCode)
func (a *AnomalyDetector) analyzeExitCode(event watcher.CorrelatorEvent) AnomalyResult {
	crash, ok := event.(watcher.CrashLoopBackOffEvent)
	if !ok || crash.LastExitCode == 0 {
		return AnomalyResult{}
	}

	// REUSE: ExitCodeCategory is already set by pod_watcher using classifyExitCode()
	if crash.ExitCodeCategory == "" {
		return AnomalyResult{}
	}

	// Look up root cause from category (no duplicate exit code mapping)
	hypothesis, exists := exitCodeRootCauses[crash.ExitCodeCategory]
	if !exists {
		// Unknown category - provide generic guidance
		hypothesis = RootCauseHypothesis{
			RootCause:  fmt.Sprintf("Application exited with code %d - review application logs for details", crash.LastExitCode),
			Confidence: "Low",
		}
	}

	return AnomalyResult{
		Detected:   true,
		Category:   "ExitCodePattern",
		RootCause:  hypothesis.RootCause,
		Confidence: hypothesis.Confidence,
		Severity:   a.severityFromConfidence(hypothesis.Confidence),
		Evidence:   []string{fmt.Sprintf("Exit code %d (%s): %s", crash.LastExitCode, crash.ExitCodeCategory, crash.ExitCodeDescription)},
	}
}

// analyzeConsecutiveExits detects repeated crashes with the same exit code.
func (a *AnomalyDetector) analyzeConsecutiveExits(event watcher.CorrelatorEvent) AnomalyResult {
	crash, ok := event.(watcher.CrashLoopBackOffEvent)
	if !ok || crash.LastExitCode == 0 {
		return AnomalyResult{}
	}

	codes := a.exitStats.getRecent(crash.Namespace, crash.PodName, a.now())

	// Count consecutive same exit code (including current)
	sameCount := 0
	for i := len(codes) - 1; i >= 0; i-- {
		if codes[i].Code == crash.LastExitCode {
			sameCount++
		} else {
			break
		}
	}

	// Threshold: 3 or more consecutive crashes with same exit code
	if sameCount >= 3 {
		// Get the category for this exit code
		category := crash.ExitCodeCategory
		if category == "" {
			category = "Unknown"
		}

		rootCause := fmt.Sprintf("Pod crashed %d times consecutively with exit code %d (%s) - persistent issue, not transient",
			sameCount, crash.LastExitCode, category)

		return AnomalyResult{
			Detected:   true,
			Category:   "ConsecutiveExitCode",
			RootCause:  rootCause,
			Confidence: "High",
			Severity:   "P2", // Escalate because persistent issue
			Evidence: []string{
				fmt.Sprintf("%d consecutive crashes with exit code %d", sameCount, crash.LastExitCode),
				fmt.Sprintf("Category: %s", category),
			},
		}
	}

	return AnomalyResult{}
}

// analyzeFrequencySpike detects unusual failure rates in a namespace.
func (a *AnomalyDetector) analyzeFrequencySpike(event watcher.CorrelatorEvent) AnomalyResult {
	if a.buffer == nil {
		return AnomalyResult{}
	}

	entries := a.buffer.snapshot()
	eventNS := extractNamespace(event)
	if eventNS == "" {
		return AnomalyResult{}
	}

	// Group failures by namespace
	nsFailures := make(map[string]int)
	nsPods := make(map[string]map[string]bool) // namespace -> pod names

	for _, e := range entries {
		if isFailureEvent(e.event) {
			ns := extractNamespace(e.event)
			if ns == "" {
				continue
			}
			nsFailures[ns]++

			// Track distinct pods
			if nsPods[ns] == nil {
				nsPods[ns] = make(map[string]bool)
			}
			pod := extractPodName(e.event)
			if pod != "" {
				nsPods[ns][pod] = true
			}
		}
	}

	// Threshold: 5+ failures in a namespace within the correlation window
	if nsFailures[eventNS] >= 5 {
		distinctPods := len(nsPods[eventNS])

		rootCause := fmt.Sprintf("Namespace %s has %d failures affecting %d pods in correlation window - possible cluster/infrastructure issue",
			eventNS, nsFailures[eventNS], distinctPods)

		return AnomalyResult{
			Detected:   true,
			Category:   "FrequencySpike",
			RootCause:  rootCause,
			Confidence: "Medium",
			Severity:   "P2",
			Evidence: []string{
				fmt.Sprintf("%d failures in namespace %s", nsFailures[eventNS], eventNS),
				fmt.Sprintf("%d distinct pods affected", distinctPods),
			},
			// Use namespace as Resource so all pods in same namespace
			// share ONE FrequencySpike incident (dedup at namespace level)
			Resource: "ns:" + eventNS,
		}
	}

	return AnomalyResult{}
}

// analyzeWeakSignalCombo detects combinations of weak signals that together indicate an issue.
func (a *AnomalyDetector) analyzeWeakSignalCombo(event watcher.CorrelatorEvent) AnomalyResult {
	if a.buffer == nil {
		return AnomalyResult{}
	}

	entries := a.buffer.snapshot()
	eventNS := extractNamespace(event)
	eventPod := extractPodName(event)
	eventNode := extractNodeName(event)

	// Collect signal types present in buffer for same pod/node
	presentSignals := make(map[string]bool)

	// Add current event's type
	currentType := signalTypeFromEvent(event)
	if currentType != "" {
		presentSignals[currentType] = true
	}

	// Check buffer for related signals
	for _, e := range entries {
		entryNS := extractNamespace(e.event)
		entryPod := extractPodName(e.event)
		entryNode := extractNodeName(e.event)

		// Consider signals from same pod OR same node as related
		if (eventPod != "" && entryPod == eventPod && entryNS == eventNS) ||
			(eventNode != "" && entryNode == eventNode) {
			signalType := signalTypeFromEvent(e.event)
			if signalType != "" {
				presentSignals[signalType] = true
			}
		}
	}

	// Check for matching weak signal combinations
	for _, combo := range weakSignalCombos {
		if containsAllSignals(presentSignals, combo.Signals) {
			return AnomalyResult{
				Detected:   true,
				Category:   "WeakSignalCombo",
				RootCause:  combo.RootCause,
				Confidence: combo.Confidence,
				Severity:   a.severityFromConfidence(combo.Confidence),
				Evidence:   combo.Signals,
			}
		}
	}

	return AnomalyResult{}
}

// severityFromConfidence maps confidence levels to severity.
func (a *AnomalyDetector) severityFromConfidence(confidence string) string {
	switch confidence {
	case "High":
		return "P2"
	case "Medium":
		return "P3"
	default:
		return "P3"
	}
}

// Helper functions

// extractNamespace gets the namespace from any event type.
func extractNamespace(event watcher.CorrelatorEvent) string {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		return e.Namespace
	case watcher.OOMKilledEvent:
		return e.Namespace
	case watcher.ImagePullBackOffEvent:
		return e.Namespace
	case watcher.PodPendingTooLongEvent:
		return e.Namespace
	case watcher.GracePeriodViolationEvent:
		return e.Namespace
	case watcher.NodeNotReadyEvent:
		return e.Namespace
	case watcher.PodEvictedEvent:
		return e.Namespace
	case watcher.ProbeFailureEvent:
		return e.Namespace
	case watcher.StalledRolloutEvent:
		return e.Namespace
	case watcher.NodePressureEvent:
		return e.Namespace
	case watcher.CPUThrottlingEvent:
		return e.Namespace
	case watcher.PodHealthyEvent:
		return e.Namespace
	case watcher.PodDeletedEvent:
		return e.Namespace
	default:
		return ""
	}
}

// extractPodName gets the pod name from any event type.
func extractPodName(event watcher.CorrelatorEvent) string {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		return e.PodName
	case watcher.OOMKilledEvent:
		return e.PodName
	case watcher.ImagePullBackOffEvent:
		return e.PodName
	case watcher.PodPendingTooLongEvent:
		return e.PodName
	case watcher.GracePeriodViolationEvent:
		return e.PodName
	case watcher.PodEvictedEvent:
		return e.PodName
	case watcher.ProbeFailureEvent:
		return e.PodName
	case watcher.CPUThrottlingEvent:
		return e.PodName
	case watcher.PodHealthyEvent:
		return e.PodName
	case watcher.PodDeletedEvent:
		return e.PodName
	default:
		return ""
	}
}

// extractNodeName gets the node name from any event type.
func extractNodeName(event watcher.CorrelatorEvent) string {
	switch e := event.(type) {
	case watcher.CrashLoopBackOffEvent:
		return e.NodeName
	case watcher.OOMKilledEvent:
		return e.NodeName
	case watcher.NodeNotReadyEvent:
		return e.NodeName
	case watcher.PodEvictedEvent:
		return e.NodeName
	case watcher.NodePressureEvent:
		return e.NodeName
	default:
		return ""
	}
}

// signalTypeFromEvent returns a simplified signal type for weak signal matching.
func signalTypeFromEvent(event watcher.CorrelatorEvent) string {
	switch event.(type) {
	case watcher.CrashLoopBackOffEvent:
		return signalTypeCrashLoop
	case watcher.OOMKilledEvent:
		return signalTypeOOM
	case watcher.ImagePullBackOffEvent:
		return signalTypeRegistry
	case watcher.PodPendingTooLongEvent:
		return signalTypeBadDeploy
	case watcher.GracePeriodViolationEvent:
		return signalTypeGracePeriodViolation
	case watcher.NodeNotReadyEvent:
		return signalTypeNodeFailure
	case watcher.PodEvictedEvent:
		return signalTypePodEvicted
	case watcher.ProbeFailureEvent:
		return signalTypeProbeFailure
	case watcher.StalledRolloutEvent:
		return signalTypeBadDeploy
	case watcher.NodePressureEvent:
		return signalTypeNodePressure
	case watcher.CPUThrottlingEvent:
		return signalTypeCPUThrottling
	default:
		return ""
	}
}

// isFailureEvent returns true if the event represents a failure condition.
func isFailureEvent(event watcher.CorrelatorEvent) bool {
	switch event.(type) {
	case watcher.CrashLoopBackOffEvent,
		watcher.OOMKilledEvent,
		watcher.ImagePullBackOffEvent,
		watcher.PodPendingTooLongEvent,
		watcher.GracePeriodViolationEvent,
		watcher.NodeNotReadyEvent,
		watcher.PodEvictedEvent,
		watcher.ProbeFailureEvent,
		watcher.StalledRolloutEvent,
		watcher.NodePressureEvent,
		watcher.CPUThrottlingEvent:
		return true
	default:
		return false
	}
}

// containsAllSignals checks if all required signals are present.
func containsAllSignals(present map[string]bool, required []string) bool {
	for _, sig := range required {
		if !present[sig] {
			return false
		}
	}
	return true
}
