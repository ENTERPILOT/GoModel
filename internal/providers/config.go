package providers

import (
	"maps"
	"os"
	"sort"
	"strings"
	"unicode"

	"gomodel/config"
	"gomodel/internal/core"
)

// ProviderConfig holds the fully resolved provider configuration after merging
// global defaults with per-provider overrides.
type ProviderConfig struct {
	Type       string
	APIKey     string
	BaseURL    string
	APIVersion string
	Models     []string
	// ModelMetadataOverrides holds operator-supplied metadata keyed by raw model
	// ID (as it appears in the provider's /models response). The registry merges
	// these onto remote-registry metadata after enrichment; non-zero fields here
	// win. Empty/nil when no per-model metadata is declared in YAML.
	ModelMetadataOverrides map[string]*core.ModelMetadata
	Resilience             config.ResilienceConfig
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
	environ := os.Environ()

	for _, providerType := range sortedDiscoveryTypes(discovery) {
		spec := discovery[providerType]
		envGroups := collectProviderEnvValues(providerType, spec, environ)

		if values, ok := envGroups[""]; ok {
			applyUnsuffixedProviderEnvVars(result, providerType, spec, values)
		}

		for _, suffix := range sortedProviderEnvSuffixes(envGroups) {
			if suffix == "" {
				continue
			}
			applySuffixedProviderEnvVars(result, providerType, spec, suffix, envGroups[suffix])
		}
	}

	return result
}

type providerEnvField int

const (
	providerEnvFieldAPIKey providerEnvField = iota
	providerEnvFieldBaseURL
	providerEnvFieldAPIVersion
	providerEnvFieldModels
)

type providerEnvValues struct {
	APIKey     string
	BaseURL    string
	APIVersion string
	Models     []string
}

func (v providerEnvValues) empty() bool {
	return strings.TrimSpace(v.APIKey) == "" &&
		strings.TrimSpace(v.BaseURL) == "" &&
		strings.TrimSpace(v.APIVersion) == "" &&
		len(v.Models) == 0
}

func collectProviderEnvValues(providerType string, spec DiscoveryConfig, environ []string) map[string]providerEnvValues {
	groups := make(map[string]providerEnvValues)
	prefix := envPrefix(providerType)
	prefixWithSeparator := prefix + "_"

	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" || !strings.HasPrefix(key, prefixWithSeparator) {
			continue
		}

		suffix, field, ok := parseProviderEnvKey(prefix, key, spec)
		if !ok {
			continue
		}

		values := groups[suffix]
		switch field {
		case providerEnvFieldAPIKey:
			values.APIKey = value
		case providerEnvFieldBaseURL:
			values.BaseURL = normalizeResolvedBaseURL(value)
		case providerEnvFieldAPIVersion:
			values.APIVersion = value
		case providerEnvFieldModels:
			values.Models = parseCSVEnvList(value)
		}
		groups[suffix] = values
	}

	for suffix, values := range groups {
		if values.empty() {
			delete(groups, suffix)
		}
	}

	return groups
}

func parseProviderEnvKey(prefix, key string, spec DiscoveryConfig) (string, providerEnvField, bool) {
	rest, ok := strings.CutPrefix(key, prefix+"_")
	if !ok {
		return "", 0, false
	}

	// Match field names from the right so suffixes can contain underscores.
	// Keep longer field tokens before their shorter overlapping forms; for
	// example, API_VERSION must be checked before a future VERSION-like token.
	fields := []struct {
		name  string
		field providerEnvField
	}{
		{name: "API_VERSION", field: providerEnvFieldAPIVersion},
		{name: "BASE_URL", field: providerEnvFieldBaseURL},
		{name: "API_KEY", field: providerEnvFieldAPIKey},
		{name: "MODELS", field: providerEnvFieldModels},
	}

	for _, candidate := range fields {
		if candidate.field == providerEnvFieldAPIVersion && !spec.SupportsAPIVersion {
			continue
		}
		if rest == candidate.name {
			return "", candidate.field, true
		}
		suffix, found := strings.CutSuffix(rest, "_"+candidate.name)
		if found && validProviderEnvSuffix(suffix) {
			return suffix, candidate.field, true
		}
	}

	return "", 0, false
}

func validProviderEnvSuffix(suffix string) bool {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" || strings.HasPrefix(suffix, "_") || strings.HasSuffix(suffix, "_") {
		return false
	}

	lastUnderscore := false
	hasAlnum := false
	for _, r := range suffix {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			hasAlnum = true
			lastUnderscore = false
		case r == '_' && !lastUnderscore:
			lastUnderscore = true
		default:
			return false
		}
	}
	return hasAlnum
}

func sortedProviderEnvSuffixes(groups map[string]providerEnvValues) []string {
	suffixes := make([]string, 0, len(groups))
	for suffix := range groups {
		suffixes = append(suffixes, suffix)
	}
	sort.Strings(suffixes)
	return suffixes
}

func applyUnsuffixedProviderEnvVars(result map[string]config.RawProviderConfig, providerType string, spec DiscoveryConfig, values providerEnvValues) {
	if values.empty() {
		return
	}

	targetKey, matched, ambiguous := findEnvOverlayTarget(result, providerType)
	if matched {
		result[targetKey] = overlayProviderEnvValues(result[targetKey], values, spec)
		return
	}
	if ambiguous {
		return
	}
	if spec.RequireBaseURL && values.BaseURL == "" {
		return
	}

	result[providerType] = values.rawConfig(providerType, spec)
}

func applySuffixedProviderEnvVars(result map[string]config.RawProviderConfig, providerType string, spec DiscoveryConfig, suffix string, values providerEnvValues) {
	if values.empty() {
		return
	}

	targetKey := providerNameForEnvSuffix(providerType, suffix)
	if targetKey == "" {
		return
	}

	if existing, ok := result[targetKey]; ok {
		if !rawProviderMatchesType(existing, providerType) {
			return
		}
		result[targetKey] = overlayProviderEnvValues(existing, values, spec)
		return
	}

	if spec.RequireBaseURL && values.BaseURL == "" {
		return
	}

	result[targetKey] = values.rawConfig(providerType, spec)
}

func (v providerEnvValues) rawConfig(providerType string, spec DiscoveryConfig) config.RawProviderConfig {
	return config.RawProviderConfig{
		Type:       providerType,
		APIKey:     v.APIKey,
		BaseURL:    v.resolvedBaseURL(spec),
		APIVersion: v.APIVersion,
		Models:     rawProviderModelsFromIDs(v.Models),
	}
}

func (v providerEnvValues) resolvedBaseURL(spec DiscoveryConfig) string {
	baseURL := strings.TrimSpace(v.BaseURL)
	if baseURL == "" && strings.TrimSpace(v.APIKey) != "" && spec.DefaultBaseURL != "" {
		return spec.DefaultBaseURL
	}
	return baseURL
}

func overlayProviderEnvValues(existing config.RawProviderConfig, values providerEnvValues, spec DiscoveryConfig) config.RawProviderConfig {
	if values.APIKey != "" {
		existing.APIKey = values.APIKey
	}
	if values.BaseURL != "" {
		existing.BaseURL = values.BaseURL
	} else if normalizeResolvedBaseURL(existing.BaseURL) == "" && values.APIKey != "" && spec.DefaultBaseURL != "" {
		existing.BaseURL = spec.DefaultBaseURL
	}
	if values.APIVersion != "" {
		existing.APIVersion = values.APIVersion
	}
	if len(values.Models) > 0 {
		existing.Models = rawProviderModelsFromIDs(values.Models)
	}
	return existing
}

func providerNameForEnvSuffix(providerType, suffix string) string {
	providerType = strings.TrimSpace(providerType)
	suffixName := normalizeEnvSuffixForProviderName(suffix)
	if suffixName == "" {
		return providerType
	}
	if providerType == "" {
		return suffixName
	}
	return providerType + "-" + suffixName
}

func normalizeEnvSuffixForProviderName(suffix string) string {
	var b strings.Builder
	lastHyphen := false
	for _, r := range strings.TrimSpace(suffix) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			lastHyphen = false
		case r == '_' && !lastHyphen:
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), "-")
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
	Models     string
}

func derivedEnvNames(providerType string) providerEnvNames {
	prefix := envPrefix(providerType)
	return providerEnvNames{
		APIKey:     prefix + "_API_KEY",
		BaseURL:    prefix + "_BASE_URL",
		APIVersion: prefix + "_API_VERSION",
		Models:     prefix + "_MODELS",
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

func parseCSVEnvList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	items := strings.Split(value, ",")
	values := make([]string, 0, len(items))
	for _, item := range items {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		values = append(values, trimmed)
	}
	if len(values) == 0 {
		return nil
	}
	return values
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
		Type:                   raw.Type,
		APIKey:                 raw.APIKey,
		BaseURL:                raw.BaseURL,
		APIVersion:             raw.APIVersion,
		Models:                 config.ProviderModelIDs(raw.Models),
		ModelMetadataOverrides: config.ProviderModelMetadataOverrides(raw.Models),
		Resilience:             global,
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
