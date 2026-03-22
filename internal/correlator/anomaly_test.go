package correlator

import (
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/gaurangkudale/rca-operator/internal/watcher"
)

func TestAnomalyDetector_AnalyzeExitCode(t *testing.T) {
	tests := []struct {
		name       string
		event      watcher.CorrelatorEvent
		wantFired  bool
		wantCat    string
		wantConf   string
		wantSubstr string // substring in rootCause
	}{
		{
			name: "exit code 127 CommandNotFound",
			event: watcher.CrashLoopBackOffEvent{
				BaseEvent:           watcher.BaseEvent{Namespace: "default", PodName: "test-pod"},
				LastExitCode:        127,
				ExitCodeCategory:    "CommandNotFound",
				ExitCodeDescription: "command not found",
			},
			wantFired:  true,
			wantCat:    "ExitCodePattern",
			wantConf:   "High",
			wantSubstr: "command not found",
		},
		{
			name: "exit code 126 PermissionDenied",
			event: watcher.CrashLoopBackOffEvent{
				BaseEvent:           watcher.BaseEvent{Namespace: "default", PodName: "test-pod"},
				LastExitCode:        126,
				ExitCodeCategory:    "PermissionDenied",
				ExitCodeDescription: "permission denied",
			},
			wantFired:  true,
			wantCat:    "ExitCodePattern",
			wantConf:   "High",
			wantSubstr: "not executable",
		},
		{
			name: "exit code 139 SegmentationFault",
			event: watcher.CrashLoopBackOffEvent{
				BaseEvent:           watcher.BaseEvent{Namespace: "default", PodName: "test-pod"},
				LastExitCode:        139,
				ExitCodeCategory:    "SegmentationFault",
				ExitCodeDescription: "segfault",
			},
			wantFired:  true,
			wantCat:    "ExitCodePattern",
			wantConf:   "High",
			wantSubstr: "SIGSEGV",
		},
		{
			name: "exit code 143 Terminated",
			event: watcher.CrashLoopBackOffEvent{
				BaseEvent:           watcher.BaseEvent{Namespace: "default", PodName: "test-pod"},
				LastExitCode:        143,
				ExitCodeCategory:    "Terminated",
				ExitCodeDescription: "terminated by SIGTERM",
			},
			wantFired:  true,
			wantCat:    "ExitCodePattern",
			wantConf:   "Medium",
			wantSubstr: "terminationGracePeriodSeconds",
		},
		{
			name: "exit code 1 GeneralError Low confidence",
			event: watcher.CrashLoopBackOffEvent{
				BaseEvent:           watcher.BaseEvent{Namespace: "default", PodName: "test-pod"},
				LastExitCode:        1,
				ExitCodeCategory:    "GeneralError",
				ExitCodeDescription: "general error",
			},
			wantFired:  true,
			wantCat:    "ExitCodePattern",
			wantConf:   "Low",
			wantSubstr: "check application logs",
		},
		{
			name: "exit code 0 does not fire",
			event: watcher.CrashLoopBackOffEvent{
				BaseEvent:        watcher.BaseEvent{Namespace: "default", PodName: "test-pod"},
				LastExitCode:     0,
				ExitCodeCategory: "",
			},
			wantFired: false,
		},
		{
			name: "empty category does not fire",
			event: watcher.CrashLoopBackOffEvent{
				BaseEvent:        watcher.BaseEvent{Namespace: "default", PodName: "test-pod"},
				LastExitCode:     42,
				ExitCodeCategory: "",
			},
			wantFired: false,
		},
		{
			name:      "non-crash event does not fire",
			event:     watcher.PodHealthyEvent{BaseEvent: watcher.BaseEvent{Namespace: "default", PodName: "test-pod"}},
			wantFired: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buffer := newBuffer(5 * time.Minute)
			detector := NewAnomalyDetector(buffer, logr.Discard())

			result := detector.analyzeExitCode(tt.event)

			if result.Detected != tt.wantFired {
				t.Errorf("Detected = %v, want %v", result.Detected, tt.wantFired)
			}
			if tt.wantFired {
				if result.Category != tt.wantCat {
					t.Errorf("Category = %q, want %q", result.Category, tt.wantCat)
				}
				if result.Confidence != tt.wantConf {
					t.Errorf("Confidence = %q, want %q", result.Confidence, tt.wantConf)
				}
				if tt.wantSubstr != "" && !containsSubstring(result.RootCause, tt.wantSubstr) {
					t.Errorf("RootCause = %q, want containing %q", result.RootCause, tt.wantSubstr)
				}
			}
		})
	}
}

func TestAnomalyDetector_AnalyzeConsecutiveExits(t *testing.T) {
	tests := []struct {
		name           string
		setupExits     []int32 // previous exit codes to record
		currentExit    int32
		wantFired      bool
		wantCategory   string
		wantConfidence string
	}{
		{
			name:           "3 consecutive same exit code fires",
			setupExits:     []int32{127, 127},
			currentExit:    127,
			wantFired:      true,
			wantCategory:   "ConsecutiveExitCode",
			wantConfidence: "High",
		},
		{
			name:           "5 consecutive same exit code fires",
			setupExits:     []int32{1, 1, 1, 1},
			currentExit:    1,
			wantFired:      true,
			wantCategory:   "ConsecutiveExitCode",
			wantConfidence: "High",
		},
		{
			name:        "2 consecutive does not fire",
			setupExits:  []int32{127},
			currentExit: 127,
			wantFired:   false,
		},
		{
			name:        "different exit codes breaks streak",
			setupExits:  []int32{127, 1, 127},
			currentExit: 127,
			wantFired:   false, // 127, 1, 127, 127 = only 2 consecutive 127s
		},
		{
			name:        "no previous exits",
			setupExits:  []int32{},
			currentExit: 127,
			wantFired:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buffer := newBuffer(5 * time.Minute)
			detector := NewAnomalyDetector(buffer, logr.Discard())

			baseTime := time.Now()

			// Record previous exit codes
			for i, code := range tt.setupExits {
				detector.exitStats.record("default", "test-pod", code, baseTime.Add(time.Duration(i)*time.Second))
			}

			// Set detector's clock after setup
			detector.now = func() time.Time {
				return baseTime.Add(time.Duration(len(tt.setupExits)+1) * time.Second)
			}

			event := watcher.CrashLoopBackOffEvent{
				BaseEvent:        watcher.BaseEvent{Namespace: "default", PodName: "test-pod"},
				LastExitCode:     tt.currentExit,
				ExitCodeCategory: "TestCategory",
			}

			// Track the current event
			detector.trackExitCode(event)

			result := detector.analyzeConsecutiveExits(event)

			if result.Detected != tt.wantFired {
				t.Errorf("Detected = %v, want %v", result.Detected, tt.wantFired)
			}
			if tt.wantFired {
				if result.Category != tt.wantCategory {
					t.Errorf("Category = %q, want %q", result.Category, tt.wantCategory)
				}
				if result.Confidence != tt.wantConfidence {
					t.Errorf("Confidence = %q, want %q", result.Confidence, tt.wantConfidence)
				}
			}
		})
	}
}

func TestAnomalyDetector_AnalyzeFrequencySpike(t *testing.T) {
	tests := []struct {
		name         string
		events       []watcher.CorrelatorEvent
		trigger      watcher.CorrelatorEvent
		wantFired    bool
		wantCat      string
		wantResource string
	}{
		{
			name: "5 failures in namespace fires",
			events: []watcher.CorrelatorEvent{
				watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod1"}},
				watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod2"}},
				watcher.OOMKilledEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod3"}},
				watcher.ImagePullBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod4"}},
			},
			trigger:      watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod5"}},
			wantFired:    true,
			wantCat:      "FrequencySpike",
			wantResource: "ns:production",
		},
		{
			name: "4 failures does not fire",
			events: []watcher.CorrelatorEvent{
				watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod1"}},
				watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod2"}},
				watcher.OOMKilledEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod3"}},
			},
			trigger:   watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod4"}},
			wantFired: false,
		},
		{
			name: "failures in different namespaces do not aggregate",
			events: []watcher.CorrelatorEvent{
				watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "ns1", PodName: "pod1"}},
				watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "ns2", PodName: "pod2"}},
				watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "ns3", PodName: "pod3"}},
				watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "ns4", PodName: "pod4"}},
			},
			trigger:   watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "ns5", PodName: "pod5"}},
			wantFired: false,
		},
		{
			name: "healthy events do not count as failures",
			events: []watcher.CorrelatorEvent{
				watcher.PodHealthyEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod1"}},
				watcher.PodHealthyEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod2"}},
				watcher.PodHealthyEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod3"}},
				watcher.PodHealthyEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod4"}},
			},
			trigger:   watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "production", PodName: "pod5"}},
			wantFired: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buffer := newBuffer(5 * time.Minute)
			detector := NewAnomalyDetector(buffer, logr.Discard())

			// Add events to buffer
			for _, e := range tt.events {
				buffer.Add(e)
			}
			buffer.Add(tt.trigger)

			result := detector.analyzeFrequencySpike(tt.trigger)

			if result.Detected != tt.wantFired {
				t.Errorf("Detected = %v, want %v", result.Detected, tt.wantFired)
			}
			if tt.wantFired {
				if result.Category != tt.wantCat {
					t.Errorf("Category = %q, want %q", result.Category, tt.wantCat)
				}
				if result.Resource != tt.wantResource {
					t.Errorf("Resource = %q, want %q", result.Resource, tt.wantResource)
				}
			}
		})
	}
}

func TestAnomalyDetector_AnalyzeWeakSignalCombo(t *testing.T) {
	tests := []struct {
		name       string
		events     []watcher.CorrelatorEvent
		trigger    watcher.CorrelatorEvent
		wantFired  bool
		wantConf   string
		wantSubstr string
	}{
		{
			name: "CPU throttling + probe failure fires",
			events: []watcher.CorrelatorEvent{
				watcher.CPUThrottlingEvent{BaseEvent: watcher.BaseEvent{Namespace: "default", PodName: "test-pod"}},
			},
			trigger:    watcher.ProbeFailureEvent{BaseEvent: watcher.BaseEvent{Namespace: "default", PodName: "test-pod"}},
			wantFired:  true,
			wantConf:   "High",
			wantSubstr: "CPU throttling",
		},
		{
			name: "probe failure + CPU throttling (reverse order) fires",
			events: []watcher.CorrelatorEvent{
				watcher.ProbeFailureEvent{BaseEvent: watcher.BaseEvent{Namespace: "default", PodName: "test-pod"}},
			},
			trigger:    watcher.CPUThrottlingEvent{BaseEvent: watcher.BaseEvent{Namespace: "default", PodName: "test-pod"}},
			wantFired:  true,
			wantConf:   "High",
			wantSubstr: "CPU throttling",
		},
		{
			name: "signals from different pods do not match",
			events: []watcher.CorrelatorEvent{
				watcher.CPUThrottlingEvent{BaseEvent: watcher.BaseEvent{Namespace: "default", PodName: "pod1"}},
			},
			trigger:   watcher.ProbeFailureEvent{BaseEvent: watcher.BaseEvent{Namespace: "default", PodName: "pod2"}},
			wantFired: false,
		},
		{
			name:      "single weak signal does not fire",
			events:    []watcher.CorrelatorEvent{},
			trigger:   watcher.CPUThrottlingEvent{BaseEvent: watcher.BaseEvent{Namespace: "default", PodName: "test-pod"}},
			wantFired: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buffer := newBuffer(5 * time.Minute)
			detector := NewAnomalyDetector(buffer, logr.Discard())

			// Add events to buffer
			for _, e := range tt.events {
				buffer.Add(e)
			}
			buffer.Add(tt.trigger)

			result := detector.analyzeWeakSignalCombo(tt.trigger)

			if result.Detected != tt.wantFired {
				t.Errorf("Detected = %v, want %v", result.Detected, tt.wantFired)
			}
			if tt.wantFired {
				if result.Confidence != tt.wantConf {
					t.Errorf("Confidence = %q, want %q", result.Confidence, tt.wantConf)
				}
				if tt.wantSubstr != "" && !containsSubstring(result.RootCause, tt.wantSubstr) {
					t.Errorf("RootCause = %q, want containing %q", result.RootCause, tt.wantSubstr)
				}
			}
		})
	}
}

func TestAnomalyDetector_Analyze_PriorityOrder(t *testing.T) {
	// Test that consecutive exit code detection has higher priority than exit code pattern
	t.Run("consecutive exit code beats exit code pattern", func(t *testing.T) {
		buffer := newBuffer(5 * time.Minute)
		detector := NewAnomalyDetector(buffer, logr.Discard())

		baseTime := time.Now()

		// Set up 3 consecutive same exit code
		detector.exitStats.record("default", "test-pod", 127, baseTime)
		detector.exitStats.record("default", "test-pod", 127, baseTime.Add(time.Second))

		detector.now = func() time.Time {
			return baseTime.Add(2 * time.Second)
		}

		event := watcher.CrashLoopBackOffEvent{
			BaseEvent:        watcher.BaseEvent{Namespace: "default", PodName: "test-pod"},
			LastExitCode:     127,
			ExitCodeCategory: "CommandNotFound",
		}

		detector.trackExitCode(event)
		result := detector.Analyze(event)

		// Should detect ConsecutiveExitCode, not ExitCodePattern
		if !result.Detected {
			t.Fatal("Expected anomaly to be detected")
		}
		if result.Category != "ConsecutiveExitCode" {
			t.Errorf("Category = %q, want ConsecutiveExitCode (higher priority)", result.Category)
		}
	})
}

func TestExitCodeStats(t *testing.T) {
	t.Run("records and retrieves exit codes", func(t *testing.T) {
		stats := newExitCodeStats(5 * time.Minute)
		now := time.Now()

		stats.record("default", "pod1", 127, now)
		stats.record("default", "pod1", 1, now.Add(time.Second))
		stats.record("default", "pod1", 127, now.Add(2*time.Second))

		entries := stats.getRecent("default", "pod1", now.Add(3*time.Second))

		if len(entries) != 3 {
			t.Errorf("got %d entries, want 3", len(entries))
		}
	})

	t.Run("prunes old entries", func(t *testing.T) {
		stats := newExitCodeStats(1 * time.Minute)
		baseTime := time.Now()

		// Add entry in the past
		stats.record("default", "pod1", 127, baseTime.Add(-2*time.Minute))
		// Add recent entry
		stats.record("default", "pod1", 1, baseTime)

		entries := stats.getRecent("default", "pod1", baseTime)

		if len(entries) != 1 {
			t.Errorf("got %d entries, want 1 (old entry should be pruned)", len(entries))
		}
		if entries[0].Code != 1 {
			t.Errorf("expected code 1, got %d", entries[0].Code)
		}
	})

	t.Run("different pods have separate tracking", func(t *testing.T) {
		stats := newExitCodeStats(5 * time.Minute)
		now := time.Now()

		stats.record("default", "pod1", 127, now)
		stats.record("default", "pod2", 1, now)

		entries1 := stats.getRecent("default", "pod1", now)
		entries2 := stats.getRecent("default", "pod2", now)

		if len(entries1) != 1 || entries1[0].Code != 127 {
			t.Errorf("pod1 should have exit code 127")
		}
		if len(entries2) != 1 || entries2[0].Code != 1 {
			t.Errorf("pod2 should have exit code 1")
		}
	})
}

func TestHelperFunctions(t *testing.T) {
	t.Run("extractNamespace", func(t *testing.T) {
		tests := []struct {
			event watcher.CorrelatorEvent
			want  string
		}{
			{watcher.CrashLoopBackOffEvent{BaseEvent: watcher.BaseEvent{Namespace: "ns1"}}, "ns1"},
			{watcher.OOMKilledEvent{BaseEvent: watcher.BaseEvent{Namespace: "ns2"}}, "ns2"},
			{watcher.PodHealthyEvent{BaseEvent: watcher.BaseEvent{Namespace: "ns3"}}, "ns3"},
		}
		for _, tt := range tests {
			if got := extractNamespace(tt.event); got != tt.want {
				t.Errorf("extractNamespace() = %q, want %q", got, tt.want)
			}
		}
	})

	t.Run("signalTypeFromEvent", func(t *testing.T) {
		tests := []struct {
			event watcher.CorrelatorEvent
			want  string
		}{
			{watcher.CrashLoopBackOffEvent{}, signalTypeCrashLoop},
			{watcher.OOMKilledEvent{}, signalTypeOOM},
			{watcher.ImagePullBackOffEvent{}, signalTypeRegistry},
			{watcher.CPUThrottlingEvent{}, signalTypeCPUThrottling},
			{watcher.ProbeFailureEvent{}, signalTypeProbeFailure},
			{watcher.NodePressureEvent{}, signalTypeNodePressure},
		}
		for _, tt := range tests {
			if got := signalTypeFromEvent(tt.event); got != tt.want {
				t.Errorf("signalTypeFromEvent(%T) = %q, want %q", tt.event, got, tt.want)
			}
		}
	})

	t.Run("isFailureEvent", func(t *testing.T) {
		failures := []watcher.CorrelatorEvent{
			watcher.CrashLoopBackOffEvent{},
			watcher.OOMKilledEvent{},
			watcher.ImagePullBackOffEvent{},
			watcher.NodeNotReadyEvent{},
		}
		nonFailures := []watcher.CorrelatorEvent{
			watcher.PodHealthyEvent{},
			watcher.PodDeletedEvent{},
		}

		for _, e := range failures {
			if !isFailureEvent(e) {
				t.Errorf("isFailureEvent(%T) = false, want true", e)
			}
		}
		for _, e := range nonFailures {
			if isFailureEvent(e) {
				t.Errorf("isFailureEvent(%T) = true, want false", e)
			}
		}
	})

	t.Run("containsAllSignals", func(t *testing.T) {
		present := map[string]bool{"A": true, "B": true, "C": true}

		if !containsAllSignals(present, []string{"A", "B"}) {
			t.Error("should contain A and B")
		}
		if containsAllSignals(present, []string{"A", "D"}) {
			t.Error("should not contain D")
		}
		if !containsAllSignals(present, []string{}) {
			t.Error("empty required should match")
		}
	})
}

func containsSubstring(s, substr string) bool {
	return strings.Contains(s, substr)
}
