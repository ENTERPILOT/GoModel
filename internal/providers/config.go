package providers

import (
	"sort"
	"strings"

	"github.com/enterpilot/gomodel/config"
	"github.com/enterpilot/gomodel/internal/core"
)

// ProviderConfig holds the fully resolved provider configuration after merging
// global defaults with per-provider overrides.
type ProviderConfig struct {
	// Name is the configured provider instance name (for example "openai-eu").
	// It is populated by configuration resolution and may be empty in tests or
	// direct constructor calls.
	Name string
	Type string
	// APIKey is the provider's primary credential: the first entry of APIKeys,
	// or "" for keyless providers. Prefer APIKeys for anything that
	// authenticates a request, so rotation is honoured.
	APIKey string
	// APIKeys is the provider's full, ordered, de-duplicated key set. Identified
	// sessions stay on one key when SessionStickyKeys is true; sessionless
	// traffic rotates round robin.
	APIKeys                  []string
	SessionStickyKeys        bool
	BaseURL                  string
	APIVersion               string
	Backend                  string
	AuthType                 string
	APIMode                  string
	VertexProject            string
	VertexLocation           string
	ServiceAccountFile       string
	ServiceAccountJSON       string
	ServiceAccountJSONBase64 string
	GCPScope                 string
	InferenceObjective       string
	FairnessFromUserPath     bool
	Models                   []string
	// ModelMetadataOverrides holds operator-supplied metadata keyed by raw model
	// ID (as it appears in the provider's /models response). The registry merges
	// these onto remote-registry metadata after enrichment; non-zero fields here
	// win. Empty/nil when no per-model metadata is declared in YAML.
	ModelMetadataOverrides map[string]*core.ModelMetadata
	// ModelFilter narrows the provider's discovered inventory by glob pattern
	// and price cap. Zero value keeps every model.
	ModelFilter config.ModelFilter
	Resilience  config.ResilienceConfig
}

// resolveProviders applies env var overrides to the raw YAML provider map, filters
// out entries with invalid credentials, and merges each entry with the global
// ResilienceConfig. The second return value is the credential-filtered raw map
// (same keys as the first); use it for auxiliary clients that need the same
// API keys and base URLs as the live router (e.g. semantic-cache embeddings).
func resolveProviders(raw map[string]config.RawProviderConfig, global config.ResilienceConfig, discovery map[string]DiscoveryConfig) (map[string]ProviderConfig, map[string]config.RawProviderConfig) {
	merged := normalizeProviderAPIKeys(applyProviderEnvVars(raw, discovery))
	filtered := filterEmptyProviders(merged, discovery)
	return buildProviderConfigs(filtered, global), filtered
}

// normalizeProviderAPIKeys collapses each provider's `api_key` and `api_keys`
// into one canonical ordered set: APIKeys holds every usable key and APIKey
// holds the first. Unresolved `${VAR}` placeholders are dropped here rather
// than forwarded as literal credentials, so a provider whose only key failed
// to resolve ends up keyless and is then dropped by filterEmptyProviders --
// the same outcome as before rotation existed.
func normalizeProviderAPIKeys(raw map[string]config.RawProviderConfig) map[string]config.RawProviderConfig {
	result := make(map[string]config.RawProviderConfig, len(raw))
	for name, p := range raw {
		keys := resolvedAPIKeys(append([]string{p.APIKey}, p.APIKeys...))
		p.APIKeys = keys
		p.APIKey = ""
		if len(keys) > 0 {
			p.APIKey = keys[0]
		}
		result[name] = p
	}
	return result
}

// resolvedAPIKeys trims, drops unresolved and empty entries, and de-duplicates
// while preserving order.
func resolvedAPIKeys(keys []string) []string {
	resolved := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if !HasResolvedProviderValue(key) {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		resolved = append(resolved, key)
	}
	if len(resolved) == 0 {
		return nil
	}
	return resolved
}

// providerOrigins splits the resolved provider names by where they were declared:
// the config file, or environment-variable discovery. A provider named in the
// config file counts as fromFile even when env vars overlay its fields. Operators
// need the split to notice a config file that contributed nothing — a misindented
// providers: section reads as zero fromFile providers.
func providerOrigins(declared map[string]config.RawProviderConfig, resolved map[string]ProviderConfig) (fromFile, fromEnv []string) {
	for name := range resolved {
		if _, ok := declared[name]; ok {
			fromFile = append(fromFile, name)
		} else {
			fromEnv = append(fromEnv, name)
		}
	}
	sort.Strings(fromFile)
	sort.Strings(fromEnv)
	return fromFile, fromEnv
}

// skippedProviderNames lists the YAML-declared providers that did not survive
// credential resolution, so operators can see why a configured provider is
// absent instead of it disappearing silently.
func skippedProviderNames(declared, resolved map[string]config.RawProviderConfig) []string {
	var names []string
	for name := range declared {
		if _, ok := resolved[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// filterEmptyProviders removes providers without valid credentials.
func filterEmptyProviders(raw map[string]config.RawProviderConfig, discovery map[string]DiscoveryConfig) map[string]config.RawProviderConfig {
	result := make(map[string]config.RawProviderConfig, len(raw))
	for name, p := range raw {
		providerType := normalizeProviderType(p)
		spec, known := discovery[providerType]
		if known && spec.RequireBaseURL && strings.TrimSpace(p.BaseURL) == "" {
			continue
		}
		if isVertexProviderConfig(p) {
			p.Type = providerType
			if validVertexProviderConfig(p) {
				result[name] = p
			}
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

func isVertexProviderConfig(p config.RawProviderConfig) bool {
	return strings.EqualFold(strings.TrimSpace(p.Type), "vertex") ||
		(strings.EqualFold(strings.TrimSpace(p.Type), "gemini") && strings.EqualFold(strings.TrimSpace(p.Backend), "vertex"))
}

func validVertexProviderConfig(p config.RawProviderConfig) bool {
	if !HasResolvedProviderValue(p.BaseURL) &&
		(!HasResolvedProviderValue(p.VertexProject) || !HasResolvedProviderValue(p.VertexLocation)) {
		return false
	}
	authType := strings.ToLower(strings.TrimSpace(p.AuthType))
	switch authType {
	case "", "gcp_adc", "adc", "google_adc":
		return true
	case "gcp_service_account", "service_account":
		return HasResolvedProviderValue(p.ServiceAccountFile) ||
			HasResolvedProviderValue(p.ServiceAccountJSON) ||
			HasResolvedProviderValue(p.ServiceAccountJSONBase64)
	default:
		return false
	}
}

// HasResolvedProviderValue reports whether a provider-config field carries a
// usable string value. It returns false for empty/whitespace input and false
// when the value still contains a literal `${` substring — that signals an
// unresolved YAML environment-variable placeholder such as `${OPENAI_API_KEY}`
// which the env-substitution pass failed to fill in. Provider builders use
// this to drop providers whose credentials never resolved.
func HasResolvedProviderValue(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && !strings.Contains(value, "${")
}

// buildProviderConfigs merges each raw provider config with the global ResilienceConfig,
// producing fully resolved ProviderConfig values.
func buildProviderConfigs(raw map[string]config.RawProviderConfig, global config.ResilienceConfig) map[string]ProviderConfig {
	result := make(map[string]ProviderConfig, len(raw))
	for name, r := range raw {
		resolved := buildProviderConfig(r, global)
		resolved.Name = name
		result[name] = resolved
	}
	return result
}

// buildProviderConfig merges a single RawProviderConfig with the global ResilienceConfig.
// Non-nil fields in the raw config override the global defaults.
func buildProviderConfig(raw config.RawProviderConfig, global config.ResilienceConfig) ProviderConfig {
	resolved := ProviderConfig{
		Type:                     normalizeProviderType(raw),
		APIKey:                   raw.APIKey,
		APIKeys:                  raw.APIKeys,
		SessionStickyKeys:        sessionStickyKeysEnabled(raw.SessionStickyKeys),
		BaseURL:                  raw.BaseURL,
		APIVersion:               raw.APIVersion,
		Backend:                  raw.Backend,
		AuthType:                 raw.AuthType,
		APIMode:                  raw.APIMode,
		VertexProject:            raw.VertexProject,
		VertexLocation:           raw.VertexLocation,
		ServiceAccountFile:       raw.ServiceAccountFile,
		ServiceAccountJSON:       raw.ServiceAccountJSON,
		ServiceAccountJSONBase64: raw.ServiceAccountJSONBase64,
		GCPScope:                 raw.GCPScope,
		Models:                   config.ProviderModelIDs(raw.Models),
		ModelMetadataOverrides:   config.ProviderModelMetadataOverrides(raw.Models),
		ModelFilter:              raw.ModelFilter.Normalize(),
		Resilience:               global,
	}
	if resolved.Type == "llmd" {
		resolved.InferenceObjective = raw.InferenceObjective
		resolved.FairnessFromUserPath = enabledByDefault(raw.FairnessFromUserPath)
	}

	resolved.Resilience.CircuitBreaker.Scope = config.NormalizeBreakerScope(resolved.Resilience.CircuitBreaker.Scope)
	if raw.Resilience == nil {
		return resolved
	}

	if r := raw.Resilience.Retry; r != nil { //nolint:dupl // Explicit field overrides preserve nil-as-inherit semantics for retry settings.
		if r.RetryOnStatuses != nil {
			resolved.Resilience.Retry.RetryOnStatuses = r.RetryOnStatuses
		}
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

	if cb := raw.Resilience.CircuitBreaker; cb != nil { //nolint:dupl // Breaker settings have independent field overrides with the same inheritance semantics.
		if cb.FailureOnStatuses != nil {
			resolved.Resilience.CircuitBreaker.FailureOnStatuses = cb.FailureOnStatuses
		}
		if cb.Scope != nil {
			resolved.Resilience.CircuitBreaker.Scope = config.NormalizeBreakerScope(*cb.Scope)
		}
		if cb.Enabled != nil {
			resolved.Resilience.CircuitBreaker.Enabled = *cb.Enabled
		}
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

func sessionStickyKeysEnabled(value *bool) bool {
	return enabledByDefault(value)
}

func enabledByDefault(value *bool) bool {
	return value == nil || *value
}

func normalizeProviderType(raw config.RawProviderConfig) string {
	providerType := strings.TrimSpace(raw.Type)
	if strings.EqualFold(providerType, "gemini") && strings.EqualFold(strings.TrimSpace(raw.Backend), "vertex") {
		return "vertex"
	}
	return providerType
}

// rawProviderModelsFromIDs wraps a plain string slice into RawProviderModel
// entries. Used for env-var-sourced model lists where metadata is never present.
func rawProviderModelsFromIDs(ids []string) []config.RawProviderModel {
	if len(ids) == 0 {
		return nil
	}
	out := make([]config.RawProviderModel, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		out = append(out, config.RawProviderModel{ID: id})
	}
	return out
}
