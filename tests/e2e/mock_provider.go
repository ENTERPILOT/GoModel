//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	"gomodel/internal/core"
)

// MockLLMServer simulates an upstream LLM provider (like OpenAI).
type MockLLMServer struct {
	server        *httptest.Server
	mu            sync.Mutex
	requests      []RecordedRequest
	responseDelay time.Duration
	customHandler func(w http.ResponseWriter, r *http.Request) bool
	failNext      bool
	failWithCode  int
	failMessage   string
}

// RecordedRequest stores information about a received request.
type RecordedRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Body    []byte
}

// Requests returns a thread-safe snapshot of recorded requests with cloned headers and body bytes.
func (m *MockLLMServer) Requests() []RecordedRequest {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]RecordedRequest, len(m.requests))
	for i, req := range m.requests {
		body := make([]byte, len(req.Body))
		copy(body, req.Body)
		out[i] = RecordedRequest{
			Method:  req.Method,
			Path:    req.Path,
			Headers: req.Headers.Clone(),
			Body:    body,
		}
	}
	return out
}

// ResetRequests clears all recorded requests in a thread-safe manner.
func (m *MockLLMServer) ResetRequests() {
	m.mu.Lock()
	m.requests = m.requests[:0]
	m.mu.Unlock()
}

// NewMockLLMServer creates a new mock LLM server.
func NewMockLLMServer() *MockLLMServer {
	m := &MockLLMServer{
		requests: make([]RecordedRequest, 0),
	}

	m.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Record the request
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewBuffer(body))

		m.mu.Lock()
		m.requests = append(m.requests, RecordedRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Headers: r.Header.Clone(),
			Body:    body,
		})

		// Check if we should fail
		if m.failNext {
			m.failNext = false
			code := m.failWithCode
			msg := m.failMessage
			m.mu.Unlock()
			w.WriteHeader(code)
			_, _ = fmt.Fprintf(w, `{"error": {"message": "%s", "type": "api_error"}}`, msg)
			return
		}

		// Check for custom handler
		if m.customHandler != nil {
			handler := m.customHandler
			m.mu.Unlock()
			if handler(w, r) {
				return
			}
			m.mu.Lock()
		}

		delay := m.responseDelay
		m.mu.Unlock()

		// Simulate network delay
		if delay > 0 {
			time.Sleep(delay)
		}

		// Handle the request based on path
		m.handleRequest(w, r, body)
	}))

	return m
}

// handleRequest processes incoming requests and returns appropriate responses.
func (m *MockLLMServer) handleRequest(w http.ResponseWriter, r *http.Request, body []byte) {
	// Verify authorization
	auth := r.Header.Get("Authorization")
	if auth == "" {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"message": "Missing API key", "type": "invalid_request_error"}}`))
		return
	}

	switch r.URL.Path {
	case "/chat/completions":
		m.handleChatCompletion(w, r, body)
	case "/responses":
		m.handleResponses(w, r, body)
	case "/models":
		m.handleListModels(w)
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error": {"message": "Not found", "type": "invalid_request_error"}}`))
	}
}

// handleChatCompletion handles chat completion requests.
func (m *MockLLMServer) handleChatCompletion(w http.ResponseWriter, r *http.Request, body []byte) {
	var req core.ChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid request body", "type": "invalid_request_error"}}`))
		return
	}

	// Check for streaming
	if req.Stream {
		if toolName := forcedToolName(req); toolName != "" {
			m.handleStreamingToolResponse(w, req, toolName)
			return
		}
		m.handleStreamingResponse(w, req)
		return
	}

	if toolName := forcedToolName(req); toolName != "" {
		response := core.ChatResponse{
			ID:      "chatcmpl-test-" + time.Now().Format("20060102150405"),
			Object:  "chat.completion",
			Model:   req.Model,
			Created: time.Now().Unix(),
			Choices: []core.Choice{
				{
					Index: 0,
					Message: core.Message{
						Role: "assistant",
						ToolCalls: []core.ToolCall{
							{
								ID:   "call_mock_123",
								Type: "function",
								Function: core.FunctionCall{
									Name:      toolName,
									Arguments: `{"city":"Warsaw"}`,
								},
							},
						},
					},
					FinishReason: "tool_calls",
				},
			},
			Usage: core.Usage{
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalTokens:      30,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response)
		return
	}

	// Generate a mock response based on the input
	responseContent := generateMockResponse(req)

	response := core.ChatResponse{
		ID:      "chatcmpl-test-" + time.Now().Format("20060102150405"),
		Object:  "chat.completion",
		Model:   req.Model,
		Created: time.Now().Unix(),
		Choices: []core.Choice{
			{
				Index: 0,
				Message: core.Message{
					Role:    "assistant",
					Content: responseContent,
				},
				FinishReason: "stop",
			},
		},
		Usage: core.Usage{
			PromptTokens:     10,
			CompletionTokens: 20,
			TotalTokens:      30,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// handleStreamingResponse handles SSE streaming responses.
func (m *MockLLMServer) handleStreamingResponse(w http.ResponseWriter, req core.ChatRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	// Generate streaming chunks
	content := generateMockResponse(req)
	chunks := splitIntoChunks(content, 5)

	for i, chunk := range chunks {
		delta := map[string]interface{}{
			"id":      "chatcmpl-test-stream",
			"object":  "chat.completion.chunk",
			"model":   req.Model,
			"created": time.Now().Unix(),
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"delta": map[string]interface{}{
						"content": chunk,
					},
					"finish_reason": nil,
				},
			},
		}

		// Last chunk has finish_reason
		if i == len(chunks)-1 {
			delta["choices"].([]map[string]interface{})[0]["finish_reason"] = "stop"
		}

		data, _ := json.Marshal(delta)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
		time.Sleep(10 * time.Millisecond)
	}

	// Send done marker
	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func (m *MockLLMServer) handleStreamingToolResponse(w http.ResponseWriter, req core.ChatRequest, toolName string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	toolCall := core.ToolCall{
		ID:   "call_mock_123",
		Type: "function",
		Function: core.FunctionCall{
			Name:      toolName,
			Arguments: `{"city":"Warsaw"}`,
		},
	}

	firstChunk := map[string]interface{}{
		"id":      "chatcmpl-test-stream",
		"object":  "chat.completion.chunk",
		"model":   req.Model,
		"created": time.Now().Unix(),
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"delta": map[string]interface{}{
					"tool_calls": []core.ToolCall{toolCall},
				},
				"finish_reason": nil,
			},
		},
	}

	data, _ := json.Marshal(firstChunk)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
	time.Sleep(10 * time.Millisecond)

	finalChunk := map[string]interface{}{
		"id":      "chatcmpl-test-stream",
		"object":  "chat.completion.chunk",
		"model":   req.Model,
		"created": time.Now().Unix(),
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         map[string]interface{}{},
				"finish_reason": "tool_calls",
			},
		},
	}

	data, _ = json.Marshal(finalChunk)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()

	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// handleListModels handles the models list endpoint.
func (m *MockLLMServer) handleListModels(w http.ResponseWriter) {
	response := core.ModelsResponse{
		Object: "list",
		Data: []core.Model{
			{ID: "gpt-4", Object: "model", OwnedBy: "openai", Created: time.Now().Unix()},
			{ID: "gpt-4-turbo", Object: "model", OwnedBy: "openai", Created: time.Now().Unix()},
			{ID: "gpt-3.5-turbo", Object: "model", OwnedBy: "openai", Created: time.Now().Unix()},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// handleResponses handles the Responses API endpoint.
func (m *MockLLMServer) handleResponses(w http.ResponseWriter, r *http.Request, body []byte) {
	var req core.ResponsesRequest
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "Invalid request body", "type": "invalid_request_error"}}`))
		return
	}

	// Validate model is present
	if req.Model == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "model is required", "type": "invalid_request_error"}}`))
		return
	}

	// Check for streaming
	if req.Stream {
		m.handleResponsesStreaming(w, req)
		return
	}

	// Generate mock response content
	inputText := extractInputText(req.Input)
	responseContent := fmt.Sprintf("Mock response to: %s", inputText)

	response := map[string]interface{}{
		"id":         "resp_test_" + time.Now().Format("20060102150405"),
		"object":     "response",
		"created_at": time.Now().Unix(),
		"model":      req.Model,
		"status":     "completed",
		"output": []map[string]interface{}{
			{
				"id":     fmt.Sprintf("msg_%d", time.Now().UnixNano()),
				"type":   "message",
				"role":   "assistant",
				"status": "completed",
				"content": []map[string]interface{}{
					{
						"type":        "output_text",
						"text":        responseContent,
						"annotations": []string{},
					},
				},
			},
		},
		"usage": map[string]interface{}{
			"input_tokens":  10,
			"output_tokens": 20,
			"total_tokens":  30,
		},
		"error": nil,
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(response)
}

// handleResponsesStreaming handles streaming responses for the Responses API.
func (m *MockLLMServer) handleResponsesStreaming(w http.ResponseWriter, req core.ResponsesRequest) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}

	responseID := "resp_test_" + time.Now().Format("20060102150405")
	inputText := extractInputText(req.Input)
	content := fmt.Sprintf("Mock response to: %s", inputText)
	chunks := splitIntoChunks(content, 5)

	// Send response.created event
	createdEvent := map[string]interface{}{
		"type": "response.created",
		"response": map[string]interface{}{
			"id":         responseID,
			"object":     "response",
			"status":     "in_progress",
			"model":      req.Model,
			"created_at": time.Now().Unix(),
		},
	}
	data, _ := json.Marshal(createdEvent)
	_, _ = fmt.Fprintf(w, "event: response.created\ndata: %s\n\n", data)
	flusher.Flush()

	// Send text delta events
	for _, chunk := range chunks {
		deltaEvent := map[string]interface{}{
			"type":  "response.output_text.delta",
			"delta": chunk,
		}
		data, _ := json.Marshal(deltaEvent)
		_, _ = fmt.Fprintf(w, "event: response.output_text.delta\ndata: %s\n\n", data)
		flusher.Flush()
		time.Sleep(10 * time.Millisecond)
	}

	// Send response.completed event
	doneEvent := map[string]interface{}{
		"type": "response.completed",
		"response": map[string]interface{}{
			"id":         responseID,
			"object":     "response",
			"status":     "completed",
			"model":      req.Model,
			"created_at": time.Now().Unix(),
			"usage": map[string]interface{}{
				"input_tokens":  10,
				"output_tokens": 20,
				"total_tokens":  30,
			},
		},
	}
	data, _ = json.Marshal(doneEvent)
	_, _ = fmt.Fprintf(w, "event: response.completed\ndata: %s\n\n", data)
	flusher.Flush()

	// Send [DONE] marker
	_, _ = fmt.Fprintf(w, "data: [DONE]\n\n")
	flusher.Flush()
}

// extractInputText extracts text content from the input field.
func extractInputText(input interface{}) string {
	switch v := input.(type) {
	case string:
		return v
	case []interface{}:
		// Array input - extract from last user message
		for i := len(v) - 1; i >= 0; i-- {
			if msg, ok := v[i].(map[string]interface{}); ok {
				if role, _ := msg["role"].(string); role == "user" {
					if content, ok := msg["content"].(string); ok {
						return content
					}
					// Handle complex content (array of content items)
					if contentArr, ok := msg["content"].([]interface{}); ok {
						for _, item := range contentArr {
							if itemMap, ok := item.(map[string]interface{}); ok {
								if text, ok := itemMap["text"].(string); ok {
									return text
								}
							}
						}
					}
				}
			}
		}
	}
	return "Hello"
}

// generateMockResponse creates a mock response based on the request.
func generateMockResponse(req core.ChatRequest) string {
	if len(req.Messages) == 0 {
		return "Hello! How can I help you today?"
	}

	lastMessage := req.Messages[len(req.Messages)-1].Content

	// Echo-style response for testing
	return fmt.Sprintf("Mock response to: %s", lastMessage)
}

func forcedToolName(req core.ChatRequest) string {
	switch choice := req.ToolChoice.(type) {
	case map[string]interface{}:
		if choiceType, _ := choice["type"].(string); choiceType == "function" {
			if function, ok := choice["function"].(map[string]interface{}); ok {
				if name, _ := function["name"].(string); name != "" {
					return name
				}
			}
		}
	case string:
		if choice == "required" && len(req.Tools) > 0 {
			if function, ok := req.Tools[0]["function"].(map[string]interface{}); ok {
				if name, _ := function["name"].(string); name != "" {
					return name
				}
			}
		}
	}

	return ""
}

// splitIntoChunks splits a string into chunks of approximately n characters.
func splitIntoChunks(s string, n int) []string {
	if len(s) == 0 {
		return []string{}
	}

	chunks := make([]string, 0)
	for i := 0; i < len(s); i += n {
		end := i + n
		if end > len(s) {
			end = len(s)
		}
		chunks = append(chunks, s[i:end])
	}
	return chunks
}

// URL returns the mock server's URL.
func (m *MockLLMServer) URL() string {
	return m.server.URL
}

// Close shuts down the mock server.
func (m *MockLLMServer) Close() {
	m.server.Close()
}

// forwardChatRequest forwards a chat request to the mock server.
func forwardChatRequest(ctx context.Context, client *http.Client, baseURL, apiKey string, req *core.ChatRequest, stream bool) (*core.ChatResponse, error) {
	req.Stream = stream
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream error: %s", string(respBody))
	}

	var chatResp core.ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return nil, err
	}

	return &chatResp, nil
}

// forwardStreamRequest forwards a streaming request to the mock server.
func forwardStreamRequest(ctx context.Context, client *http.Client, baseURL, apiKey string, req *core.ChatRequest) (io.ReadCloser, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("upstream error: %s", string(respBody))
	}

	return resp.Body, nil
}

// forwardResponsesRequest forwards a responses API request to the mock server.
func forwardResponsesRequest(ctx context.Context, client *http.Client, baseURL, apiKey string, req *core.ResponsesRequest, stream bool) (*core.ResponsesResponse, error) {
	req.Stream = stream
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("upstream error: %s", string(respBody))
	}

	var responsesResp core.ResponsesResponse
	if err := json.NewDecoder(resp.Body).Decode(&responsesResp); err != nil {
		return nil, err
	}

	return &responsesResp, nil
}

// forwardResponsesStreamRequest forwards a streaming responses API request to the mock server.
func forwardResponsesStreamRequest(ctx context.Context, client *http.Client, baseURL, apiKey string, req *core.ResponsesRequest) (io.ReadCloser, error) {
	req.Stream = true
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/responses", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return nil, fmt.Errorf("upstream error: %s", string(respBody))
	}

	return resp.Body, nil
}
