package otelingest

import (
	"regexp"
	"strings"
	"testing"
)

func TestRedact_NoPatternsReturnsInput(t *testing.T) {
	in := "hello user@example.com"
	if got := redact(in, nil); got != in {
		t.Fatalf("expected input unchanged, got %q", got)
	}
}

func TestRedact_EmptyStringReturnsEmpty(t *testing.T) {
	if got := redact("", []*regexp.Regexp{regexp.MustCompile(".*")}); got != "" {
		t.Fatalf("expected empty string, got %q", got)
	}
}

func TestRedact_ReplacesMatches(t *testing.T) {
	email := regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+`)
	digits := regexp.MustCompile(`\d{3,}`)
	patterns := []*regexp.Regexp{email, digits}

	in := "error sending to user@example.com order 123456"
	out := redact(in, patterns)

	if strings.Contains(out, "user@example.com") {
		t.Errorf("email should be redacted: %q", out)
	}
	if strings.Contains(out, "123456") {
		t.Errorf("digits should be redacted: %q", out)
	}
	if !strings.Contains(out, redactedToken) {
		t.Errorf("expected %q in output: %q", redactedToken, out)
	}
}

func TestRedact_SkipsNilPattern(t *testing.T) {
	patterns := []*regexp.Regexp{nil, regexp.MustCompile(`foo`)}
	if got := redact("foo bar", patterns); got != redactedToken+" bar" {
		t.Fatalf("unexpected output: %q", got)
	}
}

func TestBodyHash_StableAndShort(t *testing.T) {
	a := bodyHash("same message")
	b := bodyHash("same message")
	c := bodyHash("different message")
	if a != b {
		t.Fatalf("hash should be stable: %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("hashes for distinct inputs should differ")
	}
	if len(a) != 16 {
		t.Fatalf("hash length should be 16 hex chars, got %d", len(a))
	}
}

func TestCompileRedaction_SkipsEmpty(t *testing.T) {
	compiled, errs := CompileRedaction([]string{"", `\d+`, ""})
	if len(compiled) != 1 {
		t.Fatalf("expected 1 compiled pattern, got %d", len(compiled))
	}
	if len(errs) != 0 {
		t.Fatalf("expected 0 errors, got %v", errs)
	}
}

func TestCompileRedaction_ReportsInvalid(t *testing.T) {
	compiled, errs := CompileRedaction([]string{`\d+`, `[invalid(`})
	if len(compiled) != 1 {
		t.Fatalf("valid patterns should still compile: got %d", len(compiled))
	}
	if len(errs) != 1 {
		t.Fatalf("expected 1 error, got %d", len(errs))
	}
}
