package providers

import (
	"log/slog"
	"maps"
	"math"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/enterpilot/gomodel/config"
)

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
	providerEnvFieldSessionStickyKeys
	providerEnvFieldInferenceObjective
	providerEnvFieldFairnessFromUserPath
	providerEnvFieldModelFilterInclude
	providerEnvFieldModelFilterExclude
	providerEnvFieldModelFilterMaxPrice
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
	InferenceObjective       string
	Models                   []string
	ModelFilterInclude       []string
	ModelFilterExclude       []string
	ModelFilterMaxPrice      *float64
	SessionStickyKeys        *bool
	FairnessFromUserPath     *bool
}

// modelFilter assembles the filter this env group declares.
func (v providerEnvValues) modelFilter() config.ModelFilter {
	return config.ModelFilter{
		Include:         v.ModelFilterInclude,
		Exclude:         v.ModelFilterExclude,
		MaxPricePerMtok: v.ModelFilterMaxPrice,
	}
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
		strings.TrimSpace(v.InferenceObjective) == "" &&
		v.SessionStickyKeys == nil &&
		v.FairnessFromUserPath == nil &&
		len(v.Models) == 0 &&
		v.modelFilter().Empty()
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
		case providerEnvFieldModelFilterInclude:
			values.ModelFilterInclude = parseCSVEnvList(value)
		case providerEnvFieldModelFilterExclude:
			values.ModelFilterExclude = parseCSVEnvList(value)
		case providerEnvFieldModelFilterMaxPrice:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil {
				// A price cap is a cost control: a malformed value must fail
				// validation, not quietly leave the cap unset and let every
				// model through. NaN carries "not a number" to the validator,
				// which rejects it the same way it rejects a literal NaN.
				parsed = math.NaN()
			}
			values.ModelFilterMaxPrice = &parsed
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
		case providerEnvFieldSessionStickyKeys:
			if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
				values.SessionStickyKeys = &parsed
			}
		case providerEnvFieldInferenceObjective:
			values.InferenceObjective = value
		case providerEnvFieldFairnessFromUserPath:
			if parsed, err := strconv.ParseBool(strings.TrimSpace(value)); err == nil {
				values.FairnessFromUserPath = &parsed
			}
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
		{name: "SESSION_STICKY_KEYS", field: providerEnvFieldSessionStickyKeys},
		{name: "API_VERSION", field: providerEnvFieldAPIVersion},
		{name: "BASE_URL", field: providerEnvFieldBaseURL},
		{name: "AUTH_TYPE", field: providerEnvFieldAuthType},
		{name: "API_MODE", field: providerEnvFieldAPIMode},
		{name: "BACKEND", field: providerEnvFieldBackend},
		{name: "API_KEY", field: providerEnvFieldAPIKey},
		{name: "MODEL_FILTER_MAX_PRICE_PER_MTOK", field: providerEnvFieldModelFilterMaxPrice},
		{name: "MODEL_FILTER_INCLUDE", field: providerEnvFieldModelFilterInclude},
		{name: "MODEL_FILTER_EXCLUDE", field: providerEnvFieldModelFilterExclude},
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
	if strings.EqualFold(prefix, "LLMD") {
		fields = append([]struct {
			name  string
			field providerEnvField
		}{
			{name: "FAIRNESS_FROM_USER_PATH", field: providerEnvFieldFairnessFromUserPath},
			{name: "INFERENCE_OBJECTIVE", field: providerEnvFieldInferenceObjective},
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

	candidates := envOverlayCandidates(result, providerType, source)
	switch len(candidates) {
	case 0:
		if spec.RequireBaseURL && values.BaseURL == "" {
			return
		}
		result[source.DefaultName] = values.rawConfig(providerType, spec)
	case 1:
		targetKey := candidates[0]
		if targetKey == source.DefaultName {
			result[targetKey] = overlayProviderEnvValues(result[targetKey], values, spec)
			return
		}
		// A config provider that merely shares the type may borrow the bare env
		// values for fields it left empty, but its explicit settings win: a stray
		// OPENAI_API_KEY must never be sent to whatever base_url "alpha" points at.
		fill, ignored := values.withoutFieldsSetBy(result[targetKey])
		if len(ignored) > 0 {
			slog.Warn("provider env vars ignored: a differently named config provider of this type already sets these fields",
				"env_prefix", source.Prefix,
				"provider", targetKey,
				"fields", ignored,
				"hint", "name the provider after its type or add another instance with "+source.Prefix+"_<SUFFIX>_*")
		}
		if !fill.empty() {
			result[targetKey] = overlayProviderEnvValues(result[targetKey], fill, spec)
		}
	default:
		slog.Warn("provider env vars ignored: several config providers share this type and none is named after it",
			"env_prefix", source.Prefix,
			"providers", candidates,
			"hint", "name one provider after its type or add another instance with "+source.Prefix+"_<SUFFIX>_*")
	}
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
		InferenceObjective:       v.InferenceObjective,
		FairnessFromUserPath:     v.FairnessFromUserPath,
		Models:                   rawProviderModelsFromIDs(v.Models),
		ModelFilter:              v.modelFilter(),
		SessionStickyKeys:        v.SessionStickyKeys,
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
	if values.InferenceObjective != "" {
		existing.InferenceObjective = values.InferenceObjective
	}
	if values.FairnessFromUserPath != nil {
		existing.FairnessFromUserPath = values.FairnessFromUserPath
	}
	if values.SessionStickyKeys != nil {
		existing.SessionStickyKeys = values.SessionStickyKeys
	}
	if len(values.Models) > 0 {
		existing.Models = rawProviderModelsFromIDs(values.Models)
	}
	// Each filter rule overlays independently, so an env price cap can narrow a
	// YAML pattern filter (and vice versa) without restating the other rule.
	if len(values.ModelFilterInclude) > 0 {
		existing.ModelFilter.Include = values.ModelFilterInclude
	}
	if len(values.ModelFilterExclude) > 0 {
		existing.ModelFilter.Exclude = values.ModelFilterExclude
	}
	if values.ModelFilterMaxPrice != nil {
		existing.ModelFilter.MaxPricePerMtok = values.ModelFilterMaxPrice
	}
	return existing
}

// withoutFieldsSetBy drops every env value whose field the config provider
// already sets explicitly, so the remainder only fills gaps. It returns the
// YAML names of the dropped fields for logging; values are never returned.
func (v providerEnvValues) withoutFieldsSetBy(existing config.RawProviderConfig) (providerEnvValues, []string) {
	var ignored []string
	drop := func(name string, envSet, cfgSet bool, clear func()) {
		if envSet && cfgSet {
			clear()
			ignored = append(ignored, name)
		}
	}

	drop("api_key", v.hasAPIKey(), rawProviderHasAPIKey(existing), func() {
		v.APIKey = ""
		v.APIKeysByIndex = nil
	})

	stringFields := []struct {
		name string
		env  *string
		cfg  string
	}{
		{"base_url", &v.BaseURL, normalizeResolvedBaseURL(existing.BaseURL)},
		{"api_version", &v.APIVersion, existing.APIVersion},
		{"backend", &v.Backend, existing.Backend},
		{"auth_type", &v.AuthType, existing.AuthType},
		{"api_mode", &v.APIMode, existing.APIMode},
		{"vertex_project", &v.VertexProject, existing.VertexProject},
		{"vertex_location", &v.VertexLocation, existing.VertexLocation},
		{"service_account_file", &v.ServiceAccountFile, existing.ServiceAccountFile},
		{"service_account_json", &v.ServiceAccountJSON, existing.ServiceAccountJSON},
		{"service_account_json_base64", &v.ServiceAccountJSONBase64, existing.ServiceAccountJSONBase64},
		{"gcp_scope", &v.GCPScope, existing.GCPScope},
		{"inference_objective", &v.InferenceObjective, existing.InferenceObjective},
	}
	for _, f := range stringFields {
		env := f.env
		drop(f.name, strings.TrimSpace(*env) != "", HasResolvedProviderValue(f.cfg), func() { *env = "" })
	}

	drop("session_sticky_keys", v.SessionStickyKeys != nil, existing.SessionStickyKeys != nil, func() { v.SessionStickyKeys = nil })
	drop("fairness_from_user_path", v.FairnessFromUserPath != nil, existing.FairnessFromUserPath != nil, func() { v.FairnessFromUserPath = nil })
	drop("models", len(v.Models) > 0, len(existing.Models) > 0, func() { v.Models = nil })
	drop("model_filter.include", len(v.ModelFilterInclude) > 0, len(existing.ModelFilter.Include) > 0, func() { v.ModelFilterInclude = nil })
	drop("model_filter.exclude", len(v.ModelFilterExclude) > 0, len(existing.ModelFilter.Exclude) > 0, func() { v.ModelFilterExclude = nil })
	drop("model_filter.max_price_per_mtok", v.ModelFilterMaxPrice != nil, existing.ModelFilter.MaxPricePerMtok != nil, func() { v.ModelFilterMaxPrice = nil })

	return v, ignored
}

func rawProviderHasAPIKey(cfg config.RawProviderConfig) bool {
	return HasResolvedProviderValue(cfg.APIKey) || slices.ContainsFunc(cfg.APIKeys, HasResolvedProviderValue)
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

// envOverlayCandidates returns the config providers the bare (unsuffixed) env
// vars of a type may apply to: the provider named after the type when it
// exists, otherwise every provider of that type. An empty result means the env
// vars register a new provider named after the type.
func envOverlayCandidates(raw map[string]config.RawProviderConfig, providerType string, source providerEnvSource) []string {
	if existing, ok := raw[source.DefaultName]; ok && rawProviderMatchesType(existing, providerType) {
		return []string{source.DefaultName}
	}
	if !source.OverlayByType {
		return nil
	}

	var candidates []string
	for name, cfg := range raw {
		if rawProviderMatchesType(cfg, providerType) {
			candidates = append(candidates, name)
		}
	}
	sort.Strings(candidates)
	return candidates
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
	// A registered prefix can also look like a suffixed instance of a shorter
	// provider (BEDROCK_MANTLE_* vs BEDROCK_*). Apply the more specific prefix
	// first; the shorter provider will then see an existing, differently typed
	// target and leave it alone. Keep lexical order as the deterministic tie
	// breaker.
	sort.Slice(types, func(i, j int) bool {
		left, right := envPrefix(types[i]), envPrefix(types[j])
		if len(left) != len(right) {
			return len(left) > len(right)
		}
		return types[i] < types[j]
	})
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
