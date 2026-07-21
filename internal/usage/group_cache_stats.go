package usage

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/goccy/go-json"
)

// GroupCacheStats aggregates the cache figures for one chart group (a model,
// user path, or label) during the second streaming pass the aggregate SQL
// cannot produce (it needs per-row raw_data).
type GroupCacheStats struct {
	UncachedInputTokens     int64
	CachedInputTokens       int64
	CacheWriteInputTokens   int64
	LocalCachedInputTokens  int64
	LocalCachedOutputTokens int64
	CachedTokensByPricing   map[CachedPricingKey]int64
}

// usageCacheStatRow is one streamed usage row for the group cache fold. The
// fold always streams with cache mode "all": provider rows feed the
// prompt-cache split, local-cache rows feed the local-cached token counts.
type usageCacheStatRow struct {
	Model        string
	Provider     string
	ProviderName string
	UserPath     string
	Labels       []string
	CacheType    string
	InputTokens  int
	OutputTokens int
	RawData      map[string]any
}

// groupKeysFunc derives the fold keys one row contributes to. Most groupings
// return one key; the label grouping returns one per label.
type groupKeysFunc func(row usageCacheStatRow) []string

// usageModelGroupKey mirrors the SQL grouping (model, provider,
// COALESCE(NULLIF(TRIM(provider_name), ”), provider)).
func usageModelGroupKey(model, provider, providerName string) string {
	name := strings.TrimSpace(providerName)
	if name == "" {
		name = provider
	}
	return model + "\x00" + provider + "\x00" + name
}

// usageUserPathGroupKey mirrors usageGroupedUserPathSQL: blank paths collapse
// to the tracked root.
func usageUserPathGroupKey(userPath string) string {
	path := strings.TrimSpace(userPath)
	if path == "" {
		return "/"
	}
	return path
}

func modelGroupKeys(row usageCacheStatRow) []string {
	return []string{usageModelGroupKey(row.Model, row.Provider, row.ProviderName)}
}

func userPathGroupKeys(row usageCacheStatRow) []string {
	return []string{usageUserPathGroupKey(row.UserPath)}
}

func labelGroupKeys(row usageCacheStatRow) []string {
	return row.Labels
}

// accumulateGroupCacheStats folds one row into every group it belongs to.
func accumulateGroupCacheStats(out map[string]*GroupCacheStats, keysFor groupKeysFunc, row usageCacheStatRow) {
	keys := keysFor(row)
	if len(keys) == 0 {
		return
	}
	local := isLocalCacheType(&row.CacheType)
	var uncached, cached, cacheWrite int64
	if !local {
		uncached, cached, cacheWrite = EntryInputSegments(UsageLogEntry{
			InputTokens: row.InputTokens,
			Provider:    row.Provider,
			RawData:     row.RawData,
		})
	}
	for _, key := range keys {
		stats := out[key]
		if stats == nil {
			stats = &GroupCacheStats{}
			out[key] = stats
		}
		if local {
			stats.LocalCachedInputTokens += int64(row.InputTokens)
			stats.LocalCachedOutputTokens += int64(row.OutputTokens)
			continue
		}
		stats.UncachedInputTokens += uncached
		stats.CachedInputTokens += cached
		stats.CacheWriteInputTokens += cacheWrite
		if cached > 0 {
			if stats.CachedTokensByPricing == nil {
				stats.CachedTokensByPricing = map[CachedPricingKey]int64{}
			}
			stats.CachedTokensByPricing[CachedPricingKey{
				Model:        row.Model,
				Provider:     row.Provider,
				ProviderName: strings.TrimSpace(row.ProviderName),
			}] += cached
		}
	}
}

// foldUsageCacheRows folds streamed SQL rows (model, provider, provider_name,
// user_path, labels JSON, cache_type, input_tokens, output_tokens, raw_data)
// into per-group cache stats. Shared by the SQL backends; MongoDB folds its
// decoded documents with accumulateGroupCacheStats directly.
func foldUsageCacheRows(rows inputSegmentRows, keysFor groupKeysFunc) (map[string]*GroupCacheStats, error) {
	out := map[string]*GroupCacheStats{}
	for rows.Next() {
		var model, provider string
		var providerName, userPath, labelsJSON, cacheType, rawDataJSON *string
		var inputTokens, outputTokens int
		if err := rows.Scan(&model, &provider, &providerName, &userPath, &labelsJSON, &cacheType, &inputTokens, &outputTokens, &rawDataJSON); err != nil {
			return nil, fmt.Errorf("failed to scan usage cache stat row: %w", err)
		}
		row := usageCacheStatRow{
			Model:        model,
			Provider:     provider,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
		}
		if providerName != nil {
			row.ProviderName = *providerName
		}
		if userPath != nil {
			row.UserPath = *userPath
		}
		if cacheType != nil {
			row.CacheType = *cacheType
		}
		if labelsJSON != nil && *labelsJSON != "" {
			if err := json.Unmarshal([]byte(*labelsJSON), &row.Labels); err != nil {
				slog.Warn("failed to unmarshal labels JSON", "error", err)
			}
		}
		if rawDataJSON != nil && *rawDataJSON != "" {
			if err := json.Unmarshal([]byte(*rawDataJSON), &row.RawData); err != nil {
				slog.Warn("failed to unmarshal raw_data JSON", "error", err)
			}
		}
		accumulateGroupCacheStats(out, keysFor, row)
	}
	return out, rows.Err()
}

// assign copies the fold result onto a row's shared cache fields.
func (f *GroupCacheFields) assign(stats *GroupCacheStats) {
	if stats == nil {
		return
	}
	f.UncachedInputTokens = stats.UncachedInputTokens
	f.CachedInputTokens = stats.CachedInputTokens
	f.CacheWriteInputTokens = stats.CacheWriteInputTokens
	f.LocalCachedInputTokens = stats.LocalCachedInputTokens
	f.LocalCachedOutputTokens = stats.LocalCachedOutputTokens
	f.CachedTokensByPricing = stats.CachedTokensByPricing
}

// applyModelCacheStats merges the fold onto matching by-model rows.
func applyModelCacheStats(rows []ModelUsage, stats map[string]*GroupCacheStats) {
	for i := range rows {
		rows[i].assign(stats[usageModelGroupKey(rows[i].Model, rows[i].Provider, rows[i].ProviderName)])
	}
}

// applyUserPathCacheStats merges the fold onto matching by-user-path rows.
func applyUserPathCacheStats(rows []UserPathUsage, stats map[string]*GroupCacheStats) {
	for i := range rows {
		rows[i].assign(stats[usageUserPathGroupKey(rows[i].UserPath)])
	}
}

// applyLabelCacheStats merges the fold onto matching by-label rows.
func applyLabelCacheStats(rows []LabelUsage, stats map[string]*GroupCacheStats) {
	for i := range rows {
		rows[i].assign(stats[rows[i].Label])
	}
}

// EstimateCachedInputCost prices a group's prompt-cached input tokens with
// current catalog pricing (CachedInputPerMtok per pricing identity). It is an
// estimate: models without a cached-input rate contribute nothing, and it
// reflects today's prices, not the prices at request time — the same
// trade-off as pricing recalculation. Returns nil when nothing could be
// priced.
func EstimateCachedInputCost(byPricing map[CachedPricingKey]int64, resolver PricingResolver) *float64 {
	if resolver == nil || len(byPricing) == 0 {
		return nil
	}
	var total float64
	priced := false
	for key, tokens := range byPricing {
		if tokens <= 0 {
			continue
		}
		pricing := resolver.ResolvePricing(key.Model, effectiveRecalculationPricingProvider(key.Provider, key.ProviderName))
		if pricing == nil || pricing.CachedInputPerMtok == nil {
			continue
		}
		total += float64(tokens) * *pricing.CachedInputPerMtok / 1_000_000
		priced = true
	}
	if !priced {
		return nil
	}
	return &total
}
