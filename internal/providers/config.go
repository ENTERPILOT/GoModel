package providers

import (
	"maps"
	"os"
	"sort"
	"strings"
	"unicode"

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

// resolveProviders applies env var overrides to the raw YAML provider map, filters
// out entries with invalid credentials, and merges each entry with the global
// ResilienceConfig. The second return value is the credential-filtered raw map
// (same keys as the first); use it for auxiliary clients that need the same
// API keys and base URLs as the live router (e.g. semantic-cache embeddings).
func resolveProviders(raw map[string]config.RawProviderConfig, global config.ResilienceConfig, discovery map[string]DiscoveryConfig) (map[string]ProviderConfig, map[string]config.RawProviderConfig) {
	merged := applyProviderEnvVars(raw, discovery)
	filtered := filterEmptyProviders(merged, discovery)
	return buildProviderConfigs(filtered, global), filtered
}

// applyProviderEnvVars overlays well-known provider env vars onto the raw YAML map.
// Env var values always win over YAML values for the same provider name.
func applyProviderEnvVars(raw map[string]config.RawProviderConfig, discovery map[string]DiscoveryConfig) map[string]config.RawProviderConfig {
	result := make(map[string]config.RawProviderConfig, len(raw))
	maps.Copy(result, raw)

	for _, providerType := range sortedDiscoveryTypes(discovery) {
		spec := discovery[providerType]
		envNames := derivedEnvNames(providerType)

		apiKey := os.Getenv(envNames.APIKey)
		explicitBaseURL := normalizeResolvedBaseURL(os.Getenv(envNames.BaseURL))
		apiVersion := ""
		if spec.SupportsAPIVersion {
			apiVersion = os.Getenv(envNames.APIVersion)
		}
		baseURL := explicitBaseURL
		if baseURL == "" && apiKey != "" && spec.DefaultBaseURL != "" {
			baseURL = spec.DefaultBaseURL
		}

		if apiKey == "" && baseURL == "" && apiVersion == "" {
			continue
		}

		targetKey, matched, ambiguous := findEnvOverlayTarget(result, providerType)
		if matched {
			existing := result[targetKey]
			if apiKey != "" {
				existing.APIKey = apiKey
			}
			if explicitBaseURL != "" {
				existing.BaseURL = baseURL
			} else if normalizeResolvedBaseURL(existing.BaseURL) == "" && apiKey != "" && spec.DefaultBaseURL != "" {
				existing.BaseURL = spec.DefaultBaseURL
			}
			if apiVersion != "" {
				existing.APIVersion = apiVersion
			}
			result[targetKey] = existing
		} else if ambiguous {
			continue
		} else {
			if spec.RequireBaseURL && explicitBaseURL == "" {
				continue
			}
			result[providerType] = config.RawProviderConfig{
				Type:       providerType,
				APIKey:     apiKey,
				BaseURL:    baseURL,
				APIVersion: apiVersion,
			}
		}
	}

	return result
}

func findEnvOverlayTarget(raw map[string]config.RawProviderConfig, providerType string) (string, bool, bool) {
	if existing, ok := raw[providerType]; ok && rawProviderMatchesType(existing, providerType) {
		return providerType, true, false
	}

	var matchedKey string
	var matches int
	for name, cfg := range raw {
		if !rawProviderMatchesType(cfg, providerType) {
			continue
		}
		matchedKey = name
		matches++
		if matches > 1 {
			return "", false, true
		}
	}

	if matches == 1 {
		return matchedKey, true, false
	}
	return "", false, false
}

func rawProviderMatchesType(cfg config.RawProviderConfig, providerType string) bool {
	return strings.TrimSpace(cfg.Type) == strings.TrimSpace(providerType)
}

type providerEnvNames struct {
	APIKey     string
	BaseURL    string
	APIVersion string
}

func derivedEnvNames(providerType string) providerEnvNames {
	prefix := envPrefix(providerType)
	return providerEnvNames{
		APIKey:     prefix + "_API_KEY",
		BaseURL:    prefix + "_BASE_URL",
		APIVersion: prefix + "_API_VERSION",
	}
}

func envPrefix(providerType string) string {
	var b strings.Builder
	b.Grow(len(providerType))
	lastUnderscore := false
	for _, r := range providerType {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToUpper(r))
			lastUnderscore = false
		case !lastUnderscore:
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	return strings.Trim(b.String(), "_")
}

func sortedDiscoveryTypes(discovery map[string]DiscoveryConfig) []string {
	types := make([]string, 0, len(discovery))
	for providerType := range discovery {
		types = append(types, providerType)
	}
	sort.Strings(types)
	return types
}

func normalizeResolvedBaseURL(value string) string {
	trimmed := strings.TrimSpace(value)
	if isUnresolvedEnvPlaceholder(trimmed) {
		return ""
	}
	return trimmed
}

func isUnresolvedEnvPlaceholder(value string) bool {
	if !strings.HasPrefix(value, "${") || !strings.HasSuffix(value, "}") || len(value) <= 3 {
		return false
	}
	inner := value[2 : len(value)-1]
	return inner != "" && !strings.ContainsAny(inner, "{}")
}

// filterEmptyProviders removes providers without valid credentials.
func filterEmptyProviders(raw map[string]config.RawProviderConfig, discovery map[string]DiscoveryConfig) map[string]config.RawProviderConfig {
	result := make(map[string]config.RawProviderConfig, len(raw))
	for name, p := range raw {
		spec, known := discovery[strings.TrimSpace(p.Type)]
		if known && spec.RequireBaseURL && strings.TrimSpace(p.BaseURL) == "" {
			continue
		}
		if known && spec.AllowAPIKeyless {
			result[name] = p
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
