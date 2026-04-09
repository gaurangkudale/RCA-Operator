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
	"regexp"
	"strings"
)

// Redaction placeholder constants
const (
	redactionPlaceholder = "[REDACTED]"
)

// Redactor sanitizes telemetry data before sending to LLMs.
// It removes PII, secrets, and sensitive connection strings.
type Redactor struct {
	patterns []*redactPattern
}

type redactPattern struct {
	re          *regexp.Regexp
	replacement string
}

// NewRedactor creates a Redactor with default PII patterns.
func NewRedactor() *Redactor {
	return &Redactor{
		patterns: defaultPatterns(),
	}
}

func defaultPatterns() []*redactPattern {
	return []*redactPattern{
		// Email addresses
		{
			re:          regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`),
			replacement: "[REDACTED_EMAIL]",
		},
		// IPv4 addresses (but not localhost/loopback)
		{
			re:          regexp.MustCompile(`\b(?:(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\.){3}(?:25[0-5]|2[0-4]\d|[01]?\d\d?)\b`),
			replacement: "[REDACTED_IP]",
		},
		// JWT tokens (three base64 segments separated by dots)
		{
			re:          regexp.MustCompile(`eyJ[a-zA-Z0-9_-]+\.eyJ[a-zA-Z0-9_-]+\.[a-zA-Z0-9_-]+`),
			replacement: "[REDACTED_JWT]",
		},
		// Bearer tokens in headers
		{
			re:          regexp.MustCompile(`(?i)bearer\s+[a-zA-Z0-9._\-/+=]{20,}`),
			replacement: "Bearer [REDACTED_TOKEN]",
		},
		// Connection strings (postgres, mysql, mongodb, redis)
		{
			re:          regexp.MustCompile(`(?i)(?:postgres|mysql|mongodb|redis)(?:ql)?://[^\s"']+`),
			replacement: "[REDACTED_CONNECTION_STRING]",
		},
		// Generic API keys in key=value patterns
		{
			re:          regexp.MustCompile(`(?i)(?:api[_-]?key|secret[_-]?key|password|token|access[_-]?key)\s*[=:]\s*["']?[a-zA-Z0-9._\-/+=]{8,}["']?`),
			replacement: "[REDACTED_SECRET]",
		},
		// AWS access key IDs
		{
			re:          regexp.MustCompile(`(?:AKIA|ASIA)[A-Z0-9]{16}`),
			replacement: "[REDACTED_AWS_KEY]",
		},
		// Credit card numbers (basic pattern)
		{
			re:          regexp.MustCompile(`\b\d{4}[\s-]?\d{4}[\s-]?\d{4}[\s-]?\d{4}\b`),
			replacement: "[REDACTED_CARD]",
		},
	}
}

// Redact applies all redaction patterns to the input string.
func (r *Redactor) Redact(input string) string {
	result := input
	for _, p := range r.patterns {
		result = p.re.ReplaceAllString(result, p.replacement)
	}
	return result
}

// RedactSlice redacts each string in a slice.
func (r *Redactor) RedactSlice(inputs []string) []string {
	out := make([]string, len(inputs))
	for i, s := range inputs {
		out[i] = r.Redact(s)
	}
	return out
}

// RedactMap redacts all values in a string map.
func (r *Redactor) RedactMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		// Also redact the key name if it looks sensitive
		if isSensitiveKey(k) {
			out[k] = redactionPlaceholder
		} else {
			out[k] = r.Redact(v)
		}
	}
	return out
}

// isSensitiveKey returns true if a key name suggests it contains secrets.
func isSensitiveKey(key string) bool {
	lower := strings.ToLower(key)
	sensitiveWords := []string{
		"password", "secret", "token", "apikey", "api_key",
		"authorization", "credential", "private_key",
	}
	for _, w := range sensitiveWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}
