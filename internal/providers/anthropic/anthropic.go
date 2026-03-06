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
	defaultBaseURL      = "https://api.anthropic.com/v1"
	anthropicAPIVersion = "2023-06-01"
)

// Provider implements the core.Provider interface for Anthropic
type Provider struct {
	client *llmclient.Client
	apiKey string

	batchEndpointsMu sync.RWMutex
	// batchResultEndpoints keeps endpoint hints by provider batch id and custom_id.
	// Used only to shape native batch result items (e.g., /v1/responses vs /v1/chat/completions).
	batchResultEndpoints map[string]map[string]string
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
	p.batchResultEndpoints[batchID] = cloned
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
	MaxTokens    int                    `json:"max_tokens"`
	Temperature  *float64               `json:"temperature,omitempty"`
	System       string                 `json:"system,omitempty"`
	Stream       bool                   `json:"stream,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
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
	Content string `json:"content"`
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

// applyReasoning configures thinking and effort on an anthropicRequest.
// Opus 4.6 and Sonnet 4.6 use adaptive thinking with output_config.effort.
// Older models and Haiku 4.6 use manual thinking with budget_tokens.
func applyReasoning(req *anthropicRequest, model, effort string) {
	if isAdaptiveThinkingModel(model) {
		req.Thinking = &anthropicThinking{Type: "adaptive"}
		req.OutputConfig = &anthropicOutputConfig{Effort: normalizeEffort(effort)}
	} else {
		budget := reasoningEffortToBudgetTokens(effort)
		req.Thinking = &anthropicThinking{
			Type:         "enabled",
			BudgetTokens: budget,
		}
		if req.MaxTokens <= budget {
			adjusted := budget + 1024
			slog.Info("MaxTokens adjusted for extended thinking",
				"original", req.MaxTokens, "adjusted", adjusted)
			req.MaxTokens = adjusted
		}
	}

	if req.Temperature != nil {
		if *req.Temperature != 1.0 {
			slog.Warn("temperature overridden to nil; extended thinking requires temperature=1",
				"original_temperature", *req.Temperature)
			req.Temperature = nil
		}
	}
}

func reasoningEffortToBudgetTokens(effort string) int {
	switch normalizeEffort(effort) {
	case "medium":
		return 10000
	case "high":
		return 20000
	default:
		return 5000
	}
}

// convertToAnthropicRequest converts core.ChatRequest to Anthropic format
func convertToAnthropicRequest(req *core.ChatRequest) *anthropicRequest {
	anthropicReq := &anthropicRequest{
		Model:       req.Model,
		Messages:    make([]anthropicMessage, 0, len(req.Messages)),
		MaxTokens:   4096, // Default max tokens
		Temperature: req.Temperature,
		Stream:      req.Stream,
	}

	if req.MaxTokens != nil {
		anthropicReq.MaxTokens = *req.MaxTokens
	}

	if req.Reasoning != nil && req.Reasoning.Effort != "" {
		applyReasoning(anthropicReq, req.Model, req.Reasoning.Effort)
	}

	for _, msg := range req.Messages {
		if msg.Role == "system" {
			anthropicReq.System = msg.Content
		} else {
			anthropicReq.Messages = append(anthropicReq.Messages, anthropicMessage{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
	}

	return anthropicReq
}

func mapAnthropicStopReasonToOpenAI(reason string) string {
	switch reason {
	case "", "end_turn", "stop_sequence":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens", "model_context_window_exceeded":
		return "length"
	default:
		return reason
	}
}

// convertFromAnthropicResponse converts Anthropic response to core.ChatResponse
func convertFromAnthropicResponse(resp *anthropicResponse) *core.ChatResponse {
	content := extractTextContent(resp.Content)
	toolCalls := extractToolCalls(resp.Content)

	finishReason := mapAnthropicStopReasonToOpenAI(resp.StopReason)

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
				Message: core.Message{
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
	anthropicReq := convertToAnthropicRequest(req)

	var anthropicResp anthropicResponse
	err := p.client.Do(ctx, llmclient.Request{
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
	anthropicReq := convertToAnthropicRequest(req)
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
	reader *bufio.Reader
	body   io.ReadCloser
	model  string
	msgID  string
	buffer []byte
	closed bool

	toolCalls        map[int]streamToolCallState
	nextToolCallIdx  int
	emittedToolCalls bool
}

type streamToolCallState struct {
	ID                string
	Name              string
	Ordinal           int
	HasArgumentsDelta bool
}

func newStreamConverter(body io.ReadCloser, model string) *streamConverter {
	return &streamConverter{
		reader:    bufio.NewReader(body),
		body:      body,
		model:     model,
		buffer:    make([]byte, 0, 1024),
		toolCalls: make(map[int]streamToolCallState),
	}
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
				continue
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
	// Preserve raw "tool_use" when we never emitted tool_calls deltas.
	// This avoids signaling OpenAI-style "tool_calls" in malformed/partial
	// streams where no callable payload reached the client.
	if reason == "tool_use" && !sc.emittedToolCalls {
		return reason
	}
	return mapAnthropicStopReasonToOpenAI(reason)
}

func (sc *streamConverter) emitChatChunk(delta map[string]interface{}, finishReason interface{}, usage *anthropicUsage) string {
	chunk := map[string]interface{}{
		"id":       sc.msgID,
		"object":   "chat.completion.chunk",
		"created":  time.Now().Unix(),
		"model":    sc.model,
		"provider": "anthropic",
		"choices": []map[string]interface{}{
			{
				"index":         0,
				"delta":         delta,
				"finish_reason": finishReason,
			},
		},
	}
	if usage != nil {
		chunk["usage"] = map[string]interface{}{
			"prompt_tokens":     usage.InputTokens,
			"completion_tokens": usage.OutputTokens,
			"total_tokens":      usage.InputTokens + usage.OutputTokens,
		}
	}

	jsonData, err := json.Marshal(chunk)
	if err != nil {
		slog.Error("failed to marshal anthropic chat chunk", "error", err, "msg_id", sc.msgID)
		return ""
	}
	return fmt.Sprintf("data: %s\n\n", string(jsonData))
}

func (sc *streamConverter) getToolCallState(blockIndex int, block *anthropicContent) streamToolCallState {
	if state, ok := sc.toolCalls[blockIndex]; ok {
		return state
	}

	state := streamToolCallState{
		Ordinal: sc.nextToolCallIdx,
	}
	if block != nil {
		state.ID = block.ID
		state.Name = block.Name
	}
	sc.toolCalls[blockIndex] = state
	sc.nextToolCallIdx++
	return state
}

func (sc *streamConverter) convertEvent(event *anthropicStreamEvent) string {
	switch event.Type {
	case "message_start":
		if event.Message != nil {
			sc.msgID = event.Message.ID
		}
		return ""

	case "content_block_start":
		if event.ContentBlock != nil && event.ContentBlock.Type == "tool_use" {
			state := sc.getToolCallState(event.Index, event.ContentBlock)
			sc.emittedToolCalls = true
			return sc.emitChatChunk(map[string]interface{}{
				"tool_calls": []map[string]interface{}{
					{
						"index": state.Ordinal,
						"id":    state.ID,
						"type":  "function",
						"function": map[string]interface{}{
							"name":      state.Name,
							"arguments": "",
						},
					},
				},
			}, nil, nil)
		}
		return ""

	case "content_block_delta":
		if event.Delta != nil && event.Delta.Type == "input_json_delta" && event.Delta.PartialJSON != "" {
			state, ok := sc.toolCalls[event.Index]
			if !ok {
				return ""
			}
			state.HasArgumentsDelta = true
			sc.toolCalls[event.Index] = state
			sc.emittedToolCalls = true
			return sc.emitChatChunk(map[string]interface{}{
				"tool_calls": []map[string]interface{}{
					{
						"index": state.Ordinal,
						"function": map[string]interface{}{
							"arguments": event.Delta.PartialJSON,
						},
					},
				},
			}, nil, nil)
		}

		if event.Delta != nil && event.Delta.Text != "" {
			return sc.emitChatChunk(map[string]interface{}{
				"content": event.Delta.Text,
			}, nil, nil)
		}

	case "content_block_stop":
		if state, ok := sc.toolCalls[event.Index]; ok && !state.HasArgumentsDelta {
			return sc.emitChatChunk(map[string]interface{}{
				"tool_calls": []map[string]interface{}{
					{
						"index": state.Ordinal,
						"function": map[string]interface{}{
							"arguments": "{}",
						},
					},
				},
			}, nil, nil)
		}
		return ""

	case "message_delta":
		// Emit chunk if we have stop_reason or usage data
		if (event.Delta != nil && event.Delta.StopReason != "") || event.Usage != nil {
			var finishReason interface{}
			if event.Delta != nil && event.Delta.StopReason != "" {
				finishReason = sc.mapStreamStopReason(event.Delta.StopReason)
			}
			return sc.emitChatChunk(map[string]interface{}{}, finishReason, event.Usage)
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

// convertResponsesRequestToAnthropic converts a ResponsesRequest to Anthropic format
func convertResponsesRequestToAnthropic(req *core.ResponsesRequest) *anthropicRequest {
	anthropicReq := &anthropicRequest{
		Model:       req.Model,
		Messages:    make([]anthropicMessage, 0),
		MaxTokens:   4096, // Default max tokens
		Temperature: req.Temperature,
		Stream:      req.Stream,
	}

	if req.MaxOutputTokens != nil {
		anthropicReq.MaxTokens = *req.MaxOutputTokens
	}

	if req.Reasoning != nil && req.Reasoning.Effort != "" {
		applyReasoning(anthropicReq, req.Model, req.Reasoning.Effort)
	}

	// Set system instruction if provided
	if req.Instructions != "" {
		anthropicReq.System = req.Instructions
	}

	// Convert input to messages
	switch input := req.Input.(type) {
	case string:
		anthropicReq.Messages = append(anthropicReq.Messages, anthropicMessage{
			Role:    "user",
			Content: input,
		})
	case []interface{}:
		for _, item := range input {
			if msgMap, ok := item.(map[string]interface{}); ok {
				role, _ := msgMap["role"].(string)
				content := extractContentFromResponsesInput(msgMap["content"])
				if role != "" && content != "" {
					anthropicReq.Messages = append(anthropicReq.Messages, anthropicMessage{
						Role:    role,
						Content: content,
					})
				}
			}
		}
	}

	return anthropicReq
}

// extractContentFromResponsesInput extracts text content from responses input
func extractContentFromResponsesInput(content interface{}) string {
	switch c := content.(type) {
	case string:
		return c
	case []interface{}:
		// Array of content parts - extract text
		var texts []string
		for _, part := range c {
			if partMap, ok := part.(map[string]interface{}); ok {
				if text, ok := partMap["text"].(string); ok {
					texts = append(texts, text)
				}
			}
		}
		return strings.Join(texts, " ")
	}
	return ""
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

	return &core.ResponsesResponse{
		ID:        resp.ID,
		Object:    "response",
		CreatedAt: time.Now().Unix(),
		Model:     model,
		Status:    "completed",
		Output: []core.ResponsesOutputItem{
			{
				ID:     "msg_" + uuid.New().String(),
				Type:   "message",
				Role:   "assistant",
				Status: "completed",
				Content: []core.ResponsesContentItem{
					{
						Type:        "output_text",
						Text:        content,
						Annotations: []string{},
					},
				},
			},
		},
		Usage: buildAnthropicResponsesUsage(resp.Usage),
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
	anthropicReq := convertResponsesRequestToAnthropic(req)

	var anthropicResp anthropicResponse
	err := p.client.Do(ctx, llmclient.Request{
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
	if req == nil {
		return nil, nil, core.NewInvalidRequestError("request is required for anthropic batch processing", nil)
	}
	if len(req.Requests) == 0 {
		return nil, nil, core.NewInvalidRequestError("requests is required for anthropic batch processing", nil)
	}

	out := &anthropicBatchCreateRequest{
		Requests: make([]anthropicBatchRequest, 0, len(req.Requests)),
	}
	endpointByCustomID := make(map[string]string, len(req.Requests))

	for i, item := range req.Requests {
		method := strings.ToUpper(strings.TrimSpace(item.Method))
		if method == "" {
			method = http.MethodPost
		}
		if method != http.MethodPost {
			return nil, nil, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: only POST is supported", i), nil)
		}

		endpoint := strings.TrimSpace(item.URL)
		if endpoint == "" {
			endpoint = strings.TrimSpace(req.Endpoint)
		}
		endpoint = strings.TrimRight(endpoint, "/")
		if endpoint == "" {
			return nil, nil, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: url is required", i), nil)
		}

		var params *anthropicRequest
		switch endpoint {
		case "/v1/chat/completions":
			var chatReq core.ChatRequest
			if err := json.Unmarshal(item.Body, &chatReq); err != nil {
				return nil, nil, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: invalid chat body: %v", i, err), err)
			}
			if chatReq.Stream {
				return nil, nil, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: streaming is not supported for native batch", i), nil)
			}
			params = convertToAnthropicRequest(&chatReq)
			params.Stream = false
		case "/v1/responses":
			var respReq core.ResponsesRequest
			if err := json.Unmarshal(item.Body, &respReq); err != nil {
				return nil, nil, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: invalid responses body: %v", i, err), err)
			}
			if respReq.Stream {
				return nil, nil, core.NewInvalidRequestError(fmt.Sprintf("batch item %d: streaming is not supported for native batch", i), nil)
			}
			params = convertResponsesRequestToAnthropic(&respReq)
			params.Stream = false
		case "/v1/embeddings":
			return nil, nil, core.NewInvalidRequestError("anthropic does not support native embedding batches", nil)
		default:
			return nil, nil, core.NewInvalidRequestError(fmt.Sprintf("unsupported anthropic batch url: %s", endpoint), nil)
		}

		customID := strings.TrimSpace(item.CustomID)
		if customID == "" {
			customID = fmt.Sprintf("req-%d", i)
		}
		out.Requests = append(out.Requests, anthropicBatchRequest{
			CustomID: customID,
			Params:   *params,
		})
		endpointByCustomID[customID] = endpoint
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
	anthropicReq := convertResponsesRequestToAnthropic(req)
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
	reader      *bufio.Reader
	body        io.ReadCloser
	model       string
	responseID  string
	buffer      []byte
	closed      bool
	sentDone    bool
	cachedUsage *anthropicUsage // Stores usage from message_delta for inclusion in response.completed
}

func newResponsesStreamConverter(body io.ReadCloser, model string) *responsesStreamConverter {
	return &responsesStreamConverter{
		reader:     bufio.NewReader(body),
		body:       body,
		model:      model,
		responseID: "resp_" + uuid.New().String(),
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
					responseData := map[string]interface{}{
						"id":         sc.responseID,
						"object":     "response",
						"status":     "completed",
						"model":      sc.model,
						"provider":   "anthropic",
						"created_at": time.Now().Unix(),
					}
					// Include usage data if captured from message_delta
					if sc.cachedUsage != nil {
						responseData["usage"] = map[string]interface{}{
							"input_tokens":  sc.cachedUsage.InputTokens,
							"output_tokens": sc.cachedUsage.OutputTokens,
							"total_tokens":  sc.cachedUsage.InputTokens + sc.cachedUsage.OutputTokens,
						}
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
					doneMsg := fmt.Sprintf("event: response.completed\ndata: %s\n\ndata: [DONE]\n\n", jsonData)
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
				continue
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

func (sc *responsesStreamConverter) convertEvent(event *anthropicStreamEvent) string {
	switch event.Type {
	case "message_start":
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

	case "content_block_delta":
		if event.Delta != nil && event.Delta.Text != "" {
			deltaEvent := map[string]interface{}{
				"type":  "response.output_text.delta",
				"delta": event.Delta.Text,
			}
			jsonData, err := json.Marshal(deltaEvent)
			if err != nil {
				slog.Error("failed to marshal content delta event", "error", err, "response_id", sc.responseID)
				return ""
			}
			return fmt.Sprintf("event: response.output_text.delta\ndata: %s\n\n", jsonData)
		}

	case "message_delta":
		// Capture usage data for inclusion in response.completed
		if event.Usage != nil {
			sc.cachedUsage = event.Usage
		}
		return ""

	case "message_stop":
		// Will be handled in Read() when we get EOF
		return ""
	}

	return ""
}
