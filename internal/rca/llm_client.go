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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// LLMClient sends chat completion requests to an OpenAI-compatible API.
type LLMClient struct {
	endpoint   string
	apiKey     string
	model      string
	httpClient *http.Client
}

// NewLLMClient creates a client targeting any OpenAI-compatible chat completions endpoint.
// Works with OpenAI, Azure OpenAI, ollama, LiteLLM, vLLM, etc.
func NewLLMClient(endpoint, apiKey, model string) *LLMClient {
	return &LLMClient{
		endpoint: endpoint,
		apiKey:   apiKey,
		model:    model,
		httpClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// ChatMessage represents a single message in a chat conversation.
type ChatMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content,omitempty"`
	ToolCalls  []ToolCallEntry `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

// ToolCallEntry represents a tool call returned by the LLM.
type ToolCallEntry struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall holds the function name and arguments from a tool call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatRequest is the request body for the chat completions API.
type ChatRequest struct {
	Model    string        `json:"model"`
	Messages []ChatMessage `json:"messages"`
	Tools    []ToolDef     `json:"tools,omitempty"`
}

// ChatResponse is the response from the chat completions API.
type ChatResponse struct {
	ID      string         `json:"id"`
	Choices []ChatChoice   `json:"choices"`
	Usage   *ChatUsage     `json:"usage,omitempty"`
	Error   *ChatErrorBody `json:"error,omitempty"`
}

// ChatChoice represents one completion choice.
type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// ChatUsage tracks token usage for the request.
type ChatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatErrorBody holds error details from the API.
type ChatErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// Complete sends a chat completion request and returns the response.
func (c *LLMClient) Complete(ctx context.Context, messages []ChatMessage, tools []ToolDef) (*ChatResponse, error) {
	reqBody := ChatRequest{
		Model:    c.model,
		Messages: messages,
		Tools:    tools,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.endpoint + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if chatResp.Error != nil {
		return nil, fmt.Errorf("API error: %s (type=%s, code=%s)", chatResp.Error.Message, chatResp.Error.Type, chatResp.Error.Code)
	}

	return &chatResp, nil
}

// HasToolCalls returns true if the first choice contains tool calls.
func (r *ChatResponse) HasToolCalls() bool {
	if len(r.Choices) == 0 {
		return false
	}
	return len(r.Choices[0].Message.ToolCalls) > 0
}

// FirstContent returns the text content of the first choice, or empty string.
func (r *ChatResponse) FirstContent() string {
	if len(r.Choices) == 0 {
		return ""
	}
	return r.Choices[0].Message.Content
}
