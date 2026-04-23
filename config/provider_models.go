package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"gomodel/internal/core"
)

// RawProviderModel is a single entry under providers.<name>.models. It supports
// two YAML shapes so operators can opt into rich metadata without churning
// simple configs:
//
//	models:
//	  - some-model-id                    # bare string
//	  - id: local-model                  # mapping with optional metadata
//	    metadata:
//	      context_window: 131072
//	      capabilities:
//	        tools: true
//
// Metadata is merged onto whatever the remote model registry supplies, with
// config-declared fields taking precedence. This lets local providers (Ollama,
// custom OpenAI-compatible endpoints) advertise their context windows, pricing,
// and capabilities via /v1/models even when the remote registry has no entry.
type RawProviderModel struct {
	ID       string              `yaml:"id"`
	Metadata *core.ModelMetadata `yaml:"metadata,omitempty"`
}

// UnmarshalYAML accepts either a bare string (model ID) or a mapping with id and metadata.
func (m *RawProviderModel) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		var id string
		if err := node.Decode(&id); err != nil {
			return fmt.Errorf("provider model: %w", err)
		}
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("provider model: id is required")
		}
		m.ID = id
		return nil
	case yaml.MappingNode:
		type rawAlias RawProviderModel
		var alias rawAlias
		if err := node.Decode(&alias); err != nil {
			return fmt.Errorf("provider model: %w", err)
		}
		*m = RawProviderModel(alias)
		m.ID = strings.TrimSpace(m.ID)
		if m.ID == "" {
			return fmt.Errorf("provider model: id is required")
		}
		return nil
	default:
		return fmt.Errorf("provider model: expected scalar or mapping, got kind %d", node.Kind)
	}
}

// ProviderModelIDs returns the ID of each model entry, preserving order and
// dropping entries with empty IDs.
func ProviderModelIDs(models []RawProviderModel) []string {
	if len(models) == 0 {
		return nil
	}
	ids := make([]string, 0, len(models))
	for _, m := range models {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

// ProviderModelMetadataOverrides returns id -> metadata for entries with
// non-nil Metadata. Returns nil if no entries declare metadata.
func ProviderModelMetadataOverrides(models []RawProviderModel) map[string]*core.ModelMetadata {
	var out map[string]*core.ModelMetadata
	for _, m := range models {
		if m.ID == "" || m.Metadata == nil {
			continue
		}
		if out == nil {
			out = make(map[string]*core.ModelMetadata)
		}
		out[m.ID] = m.Metadata
	}
	return out
}
