package core

import (
	"bytes"
	"encoding/json"
	"strings"
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
	OpaqueJSONFields map[string]json.RawMessage
	JSONBodyParsed   bool
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

	switch {
	case frame.Path == "/v1/chat/completions":
		env.Dialect = "openai_compat"
		env.Operation = "chat_completions"
	case frame.Path == "/v1/responses":
		env.Dialect = "openai_compat"
		env.Operation = "responses"
	case frame.Path == "/v1/embeddings":
		env.Dialect = "openai_compat"
		env.Operation = "embeddings"
	case strings.HasPrefix(frame.Path, "/p/"):
		env.Dialect = "provider_passthrough"
		env.Operation = "provider_passthrough"
		if provider := frame.RouteParams["provider"]; provider != "" {
			env.SelectorHints.Provider = provider
		}
		if endpoint := frame.RouteParams["endpoint"]; endpoint != "" {
			env.SelectorHints.Endpoint = endpoint
		}
	default:
		return nil
	}

	trimmed := bytes.TrimSpace(frame.RawBody)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return env
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &raw); err != nil {
		return env
	}
	env.JSONBodyParsed = true

	if value, ok := raw["model"]; ok {
		_ = json.Unmarshal(value, &env.SelectorHints.Model)
	}
	if value, ok := raw["provider"]; ok && env.SelectorHints.Provider == "" {
		_ = json.Unmarshal(value, &env.SelectorHints.Provider)
	}

	delete(raw, "model")
	delete(raw, "provider")
	env.OpaqueJSONFields = cloneRawJSONMap(raw)

	return env
}
