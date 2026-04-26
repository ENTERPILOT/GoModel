package usage

import (
	"strings"

	"gomodel/internal/core"
)

const (
	CacheTypeExact    = "exact"
	CacheTypeSemantic = "semantic"

	CacheModeUncached = "uncached"
	CacheModeCached   = "cached"
	CacheModeAll      = "all"
)

func normalizeCacheType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CacheTypeExact:
		return CacheTypeExact
	case CacheTypeSemantic:
		return CacheTypeSemantic
	default:
		return ""
	}
}

func normalizeCacheMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CacheModeCached:
		return CacheModeCached
	case CacheModeAll:
		return CacheModeAll
	default:
		return CacheModeUncached
	}
}

func cacheTypeValue(value string) any {
	if normalized := normalizeCacheType(value); normalized != "" {
		return normalized
	}
	return nil
}

func normalizedUsageEntryForStorage(entry *UsageEntry) *UsageEntry {
	if entry == nil {
		return nil
	}

	normalized := normalizeCacheType(entry.CacheType)
	providerName := strings.TrimSpace(entry.ProviderName)
	userPath := normalizeUsageEntryUserPath(entry.UserPath)
	if normalized == entry.CacheType && providerName == entry.ProviderName && userPath == entry.UserPath {
		return entry
	}

	cloned := *entry
	cloned.CacheType = normalized
	cloned.ProviderName = providerName
	cloned.UserPath = userPath
	return &cloned
}

func normalizeUsageEntryUserPath(value string) string {
	normalized, err := core.NormalizeUserPath(value)
	if err != nil || normalized == "" {
		return "/"
	}
	return normalized
}
