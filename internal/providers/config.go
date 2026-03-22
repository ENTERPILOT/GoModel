package providers

import (
	"os"
	"strings"

	"gomodel/config"
)

// ProviderConfig holds the fully resolved provider configuration after merging
// global defaults with per-provider overrides.
type ProviderConfig struct {
	Type       string
	APIKey     string
	BaseURL    string
	APIVersion string
	Models     []string
	Resilience config.ResilienceConfig
}

const openRouterDefaultBaseURL = "https://openrouter.ai/api/v1"

// knownProviderEnvs maps well-known provider names to their environment variables.
// This list is the authoritative source for provider auto-discovery from env vars.
var knownProviderEnvs = []struct {
	name          string
	providerType  string
	apiKeyEnv     string
	baseURLEnv    string
	apiVersionEnv string
	defaultBase   string
	requireBase   bool
}{
	{"openai", "openai", "OPENAI_API_KEY", "OPENAI_BASE_URL", "", "", false},
	{"anthropic", "anthropic", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "", "", false},
	{"gemini", "gemini", "GEMINI_API_KEY", "GEMINI_BASE_URL", "", "", false},
	{"xai", "xai", "XAI_API_KEY", "XAI_BASE_URL", "", "", false},
	{"groq", "groq", "GROQ_API_KEY", "GROQ_BASE_URL", "", "", false},
	{"openrouter", "openrouter", "OPENROUTER_API_KEY", "OPENROUTER_BASE_URL", "", openRouterDefaultBaseURL, false},
	{"azure", "azure", "AZURE_API_KEY", "AZURE_API_BASE", "AZURE_API_VERSION", "", true},
	{"ollama", "ollama", "OLLAMA_API_KEY", "OLLAMA_BASE_URL", "", "", false},
}

// resolveProviders applies env var overrides to the raw YAML provider map, filters
// out entries with invalid credentials, and merges each entry with the global
// ResilienceConfig. Returns a fully resolved map ready for provider instantiation.
func resolveProviders(raw map[string]config.RawProviderConfig, global config.ResilienceConfig) map[string]ProviderConfig {
	merged := applyProviderEnvVars(raw)
	filtered := filterEmptyProviders(merged)
	return buildProviderConfigs(filtered, global)
}

// applyProviderEnvVars overlays well-known provider env vars onto the raw YAML map.
// Env var values always win over YAML values for the same provider name.
func applyProviderEnvVars(raw map[string]config.RawProviderConfig) map[string]config.RawProviderConfig {
	result := make(map[string]config.RawProviderConfig, len(raw))
	for k, v := range raw {
		result[k] = v
	}

	for _, kp := range knownProviderEnvs {
		apiKey := os.Getenv(kp.apiKeyEnv)
		explicitBaseURL := os.Getenv(kp.baseURLEnv)
		apiVersion := os.Getenv(kp.apiVersionEnv)
		baseURL := explicitBaseURL
		if baseURL == "" && apiKey != "" && kp.defaultBase != "" {
			baseURL = kp.defaultBase
		}

		if apiKey == "" && baseURL == "" && apiVersion == "" {
			continue
		}

		existing, exists := result[kp.name]
		if exists {
			if apiKey != "" {
				existing.APIKey = apiKey
			}
			if explicitBaseURL != "" {
				existing.BaseURL = baseURL
			} else if existing.BaseURL == "" && apiKey != "" && kp.defaultBase != "" {
				existing.BaseURL = kp.defaultBase
			}
			if apiVersion != "" {
				existing.APIVersion = apiVersion
			}
			result[kp.name] = existing
		} else {
			if kp.requireBase && explicitBaseURL == "" {
				continue
			}
			result[kp.name] = config.RawProviderConfig{
				Type:       kp.providerType,
				APIKey:     apiKey,
				BaseURL:    baseURL,
				APIVersion: apiVersion,
			}
		}
	}

	return result
}

// filterEmptyProviders removes providers without valid credentials.
// Ollama is always valid — it uses a default base URL when none is configured.
func filterEmptyProviders(raw map[string]config.RawProviderConfig) map[string]config.RawProviderConfig {
	result := make(map[string]config.RawProviderConfig, len(raw))
	for name, p := range raw {
		if p.Type == "ollama" {
			result[name] = p
			continue
		}
		if p.Type == "azure" && strings.TrimSpace(p.BaseURL) == "" {
			continue
		}
		if p.APIKey != "" && !strings.Contains(p.APIKey, "${") {
			result[name] = p
		}
	}
	return result
}

// buildProviderConfigs merges each raw provider config with the global ResilienceConfig,
// producing fully resolved ProviderConfig values.
func buildProviderConfigs(raw map[string]config.RawProviderConfig, global config.ResilienceConfig) map[string]ProviderConfig {
	result := make(map[string]ProviderConfig, len(raw))
	for name, r := range raw {
		result[name] = buildProviderConfig(r, global)
	}
	return result
}

// buildProviderConfig merges a single RawProviderConfig with the global ResilienceConfig.
// Non-nil fields in the raw config override the global defaults.
func buildProviderConfig(raw config.RawProviderConfig, global config.ResilienceConfig) ProviderConfig {
	resolved := ProviderConfig{
		Type:       raw.Type,
		APIKey:     raw.APIKey,
		BaseURL:    raw.BaseURL,
		APIVersion: raw.APIVersion,
		Models:     raw.Models,
		Resilience: global,
	}

	if raw.Resilience == nil {
		return resolved
	}

	if r := raw.Resilience.Retry; r != nil {
		if r.MaxRetries != nil {
			resolved.Resilience.Retry.MaxRetries = *r.MaxRetries
		}
		if r.InitialBackoff != nil {
			resolved.Resilience.Retry.InitialBackoff = *r.InitialBackoff
		}
		if r.MaxBackoff != nil {
			resolved.Resilience.Retry.MaxBackoff = *r.MaxBackoff
		}
		if r.BackoffFactor != nil {
			resolved.Resilience.Retry.BackoffFactor = *r.BackoffFactor
		}
		if r.JitterFactor != nil {
			resolved.Resilience.Retry.JitterFactor = *r.JitterFactor
		}
	}

	if cb := raw.Resilience.CircuitBreaker; cb != nil {
		if cb.FailureThreshold != nil {
			resolved.Resilience.CircuitBreaker.FailureThreshold = *cb.FailureThreshold
		}
		if cb.SuccessThreshold != nil {
			resolved.Resilience.CircuitBreaker.SuccessThreshold = *cb.SuccessThreshold
		}
		if cb.Timeout != nil {
			resolved.Resilience.CircuitBreaker.Timeout = *cb.Timeout
		}
	}

	return resolved
}
