package rca

import (
	"strings"
	"testing"
)

func TestRedactor_Email(t *testing.T) {
	r := NewRedactor()
	input := "Error from user admin@company.com in service auth-svc"
	got := r.Redact(input)
	if strings.Contains(got, "admin@company.com") {
		t.Error("email was not redacted")
	}
	if !strings.Contains(got, "[REDACTED_EMAIL]") {
		t.Error("expected [REDACTED_EMAIL] placeholder")
	}
}

func TestRedactor_IPv4(t *testing.T) {
	r := NewRedactor()
	input := "Connection refused to 192.168.1.100:5432"
	got := r.Redact(input)
	if strings.Contains(got, "192.168.1.100") {
		t.Error("IP address was not redacted")
	}
	if !strings.Contains(got, "[REDACTED_IP]") {
		t.Error("expected [REDACTED_IP] placeholder")
	}
}

func TestRedactor_JWT(t *testing.T) {
	r := NewRedactor()
	// Build a JWT-shaped value at runtime so no token-like literal is committed.
	header := "ey" + "J" + strings.Repeat("a", 12)
	payload := "ey" + "J" + strings.Repeat("b", 12)
	sig := strings.Repeat("c", 20)
	input := "Token: " + header + "." + payload + "." + sig
	got := r.Redact(input)
	if strings.Contains(got, header) || strings.Contains(got, payload) || strings.Contains(got, sig) {
		t.Error("JWT was not redacted")
	}
	if !strings.Contains(got, "[REDACTED_JWT]") {
		t.Error("expected [REDACTED_JWT] placeholder")
	}
}

func TestRedactor_BearerToken(t *testing.T) {
	r := NewRedactor()
	token := strings.Repeat("x", 24)
	input := "Authorization: Bearer " + token
	got := r.Redact(input)
	if strings.Contains(got, token) {
		t.Error("bearer token was not redacted")
	}
	if !strings.Contains(got, "[REDACTED_TOKEN]") {
		t.Error("expected [REDACTED_TOKEN] placeholder")
	}
}

func TestRedactor_ConnectionString(t *testing.T) {
	r := NewRedactor()
	tests := []struct {
		name  string
		input string
	}{
		{"postgres", "DSN: postgresql://user:pass@db.host:5432/mydb"},
		{"mongodb", "URI: mongodb://admin:secret@mongo:27017/app"},
		{"redis", "REDIS: redis://default:pw@cache:6379/0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := r.Redact(tt.input)
			if !strings.Contains(got, "[REDACTED_CONNECTION_STRING]") {
				t.Errorf("connection string not redacted: %s", got)
			}
		})
	}
}

func TestRedactor_APIKeys(t *testing.T) {
	r := NewRedactor()
	input := "config: api_key=sk_live_1234567890abcdef and secret_key=\"mySecret12345678\""
	got := r.Redact(input)
	if !strings.Contains(got, "[REDACTED_SECRET]") {
		t.Errorf("API key not redacted: %s", got)
	}
}

func TestRedactor_AWSKey(t *testing.T) {
	r := NewRedactor()
	// Test AWS key redaction - must be AKIA + 16 hex-like chars (obviously fake)
	input := "AWS key: AKIATESTEXAMPLETOKENnn"
	got := r.Redact(input)
	if strings.Contains(got, "AKIATEST") {
		t.Error("AWS key was not redacted")
	}
	if !strings.Contains(got, "[REDACTED_AWS_KEY]") {
		t.Error("expected [REDACTED_AWS_KEY] placeholder")
	}
}

func TestRedactor_CreditCard(t *testing.T) {
	r := NewRedactor()
	input := "Card: 4111-1111-1111-1111"
	got := r.Redact(input)
	if strings.Contains(got, "4111") {
		t.Error("credit card was not redacted")
	}
}

func TestRedactor_PreservesNormal(t *testing.T) {
	r := NewRedactor()
	input := "CrashLoopBackOff on pod payment-svc-7d4b8c6f5-x2k9n in namespace production"
	got := r.Redact(input)
	if got != input {
		t.Errorf("normal text was modified: %q", got)
	}
}

func TestRedactor_RedactSlice(t *testing.T) {
	r := NewRedactor()
	bearer := "Bearer " + strings.Repeat("x", 24)
	inputs := []string{
		"error from admin@test.com",
		"normal log line",
		"token: " + bearer,
	}
	out := r.RedactSlice(inputs)
	if len(out) != 3 {
		t.Fatalf("expected 3 results, got %d", len(out))
	}
	if strings.Contains(out[0], "admin@test.com") {
		t.Error("email not redacted in slice")
	}
	if out[1] != "normal log line" {
		t.Error("normal line modified in slice")
	}
}

func TestRedactor_RedactMap(t *testing.T) {
	r := NewRedactor()
	m := map[string]string{
		"service":       "payment-svc",
		"authorization": "Bearer secret123",
		"password":      "hunter2",
		"error":         "connection to 10.0.0.5 refused",
	}
	out := r.RedactMap(m)
	if out["authorization"] != "[REDACTED]" {
		t.Errorf("sensitive key 'authorization' not redacted: %s", out["authorization"])
	}
	if out["password"] != "[REDACTED]" {
		t.Errorf("sensitive key 'password' not redacted: %s", out["password"])
	}
	if out["service"] != "payment-svc" {
		t.Error("non-sensitive key was modified")
	}
	if !strings.Contains(out["error"], "[REDACTED_IP]") {
		t.Error("IP in non-sensitive key value not redacted")
	}
}

func TestIsSensitiveKey(t *testing.T) {
	tests := []struct {
		key      string
		expected bool
	}{
		{"password", true},
		{"Authorization", true},
		{"api_key", true},
		{"APIKEY", true},
		{"secret_value", true},
		{"private_key_path", true},
		{"service", false},
		{"namespace", false},
		{"pod_name", false},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got := isSensitiveKey(tt.key)
			if got != tt.expected {
				t.Errorf("isSensitiveKey(%q) = %v, want %v", tt.key, got, tt.expected)
			}
		})
	}
}
