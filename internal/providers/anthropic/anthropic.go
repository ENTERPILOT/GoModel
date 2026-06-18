// Package anthropic provides Anthropic API integration for the LLM gateway.
package anthropic

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-json"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
	"gomodel/internal/streaming"
)

// Registration provides factory registration for the Anthropic provider.
var Registration = providers.Registration{
	Type:                        "anthropic",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher,
	Discovery: providers.DiscoveryConfig{
		DefaultBaseURL: defaultBaseURL,
	},
}

const (
	defaultBaseURL      = "https://api.anthropic.com/v1"
	anthropicAPIVersion = "2023-06-01"
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
}

// New creates a new Anthropic provider.
func New(providerCfg providers.ProviderConfig, opts providers.ProviderOptions) core.Provider {
	p := &Provider{
		apiKey:               providerCfg.APIKey,
		batchResultEndpoints: make(map[string]map[string]string),
	}
	clientCfg := llmclient.Config{
		ProviderName:   "anthropic",
		BaseURL:        providers.ResolveBaseURL(providerCfg.BaseURL, defaultBaseURL),
		Retry:          opts.Resilience.Retry,
		Hooks:          opts.Hooks,
		CircuitBreaker: opts.Resilience.CircuitBreaker,
	}
	p.client = llmclient.New(clientCfg, p.setHeaders)
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

func cloneBatchResultEndpoints(endpoints map[string]string) map[string]string {
	if len(endpoints) == 0 {
		return nil
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
		return nil
	}
	return cloned
}

func (p *Provider) setBatchResultEndpoints(batchID string, endpoints map[string]string) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" || len(endpoints) == 0 {
		return
	}
	cloned := cloneBatchResultEndpoints(endpoints)
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

func (p *Provider) clearBatchResultEndpoints(batchID string) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" {
		return
	}
	p.batchEndpointsMu.Lock()
	if p.batchResultEndpoints != nil {
		delete(p.batchResultEndpoints, batchID)
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
	maps.Copy(cloned, endpoints)
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

var adaptiveThinkingPrefixes = []string{
	"claude-fable-5",
	"claude-opus-4-8",
	"claude-opus-4-7",
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

// normalizeEffort maps effort to the values Anthropic's adaptive thinking
// accepts: "low", "medium", "high", "xhigh", and "max". Opus 4.8 introduced the
// "xhigh" and "max" levels for deeper reasoning. Any unsupported value is
// downgraded to "low" and logged via slog.Warn.
func normalizeEffort(effort string) string {
	switch effort {
	case "low", "medium", "high", "xhigh", "max":
		return effort
	default:
		slog.Warn("invalid reasoning effort, defaulting to 'low'", "effort", effort)
		return "low"
	}
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

// extractTextContent returns the text content from the response.
// When thinking blocks are present, only text blocks after the last thinking block
// are included (earlier text blocks are typically empty preambles).
// When no thinking blocks are present, all text blocks are concatenated.
func extractTextContent(blocks []anthropicContent) string {
	lastThinkingIdx := -1
	for i, b := range blocks {
		if b.Type == "thinking" {
			lastThinkingIdx = i
		}
	}

	var sb strings.Builder
	for i, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			if lastThinkingIdx >= 0 && i < lastThinkingIdx {
				continue // skip text blocks before thinking
			}
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(b.Text)
		}
	}
	return sb.String()
}

// extractThinkingContent returns the concatenated thinking text from all "thinking" content blocks.
func extractThinkingContent(blocks []anthropicContent) string {
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "thinking" && b.Thinking != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n\n")
			}
			sb.WriteString(b.Thinking)
		}
	}
	return sb.String()
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

func malformedAnthropicStreamError(err error) error {
	return core.NewProviderError("anthropic", http.StatusBadGateway, "failed to decode anthropic stream event: "+err.Error(), err)
}

func consumeAnthropicSSELine(p []byte, line []byte, body io.ReadCloser, buffer *streaming.StreamBuffer, convert func(*anthropicStreamEvent) string) (n int, handled bool, err error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || bytes.HasPrefix(line, []byte("event:")) {
		return 0, false, nil
	}
	if !bytes.HasPrefix(line, []byte("data:")) {
		return 0, false, nil
	}

	data := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))

	var event anthropicStreamEvent
	if err := json.Unmarshal(data, &event); err != nil {
		_ = body.Close() //nolint:errcheck
		return 0, false, malformedAnthropicStreamError(err)
	}

	chunk := convert(&event)
	if chunk == "" {
		return 0, false, nil
	}

	buffer.AppendString(chunk)
	return buffer.Read(p), true, nil
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

// Embeddings returns an error because Anthropic does not natively support embeddings.
// Voyage AI (Anthropic's recommended embedding provider) may be added in the future.
func (p *Provider) Embeddings(_ context.Context, _ *core.EmbeddingRequest) (*core.EmbeddingResponse, error) {
	return nil, core.NewInvalidRequestError("anthropic does not support embeddings — consider using Voyage AI", nil)
}
