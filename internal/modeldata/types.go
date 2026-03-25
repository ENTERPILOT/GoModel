// Package modeldata provides fetching, parsing, and merging of the external
// AI model metadata registry (models.json) for enriching GoModel's model data.
package modeldata

import (
	"strings"

	"gomodel/internal/core"
)

// ModelList represents the top-level structure of models.json.
type ModelList struct {
	Version        int                           `json:"version"`
	UpdatedAt      string                        `json:"updated_at"`
	Providers      map[string]ProviderEntry      `json:"providers"`
	Models         map[string]ModelEntry         `json:"models"`
	ProviderModels map[string]ProviderModelEntry `json:"provider_models"`

	// providerModelByActualID maps "providerType/actualModelID" -> composite key
	// in ProviderModels, enabling reverse lookup from a provider's response model
	// (e.g., "openai/gpt-4o-2024-08-06") to the canonical registry key
	// (e.g., "openai/gpt-4o"). Built by buildReverseIndex().
	providerModelByActualID map[string]string

	// aliasTargetsByID maps a non-canonical model ID to one or more canonical
	// model refs. ProviderType is set when the alias is provider-qualified in the
	// external model list, e.g. "gemini/claude-opus-4" -> alias key
	// "claude-opus-4" with ProviderType "gemini".
	aliasTargetsByID map[string][]aliasTarget
}

type aliasTarget struct {
	ModelRef     string
	ProviderType string
}

// buildReverseIndex populates providerModelByActualID from ProviderModels entries
// and aliasTargetsByID from model aliases.
func (l *ModelList) buildReverseIndex() {
	l.providerModelByActualID = make(map[string]string)
	l.aliasTargetsByID = make(map[string][]aliasTarget)

	for modelRef, model := range l.Models {
		for _, alias := range model.Aliases {
			l.addAliasTarget(alias, aliasTarget{ModelRef: modelRef})
			if providerType, aliasID, ok := l.splitProviderQualifiedAlias(alias); ok {
				l.addAliasTarget(aliasID, aliasTarget{
					ModelRef:     modelRef,
					ProviderType: providerType,
				})
			}
		}
	}

	for compositeKey, pm := range l.ProviderModels {
		actualID := pm.actualModelID()
		if actualID == "" {
			continue
		}
		// compositeKey is "providerType/modelID"
		providerType, _, ok := strings.Cut(compositeKey, "/")
		if !ok {
			continue
		}
		reverseKey := providerType + "/" + actualID
		// Only add if the actual ID differs from the key's model portion
		if reverseKey != compositeKey {
			l.providerModelByActualID[reverseKey] = compositeKey
		}
	}
}

func (l *ModelList) addAliasTarget(alias string, target aliasTarget) {
	if alias == "" {
		return
	}
	existing := l.aliasTargetsByID[alias]
	for _, candidate := range existing {
		if candidate == target {
			return
		}
	}
	l.aliasTargetsByID[alias] = append(existing, target)
}

func (l *ModelList) splitProviderQualifiedAlias(alias string) (providerType, modelID string, ok bool) {
	providerType, modelID, ok = strings.Cut(alias, "/")
	if !ok || providerType == "" || modelID == "" {
		return "", "", false
	}
	if _, exists := l.Providers[providerType]; !exists {
		return "", "", false
	}
	return providerType, modelID, true
}

// ProviderEntry represents a provider in the registry.
type ProviderEntry struct {
	DisplayName       string      `json:"display_name"`
	Website           *string     `json:"website"`
	DocsURL           *string     `json:"docs_url"`
	PricingURL        *string     `json:"pricing_url"`
	StatusURL         *string     `json:"status_url"`
	APIType           string      `json:"api_type"`
	DefaultBaseURL    *string     `json:"default_base_url"`
	SupportedModes    []string    `json:"supported_modes"`
	Auth              *AuthConfig `json:"auth"`
	BaseURLEnv        *string     `json:"base_url_env"`
	DefaultRateLimits *RateLimits `json:"default_rate_limits"`
}

// ModelEntry represents a model in the registry.
type ModelEntry struct {
	DisplayName           string                   `json:"display_name"`
	Description           *string                  `json:"description"`
	OwnedBy               *string                  `json:"owned_by"`
	Family                *string                  `json:"family"`
	ReleaseDate           *string                  `json:"release_date"`
	DeprecationDate       *string                  `json:"deprecation_date"`
	Tags                  []string                 `json:"tags"`
	Modes                 []string                 `json:"modes"`
	SourceURL             *string                  `json:"source_url"`
	Modalities            *Modalities              `json:"modalities"`
	Capabilities          map[string]bool          `json:"capabilities"`
	ContextWindow         *int                     `json:"context_window"`
	MaxOutputTokens       *int                     `json:"max_output_tokens"`
	MaxImagesPerRequest   *int                     `json:"max_images_per_request"`
	MaxVideosPerRequest   *int                     `json:"max_videos_per_request"`
	MaxAudioPerRequest    *int                     `json:"max_audio_per_request"`
	MaxAudioLengthSeconds *int                     `json:"max_audio_length_seconds"`
	MaxVideoLengthSeconds *int                     `json:"max_video_length_seconds"`
	MaxPDFSizeMB          *int                     `json:"max_pdf_size_mb"`
	OutputVectorSize      *int                     `json:"output_vector_size"`
	Parameters            map[string]ParameterSpec `json:"parameters"`
	Rankings              map[string]RankingEntry  `json:"rankings"`
	Pricing               *core.ModelPricing       `json:"pricing"`
	Aliases               []string                 `json:"aliases"`
}

// ProviderModelEntry represents a provider-specific model override.
type ProviderModelEntry struct {
	ModelRef        string  `json:"model_ref"`
	ProviderModelID *string `json:"provider_model_id"`
	// CustomModelID is retained for compatibility with older cached payloads.
	CustomModelID   *string            `json:"custom_model_id"`
	Enabled         bool               `json:"enabled"`
	Pricing         *core.ModelPricing `json:"pricing"`
	ContextWindow   *int               `json:"context_window"`
	MaxOutputTokens *int               `json:"max_output_tokens"`
	Capabilities    map[string]bool    `json:"capabilities"`
	RateLimits      *RateLimits        `json:"rate_limits"`
	Endpoints       []string           `json:"endpoints"`
	Regions         []string           `json:"regions"`
}

func (e ProviderModelEntry) actualModelID() string {
	if e.ProviderModelID != nil && *e.ProviderModelID != "" {
		return *e.ProviderModelID
	}
	if e.CustomModelID != nil && *e.CustomModelID != "" {
		return *e.CustomModelID
	}
	return ""
}

// ParameterSpec describes a model parameter's constraints.
// Values are any because upstream mixes floats, ints, strings, and nulls.
type ParameterSpec struct {
	Type    string `json:"type"`
	Min     any    `json:"min"`
	Max     any    `json:"max"`
	Default any    `json:"default"`
	Enum    []any  `json:"enum"`
}

// RankingEntry holds a model's score in a benchmark or ranking.
type RankingEntry struct {
	Score *float64 `json:"score"`
	Rank  *int     `json:"rank"`
}

// AuthConfig describes provider authentication configuration.
type AuthConfig struct {
	Type      string  `json:"type"`
	HeaderKey *string `json:"header_key"`
	EnvVar    *string `json:"env_var"`
}

// Modalities describes input/output modality support.
type Modalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// RateLimits holds rate limit information.
type RateLimits struct {
	RPM *int `json:"rpm"`
	TPM *int `json:"tpm"`
	RPD *int `json:"rpd"`
}
