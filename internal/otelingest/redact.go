package otelingest

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

// redactedToken is the replacement written in place of any PII match.
const redactedToken = "[REDACTED]"

// redact returns s with every match of every compiled pattern replaced by
// redactedToken. When patterns is empty, redact returns s unchanged. Redaction
// is applied at ingest time (earliest point in the pipeline) so that buffered
// signals, IncidentReport.status.correlatedSignals, and any LLM submission all
// see the same scrubbed text.
func redact(s string, patterns []*regexp.Regexp) string {
	if s == "" || len(patterns) == 0 {
		return s
	}
	out := s
	for _, re := range patterns {
		if re == nil {
			continue
		}
		out = re.ReplaceAllString(out, redactedToken)
	}
	return out
}

// bodyHash returns a short, stable hash of a (redacted) log body. It is used
// as part of OTelLogMatchEvent.DedupKey so repeated identical log lines within
// the buffer window collapse to a single entry.
func bodyHash(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:8]) // 16-char hex prefix is enough for dedup
}
