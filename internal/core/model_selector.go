package core

import (
	"fmt"
	"strings"
)

// ModelSelector is a normalized model routing selector.
// Model is always the raw upstream model ID (without provider prefix).
type ModelSelector struct {
	Model    string
	Provider string
}

// QualifiedModel returns "provider/model" when Provider is set, or only model otherwise.
func (s ModelSelector) QualifiedModel() string {
	if s.Provider == "" {
		return s.Model
	}
	return s.Provider + "/" + s.Model
}

// ParseModelSelector normalizes model/provider routing input.
//
// Accepted forms:
//   - model only: "gpt-4o"
//   - model with prefix: "openai/gpt-4o"
//   - explicit provider field: provider="openai", model="gpt-4o"
//   - explicit provider with raw slash model: provider="groq", model="openai/gpt-oss-120b"
//
// When provider is explicit, it is authoritative. A matching leading
// "provider/" prefix on the model is stripped once as redundant qualification.
func ParseModelSelector(model, provider string) (ModelSelector, error) {
	model = strings.TrimSpace(model)
	provider = strings.TrimSpace(provider)

	if model == "" {
		return ModelSelector{}, fmt.Errorf("model is required")
	}

	if provider != "" {
		if prefix, rest, ok := splitQualifiedModel(model); ok && prefix == provider {
			model = rest
		}
	} else if prefix, rest, ok := splitQualifiedModel(model); ok {
		provider = prefix
		model = rest
	}

	if model == "" {
		return ModelSelector{}, fmt.Errorf("model is required")
	}

	return ModelSelector{
		Model:    model,
		Provider: provider,
	}, nil
}

func splitQualifiedModel(model string) (prefix, rest string, ok bool) {
	prefix, rest, ok = strings.Cut(model, "/")
	if !ok {
		return "", "", false
	}
	prefix = strings.TrimSpace(prefix)
	rest = strings.TrimSpace(rest)
	if prefix == "" || rest == "" {
		return "", "", false
	}
	return prefix, rest, true
}
