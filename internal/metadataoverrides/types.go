// Package metadataoverrides stores dashboard-managed per-model metadata
// overrides (category, input capabilities, token limits) and pushes them into
// the model registry, where they layer over config.yaml metadata, provider
// discovery and the model catalog.
package metadataoverrides

import (
	"maps"
	"slices"
	"strconv"
	"time"

	"github.com/enterpilot/gomodel/internal/core"
	"github.com/enterpilot/gomodel/internal/modelselectors"
)

// InputCapabilities are the capability keys an override may set: the media a
// model accepts besides text, under the model catalog's vocabulary.
var InputCapabilities = []string{"vision", "audio_input", "video_input", "pdf_input"}

// Metadata is the editable subset of core.ModelMetadata. Unset fields inherit
// from the layers below.
type Metadata struct {
	Categories      []core.ModelCategory `json:"categories,omitempty" bson:"categories,omitempty"`
	Capabilities    map[string]bool      `json:"capabilities,omitempty" bson:"capabilities,omitempty"`
	ContextWindow   *int                 `json:"context_window,omitempty" bson:"context_window,omitempty"`
	MaxOutputTokens *int                 `json:"max_output_tokens,omitempty" bson:"max_output_tokens,omitempty"`
}

// Override stores one metadata override for one provider model.
type Override struct {
	Selector     string    `json:"selector" bson:"_id"`
	ProviderName string    `json:"provider_name" bson:"provider_name"`
	Model        string    `json:"model" bson:"model"`
	Metadata     Metadata  `json:"metadata" bson:"metadata"`
	CreatedAt    time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt    time.Time `json:"updated_at" bson:"updated_at"`
}

// coreMetadata converts the override into the registry's metadata shape.
func (m Metadata) coreMetadata() *core.ModelMetadata {
	return &core.ModelMetadata{
		Categories:      slices.Clone(m.Categories),
		Capabilities:    maps.Clone(m.Capabilities),
		ContextWindow:   cloneInt(m.ContextWindow),
		MaxOutputTokens: cloneInt(m.MaxOutputTokens),
	}
}

func (m Metadata) empty() bool {
	return len(m.Categories) == 0 && len(m.Capabilities) == 0 && m.ContextWindow == nil && m.MaxOutputTokens == nil
}

func cloneInt(v *int) *int {
	if v == nil {
		return nil
	}
	out := *v
	return &out
}

func cloneOverride(o Override) Override {
	o.Metadata = Metadata{
		Categories:      slices.Clone(o.Metadata.Categories),
		Capabilities:    maps.Clone(o.Metadata.Capabilities),
		ContextWindow:   cloneInt(o.Metadata.ContextWindow),
		MaxOutputTokens: cloneInt(o.Metadata.MaxOutputTokens),
	}
	return o
}

// normalizeInput validates a dashboard-submitted override against the
// configured providers.
func normalizeInput(catalog modelselectors.Catalog, override Override) (Override, error) {
	parts, err := modelselectors.NormalizeInput(catalog, override.Selector)
	if err != nil {
		return Override{}, err
	}
	return normalizeParts(override, parts)
}

// normalizeStored validates a row loaded from storage. Provider names are not
// checked against the configured providers: an override for a provider that
// is currently absent applies again once it returns.
func normalizeStored(override Override) (Override, error) {
	parts, err := modelselectors.NormalizeStored(override.Selector, override.ProviderName, override.Model)
	if err != nil {
		return Override{}, err
	}
	return normalizeParts(override, parts)
}

func normalizeParts(override Override, parts modelselectors.Selector) (Override, error) {
	if modelselectors.ScopeKindFor(parts.Selector, parts.ProviderName, parts.Model) != modelselectors.ScopeProviderModel {
		return Override{}, modelselectors.NewValidationError("selector must name one provider model as provider/model", nil)
	}
	override.Selector = parts.Selector
	override.ProviderName = parts.ProviderName
	override.Model = parts.Model
	metadata, err := normalizeMetadata(override.Metadata)
	if err != nil {
		return Override{}, err
	}
	override.Metadata = metadata
	return override, nil
}

func normalizeMetadata(m Metadata) (Metadata, error) {
	for _, category := range m.Categories {
		if category == core.CategoryAll || !slices.Contains(core.AllCategories(), category) {
			return Metadata{}, modelselectors.NewValidationError("unknown category: "+string(category), nil)
		}
	}
	// Canonical order and no duplicates, so equal overrides compare equal.
	var out Metadata
	for _, category := range core.AllCategories() {
		if slices.Contains(m.Categories, category) {
			out.Categories = append(out.Categories, category)
		}
	}
	for key, supported := range m.Capabilities {
		if !slices.Contains(InputCapabilities, key) {
			return Metadata{}, modelselectors.NewValidationError("unsupported capability: "+key, nil)
		}
		if out.Capabilities == nil {
			out.Capabilities = make(map[string]bool, len(m.Capabilities))
		}
		out.Capabilities[key] = supported
	}
	if err := validatePositive("context_window", m.ContextWindow); err != nil {
		return Metadata{}, err
	}
	if err := validatePositive("max_output_tokens", m.MaxOutputTokens); err != nil {
		return Metadata{}, err
	}
	out.ContextWindow = cloneInt(m.ContextWindow)
	out.MaxOutputTokens = cloneInt(m.MaxOutputTokens)
	if out.empty() {
		return Metadata{}, modelselectors.NewValidationError("metadata override sets no fields", nil)
	}
	return out, nil
}

func validatePositive(name string, value *int) error {
	if value != nil && *value <= 0 {
		return modelselectors.NewValidationError(name+" must be greater than 0, got "+strconv.Itoa(*value), nil)
	}
	return nil
}
