package rca

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLLMClient_Complete_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected auth header: %s", r.Header.Get("Authorization"))
		}

		// Verify request body.
		var req ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != "gpt-4o" {
			t.Errorf("unexpected model: %s", req.Model)
		}

		resp := ChatResponse{
			ID: "chatcmpl-123",
			Choices: []ChatChoice{
				{
					Index: 0,
					Message: ChatMessage{
						Role:    "assistant",
						Content: `{"rootCause":"OOM","confidence":"0.9","playbook":["restart"],"evidence":["trace-1"]}`,
					},
					FinishReason: "stop",
				},
			},
			Usage: &ChatUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewLLMClient(server.URL, "test-key", "gpt-4o")
	messages := []ChatMessage{
		{Role: "system", Content: "You are an expert."},
		{Role: "user", Content: "Analyze this incident."},
	}

	resp, err := client.Complete(context.Background(), messages, nil)
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}

	if resp.FirstContent() == "" {
		t.Error("expected non-empty content")
	}
	if resp.HasToolCalls() {
		t.Error("expected no tool calls")
	}
	if resp.Usage.TotalTokens != 150 {
		t.Errorf("unexpected total tokens: %d", resp.Usage.TotalTokens)
	}
}

func TestLLMClient_Complete_WithToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ChatResponse{
			Choices: []ChatChoice{
				{
					Message: ChatMessage{
						Role: "assistant",
						ToolCalls: []ToolCallEntry{
							{
								ID:   "call_1",
								Type: "function",
								Function: FunctionCall{
									Name:      "query_metrics",
									Arguments: `{"query":"rate(http_requests_total[5m])"}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewLLMClient(server.URL, "key", "gpt-4o")
	resp, err := client.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "test"}}, nil)
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}

	if !resp.HasToolCalls() {
		t.Fatal("expected tool calls")
	}
	if resp.Choices[0].Message.ToolCalls[0].Function.Name != "query_metrics" {
		t.Error("unexpected tool name")
	}
}

func TestLLMClient_Complete_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error": {"message": "rate limited", "type": "rate_limit"}}`))
	}))
	defer server.Close()

	client := NewLLMClient(server.URL, "key", "gpt-4o")
	_, err := client.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "test"}}, nil)
	if err == nil {
		t.Fatal("expected error for 429 response")
	}
}

func TestLLMClient_Complete_NoAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("expected no auth header when API key is empty")
		}
		resp := ChatResponse{
			Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: "ok"}}},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	client := NewLLMClient(server.URL, "", "llama3")
	resp, err := client.Complete(context.Background(), []ChatMessage{{Role: "user", Content: "test"}}, nil)
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if resp.FirstContent() != "ok" {
		t.Error("unexpected response")
	}
}

func TestChatResponse_EmptyChoices(t *testing.T) {
	resp := &ChatResponse{Choices: nil}
	if resp.HasToolCalls() {
		t.Error("expected no tool calls for empty choices")
	}
	if resp.FirstContent() != "" {
		t.Error("expected empty content for empty choices")
	}
}
