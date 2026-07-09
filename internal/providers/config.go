package providers

import (
	"maps"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"gomodel/config"
	"gomodel/internal/core"
)

// ProviderConfig holds the fully resolved provider configuration after merging
// global defaults with per-provider overrides.
type ProviderConfig struct {
	Type string
	// APIKey is the provider's primary credential: the first entry of APIKeys,
	// or "" for keyless providers. Prefer APIKeys for anything that
	// authenticates a request, so rotation is honoured.
	APIKey string
	// APIKeys is the provider's full, ordered, de-duplicated key set. Requests
	// rotate across it round robin when it holds more than one key. It is nil
	// for keyless providers and holds exactly one entry in the common case.
	APIKeys                  []string
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
	Models                   []string
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

// applyProviderEnvVars overlays well-known provider env vars onto the raw YAML map.
// Env var values always win over YAML values for the same provider name.
func applyProviderEnvVars(raw map[string]config.RawProviderConfig, discovery map[string]DiscoveryConfig) map[string]config.RawProviderConfig {
	result := make(map[string]config.RawProviderConfig, len(raw))
	maps.Copy(result, raw)
	environ := os.Environ()

	for _, providerType := range sortedDiscoveryTypes(discovery) {
		spec := discovery[providerType]
		for _, source := range providerEnvSources(providerType, spec) {
			envGroups := collectProviderEnvValues(source.Prefix, spec, environ)

			if values, ok := envGroups[""]; ok {
				applyUnsuffixedProviderEnvVars(result, providerType, spec, source, values)
			}

			for _, suffix := range sortedProviderEnvSuffixes(envGroups) {
				if suffix == "" {
					continue
				}
				applySuffixedProviderEnvVars(result, providerType, spec, source, suffix, envGroups[suffix])
			}
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
	providerEnvFieldBackend
	providerEnvFieldAuthType
	providerEnvFieldAPIMode
	providerEnvFieldVertexProject
	providerEnvFieldVertexLocation
	providerEnvFieldServiceAccountFile
	providerEnvFieldServiceAccountJSON
	providerEnvFieldServiceAccountJSONBase64
	providerEnvFieldGCPScope
)

type providerEnvSource struct {
	Prefix        string
	DefaultName   string
	NameSeparator string
	OverlayByType bool
}

type providerEnvValues struct {
	APIKey string
	// APIKeysByIndex holds `<PROVIDER>_API_KEY_<n>` values keyed by n. The
	// unsuffixed `<PROVIDER>_API_KEY` is kept apart in APIKey so it can claim
	// slot 1 regardless of the order os.Environ happens to return.
	APIKeysByIndex           map[int]string
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
	Models                   []string
}

// apiKeys returns the ordered key set this env group declares: the unsuffixed
// key leads, then the numbered keys in ascending index order. Gaps are ignored,
// so setting only `_API_KEY` and `_API_KEY_3` yields two keys, and a key
// repeated across `_API_KEY` and `_API_KEY_1` is de-duplicated to one.
func (v providerEnvValues) apiKeys() []string {
	if strings.TrimSpace(v.APIKey) == "" && len(v.APIKeysByIndex) == 0 {
		return nil
	}

	// The unsuffixed key sorts ahead of every numbered slot, which are 1-based.
	byIndex := make(map[int]string, len(v.APIKeysByIndex)+1)
	maps.Copy(byIndex, v.APIKeysByIndex)
	if strings.TrimSpace(v.APIKey) != "" {
		byIndex[0] = v.APIKey
	}

	indexes := make([]int, 0, len(byIndex))
	for index := range byIndex {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)

	keys := make([]string, 0, len(indexes))
	for _, index := range indexes {
		keys = append(keys, byIndex[index])
	}
	return resolvedAPIKeys(keys)
}

// hasAPIKey reports whether this env group carries any credential, numbered or
// not. Base-URL defaulting keys off it, so a provider configured only through
// `<PROVIDER>_API_KEY_2` still resolves its default endpoint.
//
// It probes the fields directly rather than calling apiKeys: empty() asks this
// question for every env group, and ordering the keys to then discard them
// costs a map, a sort, and two slices. Both spellings agree because a key that
// fails HasResolvedProviderValue -- blank, whitespace, or an unresolved
// `${VAR}` -- is one that apiKeys would drop.
func (v providerEnvValues) hasAPIKey() bool {
	if HasResolvedProviderValue(v.APIKey) {
		return true
	}
	for _, key := range v.APIKeysByIndex {
		if HasResolvedProviderValue(key) {
			return true
		}
	}
	return false
}

func (v providerEnvValues) empty() bool {
	return !v.hasAPIKey() &&
		strings.TrimSpace(v.BaseURL) == "" &&
		strings.TrimSpace(v.APIVersion) == "" &&
		strings.TrimSpace(v.Backend) == "" &&
		strings.TrimSpace(v.AuthType) == "" &&
		strings.TrimSpace(v.APIMode) == "" &&
		strings.TrimSpace(v.VertexProject) == "" &&
		strings.TrimSpace(v.VertexLocation) == "" &&
		strings.TrimSpace(v.ServiceAccountFile) == "" &&
		strings.TrimSpace(v.ServiceAccountJSON) == "" &&
		strings.TrimSpace(v.ServiceAccountJSONBase64) == "" &&
		strings.TrimSpace(v.GCPScope) == "" &&
		len(v.Models) == 0
}

func providerEnvSources(providerType string, spec DiscoveryConfig) []providerEnvSource {
	separator := spec.NameSeparator
	if separator == "" {
		separator = "-"
	}
	return []providerEnvSource{{
		Prefix:        envPrefix(providerType),
		DefaultName:   providerType,
		NameSeparator: separator,
		OverlayByType: true,
	}}
}

func collectProviderEnvValues(prefix string, spec DiscoveryConfig, environ []string) map[string]providerEnvValues {
	groups := make(map[string]providerEnvValues)
	prefixWithSeparator := prefix + "_"

	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || value == "" || !strings.HasPrefix(key, prefixWithSeparator) {
			continue
		}

		suffix, field, index, ok := parseProviderEnvKey(prefix, key, spec)
		if !ok {
			continue
		}

		values := groups[suffix]
		switch field {
		case providerEnvFieldAPIKey:
			if index == 0 {
				values.APIKey = value
				break
			}
			if values.APIKeysByIndex == nil {
				values.APIKeysByIndex = make(map[int]string)
			}
			values.APIKeysByIndex[index] = value
		case providerEnvFieldBaseURL:
			values.BaseURL = normalizeResolvedBaseURL(value)
		case providerEnvFieldAPIVersion:
			values.APIVersion = value
		case providerEnvFieldModels:
			values.Models = parseCSVEnvList(value)
		case providerEnvFieldBackend:
			values.Backend = value
		case providerEnvFieldAuthType:
			values.AuthType = value
		case providerEnvFieldAPIMode:
			values.APIMode = value
		case providerEnvFieldVertexProject:
			values.VertexProject = value
		case providerEnvFieldVertexLocation:
			values.VertexLocation = value
		case providerEnvFieldServiceAccountFile:
			values.ServiceAccountFile = value
		case providerEnvFieldServiceAccountJSON:
			values.ServiceAccountJSON = value
		case providerEnvFieldServiceAccountJSONBase64:
			values.ServiceAccountJSONBase64 = value
		case providerEnvFieldGCPScope:
			values.GCPScope = value
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

// parseProviderEnvKey splits a provider env var into the provider-name suffix,
// the field it sets, and (for API keys) the 1-based rotation index. An index of
// 0 means the unsuffixed `<PREFIX>_API_KEY`.
func parseProviderEnvKey(prefix, key string, spec DiscoveryConfig) (string, providerEnvField, int, bool) {
	rest, ok := strings.CutPrefix(key, prefix+"_")
	if !ok {
		return "", 0, 0, false
	}

	// A trailing `_<n>` on an API key names a rotation slot, so check it before
	// the field table: `OPENAI_API_KEY_2` is key 2 of provider `openai`, and
	// `OPENAI_EU_API_KEY_2` is key 2 of provider `openai-eu`. A suffix that
	// merely ends in a number is unambiguous the other way -- in
	// `OPENAI_REGION_2_API_KEY` the digits do not trail the key, so it stays
	// provider `openai-region-2`.
	if base, index, isIndexed := cutAPIKeyIndex(rest); isIndexed {
		if base == "API_KEY" {
			return "", providerEnvFieldAPIKey, index, true
		}
		if suffix, found := strings.CutSuffix(base, "_API_KEY"); found && validProviderEnvSuffix(suffix) {
			return suffix, providerEnvFieldAPIKey, index, true
		}
		return "", 0, 0, false
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
		{name: "AUTH_TYPE", field: providerEnvFieldAuthType},
		{name: "API_MODE", field: providerEnvFieldAPIMode},
		{name: "BACKEND", field: providerEnvFieldBackend},
		{name: "API_KEY", field: providerEnvFieldAPIKey},
		{name: "MODELS", field: providerEnvFieldModels},
	}
	if strings.EqualFold(prefix, "VERTEX") {
		fields = append([]struct {
			name  string
			field providerEnvField
		}{
			{name: "SERVICE_ACCOUNT_JSON_BASE64", field: providerEnvFieldServiceAccountJSONBase64},
			{name: "SERVICE_ACCOUNT_JSON", field: providerEnvFieldServiceAccountJSON},
			{name: "SERVICE_ACCOUNT_FILE", field: providerEnvFieldServiceAccountFile},
			{name: "VERTEX_PROJECT", field: providerEnvFieldVertexProject},
			{name: "VERTEX_LOCATION", field: providerEnvFieldVertexLocation},
			{name: "PROJECT", field: providerEnvFieldVertexProject},
			{name: "LOCATION", field: providerEnvFieldVertexLocation},
			{name: "GCP_SCOPE", field: providerEnvFieldGCPScope},
		}, fields...)
	}

	for _, candidate := range fields {
		if candidate.field == providerEnvFieldAPIVersion && !spec.SupportsAPIVersion {
			continue
		}
		if rest == candidate.name {
			return "", candidate.field, 0, true
		}
		suffix, found := strings.CutSuffix(rest, "_"+candidate.name)
		if found && validProviderEnvSuffix(suffix) {
			return suffix, candidate.field, 0, true
		}
	}

	return "", 0, 0, false
}

// cutAPIKeyIndex splits a trailing rotation index off an API-key env var,
// reporting the remaining base and the 1-based index. `_1` is accepted as well
// as `_2` and up: operators who spell every slot out (`_1`, `_2`, `_3`) get
// the keys they configured rather than a silently dropped first one.
func cutAPIKeyIndex(rest string) (string, int, bool) {
	base, digits, found := lastCut(rest, "_")
	if !found || !strings.HasSuffix(base, "API_KEY") {
		return "", 0, false
	}
	index, err := strconv.Atoi(digits)
	if err != nil || index < 1 {
		return "", 0, false
	}
	return base, index, true
}

// lastCut is strings.Cut anchored at the final separator.
func lastCut(s, sep string) (string, string, bool) {
	i := strings.LastIndex(s, sep)
	if i < 0 {
		return s, "", false
	}
	return s[:i], s[i+len(sep):], true
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

func applyUnsuffixedProviderEnvVars(result map[string]config.RawProviderConfig, providerType string, spec DiscoveryConfig, source providerEnvSource, values providerEnvValues) {
	if values.empty() {
		return
	}

	targetKey, matched, ambiguous := findEnvOverlayTarget(result, providerType, source)
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

	result[source.DefaultName] = values.rawConfig(providerType, spec)
}

func applySuffixedProviderEnvVars(result map[string]config.RawProviderConfig, providerType string, spec DiscoveryConfig, source providerEnvSource, suffix string, values providerEnvValues) {
	if values.empty() {
		return
	}

	targetKey := providerNameForEnvSuffix(source, suffix)
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
	backend := v.Backend
	keys := v.apiKeys()
	primary := ""
	if len(keys) > 0 {
		primary = keys[0]
	}
	return config.RawProviderConfig{
		Type:                     providerType,
		APIKey:                   primary,
		APIKeys:                  keys,
		BaseURL:                  v.resolvedBaseURL(spec),
		APIVersion:               v.APIVersion,
		Backend:                  backend,
		AuthType:                 v.AuthType,
		APIMode:                  v.APIMode,
		VertexProject:            v.VertexProject,
		VertexLocation:           v.VertexLocation,
		ServiceAccountFile:       v.ServiceAccountFile,
		ServiceAccountJSON:       v.ServiceAccountJSON,
		ServiceAccountJSONBase64: v.ServiceAccountJSONBase64,
		GCPScope:                 v.GCPScope,
		Models:                   rawProviderModelsFromIDs(v.Models),
	}
}

func (v providerEnvValues) resolvedBaseURL(spec DiscoveryConfig) string {
	baseURL := strings.TrimSpace(v.BaseURL)
	if baseURL == "" && v.hasAPIKey() && spec.DefaultBaseURL != "" {
		return spec.DefaultBaseURL
	}
	return baseURL
}

func overlayProviderEnvValues(existing config.RawProviderConfig, values providerEnvValues, spec DiscoveryConfig) config.RawProviderConfig {
	// Env replaces the provider's whole key set rather than merging into it, so
	// dropping `OPENAI_API_KEY_2` from the environment removes that key instead
	// of leaving a stale YAML entry rotating behind it.
	if keys := values.apiKeys(); len(keys) > 0 {
		existing.APIKey = keys[0]
		existing.APIKeys = keys
	}
	if values.BaseURL != "" {
		existing.BaseURL = values.BaseURL
	} else if normalizeResolvedBaseURL(existing.BaseURL) == "" && values.hasAPIKey() && spec.DefaultBaseURL != "" {
		existing.BaseURL = spec.DefaultBaseURL
	}
	if values.APIVersion != "" {
		existing.APIVersion = values.APIVersion
	}
	if values.Backend != "" {
		existing.Backend = values.Backend
	}
	if values.AuthType != "" {
		existing.AuthType = values.AuthType
	}
	if values.APIMode != "" {
		existing.APIMode = values.APIMode
	}
	if values.VertexProject != "" {
		existing.VertexProject = values.VertexProject
	}
	if values.VertexLocation != "" {
		existing.VertexLocation = values.VertexLocation
	}
	if values.ServiceAccountFile != "" {
		existing.ServiceAccountFile = values.ServiceAccountFile
	}
	if values.ServiceAccountJSON != "" {
		existing.ServiceAccountJSON = values.ServiceAccountJSON
	}
	if values.ServiceAccountJSONBase64 != "" {
		existing.ServiceAccountJSONBase64 = values.ServiceAccountJSONBase64
	}
	if values.GCPScope != "" {
		existing.GCPScope = values.GCPScope
	}
	if len(values.Models) > 0 {
		existing.Models = rawProviderModelsFromIDs(values.Models)
	}
	return existing
}

func providerNameForEnvSuffix(source providerEnvSource, suffix string) string {
	baseName := strings.TrimSpace(source.DefaultName)
	suffixName := normalizeEnvSuffixForProviderName(suffix, source.NameSeparator)
	if suffixName == "" {
		return baseName
	}
	if baseName == "" {
		return suffixName
	}
	return baseName + source.NameSeparator + suffixName
}

func normalizeEnvSuffixForProviderName(suffix, separator string) string {
	if separator == "" {
		separator = "-"
	}
	var b strings.Builder
	lastHyphen := false
	for _, r := range strings.TrimSpace(suffix) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
			lastHyphen = false
		case r == '_' && !lastHyphen:
			b.WriteString(separator)
			lastHyphen = true
		}
	}
	return strings.Trim(b.String(), separator)
}

func findEnvOverlayTarget(raw map[string]config.RawProviderConfig, providerType string, source providerEnvSource) (string, bool, bool) {
	if existing, ok := raw[source.DefaultName]; ok && rawProviderMatchesType(existing, providerType) {
		return source.DefaultName, true, false
	}
	if !source.OverlayByType {
		return "", false, false
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
	if strings.EqualFold(strings.TrimSpace(providerType), "vertex") {
		return isVertexProviderConfig(cfg)
	}
	return strings.TrimSpace(cfg.Type) == strings.TrimSpace(providerType)
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
		result[name] = buildProviderConfig(r, global)
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
		Resilience:               global,
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
