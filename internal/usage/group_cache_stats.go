package usage

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/goccy/go-json"

	"github.com/enterpilot/gomodel/internal/storage/sqlutil"
)

// GroupCacheStats aggregates the cache figures for one chart group (a model,
// user path, or label) during the second streaming pass the aggregate SQL
// cannot produce (it needs per-row raw_data). The identity fields capture the
// group's coordinates from the first folded row so groups served entirely
// from the local cache — which the uncached aggregate pass never surfaces —
// can still materialize a row.
type GroupCacheStats struct {
	Model        string
	Provider     string
	ProviderName string
	UserPath     string

	UncachedInputTokens     int64
	CachedInputTokens       int64
	CacheWriteInputTokens   int64
	LocalCachedInputTokens  int64
	LocalCachedOutputTokens int64
	LocalRequests           int
	CachedTokensByPricing   map[CachedPricingKey]int64
}

// hasLocalTokens reports whether the group saw any local-cache traffic.
func (s *GroupCacheStats) hasLocalTokens() bool {
	return s.LocalCachedInputTokens > 0 || s.LocalCachedOutputTokens > 0
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
	Timestamp    time.Time
}

// groupKeysFunc derives the fold keys one row contributes to. Most groupings
// return one key; the label grouping returns one per label.
type groupKeysFunc func(row usageCacheStatRow) []string

// usageModelGroupKey mirrors the SQL grouping (model, provider,
// usageModelGroupKey builds a grouping key from the model, provider, and provider name, using the provider when the trimmed provider name is empty.
func usageModelGroupKey(model, provider, providerName string) string {
	name := strings.TrimSpace(providerName)
	if name == "" {
		name = provider
	}
	return model + "\x00" + provider + "\x00" + name
}

// usageUserPathGroupKey mirrors usageGroupedUserPathSQL: blank paths collapse
// usageUserPathGroupKey normalizes a user path, using "/" when the path is empty or contains only whitespace.
func usageUserPathGroupKey(userPath string) string {
	path := strings.TrimSpace(userPath)
	if path == "" {
		return "/"
	}
	return path
}

// modelGroupKeys returns the model and provider grouping key for a usage cache-stat row.
func modelGroupKeys(row usageCacheStatRow) []string {
	return []string{usageModelGroupKey(row.Model, row.Provider, row.ProviderName)}
}

// userPathGroupKeys returns the grouping key for a usage row's user path.
func userPathGroupKeys(row usageCacheStatRow) []string {
	return []string{usageUserPathGroupKey(row.UserPath)}
}

// labelGroupKeys returns the labels associated with a usage cache statistic row as grouping keys.
func labelGroupKeys(row usageCacheStatRow) []string {
	return row.Labels
}

// accumulateGroupCacheStats folds a usage row into each group identified by the grouping function.
// Local-cache rows update local token and request totals; other rows update uncached, cached, and
// cache-write token totals and record cached tokens by pricing identity.
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
			name := strings.TrimSpace(row.ProviderName)
			if name == "" {
				name = row.Provider
			}
			stats = &GroupCacheStats{
				Model:        row.Model,
				Provider:     row.Provider,
				ProviderName: name,
				UserPath:     usageUserPathGroupKey(row.UserPath),
			}
			out[key] = stats
		}
		if local {
			stats.LocalCachedInputTokens += int64(row.InputTokens)
			stats.LocalCachedOutputTokens += int64(row.OutputTokens)
			stats.LocalRequests++
			continue
		}
		stats.UncachedInputTokens += uncached
		stats.CachedInputTokens += cached
		stats.CacheWriteInputTokens += cacheWrite
		if cached > 0 {
			if stats.CachedTokensByPricing == nil {
				stats.CachedTokensByPricing = map[CachedPricingKey]int64{}
			}
			at := row.Timestamp.UTC()
			stats.CachedTokensByPricing[CachedPricingKey{
				Model:        row.Model,
				Provider:     row.Provider,
				ProviderName: strings.TrimSpace(row.ProviderName),
				Weekday:      at.Weekday(),
				Hour:         at.Hour(),
			}] += cached
		}
	}
}

// foldUsageCacheRows folds streamed SQL rows (model, provider, provider_name,
// user_path, labels JSON, cache_type, input_tokens, output_tokens, raw_data)
// into per-group cache stats. Shared by the SQL backends; MongoDB folds its
// foldUsageCacheRows aggregates streamed usage cache-stat rows by the keys produced by keysFor.
// It returns an error if scanning the rows fails or the result set reports an error.
func foldUsageCacheRows(rows inputSegmentRows, keysFor groupKeysFunc) (map[string]*GroupCacheStats, error) {
	out := map[string]*GroupCacheStats{}
	for rows.Next() {
		var model, provider string
		var providerName, userPath, labelsJSON, cacheType, rawDataJSON *string
		var inputTokens, outputTokens int
		var timestamp any
		if err := rows.Scan(&model, &provider, &providerName, &userPath, &labelsJSON, &cacheType, &inputTokens, &outputTokens, &rawDataJSON, &timestamp); err != nil {
			return nil, fmt.Errorf("failed to scan usage cache stat row: %w", err)
		}
		row := usageCacheStatRow{
			Model:        model,
			Provider:     provider,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			Timestamp:    cacheStatTimestamp(timestamp),
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

// cacheStatTimestamp reads the streamed timestamp column, which arrives as
// time.Time from PostgreSQL and as stored text (or a driver-parsed time)
// from SQLite. Unreadable values yield the zero time, which prices at the
// base rates.
func cacheStatTimestamp(value any) time.Time {
	switch v := value.(type) {
	case time.Time:
		return v
	case string:
		if parsed, ok := sqlutil.ParseSQLiteTimestamp(v); ok {
			return parsed
		}
	case []byte:
		if parsed, ok := sqlutil.ParseSQLiteTimestamp(string(v)); ok {
			return parsed
		}
	}
	return time.Time{}
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

// applyModelCacheStats merges the fold onto matching by-model rows and
// materializes rows for local-only groups the uncached aggregates never
// applyModelCacheStats merges grouped cache statistics into model usage rows and adds
// rows for model groups with local-cache activity that are absent from the input.
func applyModelCacheStats(rows []ModelUsage, stats map[string]*GroupCacheStats) []ModelUsage {
	seen := map[string]bool{}
	for i := range rows {
		key := usageModelGroupKey(rows[i].Model, rows[i].Provider, rows[i].ProviderName)
		seen[key] = true
		rows[i].assign(stats[key])
	}
	for key, s := range stats {
		if seen[key] || !s.hasLocalTokens() {
			continue
		}
		row := ModelUsage{Model: s.Model, Provider: s.Provider, ProviderName: s.ProviderName}
		row.assign(s)
		rows = append(rows, row)
	}
	return rows
}

// applyUserPathCacheStats merges the fold onto matching by-user-path rows,
// applyUserPathCacheStats merges cache statistics into user-path usage rows and adds rows for user paths with only local-cache activity.
func applyUserPathCacheStats(rows []UserPathUsage, stats map[string]*GroupCacheStats) []UserPathUsage {
	seen := map[string]bool{}
	for i := range rows {
		key := usageUserPathGroupKey(rows[i].UserPath)
		seen[key] = true
		rows[i].assign(stats[key])
	}
	for key, s := range stats {
		if seen[key] || !s.hasLocalTokens() {
			continue
		}
		row := UserPathUsage{UserPath: s.UserPath}
		row.assign(s)
		rows = append(rows, row)
	}
	return rows
}

// applyLabelCacheStats merges the fold onto matching by-label rows,
// materializing local-only groups like applyModelCacheStats. Synthetic rows
// count their local-cache requests so a label row never shows tokens with
// applyLabelCacheStats merges cache statistics into label usage rows and adds rows for labels with local-cache activity.
func applyLabelCacheStats(rows []LabelUsage, stats map[string]*GroupCacheStats) []LabelUsage {
	seen := map[string]bool{}
	for i := range rows {
		seen[rows[i].Label] = true
		rows[i].assign(stats[rows[i].Label])
	}
	for key, s := range stats {
		if seen[key] || !s.hasLocalTokens() {
			continue
		}
		row := LabelUsage{Label: key, Requests: s.LocalRequests}
		row.assign(s)
		rows = append(rows, row)
	}
	return rows
}

// EstimateCachedInputCost prices a group's prompt-cached input tokens with
// current catalog pricing (CachedInputPerMtok per pricing identity). It is an
// estimate: models without a cached-input rate contribute nothing, and it
// reflects today's prices, not the prices at request time — the same
// trade-off as pricing recalculation. Time-of-day pricing windows are applied
// per bucket at hour precision (see CachedPricingKey). Returns nil when
// nothing could be priced.
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
		pricing := resolver.ResolvePricing(key.Model, effectiveRecalculationPricingProvider(key.Provider, key.ProviderName)).AtTime(key.slotTime())
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
