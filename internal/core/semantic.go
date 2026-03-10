package core

import (
	"bytes"
	"encoding/json"
)

// SelectorHints holds the minimal routing-relevant request hints derived from ingress.
// These hints are intentionally smaller than a full semantic interpretation.
type SelectorHints struct {
	Model    string
	Provider string
	Endpoint string
}

// SemanticEnvelope is the gateway's best-effort semantic extraction from ingress.
// It may be partial and should not be treated as authoritative transport state.
type SemanticEnvelope struct {
	Dialect          string
	Operation        string
	SelectorHints    SelectorHints
	JSONBodyParsed   bool
	ChatRequest      *ChatRequest
	ResponsesRequest *ResponsesRequest
	EmbeddingRequest *EmbeddingRequest
}

// BuildSemanticEnvelope derives a best-effort semantic envelope from ingress.
// Unknown or invalid bodies are tolerated; the returned envelope may be partial.
func BuildSemanticEnvelope(frame *IngressFrame) *SemanticEnvelope {
	if frame == nil {
		return nil
	}

	env := &SemanticEnvelope{
		SelectorHints: SelectorHints{
			Endpoint: frame.Path,
		},
	}

	desc := DescribeEndpointPath(frame.Path)
	if desc.Operation == "" {
		return nil
	}
	env.Dialect = desc.Dialect
	env.Operation = desc.Operation

	if env.Dialect == "provider_passthrough" {
		env.SelectorHints.Endpoint = ""
		if provider := frame.RouteParams["provider"]; provider != "" {
			env.SelectorHints.Provider = provider
		}
		if endpoint := frame.RouteParams["endpoint"]; endpoint != "" {
			env.SelectorHints.Endpoint = endpoint
		}
		if env.SelectorHints.Provider == "" || env.SelectorHints.Endpoint == "" {
			if provider, endpoint, ok := ParseProviderPassthroughPath(frame.Path); ok {
				if env.SelectorHints.Provider == "" {
					env.SelectorHints.Provider = provider
				}
				if env.SelectorHints.Endpoint == "" {
					env.SelectorHints.Endpoint = endpoint
				}
			}
		}
	}

	if frame.RawBody == nil {
		return env
	}

	trimmed := bytes.TrimSpace(frame.RawBody)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return env
	}

	var selectors struct {
		Model    string `json:"model"`
		Provider string `json:"provider"`
	}
	if err := json.Unmarshal(trimmed, &selectors); err != nil {
		return env
	}
	env.JSONBodyParsed = true

	env.SelectorHints.Model = selectors.Model
	if env.SelectorHints.Provider == "" {
		env.SelectorHints.Provider = selectors.Provider
	}

	return env
}
