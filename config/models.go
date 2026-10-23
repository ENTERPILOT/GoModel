package config

import "strings"

// ModelsConfig holds global model access defaults.
type ModelsConfig struct {
	// EnabledByDefault controls whether provider models are available
	// when no persisted user-path access override exists.
	// Default: true.
	EnabledByDefault bool `yaml:"enabled_by_default" env:"MODELS_ENABLED_BY_DEFAULT"`

	// KeepOnlyAliasesAtModelsEndpoint controls whether GET /v1/models hides
	// provider models and returns only alias-projected model entries.
	// Default: false.
	KeepOnlyAliasesAtModelsEndpoint bool `yaml:"keep_only_aliases_at_models_endpoint" env:"KEEP_ONLY_ALIASES_AT_MODELS_ENDPOINT"`

	// VLLMDiscoveryCompat makes GET /v1/models advertise every model the way a
	// vLLM runtime does, so a client whose model discovery only understands
	// vLLM picks the gateway's models up instead of requiring a hand-written
	// list: owned_by becomes "vllm" and each entry carries max_model_len read
	// from the model's context window. It is a lie about the owner, so it stays
	// opt-in and only for deployments whose consumers need it.
	// Default: false.
	VLLMDiscoveryCompat bool `yaml:"vllm_discovery_compat" env:"MODELS_VLLM_DISCOVERY_COMPAT"`

	// UnqualifiedModelIDsAtModelsEndpoint controls whether GET /v1/models
	// returns bare model IDs (gpt-5) instead of provider-qualified ones
	// (openai/gpt-5). When two providers expose the same model ID, only the
	// entry for the provider an unqualified request routes to is listed.
	// Default: false.
	UnqualifiedModelIDsAtModelsEndpoint bool `yaml:"unqualified_model_ids_at_models_endpoint" env:"UNQUALIFIED_MODEL_IDS_AT_MODELS_ENDPOINT"`

	// ConfiguredProviderModelsMode controls how providers.<name>.models and
	// provider *_MODELS env vars affect the provider model inventory.
	// Supported values: "fallback", "allowlist", "merge". Default: "fallback".
	ConfiguredProviderModelsMode ConfiguredProviderModelsMode `yaml:"configured_provider_models_mode" env:"CONFIGURED_PROVIDER_MODELS_MODE"`
}

// ConfiguredProviderModelsMode controls how explicitly configured provider
// model lists are applied to the discovered model inventory.
type ConfiguredProviderModelsMode string

const (
	// ConfiguredProviderModelsModeFallback uses configured models only when the
	// upstream /models call fails or returns nothing.
	ConfiguredProviderModelsModeFallback ConfiguredProviderModelsMode = "fallback"
	// ConfiguredProviderModelsModeAllowlist exposes only the configured models
	// and skips the upstream /models call.
	ConfiguredProviderModelsModeAllowlist ConfiguredProviderModelsMode = "allowlist"
	// ConfiguredProviderModelsModeMerge unions the upstream inventory with the
	// configured models, so models a provider serves but does not list stay
	// routable without hiding the discovered ones.
	ConfiguredProviderModelsModeMerge ConfiguredProviderModelsMode = "merge"
)

// Valid reports whether mode is one of the supported configured-provider-models modes.
func (m ConfiguredProviderModelsMode) Valid() bool {
	switch NormalizeConfiguredProviderModelsMode(m) {
	case ConfiguredProviderModelsModeFallback, ConfiguredProviderModelsModeAllowlist, ConfiguredProviderModelsModeMerge:
		return true
	default:
		return false
	}
}

// NormalizeConfiguredProviderModelsMode canonicalizes a configured provider models mode.
func NormalizeConfiguredProviderModelsMode(mode ConfiguredProviderModelsMode) ConfiguredProviderModelsMode {
	return ConfiguredProviderModelsMode(strings.ToLower(strings.TrimSpace(string(mode))))
}

// ResolveConfiguredProviderModelsMode canonicalizes mode and applies the process default.
func ResolveConfiguredProviderModelsMode(mode ConfiguredProviderModelsMode) ConfiguredProviderModelsMode {
	mode = NormalizeConfiguredProviderModelsMode(mode)
	if mode == "" {
		return ConfiguredProviderModelsModeFallback
	}
	return mode
}
