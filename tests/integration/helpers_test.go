//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/enterpilot/gomodel/internal/core"
)

// API endpoints
const (
	chatCompletionsPath = "/v1/chat/completions"
	responsesPath       = "/v1/responses"
	modelsPath          = "/v1/models"
	healthPath          = "/health"
)

// sendChatRequest sends a chat completion request and returns the response.
func sendChatRequest(t *testing.T, serverURL string, payload core.ChatRequest) *http.Response {
	t.Helper()
	return sendJSONRequest(t, serverURL+chatCompletionsPath, payload, nil)
}

// sendChatRequestWithHeaders sends a chat completion request with custom headers.
func sendChatRequestWithHeaders(t *testing.T, serverURL string, payload core.ChatRequest, headers map[string]string) *http.Response {
	t.Helper()
	return sendJSONRequest(t, serverURL+chatCompletionsPath, payload, headers)
}

// sendResponsesRequestWithHeaders sends a responses API request with custom headers.
func sendResponsesRequestWithHeaders(t *testing.T, serverURL string, payload core.ResponsesRequest, headers map[string]string) *http.Response {
	t.Helper()
	return sendJSONRequest(t, serverURL+responsesPath, payload, headers)
}

// sendJSONRequest sends a JSON POST request and returns the response.
func sendJSONRequest(t *testing.T, url string, payload any, headers map[string]string) *http.Response {
	t.Helper()

	body, err := json.Marshal(payload)
	require.NoError(t, err, "failed to marshal request payload")

	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	require.NoError(t, err, "failed to create request")

	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	require.NoError(t, err, "failed to send request")

	return resp
}

// closeBody is a helper to close response body in defer statements.
func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
}

// newChatRequest creates a basic chat request for testing.
func newChatRequest(model, content string) core.ChatRequest {
	return core.ChatRequest{
		Model: model,
		Messages: []core.Message{
			{
				Role:    "user",
				Content: content,
			},
		},
	}
}

// newResponsesRequest creates a basic responses request for testing.
func newResponsesRequest(model, content string) core.ResponsesRequest {
	return core.ResponsesRequest{
		Model: model,
		Input: content, // Simple string input for basic testing
	}
}

// forwardChatRequest forwards a chat request to the upstream server.
func forwardChatRequest(ctx context.Context, client *http.Client, baseURL, apiKey string, req *core.ChatRequest) (*core.ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+chatCompletionsPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var chatResp core.ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &chatResp, nil
}

// forwardResponsesRequest forwards a responses request to the upstream server.
func forwardResponsesRequest(ctx context.Context, client *http.Client, baseURL, apiKey string, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+responsesPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	var responsesResp core.ResponsesResponse
	if err := json.NewDecoder(resp.Body).Decode(&responsesResp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &responsesResp, nil
}

// forwardStreamingChatRequest forwards a streaming chat request to the upstream server.
func forwardStreamingChatRequest(ctx context.Context, client *http.Client, baseURL, apiKey string, req *core.ChatRequest) (io.ReadCloser, error) {
	// Ensure stream is set
	req.Stream = true

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+chatCompletionsPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return resp.Body, nil
}

// forwardStreamingResponsesRequest forwards a streaming responses request to the upstream server.
func forwardStreamingResponsesRequest(ctx context.Context, client *http.Client, baseURL, apiKey string, req *core.ResponsesRequest) (io.ReadCloser, error) {
	// Ensure stream is set
	req.Stream = true

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+responsesPath, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	return resp.Body, nil
}

// newStreamingChatRequest creates a streaming chat request for testing.
func newStreamingChatRequest(model, content string) core.ChatRequest {
	return core.ChatRequest{
		Model:  model,
		Stream: true,
		Messages: []core.Message{
			{
				Role:    "user",
				Content: content,
			},
		},
	}
}

// newStreamingResponsesRequest creates a streaming responses request for testing.
func newStreamingResponsesRequest(model, content string) core.ResponsesRequest {
	return core.ResponsesRequest{
		Model:  model,
		Stream: true,
		Input:  content,
	}
}
