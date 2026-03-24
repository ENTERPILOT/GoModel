// Package openai provides OpenAI API integration for the LLM gateway.
package openai

import (
	"encoding/json"
	"net/http"
	"strings"

	"gomodel/internal/core"
	"gomodel/internal/llmclient"
	"gomodel/internal/providers"
)

// Registration provides factory registration for the OpenAI provider.
var Registration = providers.Registration{
	Type:                        "openai",
	New:                         New,
	PassthroughSemanticEnricher: passthroughSemanticEnricher{},
}

const (
	defaultBaseURL = "https://api.openai.com/v1"
)

// Provider implements the core.Provider interface for OpenAI
type Provider struct {
	*CompatibleProvider
}

// New creates a new OpenAI provider.
func New(apiKey string, opts providers.ProviderOptions) core.Provider {
	return &Provider{
		CompatibleProvider: NewCompatibleProvider(apiKey, opts, CompatibleProviderConfig{
			ProviderName:   "openai",
			DefaultBaseURL: defaultBaseURL,
			SetHeaders:     setHeaders,
		}),
	}
}

// NewWithHTTPClient creates a new OpenAI provider with a custom HTTP client.
// If httpClient is nil, http.DefaultClient is used.
func NewWithHTTPClient(apiKey string, httpClient *http.Client, hooks llmclient.Hooks) *Provider {
	return &Provider{
		CompatibleProvider: NewCompatibleProviderWithHTTPClient(apiKey, httpClient, hooks, CompatibleProviderConfig{
			ProviderName:   "openai",
			DefaultBaseURL: defaultBaseURL,
			SetHeaders:     setHeaders,
		}),
	}
}

// setHeaders sets the required headers for OpenAI API requests
func setHeaders(req *http.Request, apiKey string) {
	req.Header.Set("Authorization", "Bearer "+apiKey)

	// Forward request ID if present in context using OpenAI's X-Client-Request-Id header.
	// OpenAI requires ASCII-only characters and max 512 bytes, otherwise returns 400.
	if requestID := core.GetRequestID(req.Context()); requestID != "" && isValidClientRequestID(requestID) {
		req.Header.Set("X-Client-Request-Id", requestID)
	}
}

// isValidClientRequestID checks if the request ID is valid for OpenAI's X-Client-Request-Id header.
// OpenAI requires: ASCII characters only, max 512 characters.
func isValidClientRequestID(id string) bool {
	if len(id) > 512 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if id[i] > 127 {
			return false
		}
	}
	return true
}

// isOSeriesModel reports whether the model is an OpenAI o-series model
// (o1, o3, o4) that requires max_completion_tokens instead of max_tokens
// and does not support the temperature parameter.
func isOSeriesModel(model string) bool {
	m := strings.ToLower(model)
	// Match o1, o3, o4 families (e.g. o3-mini, o4-mini, o3, o1-preview).
	// Non-reasoning models like gpt-4o start with "gpt-", not "o".
	return len(m) >= 2 && m[0] == 'o' && m[1] >= '0' && m[1] <= '9'
}

// adaptForOSeries rewrites a ChatRequest body for OpenAI o-series models,
// mapping max_tokens -> max_completion_tokens and dropping temperature while
// preserving all unknown top-level JSON fields.
func adaptForOSeries(req *core.ChatRequest) (any, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, core.NewInvalidRequestError("failed to marshal o-series request: "+err.Error(), err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, core.NewInvalidRequestError("failed to decode o-series request payload: "+err.Error(), err)
	}
	if maxTokens, ok := raw["max_tokens"]; ok {
		raw["max_completion_tokens"] = maxTokens
		delete(raw, "max_tokens")
	}
	delete(raw, "temperature")
	return raw, nil
}

// chatRequestBody returns the appropriate request body for the model.
// Reasoning models get parameter adaptation; others pass through as-is.
func chatRequestBody(req *core.ChatRequest) (any, error) {
	if isOSeriesModel(req.Model) {
		return adaptForOSeries(req)
	}
	return req, nil
}
