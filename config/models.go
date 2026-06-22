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

	// ConfiguredProviderModelsMode controls how providers.<name>.models and
	// provider *_MODELS env vars affect the provider model inventory.
	// Supported values: "fallback", "allowlist". Default: "fallback".
	ConfiguredProviderModelsMode ConfiguredProviderModelsMode `yaml:"configured_provider_models_mode" env:"CONFIGURED_PROVIDER_MODELS_MODE"`
}

// ConfiguredProviderModelsMode controls how explicitly configured provider
// model lists are applied to the discovered model inventory.
type ConfiguredProviderModelsMode string

const (
	ConfiguredProviderModelsModeFallback  ConfiguredProviderModelsMode = "fallback"
	ConfiguredProviderModelsModeAllowlist ConfiguredProviderModelsMode = "allowlist"
)

// Valid reports whether mode is one of the supported configured-provider-models modes.
func (m ConfiguredProviderModelsMode) Valid() bool {
	switch NormalizeConfiguredProviderModelsMode(m) {
	case ConfiguredProviderModelsModeFallback, ConfiguredProviderModelsModeAllowlist:
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
