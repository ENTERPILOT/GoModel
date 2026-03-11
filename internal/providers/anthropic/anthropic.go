// Package anthropic provides Anthropic API integration for the LLM gateway.
package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
)

// Registration provides factory registration for the Anthropic provider.
var Registration = providers.Registration{
	Type: "anthropic",
	New:  New,
}

const (
	defaultBaseURL              = "https://api.anthropic.com/v1"
	anthropicAPIVersion         = "2023-06-01"
	maxBatchResultEndpointHints = 1024
)

var allowedAnthropicImageMediaTypes = map[string]struct{}{
	"image/jpeg": {},
	"image/png":  {},
	"image/gif":  {},
	"image/webp": {},
}

// Provider implements the core.Provider interface for Anthropic
type Provider struct {
	client *llmclient.Client
	apiKey string

	batchEndpointsMu sync.RWMutex
	// batchResultEndpoints keeps endpoint hints by provider batch id and custom_id.
	// Used only to shape native batch result items (e.g., /v1/responses vs /v1/chat/completions).
	batchResultEndpoints map[string]map[string]string
	batchResultOrder     []string
}

// New creates a new Anthropic provider.
func New(apiKey string, opts providers.ProviderOptions) core.Provider {
	p := &Provider{
		apiKey:               apiKey,
		batchResultEndpoints: make(map[string]map[string]string),
	}
	cfg := llmclient.Config{
		ProviderName:   "anthropic",
		BaseURL:        defaultBaseURL,
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.New(cfg, p.setHeaders)
	return p
}

// NewWithHTTPClient creates a new Anthropic provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	p := &Provider{
		apiKey:               apiKey,
		batchResultEndpoints: make(map[string]map[string]string),
	}
	cfg := llmclient.DefaultConfig("anthropic", defaultBaseURL)
	cfg.Hooks = hooks
	p.client = llmclient.NewWithHTTPClient(httpClient, cfg, p.setHeaders)
	return p
}

// SetBaseURL allows configuring a custom base URL for the provider
func (p *Provider) SetBaseURL(url string) {
	p.client.SetBaseURL(url)
}

func (p *Provider) setBatchResultEndpoints(batchID string, endpoints map[string]string) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" || len(endpoints) == 0 {
		return
	}
	cloned := make(map[string]string, len(endpoints))
	for customID, endpoint := range endpoints {
		customID = strings.TrimSpace(customID)
		endpoint = strings.TrimSpace(endpoint)
		if customID == "" || endpoint == "" {
			continue
		}
		cloned[customID] = endpoint
	}
	if len(cloned) == 0 {
		return
	}
	p.batchEndpointsMu.Lock()
	if p.batchResultEndpoints == nil {
		p.batchResultEndpoints = make(map[string]map[string]string)
	}
	for i, existingID := range p.batchResultOrder {
		if existingID == batchID {
			p.batchResultOrder = append(p.batchResultOrder[:i], p.batchResultOrder[i+1:]...)
			break
		}
	}
	p.batchResultEndpoints[batchID] = cloned
	p.batchResultOrder = append(p.batchResultOrder, batchID)
	for len(p.batchResultEndpoints) > maxBatchResultEndpointHints && len(p.batchResultOrder) > 0 {
		evictID := p.batchResultOrder[0]
		p.batchResultOrder = p.batchResultOrder[1:]
		delete(p.batchResultEndpoints, evictID)
	}
	p.batchEndpointsMu.Unlock()
}

func (p *Provider) getBatchResultEndpoints(batchID string) map[string]string {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return nil
	}
	p.batchEndpointsMu.RLock()
	defer p.batchEndpointsMu.RUnlock()
	endpoints, ok := p.batchResultEndpoints[batchID]
	if !ok || len(endpoints) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(endpoints))
	for customID, endpoint := range endpoints {
		cloned[customID] = endpoint
	}
	return cloned
}

// setHeaders sets the required headers for Anthropic API requests
func (p *Provider) setHeaders(req *http.Request) {
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", anthropicAPIVersion)

	// Forward request ID if present in context
	if requestID := core.GetRequestID(req.Context()); requestID != "" {
		req.Header.Set("X-Request-Id", requestID)
	}
}

// Passthrough forwards an opaque Anthropic-native request without typed translation.
func (p *Provider) Passthrough(ctx context.Context, req *core.PassthroughRequest) (*core.PassthroughResponse, error) {
	if req == nil {
		return nil, core.NewInvalidRequestError("passthrough request is required", nil)
	}

	resp, err := p.client.DoPassthrough(ctx, llmclient.Request{
		Method:        req.Method,
		Endpoint:      providers.PassthroughEndpoint(req.Endpoint),
		RawBodyReader: req.Body,
		Headers:       req.Headers,
	})
	if err != nil {
		return nil, err
	}

	return &core.PassthroughResponse{
		StatusCode: resp.StatusCode,
		Headers:    providers.CloneHTTPHeaders(resp.Header),
		Body:       resp.Body,
	}, nil
}

// anthropicThinking represents the thinking configuration for Anthropic's extended thinking.
// For 4.6 models: {type: "adaptive"} (budget_tokens omitted).
// For older models: {type: "enabled", budget_tokens: N}.
type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// anthropicOutputConfig controls effort level for adaptive thinking on 4.6 models.
type anthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

// anthropicRequest represents the Anthropic API request format
type anthropicRequest struct {
	Model        string                 `json:"model"`
	Messages     []anthropicMessage     `json:"messages"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	ToolChoice   *anthropicToolChoice   `json:"tool_choice,omitempty"`
	MaxTokens    int                    `json:"max_tokens"`
	Temperature  *float64               `json:"temperature,omitempty"`
	System       string                 `json:"system,omitempty"`
	Stream       bool                   `json:"stream,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse *bool  `json:"disable_parallel_tool_use,omitempty"`
}

var adaptiveThinkingPrefixes = []string{
	"claude-opus-4-6",
	"claude-sonnet-4-6",
}

func isAdaptiveThinkingModel(model string) bool {
	for _, prefix := range adaptiveThinkingPrefixes {
		if model == prefix || strings.HasPrefix(model, prefix+"-") {
			return true
		}
	}
	return false
}

// anthropicMessage represents a message in Anthropic format
type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicContentBlock struct {
	Type      string                  `json:"type"`
	Text      string                  `json:"text,omitempty"`
	ID        string                  `json:"id,omitempty"`
	Name      string                  `json:"name,omitempty"`
	Input     any                     `json:"input,omitempty"`
	ToolUseID string                  `json:"tool_use_id,omitempty"`
	Content   any                     `json:"content,omitempty"`
	IsError   bool                    `json:"is_error,omitempty"`
	Source    *anthropicContentSource `json:"source,omitempty"`
}

type anthropicContentSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// anthropicResponse represents the Anthropic API response format
type anthropicResponse struct {
	ID         string             `json:"id"`
	Type       string             `json:"type"`
	Role       string             `json:"role"`
	Content    []anthropicContent `json:"content"`
	Model      string             `json:"model"`
	StopReason string             `json:"stop_reason"`
	Usage      anthropicUsage     `json:"usage"`
}

// anthropicContent represents content in Anthropic response
type anthropicContent struct {
	Type  string          `json:"type"`
	Text  string          `json:"text"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

// anthropicUsage represents token usage in Anthropic response
type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// anthropicStreamEvent represents a streaming event from Anthropic
type anthropicStreamEvent struct {
	Type         string             `json:"type"`
	Index        int                `json:"index,omitempty"`
	Delta        *anthropicDelta    `json:"delta,omitempty"`
	ContentBlock *anthropicContent  `json:"content_block,omitempty"`
	Message      *anthropicResponse `json:"message,omitempty"`
	Usage        *anthropicUsage    `json:"usage,omitempty"`
}

// anthropicDelta represents a delta in streaming response
type anthropicDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

// anthropicModelInfo represents a model in Anthropic's models API response
type anthropicModelInfo struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	CreatedAt   string `json:"created_at"`
	DisplayName string `json:"display_name"`
}

// anthropicModelsResponse represents the Anthropic models API response
type anthropicModelsResponse struct {
	Data    []anthropicModelInfo `json:"data"`
	FirstID string               `json:"first_id"`
	HasMore bool                 `json:"has_more"`
	LastID  string               `json:"last_id"`
}

type anthropicBatchCreateRequest struct {
	Requests []anthropicBatchRequest `json:"requests"`
}

type anthropicBatchRequest struct {
	CustomID string           `json:"custom_id"`
	Params   anthropicRequest `json:"params"`
}

type anthropicBatchRequestCounts struct {
	Processing int `json:"processing"`
	Succeeded  int `json:"succeeded"`
	Errored    int `json:"errored"`
	Canceled   int `json:"canceled"`
	Expired    int `json:"expired"`
}

type anthropicBatchResponse struct {
	ID                string                      `json:"id"`
	Type              string                      `json:"type"`
	ProcessingStatus  string                      `json:"processing_status"`
	RequestCounts     anthropicBatchRequestCounts `json:"request_counts"`
	CreatedAt         string                      `json:"created_at"`
	EndedAt           string                      `json:"ended_at"`
	CancelInitiatedAt string                      `json:"cancel_initiated_at"`
}

type anthropicBatchListResponse struct {
	Data    []anthropicBatchResponse `json:"data"`
	FirstID string                   `json:"first_id"`
	LastID  string                   `json:"last_id"`
	HasMore bool                     `json:"has_more"`
}

type anthropicBatchResultLine struct {
	CustomID string `json:"custom_id"`
	Result   struct {
		Type    string          `json:"type"`
		Message json.RawMessage `json:"message,omitempty"`
		Error   *struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	} `json:"result"`
}

// normalizeEffort maps effort to gateway-supported values. Anthropic Opus 4.6
// supports "max" for adaptive thinking, but the gateway's public type
// core.Reasoning.Effort only exposes "low", "medium", and "high". "max" is
// therefore intentionally rejected; any unsupported value is downgraded to
// "low" and logged via slog.Warn.
func normalizeEffort(effort string) string {
	switch effort {
	case "low", "medium", "high":
		return effort
	default:
		slog.Warn("invalid reasoning effort, defaulting to 'low'", "effort", effort)
		return "low"
	}
}

// convertFromAnthropicResponse converts Anthropic response to core.ChatResponse
func convertFromAnthropicResponse(resp *anthropicResponse) *core.ChatResponse {
	content := extractTextContent(resp.Content)
	toolCalls := extractToolCalls(resp.Content)

	finishReason := normalizeAnthropicStopReason(resp.StopReason)
	if finishReason == "" {
		finishReason = "stop"
	}

	usage := core.Usage{
		PromptTokens:     resp.Usage.InputTokens,
		CompletionTokens: resp.Usage.OutputTokens,
		TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
	}

	rawUsage := buildAnthropicRawUsage(resp.Usage)
	if len(rawUsage) > 0 {
		usage.RawUsage = rawUsage
	}

	return &core.ChatResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Model:   resp.Model,
		Created: time.Now().Unix(),
		Choices: []core.Choice{
			{
				Index: 0,
				Message: core.ResponseMessage{
					Role:      "assistant",
					Content:   content,
					ToolCalls: toolCalls,
				},
				FinishReason: finishReason,
			},
		},
		Usage: usage,
	}
}

// ChatCompletion sends a chat completion request to Anthropic
func (p *Provider) ChatCompletion(ctx context.Context, req *core.ChatRequest) (*core.ChatResponse, error) {
	anthropicReq, err := convertToAnthropicRequest(req)
	if err != nil {
		return nil, err
	}

	var anthropicResp anthropicResponse
	err = p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/messages",
		Body:     anthropicReq,
	}, &anthropicResp)
	if err != nil {
		return nil, err
	}

	return convertFromAnthropicResponse(&anthropicResp), nil
}

// StreamChatCompletion returns a raw response body for streaming (caller must close)
func (p *Provider) StreamChatCompletion(ctx context.Context, req *core.ChatRequest) (io.ReadCloser, error) {
	anthropicReq, err := convertToAnthropicRequest(req)
	if err != nil {
		return nil, err
	}
	anthropicReq.Stream = true

	stream, err := p.client.DoStream(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/messages",
		Body:     anthropicReq,
	})
	if err != nil {
		return nil, err
	}

	// Return a reader that converts Anthropic SSE format to OpenAI format
	return newStreamConverter(stream, req.Model), nil
}

// streamConverter wraps an Anthropic stream and converts it to OpenAI format
type streamConverter struct {
	reader            *bufio.Reader
	body              io.ReadCloser
	model             string
	msgID             string
	nextToolCallIndex int
	toolCalls         map[int]*streamToolCallState
	usage             anthropicUsage
	hasUsage          bool
	buffer            []byte
	closed            bool
	emittedToolCalls  bool
}

type streamToolCallState struct {
	ID                string
	Name              string
	Arguments         strings.Builder
	Index             int
	Started           bool
	PlaceholderObject bool
}

func newStreamConverter(body io.ReadCloser, model string) *streamConverter {
	return &streamConverter{
		reader:    bufio.NewReader(body),
		body:      body,
		model:     model,
		toolCalls: make(map[int]*streamToolCallState),
		buffer:    make([]byte, 0, 1024),
	}
}

func malformedAnthropicStreamError(err error) error {
	return core.NewProviderError("anthropic", http.StatusBadGateway, "failed to decode anthropic stream event: "+err.Error(), err)
}

func mergeAnthropicUsage(dst *anthropicUsage, src *anthropicUsage) bool {
	if dst == nil || src == nil {
		return false
	}

	merged := false
	if src.InputTokens != 0 {
		dst.InputTokens = src.InputTokens
		merged = true
	}
	if src.OutputTokens != 0 {
		dst.OutputTokens = src.OutputTokens
		merged = true
	}
	if src.CacheCreationInputTokens != 0 {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
		merged = true
	}
	if src.CacheReadInputTokens != 0 {
		dst.CacheReadInputTokens = src.CacheReadInputTokens
		merged = true
	}

	return merged
}

func anthropicChatUsagePayload(usage *anthropicUsage) map[string]any {
	if usage == nil {
		return nil
	}

	payload := map[string]any{
		"prompt_tokens":     usage.InputTokens,
		"completion_tokens": usage.OutputTokens,
		"total_tokens":      usage.InputTokens + usage.OutputTokens,
	}
	if usage.CacheCreationInputTokens > 0 {
		payload["cache_creation_input_tokens"] = usage.CacheCreationInputTokens
	}
	if usage.CacheReadInputTokens > 0 {
		payload["cache_read_input_tokens"] = usage.CacheReadInputTokens
	}
	return payload
}

func anthropicResponsesUsagePayload(usage *anthropicUsage) map[string]any {
	if usage == nil {
		return nil
	}

	payload := map[string]any{
		"input_tokens":  usage.InputTokens,
		"output_tokens": usage.OutputTokens,
		"total_tokens":  usage.InputTokens + usage.OutputTokens,
	}
	if usage.CacheCreationInputTokens > 0 {
		payload["cache_creation_input_tokens"] = usage.CacheCreationInputTokens
	}
	if usage.CacheReadInputTokens > 0 {
		payload["cache_read_input_tokens"] = usage.CacheReadInputTokens
	}
	return payload
}

func (sc *streamConverter) Read(p []byte) (n int, err error) {
	if sc.closed {
		return 0, io.EOF
	}

	// If we have buffered data, return it first
	if len(sc.buffer) > 0 {
		n = copy(p, sc.buffer)
		sc.buffer = sc.buffer[n:]
		return n, nil
	}

	// Read the next SSE event from Anthropic
	for {
		line, err := sc.reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				// Send final [DONE] message
				doneMsg := "data: [DONE]\n\n"
				n = copy(p, doneMsg)
				if n < len(doneMsg) {
					sc.buffer = append(sc.buffer, []byte(doneMsg)[n:]...)
				}
				sc.closed = true
				_ = sc.body.Close() //nolint:errcheck
				return n, nil
			}
			return 0, err
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		// Parse SSE line
		if bytes.HasPrefix(line, []byte("event:")) {
			continue // Skip event type lines
		}

		if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))

			var event anthropicStreamEvent
			if err := json.Unmarshal(data, &event); err != nil {
				sc.closed = true
				_ = sc.body.Close() //nolint:errcheck
				return 0, malformedAnthropicStreamError(err)
			}

			// Convert Anthropic event to OpenAI format
			openAIChunk := sc.convertEvent(&event)
			if openAIChunk == "" {
				continue
			}

			// Buffer the converted chunk
			sc.buffer = append(sc.buffer, []byte(openAIChunk)...)

			// Return as much as we can
			n = copy(p, sc.buffer)
			sc.buffer = sc.buffer[n:]
			return n, nil
		}
	}
}

func (sc *streamConverter) Close() error {
	sc.closed = true
	return sc.body.Close()
}

func (sc *streamConverter) mapStreamStopReason(reason string) string {
	// Preserve raw "tool_use" when the upstream stream never produced any
	// tool call deltas. This avoids claiming OpenAI-style tool calls for a
	// malformed or partial Anthropic stream.
	if reason == "tool_use" && !sc.emittedToolCalls {
		return reason
	}
	return normalizeAnthropicStopReason(reason)
}

func extractInitialToolArguments(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}

	trimmed := strings.TrimSpace(string(input))
	if trimmed == "" || trimmed == "null" {
		return ""
	}

	var parsed any
	if err := json.Unmarshal(input, &parsed); err != nil {
		return trimmed
	}

	canonical, err := json.Marshal(parsed)
	if err != nil {
		return trimmed
	}

	return string(canonical)
}

func normalizeAnthropicStopReason(stopReason string) string {
	switch stopReason {
	case "tool_use":
		return "tool_calls"
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	default:
		return stopReason
	}
}

func (sc *streamConverter) formatChatChunk(delta map[string]any, finishReason any, usage *anthropicUsage) string {
	chunk := map[string]any{
		"id":       sc.msgID,
		"object":   "chat.completion.chunk",
		"created":  time.Now().Unix(),
		"model":    sc.model,
		"provider": "anthropic",
		"choices": []map[string]any{
			{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			},
		},
	}
	if usage != nil {
		chunk["usage"] = anthropicChatUsagePayload(usage)
	}

	jsonData, err := json.Marshal(chunk)
	if err != nil {
		slog.Error("failed to marshal chat completion chunk", "error", err, "msg_id", sc.msgID)
		return ""
	}

	return fmt.Sprintf("data: %s\n\n", jsonData)
}

func (sc *streamConverter) convertEvent(event *anthropicStreamEvent) string {
	switch event.Type {
	case "message_start":
		if event.Message != nil {
			sc.msgID = event.Message.ID
			if mergeAnthropicUsage(&sc.usage, &event.Message.Usage) {
				sc.hasUsage = true
			}
		}
		if mergeAnthropicUsage(&sc.usage, event.Usage) {
			sc.hasUsage = true
		}
		return ""

	case "content_block_start":
		if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
			state := &streamToolCallState{
				ID:    event.ContentBlock.ID,
				Name:  event.ContentBlock.Name,
				Index: sc.nextToolCallIndex,
			}
			sc.nextToolCallIndex++

			initialArguments := extractInitialToolArguments(event.ContentBlock.Input)
			state.PlaceholderObject = initialArguments == "{}"
			if state.PlaceholderObject {
				sc.toolCalls[event.Index] = state
				return ""
			}
			if initialArguments != "" {
				_, _ = state.Arguments.WriteString(initialArguments)
			}
			state.Started = true
			sc.toolCalls[event.Index] = state
			sc.emittedToolCalls = true

			return sc.formatChatChunk(map[string]any{
				"tool_calls": []map[string]any{
					{
						"index": state.Index,
						"id":    state.ID,
						"type":  "function",
						"function": map[string]any{
							"name":      state.Name,
							"arguments": initialArguments,
						},
					},
				},
			}, nil, nil)
		}
		return ""

	case "content_block_delta":
		if event.Delta == nil {
			return ""
		}

		switch event.Delta.Type {
		case "text_delta":
			if event.Delta.Text != "" {
				return sc.formatChatChunk(map[string]any{
					"content": event.Delta.Text,
				}, nil, nil)
			}
		case "input_json_delta":
			if event.Delta.PartialJSON == "" {
				return ""
			}
			state := sc.toolCalls[event.Index]
			if state == nil {
				return ""
			}
			if state.PlaceholderObject {
				state.Arguments = strings.Builder{}
				state.PlaceholderObject = false
			}
			_, _ = state.Arguments.WriteString(event.Delta.PartialJSON)
			if !state.Started {
				state.Started = true
				sc.emittedToolCalls = true
				return sc.formatChatChunk(map[string]any{
					"tool_calls": []map[string]any{
						{
							"index": state.Index,
							"id":    state.ID,
							"type":  "function",
							"function": map[string]any{
								"name":      state.Name,
								"arguments": event.Delta.PartialJSON,
							},
						},
					},
				}, nil, nil)
			}
			sc.emittedToolCalls = true
			return sc.formatChatChunk(map[string]any{
				"tool_calls": []map[string]any{
					{
						"index": state.Index,
						"function": map[string]any{
							"arguments": event.Delta.PartialJSON,
						},
					},
				},
			}, nil, nil)
		}

	case "content_block_stop":
		state := sc.toolCalls[event.Index]
		if state != nil && !state.Started && state.PlaceholderObject {
			state.Started = true
			sc.emittedToolCalls = true
			return sc.formatChatChunk(map[string]any{
				"tool_calls": []map[string]any{
					{
						"index": state.Index,
						"id":    state.ID,
						"type":  "function",
						"function": map[string]any{
							"name":      state.Name,
							"arguments": "{}",
						},
					},
				},
			}, nil, nil)
		}
		return ""

	case "message_delta":
		if mergeAnthropicUsage(&sc.usage, event.Usage) {
			sc.hasUsage = true
		}
		// Emit chunk if we have stop_reason or usage data
		if (event.Delta != nil && event.Delta.StopReason != "") || event.Usage != nil {
			var finishReason interface{}
			if event.Delta != nil && event.Delta.StopReason != "" {
				finishReason = sc.mapStreamStopReason(event.Delta.StopReason)
			}
			var usage *anthropicUsage
			if sc.hasUsage {
				usage = &sc.usage
			}
			return sc.formatChatChunk(map[string]any{}, finishReason, usage)
		}

	case "message_stop":
		return ""
	}

	return ""
}

// ListModels retrieves the list of available models from Anthropic's /v1/models endpoint
func (p *Provider) ListModels(ctx context.Context) (*core.ModelsResponse, error) {
	var anthropicResp anthropicModelsResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/models?limit=1000",
	}, &anthropicResp)
	if err != nil {
		return nil, err
	}

	// Convert to core.Model format
	models := make([]core.Model, 0, len(anthropicResp.Data))
	for _, m := range anthropicResp.Data {
		created := parseCreatedAt(m.CreatedAt)
		models = append(models, core.Model{
			ID:      m.ID,
			Object:  "model",
			OwnedBy: "anthropic",
			Created: created,
		})
	}

	return &core.ModelsResponse{
		Object: "list",
		Data:   models,
	}, nil
}

// parseCreatedAt parses an RFC3339 timestamp string to Unix timestamp
func parseCreatedAt(createdAt string) int64 {
	t, err := time.Parse(time.RFC3339, createdAt)
	if err != nil {
		return time.Now().Unix()
	}
	return t.Unix()
}

// extractTextContent returns the text from the last "text" content block.
// When extended thinking is enabled, Anthropic returns: [text("\n\n"), thinking(...), text(answer)].
// Taking the last text block ensures we get the actual answer, not the empty preamble.
func extractTextContent(blocks []anthropicContent) string {
	last := ""
	for _, b := range blocks {
		if b.Type == "text" {
			last = b.Text
		}
	}
	return last
}

// extractToolCalls maps Anthropic "tool_use" content blocks to OpenAI-compatible tool calls.
func extractToolCalls(blocks []anthropicContent) []core.ToolCall {
	out := make([]core.ToolCall, 0)
	for _, b := range blocks {
		if b.Type != "tool_use" || b.Name == "" {
			continue
		}

		arguments := "{}"
		if len(b.Input) > 0 {
			var parsed any
			if err := json.Unmarshal(b.Input, &parsed); err == nil {
				if canonical, err := json.Marshal(parsed); err == nil {
					arguments = string(canonical)
				}
			} else {
				trimmed := strings.TrimSpace(string(b.Input))
				if trimmed != "" {
					arguments = trimmed
				}
			}
		}

		out = append(out, core.ToolCall{
			ID:   b.ID,
			Type: "function",
			Function: core.FunctionCall{
				Name:      b.Name,
				Arguments: arguments,
			},
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// convertAnthropicResponseToResponses converts an Anthropic response to ResponsesResponse
func convertAnthropicResponseToResponses(resp *anthropicResponse, model string) *core.ResponsesResponse {
	content := extractTextContent(resp.Content)
	toolCalls := extractToolCalls(resp.Content)

	msg := core.Message{
		Content:   content,
		ToolCalls: toolCalls,
	}
	output := providers.BuildResponsesOutputItems(core.ResponseMessage{
		Role:      "assistant",
		Content:   msg.Content,
		ToolCalls: msg.ToolCalls,
	})

	return &core.ResponsesResponse{
		ID:        resp.ID,
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Model:     model,
		Status:    "completed",
		Output:    output,
		Usage:     buildAnthropicResponsesUsage(resp.Usage),
	}
}

// buildAnthropicRawUsage extracts cache fields from anthropicUsage into a RawData map.
func buildAnthropicRawUsage(u anthropicUsage) map[string]any {
	raw := make(map[string]any)
	if u.CacheCreationInputTokens > 0 {
		raw["cache_creation_input_tokens"] = u.CacheCreationInputTokens
	}
	if u.CacheReadInputTokens > 0 {
		raw["cache_read_input_tokens"] = u.CacheReadInputTokens
	}
	if len(raw) == 0 {
		return nil
	}
	return raw
}

// buildAnthropicResponsesUsage creates a ResponsesUsage from anthropicUsage, including RawUsage.
func buildAnthropicResponsesUsage(u anthropicUsage) *core.ResponsesUsage {
	usage := &core.ResponsesUsage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		TotalTokens:  u.InputTokens + u.OutputTokens,
	}
	rawUsage := buildAnthropicRawUsage(u)
	if len(rawUsage) > 0 {
		usage.RawUsage = rawUsage
	}
	return usage
}

// Responses sends a Responses API request to Anthropic (converted to messages format)
func (p *Provider) Responses(ctx context.Context, req *core.ResponsesRequest) (*core.ResponsesResponse, error) {
	anthropicReq, err := convertResponsesRequestToAnthropic(req)
	if err != nil {
		return nil, err
	}

	var anthropicResp anthropicResponse
	err = p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/messages",
		Body:     anthropicReq,
	}, &anthropicResp)
	if err != nil {
		return nil, err
	}

	return convertAnthropicResponseToResponses(&anthropicResp, req.Model), nil
}

func parseOptionalUnix(ts string) *int64 {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return nil
	}
	u := t.Unix()
	return &u
}

func mapAnthropicBatchResponse(resp *anthropicBatchResponse) *core.BatchResponse {
	if resp == nil {
		return nil
	}

	total := resp.RequestCounts.Processing + resp.RequestCounts.Succeeded + resp.RequestCounts.Errored + resp.RequestCounts.Canceled + resp.RequestCounts.Expired
	failed := resp.RequestCounts.Errored + resp.RequestCounts.Canceled + resp.RequestCounts.Expired

	status := "in_progress"
	switch resp.ProcessingStatus {
	case "canceling":
		status = "cancelling"
	case "ended":
		switch {
		case resp.RequestCounts.Canceled > 0 && resp.RequestCounts.Succeeded == 0 && resp.RequestCounts.Errored == 0:
			status = "cancelled"
		case resp.RequestCounts.Errored > 0 && resp.RequestCounts.Succeeded == 0:
			status = "failed"
		default:
			status = "completed"
		}
	}

	return &core.BatchResponse{
		ID:           resp.ID,
		Object:       "batch",
		Status:       status,
		CreatedAt:    parseCreatedAt(resp.CreatedAt),
		CompletedAt:  parseOptionalUnix(resp.EndedAt),
		CancellingAt: parseOptionalUnix(resp.CancelInitiatedAt),
		RequestCounts: core.BatchRequestCounts{
			Total:     total,
			Completed: resp.RequestCounts.Succeeded,
			Failed:    failed,
		},
	}
}

func buildAnthropicBatchCreateRequest(req *core.BatchRequest) (*anthropicBatchCreateRequest, map[string]string, error) {
	const maxAnthropicBatchRequests = 10000

	if req == nil {
		return nil, nil, core.NewInvalidRequestError("request is required for anthropic batch processing", nil)
	}
	if len(req.Requests) == 0 {
		return nil, nil, core.NewInvalidRequestError("requests is required for anthropic batch processing", nil)
	}
	if len(req.Requests) > maxAnthropicBatchRequests {
		return nil, nil, core.NewInvalidRequestError("too many requests for anthropic batch processing", nil)
	}

	out := &anthropicBatchCreateRequest{
		Requests: make([]anthropicBatchRequest, 0, len(req.Requests)),
	}
	endpointByCustomID := make(map[string]string, len(req.Requests))

	for i, item := range req.Requests {
		decoded, err := core.DecodeKnownBatchItemRequest(req.Endpoint, item)
		if err != nil {
			return nil, nil, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: %s", i, err.Error()), err)
		}

		params, err := convertDecodedBatchItemToAnthropic(decoded)
		if err != nil {
			return nil, nil, prefixAnthropicBatchItemError(i, err)
		}

		customID := strings.TrimSpace(item.CustomID)
		if customID == "" {
			customID = fmt.Sprintf("req-%d", i)
		}
		out.Requests = append(out.Requests, anthropicBatchRequest{
			CustomID: customID,
			Params:   *params,
		})
		endpointByCustomID[customID] = decoded.Endpoint
	}

	return out, endpointByCustomID, nil
}

// CreateBatch creates an Anthropic native message batch.
func (p *Provider) CreateBatch(ctx context.Context, req *core.BatchRequest) (*core.BatchResponse, error) {
	anthropicReq, endpointByCustomID, err := buildAnthropicBatchCreateRequest(req)
	if err != nil {
		return nil, err
	}

	var resp anthropicBatchResponse
	err = p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/messages/batches",
		Body:     anthropicReq,
	}, &resp)
	if err != nil {
		return nil, err
	}

	mapped := mapAnthropicBatchResponse(&resp)
	if mapped == nil {
		return nil, core.NewProviderError("anthropic", http.StatusBadGateway, "failed to map anthropic batch response", nil)
	}
	mapped.ProviderBatchID = mapped.ID
	p.setBatchResultEndpoints(mapped.ProviderBatchID, endpointByCustomID)
	return mapped, nil
}

// GetBatch retrieves an Anthropic native message batch.
func (p *Provider) GetBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	var resp anthropicBatchResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/messages/batches/" + url.PathEscape(id),
	}, &resp)
	if err != nil {
		return nil, err
	}
	mapped := mapAnthropicBatchResponse(&resp)
	if mapped == nil {
		return nil, core.NewProviderError("anthropic", http.StatusBadGateway, "failed to map anthropic batch response", nil)
	}
	mapped.ProviderBatchID = mapped.ID
	return mapped, nil
}

// ListBatches lists Anthropic native message batches.
func (p *Provider) ListBatches(ctx context.Context, limit int, after string) (*core.BatchListResponse, error) {
	values := url.Values{}
	if limit > 0 {
		values.Set("limit", strconv.Itoa(limit))
	}
	// Anthropic uses before_id for reverse-chronological pagination.
	// Gateway `after` is mapped directly to before_id for provider-native paging.
	if after != "" {
		values.Set("before_id", after)
	}
	endpoint := "/messages/batches"
	if encoded := values.Encode(); encoded != "" {
		endpoint += "?" + encoded
	}

	var resp anthropicBatchListResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: endpoint,
	}, &resp)
	if err != nil {
		return nil, err
	}

	data := make([]core.BatchResponse, 0, len(resp.Data))
	for _, row := range resp.Data {
		mapped := mapAnthropicBatchResponse(&row)
		if mapped == nil {
			continue
		}
		mapped.ProviderBatchID = mapped.ID
		data = append(data, *mapped)
	}

	return &core.BatchListResponse{
		Object:  "list",
		Data:    data,
		HasMore: resp.HasMore,
		FirstID: resp.FirstID,
		LastID:  resp.LastID,
	}, nil
}

// CancelBatch cancels an Anthropic native message batch.
func (p *Provider) CancelBatch(ctx context.Context, id string) (*core.BatchResponse, error) {
	var resp anthropicBatchResponse
	err := p.client.Do(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/messages/batches/" + url.PathEscape(id) + "/cancel",
	}, &resp)
	if err != nil {
		return nil, err
	}
	mapped := mapAnthropicBatchResponse(&resp)
	if mapped == nil {
		return nil, core.NewProviderError("anthropic", http.StatusBadGateway, "failed to map anthropic batch response", nil)
	}
	mapped.ProviderBatchID = mapped.ID
	return mapped, nil
}

// GetBatchResults retrieves Anthropic native message batch results.
func (p *Provider) GetBatchResults(ctx context.Context, id string) (*core.BatchResultsResponse, error) {
	raw, err := p.client.DoRaw(ctx, llmclient.Request{
		Method:   http.MethodGet,
		Endpoint: "/messages/batches/" + url.PathEscape(id) + "/results",
	})
	if err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw.Body))
	// Allow larger result lines than Scanner's default 64K.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	endpointByCustomID := p.getBatchResultEndpoints(id)

	results := make([]core.BatchResultItem, 0)
	index := 0
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}

		var row anthropicBatchResultLine
		if err := json.Unmarshal(line, &row); err != nil {
			slog.Warn(
				"failed to decode anthropic batch result line",
				"error", err,
				"batch_id", id,
				"line_index", index,
				"line_bytes", len(line),
			)
			continue
		}
		itemEndpoint := "/v1/chat/completions"
		if endpointByCustomID != nil {
			if endpoint := strings.TrimSpace(endpointByCustomID[row.CustomID]); endpoint != "" {
				itemEndpoint = endpoint
			}
		}

		item := core.BatchResultItem{
			Index:    index,
			CustomID: row.CustomID,
			URL:      itemEndpoint,
			Provider: "anthropic",
		}
		switch row.Result.Type {
		case "succeeded":
			item.StatusCode = http.StatusOK
			if len(row.Result.Message) > 0 {
				var anthropicPayload anthropicResponse
				if err := json.Unmarshal(row.Result.Message, &anthropicPayload); err == nil {
					switch itemEndpoint {
					case "/v1/responses":
						mapped := convertAnthropicResponseToResponses(&anthropicPayload, anthropicPayload.Model)
						item.Response = mapped
						item.Model = mapped.Model
					default:
						mapped := convertFromAnthropicResponse(&anthropicPayload)
						item.Response = mapped
						item.Model = mapped.Model
					}
				} else {
					item.Response = string(row.Result.Message)
				}
			}
		default:
			item.StatusCode = http.StatusBadRequest
			errType := row.Result.Type
			errMsg := "batch item failed"
			if row.Result.Error != nil {
				if row.Result.Error.Type != "" {
					errType = row.Result.Error.Type
				}
				if row.Result.Error.Message != "" {
					errMsg = row.Result.Error.Message
				}
			}
			item.Error = &core.BatchError{
				Type:    errType,
				Message: errMsg,
			}
		}

		results = append(results, item)
		index++
	}
	if err := scanner.Err(); err != nil {
		return nil, core.NewProviderError("anthropic", http.StatusBadGateway, "failed to parse anthropic batch results", err)
	}

	return &core.BatchResultsResponse{
		Object:  "list",
		BatchID: id,
		Data:    results,
	}, nil
}

// Embeddings returns an error because Anthropic does not natively support embeddings.
// Voyage AI (Anthropic's recommended embedding provider) may be added in the future.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("anthropic does not support embeddings — consider using Voyage AI", nil)
}

// StreamResponses returns a raw response body for streaming Responses API (caller must close)
func (p *Provider) StreamResponses(ctx context.Context, req *core.ResponsesRequest) (io.ReadCloser, error) {
	anthropicReq, err := convertResponsesRequestToAnthropic(req)
	if err != nil {
		return nil, err
	}
	anthropicReq.Stream = true

	stream, err := p.client.DoStream(ctx, llmclient.Request{
		Method:   http.MethodPost,
		Endpoint: "/messages",
		Body:     anthropicReq,
	})
	if err != nil {
		return nil, err
	}

	// Return a reader that converts Anthropic SSE format to Responses API format
	return newResponsesStreamConverter(stream, req.Model), nil
}

// responsesStreamConverter wraps an Anthropic stream and converts it to Responses API format
type responsesStreamConverter struct {
	reader          *bufio.Reader
	body            io.ReadCloser
	model           string
	responseID      string
	output          *providers.ResponsesOutputEventState
	nextOutputIndex int
	toolCalls       map[int]*providers.ResponsesOutputToolCallState
	buffer          []byte
	closed          bool
	sentDone        bool
	usage           anthropicUsage
	hasUsage        bool
}

func newResponsesStreamConverter(body io.ReadCloser, model string) *responsesStreamConverter {
	responseID := "resp_" + uuid.New().String()
	return &responsesStreamConverter{
		reader:     bufio.NewReader(body),
		body:       body,
		model:      model,
		responseID: responseID,
		output:     providers.NewResponsesOutputEventState(responseID),
		toolCalls:  make(map[int]*providers.ResponsesOutputToolCallState),
		buffer:     make([]byte, 0, 1024),
	}
}

func (sc *responsesStreamConverter) Read(p []byte) (n int, err error) {
	if sc.closed {
		return 0, io.EOF
	}

	// If we have buffered data, return it first
	if len(sc.buffer) > 0 {
		n = copy(p, sc.buffer)
		sc.buffer = sc.buffer[n:]
		return n, nil
	}

	// Read the next SSE event from Anthropic
	for {
		line, err := sc.reader.ReadBytes('\n')
		if err != nil {
			if err == io.EOF {
				// Send final done event and [DONE] message
				if !sc.sentDone {
					sc.sentDone = true
					prefix := sc.output.CompleteAssistantOutput(0)
					responseData := map[string]interface{}{
						"id":         sc.responseID,
						"object":     "response",
						"status":     "completed",
						"model":      sc.model,
						"provider":   "anthropic",
						"created_at": time.Now().Unix(),
					}
					// Include merged usage data captured across message_start/message_delta.
					if sc.hasUsage {
						responseData["usage"] = anthropicResponsesUsagePayload(&sc.usage)
					}
					doneEvent := map[string]interface{}{
						"type":     "response.completed",
						"response": responseData,
					}
					jsonData, marshalErr := json.Marshal(doneEvent)
					if marshalErr != nil {
						slog.Error("failed to marshal response.completed event", "error", marshalErr, "response_id", sc.responseID)
						sc.closed = true
						_ = sc.body.Close() //nolint:errcheck
						return 0, io.EOF
					}
					doneMsg := prefix + fmt.Sprintf("event: response.completed\ndata: %s\n\ndata: [DONE]\n\n", jsonData)
					n = copy(p, doneMsg)
					if n < len(doneMsg) {
						sc.buffer = append(sc.buffer, []byte(doneMsg)[n:]...)
					}
					return n, nil
				}
				sc.closed = true
				_ = sc.body.Close() //nolint:errcheck
				return 0, io.EOF
			}
			return 0, err
		}

		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}

		// Parse SSE line
		if bytes.HasPrefix(line, []byte("event:")) {
			continue // Skip event type lines
		}

		if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))

			var event anthropicStreamEvent
			if err := json.Unmarshal(data, &event); err != nil {
				sc.closed = true
				_ = sc.body.Close() //nolint:errcheck
				return 0, malformedAnthropicStreamError(err)
			}

			// Convert Anthropic event to Responses API format
			responsesChunk := sc.convertEvent(&event)
			if responsesChunk == "" {
				continue
			}

			// Buffer the converted chunk
			sc.buffer = append(sc.buffer, []byte(responsesChunk)...)

			// Return as much as we can
			n = copy(p, sc.buffer)
			sc.buffer = sc.buffer[n:]
			return n, nil
		}
	}
}

func (sc *responsesStreamConverter) Close() error {
	sc.closed = true
	return sc.body.Close()
}

func (sc *responsesStreamConverter) reserveAssistantMessageOutput() {
	if sc.output.AssistantReserved() {
		return
	}
	sc.output.ReserveAssistant()
	sc.nextOutputIndex++
}

func (sc *responsesStreamConverter) newResponsesToolCallState(contentBlock *anthropicContent) *providers.ResponsesOutputToolCallState {
	callID := providers.ResponsesFunctionCallCallID(contentBlock.ID)
	state := &providers.ResponsesOutputToolCallState{
		CallID:      callID,
		Name:        contentBlock.Name,
		OutputIndex: sc.nextOutputIndex,
	}
	sc.nextOutputIndex++

	initialArguments := extractInitialToolArguments(contentBlock.Input)
	state.PlaceholderObject = initialArguments == "{}"
	if initialArguments != "" && !state.PlaceholderObject {
		_, _ = state.Arguments.WriteString(initialArguments)
	}

	return state
}

func (sc *responsesStreamConverter) convertEvent(event *anthropicStreamEvent) string {
	switch event.Type {
	case "message_start":
		if event.Message != nil {
			if mergeAnthropicUsage(&sc.usage, &event.Message.Usage) {
				sc.hasUsage = true
			}
		}
		if mergeAnthropicUsage(&sc.usage, event.Usage) {
			sc.hasUsage = true
		}
		// Send response.created event
		createdEvent := map[string]interface{}{
			"type": "response.created",
			"response": map[string]interface{}{
				"id":         sc.responseID,
				"object":     "response",
				"status":     "in_progress",
				"model":      sc.model,
				"provider":   "anthropic",
				"created_at": time.Now().Unix(),
			},
		}
		jsonData, err := json.Marshal(createdEvent)
		if err != nil {
			slog.Error("failed to marshal response.created event", "error", err, "response_id", sc.responseID)
			return ""
		}
		return fmt.Sprintf("event: response.created\ndata: %s\n\n", jsonData)

	case "content_block_start":
		if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
			if sc.output.AssistantStarted() && !sc.output.AssistantDone() {
				prefix := sc.output.CompleteAssistantOutput(0)
				state := sc.newResponsesToolCallState(event.ContentBlock)
				sc.toolCalls[event.Index] = state
				return prefix + sc.output.StartToolCall(state, true)
			}
			state := sc.newResponsesToolCallState(event.ContentBlock)
			sc.toolCalls[event.Index] = state
			return sc.output.StartToolCall(state, true)
		}
		return ""

	case "content_block_delta":
		if event.Delta == nil {
			return ""
		}

		switch event.Delta.Type {
		case "text_delta":
			if event.Delta.Text != "" {
				sc.reserveAssistantMessageOutput()
				prefix := sc.output.StartAssistantOutput(0)
				sc.output.AppendAssistantText(event.Delta.Text)
				deltaEvent := map[string]interface{}{
					"type":  "response.output_text.delta",
					"delta": event.Delta.Text,
				}
				jsonData, err := json.Marshal(deltaEvent)
				if err != nil {
					slog.Error("failed to marshal content delta event", "error", err, "response_id", sc.responseID)
					return ""
				}
				return prefix + fmt.Sprintf("event: response.output_text.delta\ndata: %s\n\n", jsonData)
			}
		case "input_json_delta":
			if event.Delta.PartialJSON == "" {
				return ""
			}
			state := sc.toolCalls[event.Index]
			if state == nil {
				return ""
			}
			if state.PlaceholderObject {
				state.Arguments = strings.Builder{}
				state.PlaceholderObject = false
			}
			_, _ = state.Arguments.WriteString(event.Delta.PartialJSON)
			return sc.output.WriteEvent("response.function_call_arguments.delta", map[string]any{
				"type":         "response.function_call_arguments.delta",
				"item_id":      state.ItemID,
				"output_index": state.OutputIndex,
				"delta":        event.Delta.PartialJSON,
			})
		}
		return ""

	case "content_block_stop":
		state := sc.toolCalls[event.Index]
		return sc.output.CompleteToolCall(state, true)

	case "message_delta":
		// Capture usage data for inclusion in response.completed
		if mergeAnthropicUsage(&sc.usage, event.Usage) {
			sc.hasUsage = true
		}
		if !sc.output.AssistantReserved() && len(sc.toolCalls) == 0 {
			sc.reserveAssistantMessageOutput()
		}
		return ""

	case "message_stop":
		// Will be handled in Read() when we get EOF
		return ""
	}

	return ""
}
